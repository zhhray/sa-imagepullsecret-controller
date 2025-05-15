package token

import (
	"context"
	"fmt"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Manager interface {
	GetToken(ctx context.Context, sa *corev1.ServiceAccount) (string, error)
	Start()
	Stop()
}

type tokenManager struct {
	client       kubernetes.Interface
	cache        sync.Map
	refreshQueue workqueue.RateLimitingInterface
	stopCh       chan struct{}
}

func NewTokenManager(client kubernetes.Interface) Manager {
	return &tokenManager{
		client: client,
		refreshQueue: workqueue.NewNamedRateLimitingQueue(
			workqueue.DefaultControllerRateLimiter(),
			"token-refresh",
		),
		stopCh: make(chan struct{}),
	}
}

func (m *tokenManager) GetToken(ctx context.Context, sa *corev1.ServiceAccount) (string, error) {
	key := saKey(sa)
	klog.V(4).InfoS("Looking up token in cache", "namespace", sa.Namespace, "sa", sa.Name)

	if token, ok := m.loadToken(key); ok && !m.isTokenExpired(token) {
		klog.V(4).InfoS("Using cached token",
			"namespace", sa.Namespace, "sa", sa.Name,
			"expiry", token.expiry.Format(time.RFC3339))
		return token.value, nil
	}

	return m.refreshToken(ctx, sa)
}

func (m *tokenManager) Start() {
	go wait.Until(m.runWorker, time.Second, m.stopCh)
}

func (m *tokenManager) Stop() {
	close(m.stopCh)
	m.refreshQueue.ShutDown()
}

func (m *tokenManager) runWorker() {
	for m.processNextItem() {
	}
}

func (m *tokenManager) processNextItem() bool {
	key, quit := m.refreshQueue.Get()
	if quit {
		return false
	}
	defer m.refreshQueue.Done(key)

	sa, err := m.getServiceAccount(key.(string))
	if err != nil {
		klog.Errorf("Error getting SA: %v", err)
		return true
	}

	_, err = m.refreshToken(context.Background(), sa)
	if err != nil {
		klog.Errorf("Failed to refresh token: %v", err)
		m.refreshQueue.AddAfter(key, 5*time.Second) // 使用延迟重试
	} else {
		m.refreshQueue.Forget(key)
	}
	return true
}

// Helper types and functions
type tokenEntry struct {
	value  string
	expiry time.Time
}

func (m *tokenManager) loadToken(key string) (tokenEntry, bool) {
	val, ok := m.cache.Load(key)
	if !ok {
		return tokenEntry{}, false
	}
	return val.(tokenEntry), true
}

func (m *tokenManager) isTokenExpired(entry tokenEntry) bool {
	return time.Now().After(entry.expiry.Add(-10 * time.Minute))
}

func (m *tokenManager) refreshToken(ctx context.Context, sa *corev1.ServiceAccount) (string, error) {
	klog.InfoS("Generating new token for SA", "namespace", sa.Namespace, "sa", sa.Name)

	key := saKey(sa)
	token, expiry, err := m.generateToken(ctx, sa)
	if err != nil {
		klog.ErrorS(err, "Failed to generate token", "namespace", sa.Namespace, "sa", sa.Name)
		return "", err
	}

	m.cache.Store(key, tokenEntry{
		value:  token,
		expiry: expiry,
	})

	m.scheduleRefresh(key, expiry)

	klog.InfoS("Generated new token",
		"namespace", sa.Namespace, "sa", sa.Name,
		"expiry", expiry.Format(time.RFC3339),
		"length", len(token))
	return token, nil
}

func (m *tokenManager) generateToken(ctx context.Context, sa *corev1.ServiceAccount) (string, time.Time, error) {
	expiration := int64(time.Hour.Seconds())
	tr := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expiration,
		},
	}

	token, err := m.client.CoreV1().ServiceAccounts(sa.Namespace).CreateToken(
		ctx,
		sa.Name,
		tr,
		metav1.CreateOptions{},
	)
	if err != nil {
		return "", time.Time{}, err
	}

	expiry := time.Now().Add(time.Duration(*tr.Spec.ExpirationSeconds) * time.Second)
	return token.Status.Token, expiry, nil
}

func (m *tokenManager) scheduleRefresh(key string, expiry time.Time) {
	refreshTime := expiry.Add(-10 * time.Minute)
	m.refreshQueue.AddAfter(key, time.Until(refreshTime))
}

func saKey(sa *corev1.ServiceAccount) string {
	return fmt.Sprintf("%s/%s", sa.Namespace, sa.Name)
}

func (m *tokenManager) getServiceAccount(key string) (*corev1.ServiceAccount, error) {
	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return nil, err
	}
	return m.client.CoreV1().ServiceAccounts(ns).Get(
		context.Background(),
		name,
		metav1.GetOptions{},
	)
}
