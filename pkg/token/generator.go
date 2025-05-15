package token

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Generator interface {
	GenerateToken(ctx context.Context, sa *corev1.ServiceAccount) (string, error)
}

type TokenGenerator struct {
	client kubernetes.Interface
}

func NewTokenGenerator(client kubernetes.Interface) *TokenGenerator {
	return &TokenGenerator{client: client}
}

func (g *TokenGenerator) GenerateToken(ctx context.Context, sa *corev1.ServiceAccount) (string, error) {
	expiration := int64(time.Hour.Seconds())
	tr := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: &expiration,
			Audiences:         []string{"https://kubernetes.default.svc"},
		},
	}

	token, err := g.client.CoreV1().ServiceAccounts(sa.Namespace).CreateToken(
		ctx,
		sa.Name,
		tr,
		metav1.CreateOptions{FieldValidation: "Strict"},
	)
	if err != nil {
		return "", fmt.Errorf("failed to create token: %v", err)
	}

	return token.Status.Token, nil
}
