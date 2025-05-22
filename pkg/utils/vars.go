package utils

import (
	"fmt"
	"os"
	"strings"
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
	RegistryServiceSvcClusterLocalDomain = "internal-docker-registry.cpaas-system.svc.cluster.local"
	RegistryServiceSvcDomain             = "internal-docker-registry.cpaas-system.svc"
	RegistryServiceDomain                = "internal-docker-registry.cpaas-system"
	RegistrySvcName                      = "internal-docker-registry"
	RegistrySvcNamespace                 = "cpaas-system"
)

func GenerateOperationID() string {
	// 生成一个随机的操作ID
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func GetRegistryDomains() []string {
	// 返回所有可能的 Docker Registry 域名
	domains := []string{
		RegistryServiceDomain,
		RegistryServiceSvcDomain,
		RegistryServiceSvcClusterLocalDomain,
	}

	// 从环境变量中读取 REGISTRY_INGRESS_HOSTS
	hosts := os.Getenv("REGISTRY_INGRESS_HOSTS")
	if hosts != "" {
		// 如果存在 REGISTRY_INGRESS_HOSTS 环境变量，则将其拆分为多个域名
		domains = append(domains, strings.Split(hosts, ",")...)
	}

	return domains
}
