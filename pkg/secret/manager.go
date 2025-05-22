package secret

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"sa-imagepullsecret-controller/pkg/token"
	"sa-imagepullsecret-controller/pkg/utils"
)

type Manager interface {
	SyncSecret(sa *corev1.ServiceAccount) error
	CreateSecret(sa *corev1.ServiceAccount) (*corev1.Secret, error)
	UpdateSAWithNewSecret(sa *corev1.ServiceAccount, newSecret, oldSecret string) error
}

type SecretManager struct {
	client   kubernetes.Interface
	tokenMgr token.Manager
	randPool *big.Int
}

func NewSecretManager(client kubernetes.Interface, tokenMgr token.Manager) *SecretManager {
	return &SecretManager{
		client:   client,
		tokenMgr: tokenMgr,
		randPool: big.NewInt(int64(len(utils.Charset))),
	}
}

func (m *SecretManager) SyncSecret(sa *corev1.ServiceAccount) error {
	currentSecret, err := m.getCurrentSecret(sa)
	if err != nil {
		klog.ErrorS(err, "Failed to get current secret",
			"namespace", sa.Namespace, "sa", sa.Name)
		return err
	}

	if currentSecret != nil && !m.needsUpdate(currentSecret) {
		klog.V(4).InfoS("Secret is up-to-date",
			"namespace", sa.Namespace,
			"secret", currentSecret.Name)
		return nil
	}

	newSecret, err := m.CreateSecret(sa)
	if err != nil {
		return fmt.Errorf("create secret failed: %w", err)
	}

	if currentSecret != nil {
		if err := m.UpdateSAWithNewSecret(sa, newSecret.Name, currentSecret.Name); err != nil {
			return fmt.Errorf("update SA failed: %w", err)
		}
	} else {
		if err := m.updateSA(sa, newSecret.Name); err != nil {
			return fmt.Errorf("update SA failed: %w", err)
		}
	}

	return nil
}

func (m *SecretManager) CreateSecret(sa *corev1.ServiceAccount) (*corev1.Secret, error) {
	operationID := utils.GenerateOperationID()
	klog.V(4).InfoS("Attempting to create new secret",
		"namespace", sa.Namespace,
		"sa", sa.Name,
		"operation", operationID)

	// 防重复机制：检查现有有效secret
	existingSecrets, err := m.client.CoreV1().Secrets(sa.Namespace).List(
		context.Background(),
		metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				utils.ManagedByLabel: utils.ControllerName,
			}).String(),
		},
	)
	if err != nil {
		klog.ErrorS(err, "Failed to list existing secrets",
			"namespace", sa.Namespace,
			"operation", operationID)
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	now := time.Now()
	for _, secret := range existingSecrets.Items {
		// 跳过标记为删除的secret
		if secret.DeletionTimestamp != nil {
			continue
		}

		expiryStr, ok := secret.Annotations[utils.ExpiryAnnotation]
		if !ok {
			klog.Warning("Secret missing expiry annotation, allowing creation",
				"namespace", secret.Namespace,
				"secret", secret.Name,
				"operation", operationID)
			continue
		}

		expiryTime, err := time.Parse(time.RFC3339, expiryStr)
		if err != nil {
			klog.ErrorS(err, "Invalid expiry format in existing secret",
				"namespace", secret.Namespace,
				"secret", secret.Name,
				"operation", operationID)
			continue
		}

		if expiryTime.After(now) {
			klog.V(4).InfoS("Valid secret already exists, aborting creation",
				"namespace", secret.Namespace,
				"secret", secret.Name,
				"expiry", expiryStr,
				"operation", operationID)
			return nil, fmt.Errorf("existing valid secret %s (expires at %s)",
				secret.Name, expiryStr)
		}
	}

	// 生成新的token
	// 这里的token是一个临时token，用于docker registry的身份验证
	token, err := m.tokenMgr.GetToken(context.Background(), sa)
	if err != nil {
		klog.ErrorS(err, "Failed to get token",
			"namespace", sa.Namespace,
			"operation", operationID)
		return nil, err
	}

	authString := fmt.Sprintf("<token>:%s", token)
	authBase64 := base64.StdEncoding.EncodeToString([]byte(authString))

	dockerCfg := make(map[string]interface{})
	domains := utils.GetRegistryDomains()
	authEntry := map[string]string{"auth": authBase64}
	for _, domain := range domains {
		dockerCfg[domain] = authEntry
	}

	// 动态获取registry服务的ClusterIP
	registryIP, err := m.getRegistryServiceIP()
	if err != nil {
		klog.ErrorS(err, "Failed to get registry service IP",
			"namespace", sa.Namespace,
			"operation", operationID)
	} else {
		dockerCfg[registryIP] = map[string]string{"auth": authBase64}
	}
	klog.InfoS("Registry dockercfg will be created",
		"namespace", sa.Namespace,
		"dockerCfg", dockerCfg,
		"operation", operationID)

	jsonData, err := json.Marshal(dockerCfg)
	if err != nil {
		klog.ErrorS(err, "Failed to marshal docker config",
			"namespace", sa.Namespace,
			"operation", operationID)
		return nil, err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.generateSecretName(sa.Namespace),
			Namespace: sa.Namespace,
			Labels: map[string]string{
				utils.ManagedByLabel: utils.ControllerName,
			},
			Annotations: map[string]string{
				utils.GeneratedAnnotation:   "true",
				utils.ExpiryAnnotation:      time.Now().Add(utils.DefaultTokenExpiry).Format(time.RFC3339),
				utils.OperationIDAnnotation: operationID,
			},
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{
			".dockercfg": jsonData,
		},
	}

	createdSecret, err := m.client.CoreV1().Secrets(sa.Namespace).Create(
		context.Background(),
		secret,
		metav1.CreateOptions{
			FieldManager: utils.ControllerName,
		},
	)
	if err != nil {
		klog.ErrorS(err, "Secret creation failed",
			"namespace", sa.Namespace,
			"secret", secret.Name,
			"operation", operationID)
		return nil, err
	}

	klog.InfoS("Secret created successfully",
		"namespace", createdSecret.Namespace,
		"secret", createdSecret.Name,
		"expiry", createdSecret.Annotations[utils.ExpiryAnnotation],
		"operation", operationID)

	return createdSecret, nil
}

