package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/build"
	dockerclient "github.com/docker/docker/client"
)

// fakeEngine is the smallest possible dockerclient.APIClient that
// implements only ImageBuild. Every other method panics — we only
// use ImageBuild from the real Client.
type fakeEngine struct {
	dockerclient.APIClient
	buildReq build.ImageBuildOptions
	tarBytes []byte

	mu       sync.Mutex
	bodyText string // raw response body text (newline-delimited JSON, no stdcopy)
}

func (f *fakeEngine) ImageBuild(_ context.Context, r io.Reader, opts build.ImageBuildOptions) (build.ImageBuildResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buildReq = opts
	tarBytes, _ := io.ReadAll(r)
	f.tarBytes = tarBytes
	return build.ImageBuildResponse{
		Body: io.NopCloser(strings.NewReader(f.bodyText)),
	}, nil
}

func TestClient_Build_StreamsNewlineDelimitedJSON(t *testing.T) {
	// The Docker Engine /build endpoint returns newline-delimited
	// JSON (one object per line), not stdcopy-framed. Both stream
	// and errorDetail events live on the same body. Build passes
	// the raw JSON through to the sink so the user can see the
	// full daemon response in the build-log panel.
	body := strings.Join([]string{
		`{"stream":"Building image...\n"}`,
		`{"stream":" ---\u003e abc123\n"}`,
		"",
	}, "\n")

	engine := &fakeEngine{bodyText: body}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &recordingSink{}
	if err := c.Build(context.Background(), dir, "my-api:v1", sink); err != nil {
		t.Fatalf("Build: %v", err)
	}
	lines := sink.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	have := map[string]bool{}
	for _, l := range lines {
		have[l] = true
	}
	if !have[`{"stream":"Building image...\n"}`] {
		t.Errorf("missing first line: %v", lines)
	}
	if !have[`{"stream":" ---\u003e abc123\n"}`] {
		t.Errorf("missing second line: %v", lines)
	}
}

func TestClient_Build_HandlesPartialFinalLine(t *testing.T) {
	// The daemon may close the body without a trailing newline;
	// Build should still flush the partial final line.
	engine := &fakeEngine{bodyText: `{"stream":"no newline at end"}`}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	if err := c.Build(context.Background(), dir, "x:v1", sink); err != nil {
		t.Fatal(err)
	}
	lines := sink.Lines()
	if len(lines) != 1 || lines[0] != `{"stream":"no newline at end"}` {
		t.Errorf("got %v", lines)
	}
}

func TestClient_Build_PacksTarWithDockerfile(t *testing.T) {
	engine := &fakeEngine{}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Build(context.Background(), dir, "x:v1", NopSink{}); err != nil {
		t.Fatal(err)
	}
	if len(engine.tarBytes) == 0 {
		t.Fatal("tar was empty")
	}
	tarStr := string(engine.tarBytes)
	if !strings.Contains(tarStr, "Dockerfile") {
		t.Errorf("tar missing Dockerfile name")
	}
	if !strings.Contains(tarStr, "FROM scratch") {
		t.Errorf("tar missing Dockerfile content")
	}
}

func TestClient_Build_RejectsBadTagBeforeTouchingDaemon(t *testing.T) {
	engine := &fakeEngine{}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Build(context.Background(), dir, "bad tag", NopSink{}); err == nil {
		t.Fatal("expected error on bad tag")
	}
	if len(engine.tarBytes) != 0 {
		t.Errorf("daemon should not be called on bad tag")
	}
}

func TestClient_Build_PropagatesDaemonError(t *testing.T) {
	engine := &errorEngine{err: io.ErrUnexpectedEOF}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = c.Build(context.Background(), dir, "x:v1", NopSink{})
	if err == nil || !strings.Contains(err.Error(), "image build") {
		t.Errorf("expected wrapped image build error, got %v", err)
	}
}

// TestClient_Build_DetectsErrorDetailInBody: the Docker daemon
// reports build failures (e.g. missing Dockerfile) by returning 200
// OK with the errorDetail embedded in the response body. The moby
// SDK does not surface this — engine.ImageBuild returns nil — so
// Build() must scan the captured lines for the marker. Without
// this check the orchestrator would proceed to the k8s apply step
// with no image to load.
func TestClient_Build_DetectsErrorDetailInBody(t *testing.T) {
	body := strings.Join([]string{
		`{"stream":"Sending build context to Docker daemon\n"}`,
		`{"errorDetail":{"code":1,"message":"Cannot locate specified Dockerfile: Dockerfile"},"error":"Cannot locate specified Dockerfile: Dockerfile"}`,
		"",
	}, "\n")
	engine := &fakeEngine{bodyText: body}
	c, err := NewClient(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sink := &recordingSink{}
	err = c.Build(context.Background(), dir, "x:v1", sink)
	if err == nil {
		t.Fatal("Build should fail when daemon reports errorDetail in body")
	}
	if !errors.Is(err, ErrBuildFailed) {
		t.Errorf("expected ErrBuildFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "Cannot locate specified Dockerfile") {
		t.Errorf("error should carry the daemon message, got %v", err)
	}
	// Both lines should still be visible in the log stream so the
	// user sees the full build context in the build-log panel.
	if len(sink.Lines()) < 2 {
		t.Errorf("expected at least 2 captured lines, got %d: %v", len(sink.Lines()), sink.Lines())
	}
}

func TestClient_LoadIntoKind_UsesRunner(t *testing.T) {
	engine := &fakeEngine{}
	r := &fakeRunner{response: "loaded\n"}
	c, err := NewClient(engine, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.LoadIntoKind(context.Background(), "my-api:v1"); err != nil {
		t.Fatal(err)
	}
	name, args := r.LastCall()
	if name != "kind" || !equal(args, []string{"load", "docker-image", "my-api:v1"}) {
		t.Errorf("runner call: %s %v", name, args)
	}
}

// errorEngine always returns the configured error from ImageBuild.
type errorEngine struct {
	dockerclient.APIClient
	err error
}

func (e *errorEngine) ImageBuild(_ context.Context, _ io.Reader, _ build.ImageBuildOptions) (build.ImageBuildResponse, error) {
	return build.ImageBuildResponse{Body: io.NopCloser(http.NoBody)}, e.err
}

// Compile-time assertion: errorEngine also satisfies dockerclient.APIClient.
var _ dockerclient.APIClient = (*errorEngine)(nil)
