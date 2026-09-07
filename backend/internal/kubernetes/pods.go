package kubernetes

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodSummary is the per-Pod information surfaced to the UI. It mirrors
// the columns of the spec.md §18 pods table — name, phase, ready, restarts,
// started-at — without exposing anything else. JSON tags match the
// shape the frontend expects.
type PodSummary struct {
	Name      string     `json:"name"`
	Namespace string     `json:"namespace"`
	Phase     string     `json:"phase"`
	Ready     bool       `json:"ready"`
	Restarts  int32      `json:"restarts"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// ListPods returns the pods that match the given label selector in
// the namespace. An empty selector lists every pod in the namespace.
// Used by /api/applications/{id}/state to populate the Overview tab.
func (c *Client) ListPods(ctx context.Context, namespace, labelSelector string) ([]PodSummary, error) {
	list, err := c.CS.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: list pods %s: %w", namespace, err)
	}
	out := make([]PodSummary, 0, len(list.Items))
	for _, p := range list.Items {
		out = append(out, podSummaryFrom(p))
	}
	return out, nil
}

func podSummaryFrom(p corev1.Pod) PodSummary {
	ps := PodSummary{
		Name:      p.Name,
		Namespace: p.Namespace,
		Phase:     string(p.Status.Phase),
		Ready:     true,
	}
	if len(p.Status.ContainerStatuses) == 0 {
		// No containers yet — treat as not-ready.
		ps.Ready = false
	}
	for _, cs := range p.Status.ContainerStatuses {
		ps.Restarts += cs.RestartCount
		if !cs.Ready {
			ps.Ready = false
		}
	}
	if p.Status.StartTime != nil && !p.Status.StartTime.IsZero() {
		t := p.Status.StartTime.Time
		ps.StartedAt = &t
	}
	return ps
}
