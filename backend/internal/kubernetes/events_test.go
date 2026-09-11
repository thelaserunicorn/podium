package kubernetes

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func newEvent(name, reason, note, kind, objName, uid string, when time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "podium-dev", Name: name},
		EventTime:  metav1.NewMicroTime(when),
		Reason:     reason,
		Note:       note,
		Type:       "Normal",
		Regarding: corev1.ObjectReference{
			Kind: kind,
			Name: objName,
			UID:  types.UID(uid),
		},
	}
}

func TestEvents_ListAllInNamespace(t *testing.T) {
	now := time.Now()
	cs := newTestClient(t,
		newEvent("old", "Scheduled", "old message", "Pod", "old-pod", "uid-old", now.Add(-2*time.Minute)),
		newEvent("new", "Pulled", "new message", "Pod", "new-pod", "uid-new", now),
	)
	rows, err := cs.Events(context.Background(), "podium-dev", "")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	// Newest first.
	if rows[0].Reason != "Pulled" || rows[1].Reason != "Scheduled" {
		t.Errorf("sort: %q then %q", rows[0].Reason, rows[1].Reason)
	}
	if rows[0].Object != "Pod/new-pod" {
		t.Errorf("object=%q want Pod/new-pod", rows[0].Object)
	}
}

func TestEvents_FilterByUID(t *testing.T) {
	now := time.Now()
	cs := newTestClient(t,
		newEvent("match", "Pulled", "x", "Pod", "p1", "uid-1", now),
		newEvent("skip", "Pulled", "y", "Pod", "p2", "uid-2", now),
	)
	rows, err := cs.Events(context.Background(), "podium-dev", "uid-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0].Object != "Pod/p1" {
		t.Errorf("object=%q want Pod/p1", rows[0].Object)
	}
}

func TestEvents_EmptyNamespace(t *testing.T) {
	cs := newTestClient(t)
	rows, err := cs.Events(context.Background(), "podium-dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("got=%d rows want 0", len(rows))
	}
}

func TestEvents_RejectsEmptyNamespaceArg(t *testing.T) {
	cs := newTestClient(t)
	if _, err := cs.Events(context.Background(), "", ""); err == nil {
		t.Error("empty namespace should error")
	}
}
