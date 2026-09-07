package kubernetes

import (
	"context"
	"fmt"

	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/podium/podium/internal/logs"
)

// Events fetches Kubernetes events in the namespace, optionally
// filtered to events about a single object UID. Empty UID returns
// every event in the namespace, sorted newest first.
//
// We use the modern events.k8s.io/v1 API (DECISIONS.md §10 / PLAN.md
// M5). The legacy corev1 events API is deprecated in 1.22+.
func (c *Client) Events(ctx context.Context, namespace, involvedObjectUID string) ([]logs.EventRow, error) {
	if namespace == "" {
		return nil, fmt.Errorf("kubernetes: namespace is required")
	}
	list, err := c.CS.EventsV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("kubernetes: list events %s: %w", namespace, err)
	}
	out := make([]logs.EventRow, 0, len(list.Items))
	for _, e := range list.Items {
		if involvedObjectUID != "" {
			// events.v1 renamed InvolvedObject → Regarding. Filter by
			// that object's UID; if Regarding is empty we fall back to
			// the event's own UID (which matches the deprecated corev1
			// behaviour).
			target := e.Regarding.UID
			if target == "" {
				target = e.UID
			}
			if target != types.UID(involvedObjectUID) {
				continue
			}
		}
		out = append(out, eventToRow(e))
	}
	// The k8s API doesn't guarantee order; sort newest first.
	sortEventsByTimeDesc(out)
	return out, nil
}

// sortEventsByTimeDesc is a small insertion sort. Event lists are
// typically small (tens, not thousands) so this is fine and avoids
// pulling in sort.Slice complexity in the API path.
func sortEventsByTimeDesc(rows []logs.EventRow) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j-1].TS < rows[j].TS; j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

func eventToRow(e eventsv1.Event) logs.EventRow {
	row := logs.EventRow{
		Type:    string(e.Type),
		Reason:  e.Reason,
		Message: e.Note,
	}
	if !e.EventTime.IsZero() {
		row.TS = e.EventTime.Time.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	} else if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		row.TS = e.Series.LastObservedTime.Time.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	if e.Regarding.Kind != "" {
		row.Object = fmt.Sprintf("%s/%s", e.Regarding.Kind, e.Regarding.Name)
	}
	return row
}