func (m *SecretManager) UpdateSAWithNewSecret(sa *corev1.ServiceAccount, newSecret, oldSecret string) error {
	const maxRetries = 3
	operationID := utils.GenerateOperationID()

	klog.InfoS("Updating SA imagePullSecrets",
		"namespace", sa.Namespace,
		"sa", sa.Name,
		"oldSecret", oldSecret,
		"newSecret", newSecret,
		"operation", operationID)

	// 获取最新的SA对象
	currentSA, err := m.client.CoreV1().ServiceAccounts(sa.Namespace).Get(
		context.Background(),
		sa.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		klog.ErrorS(err, "Failed to get latest SA",
			"namespace", sa.Namespace,
			"operation", operationID)
		return fmt.Errorf("get SA failed: %w", err)
	}

	// 构建新的secret引用列表
	newRefs := make([]corev1.LocalObjectReference, 0)

	// 保留所有非控制器管理的secret
	for _, ref := range currentSA.ImagePullSecrets {
		if !strings.HasPrefix(ref.Name, utils.SecretNamePrefix) {
			newRefs = append(newRefs, ref)
		}
	}

	// 添加新secret（确保只保留一个控制器secret）
	newRefs = append(newRefs, corev1.LocalObjectReference{Name: newSecret})

	// 构造JSON Patch
	patch := []map[string]interface{}{
		{
			"op":    "replace",
			"path":  "/imagePullSecrets",
			"value": newRefs,
		},
	}
	patchBytes, _ := json.Marshal(patch)

	// 带重试机制的更新
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		_, err = m.client.CoreV1().ServiceAccounts(sa.Namespace).Patch(
			context.Background(),
			sa.Name,
			types.JSONPatchType,
			patchBytes,
			metav1.PatchOptions{
				FieldManager: utils.ControllerName,
			},
		)
		if err == nil {
			klog.InfoS("SA updated successfully",
				"namespace", sa.Namespace,
				"sa", sa.Name,
				"newSecret", newSecret,
				"totalSecrets", len(newRefs),
				"operation", operationID)
			return nil
		}

		if errors.IsConflict(err) {
			// 处理版本冲突
			klog.Warning("Version conflict, retrying...",
				"namespace", sa.Namespace,
				"attempt", i+1,
				"operation", operationID)

			currentSA, err = m.client.CoreV1().ServiceAccounts(sa.Namespace).Get(
				context.Background(),
				sa.Name,
				metav1.GetOptions{},
			)
			if err != nil {
				lastErr = err
				continue
			}

			// 重新生成patch
			newRefs = newRefs[:0]
			for _, ref := range currentSA.ImagePullSecrets {
				if !strings.HasPrefix(ref.Name, utils.SecretNamePrefix) {
					newRefs = append(newRefs, ref)
				}
			}
			newRefs = append(newRefs, corev1.LocalObjectReference{Name: newSecret})

			patch = []map[string]interface{}{
				{"op": "replace", "path": "/imagePullSecrets", "value": newRefs},
			}
			if patchBytes, err = json.Marshal(patch); err != nil {
				lastErr = err
				break
			}
			continue
		}

		lastErr = err
		break
	}

	klog.ErrorS(lastErr, "Failed to update SA after retries",
		"namespace", sa.Namespace,
		"attempts", maxRetries,
		"operation", operationID)
	return fmt.Errorf("update SA failed after %d attempts: %w", maxRetries, lastErr)
}

