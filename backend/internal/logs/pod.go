package logs

import (
	"context"
	"fmt"
	"strings"
)

// PodLogLine is one line of stdout/stderr for a Pod. The API layer
// renders these as `{ "ts": "...", "line": "..." }` JSON.
type PodLogLine struct {
	TS   string `json:"ts,omitempty"`
	Line string `json:"line"`
}

// PodLogSource is the subset of the Kubernetes client the pod-log
// service needs. The real kubernetes.Client satisfies this; tests
// can supply a fake.
type PodLogSource interface {
	PodLogs(ctx context.Context, namespace, podName string) (string, error)
}

// FetchPodLogs wraps the kubernetes.Client.PodLogs call and splits the
// returned blob into newline-separated lines. The Kubernetes API
// doesn't include per-line timestamps in the basic mode; we leave TS
// empty so the frontend renders the line without a fake time.
//
// Returns ErrPodNotFound-shaped errors when the pod has been
// garbage-collected by the kubelet after a restart — the API layer
// turns that into 404.
func FetchPodLogs(ctx context.Context, src PodLogSource, namespace, podName string) ([]PodLogLine, error) {
	if src == nil {
		return nil, fmt.Errorf("logs: nil pod log source")
	}
	if namespace == "" || podName == "" {
		return nil, fmt.Errorf("logs: namespace and pod name are required")
	}
	raw, err := src.PodLogs(ctx, namespace, podName)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		// Return a non-nil empty slice (not nil) so the API layer's
		// `encoding/json` round-trip emits `"lines":[]` instead of
		// `"lines":null`. The Logs tab reads `logs?.lines.length`
		// directly — a `null` here throws "Cannot read properties of
		// null (reading 'length')" and the route falls through to the
		// ErrorBoundary. Same shape as `state.pods` — see kubernetes.go
		// stateResponse.
		return []PodLogLine{}, nil
	}
	lines := strings.Split(raw, "\n")
	out := make([]PodLogLine, 0, len(lines))
	for _, l := range lines {
		// Skip pure empty lines so the UI doesn't render paragraphs of
		// blank rows — the kubernetes log stream often ends with \n\n.
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, PodLogLine{Line: l})
	}
	return out, nil
}
