package controller

import (
	"context"
	"crypto/rand"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	tunnelSecretKey = "tunnel-secret"
	tunnelTokenKey  = "tunnel-token"
	secretSize      = 32
)

// TunnelSecretName returns the K8s Secret name for a Gateway's tunnel secret.
func TunnelSecretName(gwName string) string {
	return fmt.Sprintf("cloudflared-%s-tunnel-secret", gwName)
}

// EnsureTunnelSecret ensures a K8s Secret exists with a valid 32-byte tunnel secret.
// Returns the secret bytes, whether the secret was regenerated, and any error.
func EnsureTunnelSecret(ctx context.Context, c client.Client, gw *gwapiv1.Gateway) ([]byte, bool, error) {
	secretName := TunnelSecretName(gw.Name)
	nn := types.NamespacedName{Name: secretName, Namespace: gw.Namespace}

	var existing v1.Secret
	err := c.Get(ctx, nn, &existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, false, fmt.Errorf("getting tunnel secret: %w", err)
		}

		// Secret doesn't exist — create it
		secret, err := generateTunnelSecret()
		if err != nil {
			return nil, false, err
		}

		newSecret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: gw.Namespace,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: gw.APIVersion,
					Kind:       gw.Kind,
					Name:       gw.Name,
					UID:        gw.UID,
				}},
			},
			Data: map[string][]byte{
				tunnelSecretKey: secret,
			},
		}
		if err := c.Create(ctx, newSecret); err != nil {
			return nil, false, fmt.Errorf("creating tunnel secret: %w", err)
		}
		return secret, false, nil
	}

	// Secret exists — validate it
	data, ok := existing.Data[tunnelSecretKey]
	if ok && len(data) == secretSize {
		return data, false, nil
	}

	// Invalid — regenerate
	secret, err := generateTunnelSecret()
	if err != nil {
		return nil, false, err
	}

	existing.Data[tunnelSecretKey] = secret
	if err := c.Update(ctx, &existing); err != nil {
		return nil, false, fmt.Errorf("updating tunnel secret: %w", err)
	}
	return secret, true, nil
}

// StoreTunnelToken stores the assembled tunnel token in the Secret's stringData.
// Uses stringData to avoid double-encoding.
func StoreTunnelToken(ctx context.Context, c client.Client, namespace, secretName, token string) error {
	nn := types.NamespacedName{Name: secretName, Namespace: namespace}

	var secret v1.Secret
	if err := c.Get(ctx, nn, &secret); err != nil {
		return fmt.Errorf("getting secret for token storage: %w", err)
	}

	if secret.StringData == nil {
		secret.StringData = make(map[string]string)
	}
	secret.StringData[tunnelTokenKey] = token

	if err := c.Update(ctx, &secret); err != nil {
		return fmt.Errorf("storing tunnel token: %w", err)
	}
	return nil
}

func generateTunnelSecret() ([]byte, error) {
	secret := make([]byte, secretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating tunnel secret: %w", err)
	}
	return secret, nil
}
