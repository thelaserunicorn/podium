package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/build"
	dockerclient "github.com/docker/docker/client"
)

// Client is the real docker.Builder implementation. It talks to the
// local Docker daemon via the Engine API and shells out to `kind load
// docker-image` to push images into the kind cluster.
//
// The daemon endpoint is taken from DOCKER_HOST (or the defaults used
// by `docker client.NewClientWithOpts(client.FromEnv)`) so the binary
// works in container runtimes as long as the socket is mounted.
type Client struct {
	engine dockerclient.APIClient
	runner cmdRunner
}

// NewClient constructs a Client. If `engine` is nil, a default
// `client.FromEnv` connection is used; if `runner` is nil, the
// `osRunner` is used.
func NewClient(engine dockerclient.APIClient, runner cmdRunner) (*Client, error) {
	if engine == nil {
		engine, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv)
		if err != nil {
			return nil, fmt.Errorf("docker client: %w", err)
		}
		return &Client{engine: engine, runner: osRunner{}}, nil
	}
	r := runner
	if r == nil {
		r = osRunner{}
	}
	return &Client{engine: engine, runner: r}, nil
}

// Build packs the contents of `dir` (the git-cloned source) into a
// tar stream, sends it to the Docker daemon with ImageBuild, demuxes
// the multiplexed stream with stdcopy, and pipes each stdout/stderr
// line into the LogSink.
//
// On a non-zero exit, the function returns ErrBuildFailed with the
// captured error message. On context cancellation the in-flight build
// is aborted and the partial output is returned as-is.
func (c *Client) Build(ctx context.Context, dir, tag string, sink LogSink) error {
	if sink == nil {
		sink = NopSink{}
	}
	if err := ValidateTag(tag); err != nil {
		return err
	}

	tarBuf, err := buildTar(dir)
	if err != nil {
		return fmt.Errorf("package source: %w", err)
	}

	opts := build.ImageBuildOptions{
		Tags:        []string{tag},
		Dockerfile:  "Dockerfile",
		Remove:      true,
		ForceRemove: true,
		// BuildKit is required for Dockerfile features the legacy
		// builder doesn't support, e.g. `COPY --chmod=…` (the error
		// message users hit: "the --chmod option requires BuildKit").
		// The Docker daemon selects BuildKit when Version = "2" and
		// the daemon supports it; older daemons fall back to the
		// legacy builder rather than failing the request.
		Version: build.BuilderBuildKit,
	}

	resp, err := c.engine.ImageBuild(ctx, bytes.NewReader(tarBuf.Bytes()), opts)
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	// The Docker Engine /build endpoint returns the build progress as
	// newline-delimited JSON (one JSON object per line: stream /
	// status / errorDetail / aux). It is NOT a stdcopy-multiplexed
	// stream — that framing is reserved for attach/exec. Read the
	// body directly and line-split into the sink.
	var capture linesBuffer
	var buf string
	errCh := drainLines(resp.Body, &buf, &teeSink{sink: sink, capture: &capture})

	if err := <-errCh; err != nil && !isBenignClose(err) {
		return fmt.Errorf("read build stream: %w", err)
	}
	if buf != "" {
		if err := sink.Append(buf); err != nil {
			return err
		}
	}

	// ImageBuild returns 200 even when the build itself fails — the
	// errorDetail is embedded in the response body. Scan the captured
	// lines for it and return ErrBuildFailed if found.
	if msg := findBuildError(capture.Lines()); msg != "" {
		return fmt.Errorf("%w: %s", ErrBuildFailed, msg)
	}
	return nil
}

// teeSink forwards each line to both the user-facing sink (so the
// build-log viewer sees it) and an in-memory capture (so Build can
// inspect the stream after it drains).
type teeSink struct {
	sink    LogSink
	capture *linesBuffer
}

func (t *teeSink) Append(line string) error {
	t.capture.append(line)
	return t.sink.Append(line)
}

// linesBuffer is a tiny mutex-free append-only buffer (Build is
// single-goroutine wrt the capture; drainLines writes from one
// goroutine, Build reads after the drain channel closes).
type linesBuffer struct {
	buf []string
}

func (l *linesBuffer) append(line string) {
	l.buf = append(l.buf, line)
}

func (l *linesBuffer) Lines() []string {
	return l.buf
}

// findBuildError scans the captured stderr lines for the daemon's
// build-failure marker. The Docker daemon emits one JSON object per
// line in the response body; build errors carry an `errorDetail` key.
// The marker appears verbatim in the stderr stream after stdcopy
// demux. We look for the literal `errorDetail` substring in any line;
// if found we return the line (trimmed) as the failure message.
func findBuildError(lines []string) string {
	for _, ln := range lines {
		if strings.Contains(ln, "errorDetail") {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

// LoadIntoKind delegates to the shared loadIntoKind with the client's
// runner.
func (c *Client) LoadIntoKind(ctx context.Context, tag string) error {
	return loadIntoKind(ctx, c.runner, tag)
}

// drainLines reads from r in a goroutine, splits on newlines, and
// forwards each complete line to sink. The leftover partial line is
// kept in `buf` for the caller to flush after EOF.
func drainLines(r io.Reader, buf *string, sink LogSink) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		var chunk strings.Builder
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				chunk.Reset()
				chunk.Write(tmp[:n])
				for _, line := range splitLines(buf, chunk.String()) {
					if appendErr := sink.Append(line); appendErr != nil {
						ch <- appendErr
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// isBenignClose returns true for the EOF / closed-pipe errors that
// stdcopy produces at the end of a successful build.
func isBenignClose(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "closed pipe") || strings.Contains(s, "already closed")
}

// buildTar walks `dir` and returns a tar archive containing every
// regular file, with paths relative to `dir`. The archive is small
// enough to keep in memory for the MVP — the source repos we expect
// (single Dockerfile + app code) are well under 100 MB.
func buildTar(dir string) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	if err := tarWalk(dir, dir, tw); err != nil {
		_ = tw.Close()
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}