func (m *SecretManager) generateSecretName(namespace string) string {
	b := make([]byte, utils.RandomLength)
	for i := range b {
		n, _ := rand.Int(rand.Reader, m.randPool)
		b[i] = utils.Charset[n.Int64()]
	}
	return fmt.Sprintf("%s-%s-%s",
		utils.SecretNamePrefix,
		namespace,
		string(b))
}

func (m *SecretManager) getCurrentSecret(sa *corev1.ServiceAccount) (*corev1.Secret, error) {
	secrets, err := m.client.CoreV1().Secrets(sa.Namespace).List(
		context.Background(),
		metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				utils.ManagedByLabel: utils.ControllerName,
			}).String(),
		},
	)
	if err != nil {
		return nil, err
	}

	for _, secret := range secrets.Items {
		if secret.Annotations[utils.GeneratedAnnotation] == "true" {
			return &secret, nil
		}
	}
	return nil, nil
}

func (m *SecretManager) needsUpdate(secret *corev1.Secret) bool {
	expiryTime, err := time.Parse(time.RFC3339, secret.Annotations[utils.ExpiryAnnotation])
	if err != nil {
		klog.ErrorS(err, "Invalid expiry time",
			"namespace", secret.Namespace,
			"secret", secret.Name)
		return true
	}

	remaining := time.Until(expiryTime)
	return remaining < utils.RotationThreshold
}

func (m *SecretManager) updateSA(sa *corev1.ServiceAccount, secretName string) error {
	// 兼容旧逻辑（首次创建时使用）
	return m.UpdateSAWithNewSecret(sa, secretName, "")
}

// getRegistryServiceIP retrieves the ClusterIP of the registry service.
func (m *SecretManager) getRegistryServiceIP() (string, error) {
	const maxRetry = 3
	serviceName, namespace := utils.RegistrySvcName, utils.RegistrySvcNamespace
	operationID := utils.GenerateOperationID()

	var clusterIP string
	var lastErr error

	// 带重试机制的查询
	for i := 0; i < maxRetry; i++ {
		svc, err := m.client.CoreV1().Services(namespace).Get(
			context.Background(),
			serviceName,
			metav1.GetOptions{},
		)
		if err == nil {
			if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
				lastErr = fmt.Errorf("service %s has invalid ClusterIP: %s", serviceName, svc.Spec.ClusterIP)
				klog.ErrorS(lastErr, "Invalid ClusterIP",
					"namespace", namespace,
					"operation", operationID)
				break
			}
			clusterIP = svc.Spec.ClusterIP
			return clusterIP, nil
		}

		if errors.IsNotFound(err) {
			lastErr = fmt.Errorf("service %s not found in namespace %s", serviceName, namespace)
			klog.ErrorS(lastErr, "Service not found",
				"namespace", namespace,
				"operation", operationID)
			break // 无需重试
		}

		lastErr = err
		klog.Warning("Failed to get service, retrying...",
			"namespace", namespace,
			"attempt", i+1,
			"error", err.Error(),
			"operation", operationID)
		time.Sleep(100 * time.Millisecond)
	}

	return "", fmt.Errorf("failed to get service after %d attempts: %w", maxRetry, lastErr)
}
