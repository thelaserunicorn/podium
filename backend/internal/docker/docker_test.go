package docker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// recordingSink captures every Append call so tests can assert on the
// exact stream of lines received.
type recordingSink struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingSink) Append(line string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	return nil
}

func (r *recordingSink) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

func TestValidateTag(t *testing.T) {
	cases := []struct {
		tag     string
		wantErr bool
	}{
		{"my-api:v1", false},
		{"my_api.v1", false},
		{"registry.example.com/foo/bar:v1", false},
		{"sha256:abcdef0123456789", false},
		{"my-api@sha256:abc", false},
		{"", true},
		{"bad tag", true},
		{"bad\ntag", true},
		{"weird$char", true},
	}
	for _, c := range cases {
		err := ValidateTag(c.tag)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateTag(%q) err=%v wantErr=%v", c.tag, err, c.wantErr)
		}
	}
}

func TestSplitLines_FlushesAtNewlines(t *testing.T) {
	var buf string
	got := splitLines(&buf, "line1\nline2\nline3\n")
	want := []string{"line1", "line2", "line3"}
	if !equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if buf != "" {
		t.Errorf("buffer should be empty after flush, got %q", buf)
	}
}

func TestSplitLines_BuffersPartialTrailingLine(t *testing.T) {
	var buf string
	got := splitLines(&buf, "line1\npart")
	want := []string{"line1"}
	if !equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if buf != "part" {
		t.Errorf("buffer should hold partial line, got %q", buf)
	}
	// Next chunk completes the line.
	got = splitLines(&buf, "ial\nnext\n")
	want = []string{"partial", "next"}
	if !equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitLines_HandlesCRLF(t *testing.T) {
	var buf string
	got := splitLines(&buf, "a\r\nb\r\n")
	if !equal(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
}

func TestSplitLines_EmptyInputReturnsNoCompleteLines(t *testing.T) {
	var buf string
	got := splitLines(&buf, "")
	if len(got) != 0 {
		t.Errorf("expected zero lines, got %v", got)
	}
	if buf != "" {
		t.Errorf("buffer should be empty, got %q", buf)
	}
}

func TestNopSink_NeverErrors(t *testing.T) {
	var s NopSink
	for _, line := range []string{"", "long line with stuff\n", "another"} {
		if err := s.Append(line); err != nil {
			t.Errorf("NopSink.Append(%q) err=%v", line, err)
		}
	}
}

// fakeRunner records the command name and args of the most recent call
// and returns a canned response.
type fakeRunner struct {
	mu       sync.Mutex
	calls    []fakeCall
	response string
	err      error
}

type fakeCall struct {
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	return f.response, f.err
}

func (f *fakeRunner) LastCall() (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return "", nil
	}
	c := f.calls[len(f.calls)-1]
	return c.name, c.args
}

func TestLoadIntoKind_Success(t *testing.T) {
	r := &fakeRunner{response: "Image: \"my-api:v1\" kind cluster node(s) loaded image to nodes.\n"}
	if err := loadIntoKind(context.Background(), r, "my-api:v1", "podium"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	name, args := r.LastCall()
	if name != "kind" {
		t.Errorf("executable=%q want kind", name)
	}
	wantArgs := []string{"load", "docker-image", "my-api:v1", "--name", "podium"}
	if !equal(args, wantArgs) {
		t.Errorf("args=%v want %v", args, wantArgs)
	}
}

// TestLoadIntoKind_OmitsNameWhenEmpty: an empty cluster name means
// "let kind pick" — the command must NOT include `--name ""`. This is
// the single-cluster setup path.
func TestLoadIntoKind_OmitsNameWhenEmpty(t *testing.T) {
	r := &fakeRunner{response: "loaded\n"}
	if err := loadIntoKind(context.Background(), r, "my-api:v1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, args := r.LastCall()
	wantArgs := []string{"load", "docker-image", "my-api:v1"}
	if !equal(args, wantArgs) {
		t.Errorf("args=%v want %v (no --name flag when cluster name is empty)", args, wantArgs)
	}
}

func TestLoadIntoKind_WrapsSubprocessError(t *testing.T) {
	r := &fakeRunner{response: "ERROR: kind cluster not found\n", err: errors.New("exit 1")}
	err := loadIntoKind(context.Background(), r, "my-api:v1", "podium")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrKindLoadFailed) {
		t.Errorf("expected ErrKindLoadFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "kind cluster not found") {
		t.Errorf("error should include stderr, got %q", err.Error())
	}
}

func TestLoadIntoKind_RejectsBadTag(t *testing.T) {
	r := &fakeRunner{}
	if err := loadIntoKind(context.Background(), r, "bad tag", "podium"); err == nil {
		t.Fatal("expected error on bad tag")
	}
	if len(r.calls) != 0 {
		t.Errorf("runner should not be invoked on bad tag")
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
