package kubernetes

import (
	"context"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvSecretName returns the Secret name that carries secret env vars
// for an application. Always matches the Deployment name
// (DeploymentName) so the Deployment can envFrom both ConfigMap and
// Secret in one go.
func EnvSecretName(appName string, appID int64) string {
	return DeploymentName(appName, appID)
}

// EnvConfigMapName returns the ConfigMap name for non-secret env vars.
// Matches the Deployment name by convention (same rationale as
// EnvSecretName).
func EnvConfigMapName(appName string, appID int64) string {
	return DeploymentName(appName, appID)
}

// ApplyConfigMap creates or updates the ConfigMap holding non-secret
// env vars for an application in the given namespace. The Deployment
// references this ConfigMap via envFrom at pod-spec construction time
// (kubernetes/deployment.go ApplyDeployment).
func (c *Client) ApplyConfigMap(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error {
	if len(data) == 0 {
		// An empty ConfigMap is fine — k8s allows zero data fields. We
		// skip the write so callers don't have to special-case the
		// "no env vars yet" path. The Deployment's envFrom still
		// resolves correctly against a missing ConfigMap (no-op).
		return nil
	}
	name := EnvConfigMapName(appName, appID)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				AppLabel:        appName,
				"podium-app-id": fmt.Sprintf("%d", appID),
				"podium/role":   "env-configmap",
			},
		},
		Data: data,
	}
	cms := c.CS.CoreV1().ConfigMaps(namespace)
	_, err := cms.Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubernetes: create configmap %s/%s: %w", namespace, name, err)
	}
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("kubernetes: update configmap %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ApplySecret creates or updates the Secret holding secret env vars
// for an application in the given namespace. Per DECISIONS.md E the
// values are base64-encoded inside the Secret object — there's no
// separate encryption at rest in SQLite beyond the K8s Secret
// boundary (which the kind cluster stores as plain files in etcd).
//
// `data` keys map to values that will be exposed as envFrom entries
// on the Deployment; values are base64-std encoded here so the
// Kubernetes API server doesn't reject them.
func (c *Client) ApplySecret(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error {
	if len(data) == 0 {
		return nil
	}
	name := EnvSecretName(appName, appID)
	encoded := make(map[string][]byte, len(data))
	for k, v := range data {
		encoded[k] = []byte(base64.StdEncoding.EncodeToString([]byte(v)))
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				AppLabel:        appName,
				"podium-app-id": fmt.Sprintf("%d", appID),
				"podium/role":   "env-secret",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: encoded,
	}
	secrets := c.CS.CoreV1().Secrets(namespace)
	_, err := secrets.Create(ctx, sec, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubernetes: create secret %s/%s: %w", namespace, name, err)
	}
	_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("kubernetes: update secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteConfigMap removes the env-var ConfigMap for an app in the
// namespace. NotFound is treated as success — the API call is idempotent.
func (c *Client) DeleteConfigMap(ctx context.Context, appName string, appID int64, namespace string) error {
	name := EnvConfigMapName(appName, appID)
	err := c.CS.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete configmap %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteSecret removes the env-var Secret for an app in the namespace.
// NotFound is treated as success.
func (c *Client) DeleteSecret(ctx context.Context, appName string, appID int64, namespace string) error {
	name := EnvSecretName(appName, appID)
	err := c.CS.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete secret %s/%s: %w", namespace, name, err)
	}
	return nil
}
