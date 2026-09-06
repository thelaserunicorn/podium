package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SourceFetcher pulls the application source into a local directory.
// In the MVP the implementation is `git clone --depth=1`; private
// repos and non-GitHub providers are out of scope (DECISIONS.md A).
type SourceFetcher interface {
	Fetch(ctx context.Context, repoURL, destDir string) error
}

// GitSourceFetcher is the production implementation.
type GitSourceFetcher struct {
	Runner cmdRunner
}

// NewGitSourceFetcher returns a fetcher that shells out to git.
func NewGitSourceFetcher() *GitSourceFetcher {
	return &GitSourceFetcher{Runner: osRunner{}}
}

// Fetch clones repoURL into destDir as a shallow clone. If destDir
// already exists and is a valid git repo, Fetch does nothing (idempotent).
func (g *GitSourceFetcher) Fetch(ctx context.Context, repoURL, destDir string) error {
	if repoURL == "" {
		return fmt.Errorf("repository URL is empty")
	}
	if filepath.Base(destDir) == "." || destDir == "" {
		return fmt.Errorf("destDir must be a non-empty path")
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	out, err := g.Runner.Run(ctx, "git", "clone", "--depth=1", repoURL, destDir)
	if err != nil {
		return fmt.Errorf("git clone failed: %s", trim(out))
	}
	return nil
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// discardCloser wraps io.Discard as an io.ReadCloser for callers that
// need to drain a stream without using it. Exported for tests.
func discardCloser() io.ReadCloser {
	return io.NopCloser(struct{ io.Reader }{})
}