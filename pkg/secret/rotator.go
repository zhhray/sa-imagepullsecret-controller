package secret

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"sa-imagepullsecret-controller/pkg/utils"
)

type Rotator struct {
	client    kubernetes.Interface
	secretMgr Manager
	mutexes   sync.Map
}

func NewRotator(client kubernetes.Interface, secretMgr Manager) *Rotator {
	return &Rotator{
		client:    client,
		secretMgr: secretMgr,
	}
}

func (r *Rotator) Start(ctx context.Context) {
	ticker := time.NewTicker(utils.RotationCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.rotateSecrets(ctx)
		case <-ctx.Done():
			klog.Info("Rotation worker shutting down")
			return
		}
	}
}

func (r *Rotator) rotateSecrets(ctx context.Context) {
	secrets, err := r.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			utils.ManagedByLabel: utils.ControllerName,
		}).String(),
	})
	if err != nil {
		klog.ErrorS(err, "Failed to list secrets")
		return
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, utils.MaxConcurrentRotations)

	for _, secret := range secrets.Items {
		if !r.needsRotation(&secret) {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(s corev1.Secret) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := r.rotateSecret(ctx, &s); err != nil {
				if !strings.Contains(err.Error(), "existing valid secret") {
					klog.ErrorS(err, "Rotation failed",
						"namespace", s.Namespace,
						"secret", s.Name)
				}
			}
		}(secret)
	}

	wg.Wait()
}

func (r *Rotator) needsRotation(secret *corev1.Secret) bool {
	expiryTime, err := time.Parse(time.RFC3339, secret.Annotations[utils.ExpiryAnnotation])
	if err != nil {
		klog.ErrorS(err, "Invalid expiry time, forcing rotation",
			"namespace", secret.Namespace,
			"secret", secret.Name)
		return true
	}
	return time.Until(expiryTime) < utils.RotationThreshold
}

func (r *Rotator) rotateSecret(ctx context.Context, oldSecret *corev1.Secret) error {
	// 获取命名空间级别的锁
	mutex, _ := r.mutexes.LoadOrStore(oldSecret.Namespace, &sync.Mutex{})
	mutex.(*sync.Mutex).Lock()
	defer mutex.(*sync.Mutex).Unlock()

	klog.InfoS("Starting secret rotation",
		"namespace", oldSecret.Namespace,
		"secret", oldSecret.Name,
		"expiry", oldSecret.Annotations[utils.ExpiryAnnotation])

	sa, err := r.client.CoreV1().ServiceAccounts(oldSecret.Namespace).Get(
		ctx,
		utils.DefaultSAName,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("get SA failed: %w", err)
	}

	newSecret, err := r.secretMgr.CreateSecret(sa)
	if err != nil {
		return fmt.Errorf("create new secret failed: %w", err)
	}

	if err := r.secretMgr.UpdateSAWithNewSecret(sa, newSecret.Name, oldSecret.Name); err != nil {
		return fmt.Errorf("update SA failed: %w", err)
	}

	r.scheduleDeletion(oldSecret)
	return nil
}

func (r *Rotator) scheduleDeletion(oldSecret *corev1.Secret) {
	var (
		maxRetries = 5
		baseDelay  = 1 * time.Second
		maxDelay   = 30 * time.Second
	)

	go func() {
		time.Sleep(utils.DefaultDeletionDelay)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		var lastErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			// 获取最新Secret状态
			currentSecret, err := r.client.CoreV1().Secrets(oldSecret.Namespace).Get(ctx, oldSecret.Name, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				klog.InfoS("Secret already deleted", "namespace", oldSecret.Namespace, "secret", oldSecret.Name)
				return
			}

			// 检查引用状态
			referenced, err := r.isSecretReferenced(ctx, currentSecret)
			if err != nil {
				lastErr = err
				continue
			}
			if referenced {
				klog.InfoS("Secret still referenced, aborting deletion", "namespace", oldSecret.Namespace, "secret", oldSecret.Name)
				return
			}

			// 执行删除
			err = r.client.CoreV1().Secrets(oldSecret.Namespace).Delete(ctx, oldSecret.Name, metav1.DeleteOptions{})
			if err == nil || errors.IsNotFound(err) {
				klog.InfoS("Secret deleted successfully", "namespace", oldSecret.Namespace, "secret", oldSecret.Name)
				return
			}

			// 指数退避
			delay := time.Duration(math.Pow(2, float64(attempt))) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}
			time.Sleep(delay)
			lastErr = err
		}

		klog.ErrorS(lastErr, "Failed to delete secret after retries",
			"namespace", oldSecret.Namespace,
			"secret", oldSecret.Name,
			"retries", maxRetries)
	}()
}

// 检查Secret是否被任何资源引用
func (r *Rotator) isSecretReferenced(ctx context.Context, secret *corev1.Secret) (bool, error) {
	// 1. 判断Labels是否包含特定标签
	if _, ok := secret.Labels[utils.ManagedByLabel]; !ok {
		return false, nil
	}
	// 2. 判断名称是否包含特定前缀
	if !strings.HasPrefix(secret.Name, utils.SecretNamePrefix) {
		return false, nil
	}

	// 3. 检查所有ServiceAccount
	sas, err := r.client.CoreV1().ServiceAccounts(secret.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to list ServiceAccounts: %w", err)
	}

	for _, sa := range sas.Items {
		for _, ref := range sa.ImagePullSecrets {
			if ref.Name == secret.Name {
				return true, nil
			}
		}
	}

	return false, nil
}

// 启动垃圾回收器
// 该函数会定期检查过期的Secret，并删除不再被引用的Secret
func (r *Rotator) StartGarbageCollector(ctx context.Context) {
	ticker := time.NewTicker(utils.GCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.runGC(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (r *Rotator) runGC(ctx context.Context) {
	secrets, err := r.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			utils.ManagedByLabel: utils.ControllerName,
		}).String(),
	})
	if err != nil {
		klog.ErrorS(err, "Garbage collection failed to list secrets")
		return
	}

	now := time.Now().UTC()
	for _, secret := range secrets.Items {
		expiryStr := secret.Annotations[utils.ExpiryAnnotation]
		expiryTime, err := time.Parse(time.RFC3339, expiryStr)
		if err != nil {
			klog.Warning(err, "Invalid expiry time, forcing deletion",
				"namespace", secret.Namespace,
				"secret", secret.Name)
			continue
		}

		// 过期时间 + 宽限时间
		if now.After(expiryTime.Add(utils.GracePeriod)) {
			referenced, err := r.isSecretReferenced(ctx, &secret)
			if err != nil {
				klog.ErrorS(err, "GC check failed",
					"namespace", secret.Namespace,
					"secret", secret.Name)
				continue
			}

			if !referenced {
				klog.InfoS("GC deleting expired secret",
					"namespace", secret.Namespace,
					"secret", secret.Name,
					"expiredAt", expiryStr)
				r.scheduleDeletion(&secret)
			}
		}
	}
}
