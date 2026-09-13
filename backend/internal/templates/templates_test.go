package templates_test

import (
	"encoding/json"
	"testing"

	"github.com/podium/podium/internal/templates"
)

// TestCatalogContainsExpectedLanguages guards the public catalog against
// accidental removal. The Templates tab in the dashboard relies on
// Node, Python, and Go being present; if someone deletes one the UI
// silently loses a card and the bug only shows up in screenshots.
func TestCatalogContainsExpectedLanguages(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"node": false, "python": false, "go": false}
	for _, tpl := range templates.All() {
		if _, ok := want[tpl.ID]; ok {
			want[tpl.ID] = true
		}
		// Every catalog entry must carry enough info to create an
		// application: a repository URL, a non-zero container port,
		// and a non-empty language label.
		if tpl.RepositoryURL == "" {
			t.Errorf("template %q: empty repository_url", tpl.ID)
		}
		if tpl.ContainerPort < 1 || tpl.ContainerPort > 65535 {
			t.Errorf("template %q: invalid container_port %d", tpl.ID, tpl.ContainerPort)
		}
		if tpl.Language == "" {
			t.Errorf("template %q: empty language", tpl.ID)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("catalog missing template %q", id)
		}
	}
}

// TestAllReturnsCopy makes sure callers cannot mutate the package-level
// catalog by appending to the slice returned from All().
func TestAllReturnsCopy(t *testing.T) {
	t.Parallel()

	first := templates.All()
	if len(first) == 0 {
		t.Fatal("catalog is empty; All() should always return at least one entry")
	}

	// Mutate the returned slice and re-fetch; the package state must
	// not have changed.
	first[0].Name = "should-not-leak"

	second := templates.All()
	for _, tpl := range second {
		if tpl.Name == "should-not-leak" {
			t.Fatalf("All() leaked caller mutation: template %q was modified across calls", tpl.ID)
		}
	}
}

// TestFindRoundtrip walks the catalog through Find() and confirms the
// ID → Template mapping matches the slice returned by All().
func TestFindRoundtrip(t *testing.T) {
	t.Parallel()

	for _, want := range templates.All() {
		got, ok := templates.Find(want.ID)
		if !ok {
			t.Errorf("Find(%q): not found", want.ID)
			continue
		}
		if got.ID != want.ID {
			t.Errorf("Find(%q).ID = %q, want %q", want.ID, got.ID, want.ID)
		}
		if got.RepositoryURL != want.RepositoryURL {
			t.Errorf("Find(%q).RepositoryURL = %q, want %q", want.ID, got.RepositoryURL, want.RepositoryURL)
		}
	}

	if _, ok := templates.Find("does-not-exist"); ok {
		t.Error("Find for unknown id should return ok=false")
	}
}

// TestTemplateJSONShape pins the wire format consumed by the frontend.
// A future refactor that renames a field would otherwise silently break
// the dashboard's templates tab.
func TestTemplateJSONShape(t *testing.T) {
	t.Parallel()

	got, ok := templates.Find("node")
	if !ok {
		t.Fatal("node template missing")
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"id":"node"`, `"language":"Node"`, `"container_port":3000`} {
		if !contains(body, key) {
			t.Errorf("node template JSON missing %s; got %s", key, body)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
