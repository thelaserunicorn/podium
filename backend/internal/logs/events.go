package logs

import (
	"context"
	"fmt"
)

// EventRow is one Kubernetes event formatted for the diagnostics
// panel. It mirrors the columns of the spec.md §27 events table —
// timestamp, type (Normal / Warning), reason, message — without
// leaking the raw object metadata.
type EventRow struct {
	TS      string `json:"ts"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Object  string `json:"object,omitempty"` // "Kind/name"
}

// EventSource is the subset of the Kubernetes client the events
// service needs. The real kubernetes.Client satisfies this; tests
// supply a fake.
//
// We only model the EventsV1 surface (events.k8s.io/v1). The legacy
// corev1 events API still works but is deprecated in 1.22+; the
// modern API is the recommended path on kind/k8s 1.27+.
type EventSource interface {
	Events(ctx context.Context, namespace, involvedObjectUID string) ([]EventRow, error)
}

// FetchEvents reads Kubernetes events from the namespace, optionally
// filtered to events about a single object UID. An empty UID returns
// every event in the namespace.
//
// Sort order: most recent first (the kubernetes package sorts in
// place). We project to a slim JSON shape — the raw Event object
// carries more fields than the UI needs.
func FetchEvents(ctx context.Context, src EventSource, namespace, involvedObjectUID string) ([]EventRow, error) {
	if src == nil {
		return nil, fmt.Errorf("logs: nil event source")
	}
	if namespace == "" {
		return nil, fmt.Errorf("logs: namespace is required")
	}
	return src.Events(ctx, namespace, involvedObjectUID)
}
