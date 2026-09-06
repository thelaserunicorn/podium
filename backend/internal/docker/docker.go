// Package docker wraps the Docker Engine API for image builds and the
// `kind load docker-image` helper that pushes the built image into the
// local kind cluster's node containers.
//
// The package exposes a small Builder interface so tests can swap in a
// fake without spinning up the Docker daemon.
package docker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// LogSink receives build output line-by-line. Implementations are
// expected to be safe for concurrent use; the docker SDK may emit
// stdout/stderr from separate goroutines.
type LogSink interface {
	Append(line string) error
}

// NopSink discards all lines. Useful in tests.
type NopSink struct{}

func (NopSink) Append(string) error { return nil }

// Builder is the surface area the deployment orchestrator depends on.
// Splitting it from the rest of the package keeps tests focused on
// behaviour ("does the build pipeline run?") rather than on Docker
// engine wiring.
type Builder interface {
	// Build builds the image described by the Dockerfile in `dir`,
	// tags it `tag`, and writes progress lines to `sink`.
	Build(ctx context.Context, dir, tag string, sink LogSink) error
	// LoadIntoKind makes `tag` available to the kind cluster by
	// shelling out to `kind load docker-image <tag>`. Podium is
	// assumed to be running on the host with the `kind` CLI on PATH
	// (DECISIONS.md A).
	LoadIntoKind(ctx context.Context, tag string) error
}

// ErrBuildFailed indicates a non-zero exit from the docker build
// process. The error message is safe to surface to the user.
var ErrBuildFailed = errors.New("docker build failed")

// ErrKindLoadFailed indicates `kind load docker-image` returned a
// non-zero exit code. The error wraps stderr for diagnostics.
var ErrKindLoadFailed = errors.New("kind load failed")

// ValidateTag ensures the image tag is non-empty and uses only
// characters the Docker daemon accepts. This is a syntactic check; the
// daemon will still reject obviously bad tags, but rejecting here gives
// earlier feedback.
func ValidateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("tag is empty")
	}
	for _, r := range tag {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("tag %q contains invalid character %q", tag, r)
		}
	}
	return nil
}

// splitLines demuxes a stream of bytes into newline-delimited lines,
// buffering any trailing partial line in `*buf`. Returns nil if no
// complete lines were found.
func splitLines(buf *string, chunk string) []string {
	*buf += chunk
	out := []string{}
	for {
		i := strings.IndexByte(*buf, '\n')
		if i < 0 {
			break
		}
		line := (*buf)[:i]
		*buf = (*buf)[i+1:]
		line = strings.TrimRight(line, "\r")
		out = append(out, line)
	}
	return out
}

// cmdRunner abstracts subprocess invocation so tests can swap it.
type cmdRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type osRunner struct{}

func (osRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// loadIntoKind is the shared implementation behind the real Client
// and the test fake. The runner does the actual subprocess work; this
// just formats the command and classifies the error.
func loadIntoKind(ctx context.Context, runner cmdRunner, tag string) error {
	if err := ValidateTag(tag); err != nil {
		return err
	}
	out, err := runner.Run(ctx, "kind", "load", "docker-image", tag)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrKindLoadFailed, strings.TrimSpace(out))
	}
	return nil
}
