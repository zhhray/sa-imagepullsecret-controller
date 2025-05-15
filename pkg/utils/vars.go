package utils

import (
	"fmt"
	"time"
)

const (
	DefaultSAName    = "default"
	ControllerName   = "sa-imagepullsecret-controller"
	SecretNamePrefix = "dockercfg"
	RandomLength     = 5
	Charset          = "bcdfghjklmnpqrstvwxz2456789"

	DefaultTokenExpiry     = 1 * time.Hour
	RotationThreshold      = 10 * time.Minute
	RotationCheckInterval  = 5 * time.Minute
	MaxConcurrentRotations = 10
	DefaultDeletionDelay   = 5 * time.Minute
	GCInterval             = 30 * time.Minute
	GracePeriod            = 5 * time.Minute

	// Annotations
	GeneratedAnnotation     = "alauda.io/generated"
	ExpiryAnnotation        = "alauda.io/expiry"
	OperationIDAnnotation   = "alauda.io/operation-id"
	DeletionDelayAnnotation = "alauda.io/deletion-delay"

	// Labels
	ManagedByLabel = "alauda.io/managed-by"

	// 参考: image-registry.openshift-image-registry.svc.cluster.local image-registry.openshift-image-registry.svc
	RegistrySvcClusterLocalDomain = "proxy-docker-registry.cpaas-system.svc.cluster.local"
	RegistrySvcDomain             = "proxy-docker-registry.cpaas-system.svc"
	RegistrySvcName               = "proxy-docker-registry"
	RegistrySvcNamespace          = "cpaas-system"
)

func GenerateOperationID() string {
	// 生成一个随机的操作ID
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
