package rbac

import (
	"context"
	"fmt"
	"reflect"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"sa-imagepullsecret-controller/pkg/utils"
)

const (
	roleBindingName = "default-sa-image-puller-rolebinding"
	clusterRoleName = "system:image-puller"
)

type RoleBinder interface {
	EnsureRoleBinding(ctx context.Context, namespace string) error
}

type roleBinder struct {
	client kubernetes.Interface
}

func NewRoleBinder(client kubernetes.Interface) RoleBinder {
	return &roleBinder{client: client}
}

func (b *roleBinder) EnsureRoleBinding(ctx context.Context, namespace string) error {
	expected := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": utils.ControllerName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      utils.DefaultSAName,
				Namespace: namespace,
			},
		},
	}

	// 检查现有 RoleBinding
	existing, err := b.client.RbacV1().RoleBindings(namespace).Get(
		ctx,
		roleBindingName,
		metav1.GetOptions{},
	)

	switch {
	case err == nil:
		if !needsUpdate(existing, expected) {
			klog.V(4).InfoS("RoleBinding already exists and is up-to-date", "namespace", namespace)
			return nil
		}
		_, err = b.client.RbacV1().RoleBindings(namespace).Update(
			ctx,
			expected,
			metav1.UpdateOptions{},
		)
	case errors.IsNotFound(err):
		_, err = b.client.RbacV1().RoleBindings(namespace).Create(
			ctx,
			expected,
			metav1.CreateOptions{},
		)
	default:
		return fmt.Errorf("failed to get RoleBinding: %w", err)
	}

	if err != nil {
		return fmt.Errorf("failed to ensure RoleBinding: %w", err)
	}

	klog.InfoS("Successfully ensured RoleBinding",
		"namespace", namespace,
		"roleBinding", roleBindingName)
	return nil
}

func needsUpdate(existing, expected *rbacv1.RoleBinding) bool {
	// 比较 Subjects 和 RoleRef
	if !reflect.DeepEqual(existing.Subjects, expected.Subjects) {
		return true
	}
	if existing.RoleRef != expected.RoleRef {
		return true
	}
	return false
}
