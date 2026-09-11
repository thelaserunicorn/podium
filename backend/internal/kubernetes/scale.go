package kubernetes

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScaleDeployment sets the replica count on an existing Deployment in
// the given namespace. Used by the M4 Scale action — the API handler
// authorizes ownership before calling this, and the Deployment is
// expected to exist (created by ApplyDeployment on a prior deploy).
//
// Implementation note: we Get + Update rather than Patch because
// fake.Clientset's tracker handles whole-object Updates cleanly and
// we don't need the field-merging semantics of a strategic merge
// patch. The replica count is the only mutable field on this path;
// image / labels are owned by ApplyDeployment.
func (c *Client) ScaleDeployment(ctx context.Context, namespace, name string, replicas int) error {
	if replicas < 1 {
		return fmt.Errorf("kubernetes: scale: replicas must be >= 1 (got %d)", replicas)
	}
	deps := c.CS.AppsV1().Deployments(namespace)
	d, err := deps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("kubernetes: get deployment %s/%s: %w", namespace, name, err)
	}
	r := int32(replicas)
	d.Spec.Replicas = &r
	if _, err := deps.Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("kubernetes: update deployment %s/%s: %w", namespace, name, err)
	}
	return nil
}

// DeletePodsBySelector removes every pod in the namespace that matches
// the given label selector. Used by the M4 Restart action — the
// Deployment recreates the pods after deletion (spec.md §20).
//
// Returns the number of pods deleted. Zero is a valid result (no
// pods matched), not an error — restart against an empty namespace
// is a no-op from Podium's perspective; the Deployment will create
// the next pod the next time it scales.
func (c *Client) DeletePodsBySelector(ctx context.Context, namespace, labelSelector string) (int, error) {
	pods := c.CS.CoreV1().Pods(namespace)
	list, err := pods.List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return 0, fmt.Errorf("kubernetes: list pods %s: %w", namespace, err)
	}
	deleted := 0
	for _, p := range list.Items {
		if err := pods.Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
			return deleted, fmt.Errorf("kubernetes: delete pod %s/%s: %w", namespace, p.Name, err)
		}
		deleted++
	}
	return deleted, nil
}
