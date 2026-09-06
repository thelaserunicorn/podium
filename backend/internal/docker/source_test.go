package docker

import (
	"context"
	"errors"
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