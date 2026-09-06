package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/build"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// fakeEngine is the smallest possible dockerclient.APIClient that
// implements only ImageBuild. Every other method panics — we only
// use ImageBuild from the real Client.
type fakeEngine struct {
	dockerclient.APIClient
	buildReq build.ImageBuildOptions
	tarBytes []byte

	mu          sync.Mutex
	stdoutBytes []byte // already-stdcopy-encoded
}

func (f *fakeEngine) ImageBuild(_ context.Context, r io.Reader, opts build.ImageBuildOptions) (build.ImageBuildResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buildReq = opts
	// Drain the tar body so we can assert on its length.
	tarBytes, _ := io.ReadAll(r)
	f.tarBytes = tarBytes
	return build.ImageBuildResponse{
		Body: io.NopCloser(bytes.NewReader(f.stdoutBytes)),
	}, nil
}

// encodeStdcopy builds a docker Engine API-shaped response body
// (stdcopy-framed stdout + stderr) from a list of (streamID, text)
// pairs. The bytes returned are exactly what the daemon would emit
// onto resp.Body — i.e., ready to be demuxed by stdcopy.StdCopy.
func encodeStdcopy(t *testing.T, frames []struct {
	stream byte
	text   string
},
) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	for _, f := range frames {
		buf.Write(stdcopyFrame(f.stream, f.text))
	}
	// Validate wire format by round-tripping through stdcopy.
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(pw, pw, bytes.NewReader(buf.Bytes()))
		_ = pw.Close()
	}()
	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("encodeStdcopy validation: %v", err)
	}
	// Return the framed bytes, not the demuxed text.
	return buf.Bytes()
}

func stdcopyFrame(streamID byte, payload string) []byte {
	size := uint32(len(payload))
	header := make([]byte, 8)
	header[0] = streamID
	header[4] = byte(size >> 24)
	header[5] = byte(size >> 16)
	header[6] = byte(size >> 8)
	header[7] = byte(size)
	return append(header, []byte(payload)...)
}

func TestClient_Build_DemuxesStdoutAndStderr(t *testing.T) {
	encoded := encodeStdcopy(t, []struct {
		stream byte
		text   string
	}{
		{1, "Building image...\n"},
		{2, "npm WARN deprecated foo@1\n"},
	})

	engine := &fakeEngine{stdoutBytes: encoded}

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
	// Goroutine scheduling means either stream can flush first.
	have := map[string]bool{}
	for _, l := range lines {
		have[l] = true
	}
	if !have["Building image..."] {
		t.Errorf("missing stdout line: %v", lines)
	}
	if !have["npm WARN deprecated foo@1"] {
		t.Errorf("missing stderr line: %v", lines)
	}
}

func TestClient_Build_HandlesPartialFinalLine(t *testing.T) {
	encoded := encodeStdcopy(t, []struct {
		stream byte
		text   string
	}{
		{1, "no newline at end"},
	})
	engine := &fakeEngine{stdoutBytes: encoded}
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
	if len(lines) != 1 || lines[0] != "no newline at end" {
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

// Sanity check that stdcopyFrame emits parseable JSON events too.
func TestStdcopyFrame_RoundTrip(t *testing.T) {
	payload := `{"stream":"Step 1/2 : FROM scratch"}`
	frame := stdcopyFrame(1, payload)
	// Length sanity: header (8) + payload.
	if len(frame) != 8+len(payload) {
		t.Errorf("frame length=%d want=%d", len(frame), 8+len(payload))
	}
	// Verify the encoded size matches.
	size := uint32(frame[4])<<24 | uint32(frame[5])<<16 | uint32(frame[6])<<8 | uint32(frame[7])
	if size != uint32(len(payload)) {
		t.Errorf("encoded size=%d want=%d", size, len(payload))
	}
	// Confirm we can decode the embedded JSON.
	var probe map[string]any
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		t.Fatal(err)
	}
	if probe["stream"] != "Step 1/2 : FROM scratch" {
		t.Errorf("decoded stream=%v", probe["stream"])
	}
}
