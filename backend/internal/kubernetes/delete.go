package kubernetes

import (
	"context"
	"fmt"

	"github.com/podium/podium/internal/application"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeleteAppResources removes every Kubernetes resource Podium owns for
// an application in the given namespace: Deployment, Service, the
// env-var ConfigMap, and the env-var Secret. Each call is best-effort
// and NotFound is treated as success so the function is idempotent —
// safe to call after a partial failure, and safe to call when the app
// was never deployed to the namespace in the first place.
//
// Pods are intentionally NOT deleted here — the Deployment's
// cascading delete will reap them automatically once the Deployment
// is gone.
func (c *Client) DeleteAppResources(ctx context.Context, app *application.Application, namespace string) error {
	name := DeploymentName(app.Name, app.ID)

	// Order doesn't strictly matter for correctness (each call is
	// independent), but deleting the Deployment first lets the
	// kubelet stop the pods before the Service / ConfigMap / Secret
	// disappear from the API.
	if err := c.deleteDeployment(ctx, namespace, name); err != nil {
		return err
	}
	if err := c.deleteService(ctx, namespace, name); err != nil {
		return err
	}
	if err := c.DeleteConfigMap(ctx, app.Name, app.ID, namespace); err != nil {
		return err
	}
	if err := c.DeleteSecret(ctx, app.Name, app.ID, namespace); err != nil {
		return err
	}
	return nil
}

func (c *Client) deleteDeployment(ctx context.Context, namespace, name string) error {
	err := c.CS.AppsV1().Deployments(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete deployment %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (c *Client) deleteService(ctx context.Context, namespace, name string) error {
	err := c.CS.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubernetes: delete service %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeleteDeployment removes just the Deployment for a single deployment
// attempt (used by the M5 deployment-delete flow). Pods are reaped by
// the Deployment's cascading delete; the Service / ConfigMap / Secret
// stay around so a subsequent deploy can reattach to them.
func (c *Client) DeleteDeployment(ctx context.Context, namespace, name string) error {
	return c.deleteDeployment(ctx, namespace, name)
}
