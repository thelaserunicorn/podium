package kubernetes

import (
	"context"
	"fmt"

	"github.com/podium/podium/internal/application"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentName returns the canonical Kubernetes Deployment name for
// an application. Per DECISIONS.md E the name embeds the application
// id so two users can have apps with the same logical name without
// colliding in a shared namespace. The suffix is the lower-case hex of
// the app id, lower 24 bits — enough entropy for the MVP and easy to
// read in `kubectl get deploy`.
func DeploymentName(appName string, appID int64) string {
	return fmt.Sprintf("%s-%06x", appName, appID&0xFFFFFF)
}

// AppLabel is the label every Pod/DDeployment/Service created by
// Podium carries. Used by ListPods(labelSelector="app=<appName>") so
// the dashboard can show "all pods for this app in this namespace".
const AppLabel = "app"

// ApplyDeployment creates or updates the Deployment for an application
// in the given namespace, with the given image + replica count. Returns
// the chosen Deployment name (which is also the Service name).
func (c *Client) ApplyDeployment(ctx context.Context, app *application.Application, namespace, image string, replicas int) (string, error) {
	if replicas < 1 {
		replicas = 1
	}
	name := DeploymentName(app.Name, app.ID)
	replicas32 := int32(replicas)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				AppLabel:        app.Name,
				"podium-app-id": fmt.Sprintf("%d", app.ID),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas32,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{AppLabel: app.Name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{AppLabel: app.Name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  app.Name,
						Image: image,
						Ports: []corev1.ContainerPort{{
							ContainerPort: int32(app.ContainerPort),
							Protocol:      corev1.ProtocolTCP,
						}},
					}},
				},
			},
		},
	}
	_, err := c.CS.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = c.CS.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
			if err != nil {
				return name, fmt.Errorf("kubernetes: update deployment %s/%s: %w", namespace, name, err)
			}
			return name, nil
		}
		return name, fmt.Errorf("kubernetes: create deployment %s/%s: %w", namespace, name, err)
	}
	return name, nil
}

// CurrentReplicas reads readyReplicas / desired replicas from the
// Deployment's status. Used by Applier.Apply to decide when to flip
// STARTING → RUNNING (DECISIONS.md B).
func (c *Client) CurrentReplicas(ctx context.Context, namespace, name string) (current, desired int, err error) {
	d, err := c.CS.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("kubernetes: get deployment %s/%s: %w", namespace, name, err)
	}
	if d.Spec.Replicas == nil {
		desired = 1
	} else {
		desired = int(*d.Spec.Replicas)
	}
	current = int(d.Status.ReadyReplicas)
	return current, desired, nil
}

// AppLabelSelector returns the label selector string used to find
// Pods owned by an application. Convenience for callers that would
// otherwise need to construct the selector themselves.
func AppLabelSelector(appName string) string {
	return fmt.Sprintf("%s=%s", AppLabel, appName)
}
