package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	informers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"sa-imagepullsecret-controller/pkg/queue"
	"sa-imagepullsecret-controller/pkg/rbac"
	"sa-imagepullsecret-controller/pkg/secret"
	"sa-imagepullsecret-controller/pkg/token"
	"sa-imagepullsecret-controller/pkg/utils"
)

type Controller struct {
	clientset     kubernetes.Interface
	secretMgr     secret.Manager
	tokenMgr      token.Manager
	secretRotator *secret.Rotator
	workqueue     queue.DelayingInterface
	roleBinder    rbac.RoleBinder
}

func NewController(clientset kubernetes.Interface) *Controller {
	tokenMgr := token.NewTokenManager(clientset)
	secretMgr := secret.NewSecretManager(clientset, tokenMgr)
	secretRotator := secret.NewRotator(clientset, secretMgr)
	workqueue := queue.NewDelayingQueue()
	roleBinder := rbac.NewRoleBinder(clientset)

	return &Controller{
		clientset:     clientset,
		secretMgr:     secretMgr,
		tokenMgr:      tokenMgr,
		secretRotator: secretRotator,
		workqueue:     workqueue,
		roleBinder:    roleBinder,
	}
}

func (c *Controller) Run(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer c.workqueue.ShutDown()

	klog.Info("Starting controller")
	defer klog.Info("Shutting down controller")

	factory := informers.NewSharedInformerFactory(c.clientset, 10*time.Minute)
	saInformer := factory.Core().V1().ServiceAccounts().Informer()
	nsInformer := factory.Core().V1().Namespaces().Informer()

	saInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			sa := obj.(*corev1.ServiceAccount)
			if sa.Name == utils.DefaultSAName {
				c.enqueueSA(sa)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			newSA := newObj.(*corev1.ServiceAccount)
			if newSA.Name == utils.DefaultSAName {
				c.enqueueSA(newSA)
			}
		},
	})

	nsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ns := obj.(*corev1.Namespace)
			c.enqueueNamespace(ns.Name)
		},
	})

	go factory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(),
		saInformer.HasSynced,
		nsInformer.HasSynced,
	) {
		utilruntime.HandleError(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}

	go wait.Until(c.runWorker, time.Second, ctx.Done())
	go c.secretRotator.Start(ctx)
	// 启动secret垃圾收集器
	go c.secretRotator.StartGarbageCollector(ctx)

	<-ctx.Done()
}

func (c *Controller) enqueueSA(sa *corev1.ServiceAccount) {
	key := fmt.Sprintf("%s/%s", sa.Namespace, sa.Name)
	c.workqueue.Add(key)
}

func (c *Controller) enqueueNamespace(namespace string) {
	key := fmt.Sprintf("%s/%s", namespace, utils.DefaultSAName)
	c.workqueue.Add(key)
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workqueue.Get()
	if shutdown {
		return false
	}
	defer c.workqueue.Done(obj)

	key := obj.(string)
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return true
	}

	if err := c.syncHandler(namespace, name); err != nil {
		utilruntime.HandleError(fmt.Errorf("sync %q failed: %v", key, err))
		c.workqueue.AddAfter(key, time.Minute)
	}
	return true
}

func (c *Controller) syncHandler(namespace, name string) error {
	sa, err := c.clientset.CoreV1().ServiceAccounts(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting service account: %v", err)
	}
	klog.InfoS("Syncing ServiceAccount", "namespace", namespace, "sa", name)

	if err := c.secretMgr.SyncSecret(sa); err != nil {
		if !strings.Contains(err.Error(), "existing valid secret") {
			klog.ErrorS(err, "Failed to sync secret", "namespace", sa.Namespace)
		}
		return fmt.Errorf("sync secret error: %w", err)
	}

	klog.InfoS("Ensuring RoleBinding", "namespace", sa.Namespace)
	// RoleBinding 处理
	if err := c.roleBinder.EnsureRoleBinding(context.Background(), sa.Namespace); err != nil {
		klog.ErrorS(err, "Failed to ensure RoleBinding", "namespace", sa.Namespace)
		return fmt.Errorf("rolebinding error: %w", err)
	}

	return nil
}
