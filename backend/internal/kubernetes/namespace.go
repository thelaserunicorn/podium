package kubernetes

import (
	"context"
	"fmt"

	"github.com/podium/podium/internal/application"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureNamespace creates a namespace named `name` if it does not
// already exist. DNS-1123 validation is delegated to the existing
// application.ValidateNamespaceName so we never duplicate the regex.
//
// Idempotent: a second call for the same name is a no-op, returning
// nil. The caller (deploy handler) decides whether to call this for
// user-supplied namespaces (DECISIONS.md C: Podium creates custom
// namespaces on demand).
func (c *Client) EnsureNamespace(ctx context.Context, name string) error {
	if err := application.ValidateNamespaceName(name); err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	_, err := c.CS.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: get namespace %q: %w", name, err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if _, err := c.CS.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("kubernetes: create namespace %q: %w", name, err)
	}
	return nil
}

// DeleteNamespace removes a Kubernetes namespace. Garbage collection
// cascades to every Deployment / Service / ConfigMap / Secret / Pod
// inside it — that is the whole point of calling this from the admin
// "delete namespace" endpoint. Idempotent: a namespace that does not
// exist (already deleted, or never created in the cluster because the
// SQLite row was orphaned) returns nil. DNS-1123 validation matches
// EnsureNamespace so callers can use either side of the pair safely.
func (c *Client) DeleteNamespace(ctx context.Context, name string) error {
	if err := application.ValidateNamespaceName(name); err != nil {
		return fmt.Errorf("kubernetes: %w", err)
	}
	if err := c.CS.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("kubernetes: delete namespace %q: %w", name, err)
	}
	return nil
}
