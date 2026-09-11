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

// PodLogs fetches the current container's stdout/stderr from the
// Kubernetes API for the named pod. Per DECISIONS.md B this is the
// "current container" mode (`kubectl logs <pod>` without --previous);
// the UI polls this on demand and never expects a restart's logs.
//
// Follow=false because the UI polls on a button click, not a stream.
// TailLines bounds the response so a chatty app doesn't ship megabytes
// through the API. 5000 lines is generous for the diagnostics panel.
func (c *Client) PodLogs(ctx context.Context, namespace, podName string) (string, error) {
	if namespace == "" || podName == "" {
		return "", fmt.Errorf("kubernetes: namespace and pod name are required")
	}
	tail := int64(5000)
	req := c.CS.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow:    false,
		TailLines: &tail,
	})
	out, err := req.DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("kubernetes: pod logs %s/%s: %w", namespace, podName, err)
	}
	return string(out), nil
}
