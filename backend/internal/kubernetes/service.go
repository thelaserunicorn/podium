package kubernetes

import (
	"context"
	"fmt"

	"github.com/podium/podium/internal/application"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ApplyService creates or updates the ClusterIP Service for an
// application in the given namespace. The Service exposes the
// application port on the in-cluster network (spec.md §17, AGENTS.md
// §4 — no ingress / external exposure in the MVP).
//
// The Service name matches the Deployment name (DeploymentName) so a
// single label selector covers both resources.
func (c *Client) ApplyService(ctx context.Context, app *application.Application, namespace string) error {
	name := DeploymentName(app.Name, app.ID)
	port := int32(app.ContainerPort)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				AppLabel:        app.Name,
				"podium-app-id": fmt.Sprintf("%d", app.ID),
			},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{AppLabel: app.Name},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	_, err := c.CS.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = c.CS.CoreV1().Services(namespace).Update(ctx, svc, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("kubernetes: update service %s/%s: %w", namespace, name, err)
			}
			return nil
		}
		return fmt.Errorf("kubernetes: create service %s/%s: %w", namespace, name, err)
	}
	return nil
}
