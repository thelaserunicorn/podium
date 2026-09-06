package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitSourceFetcher_ShallowClones(t *testing.T) {
	r := &fakeRunner{response: "Cloning into 'foo'...\n"}
	f := &GitSourceFetcher{Runner: r}
	if err := f.Fetch(context.Background(), "https://github.com/x/y", "/tmp/podium-sources/y"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	name, args := r.LastCall()
	if name != "git" {
		t.Errorf("exec=%q want git", name)
	}
	want := []string{"clone", "--depth=1", "https://github.com/x/y", "/tmp/podium-sources/y"}
	for i, a := range want {
		if i >= len(args) || args[i] != a {
			t.Errorf("args=%v want %v", args, want)
			break
		}
	}
}

func TestGitSourceFetcher_RejectsEmptyURL(t *testing.T) {
	r := &fakeRunner{}
	f := &GitSourceFetcher{Runner: r}
	if err := f.Fetch(context.Background(), "", "/tmp/x"); err == nil {
		t.Fatal("expected error")
	}
	if len(r.calls) != 0 {
		t.Errorf("runner should not be invoked")
	}
}

func TestGitSourceFetcher_WrapsCloneError(t *testing.T) {
	r := &fakeRunner{response: "fatal: repository not found\n", err: errors.New("exit 128")}
	f := &GitSourceFetcher{Runner: r}
	err := f.Fetch(context.Background(), "https://github.com/x/y", "/tmp/podium-sources/y")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "repository not found") {
		t.Errorf("err=%v", err)
	}
}

// TestGitSourceFetcher_ReusesExistingClone verifies that a repeated
// Fetch into a directory that already contains a .git subdirectory
// is a no-op (the prior clone is reused, no `git clone` invocation).
// This is the idempotency contract the orchestrator relies on for
// re-deploys of the same app.
func TestGitSourceFetcher_ReusesExistingClone(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "app-1-smoke-app")
	if err := os.MkdirAll(filepath.Join(dest, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	f := &GitSourceFetcher{Runner: r}
	if err := f.Fetch(context.Background(), "https://github.com/x/y", dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("runner should not be invoked for existing clone, got calls=%v", r.calls)
	}
}

// TestGitSourceFetcher_WipesStalePartialClone verifies that a
// directory present at destDir but missing .git (a stale partial
// clone from a prior failed run) is removed before `git clone` runs,
// so the clone doesn't fail with "destination path already exists
// and is not an empty directory".
func TestGitSourceFetcher_WipesStalePartialClone(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "app-1-smoke-app")
	// Make a stale partial clone: directory exists, no .git inside.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "stray.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{response: "Cloning into 'app-1-smoke-app'...\n"}
	f := &GitSourceFetcher{Runner: r}
	if err := f.Fetch(context.Background(), "https://github.com/x/y", dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stray.txt")); !os.IsNotExist(err) {
		t.Errorf("stale file should be wiped, stat err=%v", err)
	}
	if len(r.calls) != 1 {
		t.Errorf("expected 1 git invocation after wipe, got %d", len(r.calls))
	}
}
