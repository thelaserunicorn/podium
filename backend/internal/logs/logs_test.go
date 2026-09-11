package logs

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fakePodLogSource satisfies PodLogSource for tests. Tests can preset
// the blob and error, and inspect the call args.
type fakePodLogSource struct {
	blob string
	err  error

	gotNS   string
	gotName string
}

func (f *fakePodLogSource) PodLogs(_ context.Context, ns, name string) (string, error) {
	f.gotNS, f.gotName = ns, name
	return f.blob, f.err
}

func TestFetchPodLogs_HappyPath(t *testing.T) {
	src := &fakePodLogSource{blob: "line1\nline2\nline3\n"}
	got, err := FetchPodLogs(context.Background(), src, "podium-dev", "my-api-x1")
	if err != nil {
		t.Fatalf("FetchPodLogs: %v", err)
	}
	if src.gotNS != "podium-dev" || src.gotName != "my-api-x1" {
		t.Errorf("forwarded args: ns=%q name=%q", src.gotNS, src.gotName)
	}
	want := []PodLogLine{{Line: "line1"}, {Line: "line2"}, {Line: "line3"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%+v want %+v", got, want)
	}
}

func TestFetchPodLogs_SkipsBlankLines(t *testing.T) {
	src := &fakePodLogSource{blob: "a\n\n\nb\n\nc\n"}
	got, err := FetchPodLogs(context.Background(), src, "podium-dev", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("got=%+v want 3 entries", got)
	}
}

func TestFetchPodLogs_EmptyBlob(t *testing.T) {
	// Empty blobs MUST return a non-nil empty slice so the API layer's
	// `encoding/json` round-trip emits `"lines":[]` instead of
	// `"lines":null`. The Logs tab reads `logs?.lines.length` directly
	// and a null here crashes the route — see the /apps/10 Logs tab
	// incident. Pin the contract here so it can't regress.
	src := &fakePodLogSource{blob: ""}
	got, err := FetchPodLogs(context.Background(), src, "podium-dev", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("empty blob must yield a non-nil empty slice (JSON contract)")
	}
	if len(got) != 0 {
		t.Errorf("empty blob: len=%d want 0", len(got))
	}
}

func TestFetchPodLogs_PropagatesError(t *testing.T) {
	want := errors.New("pod gone")
	src := &fakePodLogSource{err: want}
	_, err := FetchPodLogs(context.Background(), src, "podium-dev", "p")
	if !errors.Is(err, want) {
		t.Errorf("err=%v want %v", err, want)
	}
}

func TestFetchPodLogs_RejectsEmptyArgs(t *testing.T) {
	src := &fakePodLogSource{blob: "x"}
	for _, c := range []struct {
		ns, name string
	}{
		{"", "p"},
		{"ns", ""},
		{"", ""},
	} {
		if _, err := FetchPodLogs(context.Background(), src, c.ns, c.name); err == nil {
			t.Errorf("ns=%q name=%q should error", c.ns, c.name)
		}
	}
}

func TestFetchPodLogs_NilSource(t *testing.T) {
	if _, err := FetchPodLogs(context.Background(), nil, "ns", "p"); err == nil {
		t.Error("nil source should error")
	}
}

// fakeEventSource satisfies EventSource.
type fakeEventSource struct {
	rows []EventRow
	err  error

	gotNS  string
	gotUID string
}

func (f *fakeEventSource) Events(_ context.Context, ns, uid string) ([]EventRow, error) {
	f.gotNS, f.gotUID = ns, uid
	return f.rows, f.err
}

func TestFetchEvents_HappyPath(t *testing.T) {
	src := &fakeEventSource{rows: []EventRow{{Type: "Normal", Reason: "Pulled"}}}
	got, err := FetchEvents(context.Background(), src, "podium-dev", "abc-123")
	if err != nil {
		t.Fatal(err)
	}
	if src.gotNS != "podium-dev" || src.gotUID != "abc-123" {
		t.Errorf("forwarded args: ns=%q uid=%q", src.gotNS, src.gotUID)
	}
	if len(got) != 1 || got[0].Reason != "Pulled" {
		t.Errorf("got=%+v", got)
	}
}

func TestFetchEvents_RejectsEmptyNamespace(t *testing.T) {
	if _, err := FetchEvents(context.Background(), &fakeEventSource{}, "", ""); err == nil {
		t.Error("empty namespace should error")
	}
}

func TestFetchEvents_PropagatesError(t *testing.T) {
	want := errors.New("apiserver down")
	src := &fakeEventSource{err: want}
	_, err := FetchEvents(context.Background(), src, "ns", "u")
	if !errors.Is(err, want) {
		t.Errorf("err=%v want %v", err, want)
	}
}

// FetchBuildLogs is a thin wrapper over storage, so we don't need a
// full fake storage layer here. The storage tests already cover the
// cursor behaviour. We just verify the error path is wired (nil
// source returns no rows, no error) so the API layer doesn't have to
// nil-check.
func TestFetchBuildLogs_NilSource(t *testing.T) {
	got, err := FetchBuildLogs(context.Background(), nil, 1, time.Time{})
	if err != nil {
		t.Errorf("nil source: err=%v", err)
	}
	if got != nil {
		t.Errorf("nil source: got=%+v want nil", got)
	}
}
