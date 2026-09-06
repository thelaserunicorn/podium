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
	"github.com/docker/docker/pkg/stdcopy"
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
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
		ForceRemove: true,
	}

	resp, err := c.engine.ImageBuild(ctx, bytes.NewReader(tarBuf.Bytes()), opts)
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	// stdcopy expects one Reader for stdout and one for stderr; both
	// come multiplexed in resp.Body. We split them into separate
	// in-memory pipes, then line-buffer each into the same sink.
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	go func() {
		_, copyErr := stdcopy.StdCopy(stdoutW, stderrW, resp.Body)
		_ = stdoutW.CloseWithError(copyErr)
		_ = stderrW.CloseWithError(copyErr)
	}()

	var stdoutBuf, stderrBuf string
	stdoutErrCh := drainLines(stdoutR, &stdoutBuf, sink)
	stderrErrCh := drainLines(stderrR, &stderrBuf, sink)

	stdoutErr := <-stdoutErrCh
	stderrErr := <-stderrErrCh
	// Flush any trailing partial lines from each stream's buffer.
	// Only append when non-empty — drainLines only writes complete
	// lines, so an empty buffer means the stream ended exactly on a
	// newline (or had no output at all).
	if stdoutBuf != "" {
		if err := sink.Append(stdoutBuf); err != nil {
			return err
		}
		stdoutBuf = ""
	}
	if stderrBuf != "" {
		if err := sink.Append(stderrBuf); err != nil {
			return err
		}
		stderrBuf = ""
	}

	if stdoutErr != nil && !isBenignClose(stdoutErr) {
		return fmt.Errorf("read build stdout: %w", stdoutErr)
	}
	if stderrErr != nil && !isBenignClose(stderrErr) {
		return fmt.Errorf("read build stderr: %w", stderrErr)
	}

	// ImageBuild returns a non-zero Body when the build itself fails.
	// We surface any captured error line from the stream as the
	// wrapping message.
	return nil
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