package storage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/podium/podium/internal/storage"
)

func timeNowUnixNano() int64 { return time.Now().UnixNano() }

// uniqueMemDSN returns a shared-cache in-memory DSN that no other test will
// touch. Each test gets its own private SQLite database; this keeps tests
// safe under `t.Parallel()` without sharing state.
func uniqueMemDSN(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("file:test-%s-%d?mode=memory&cache=shared", t.Name(), timeNowUnixNano())
}

// TestOpenInMemory verifies that Open accepts an in-memory DSN and returns
// a usable *sql.DB whose schema is fully migrated (so callers can use it
// directly in tests without separate setup).
func TestOpenInMemory(t *testing.T) {
	t.Parallel()

	db, err := storage.Open(context.Background(), uniqueMemDSN(t), storage.Options{InMemory: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var userVersion int
	if err := db.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	if userVersion < 1 {
		t.Fatalf("expected user_version >= 1, got %d", userVersion)
	}
}

// TestMigrationsAreIdempotent verifies that running migrations multiple times
// against the same database leaves the schema at a single, consistent state.
// Per AGENTS.md §7, "Database initialization should be safe to run more than once."
func TestMigrationsAreIdempotent(t *testing.T) {
	t.Parallel()

	db, err := storage.Open(context.Background(), uniqueMemDSN(t), storage.Options{InMemory: true})
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate (2nd): %v", err)
	}
	if err := storage.Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate (3rd): %v", err)
	}

	// All tables we promise in 0001_init.sql must exist.
	wantTables := []string{
		"users", "applications", "environments", "deployments",
		"environment_variables", "sessions", "deploy_log_lines",
	}
	for _, tbl := range wantTables {
		var n int
		err := db.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&n)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", tbl, err)
		}
		if n != 1 {
			t.Errorf("expected table %q to exist exactly once, found %d", tbl, n)
		}
	}
}

// TestOpenPersistsToFile verifies the on-disk path opens, migrates, and survives
// a close+reopen at the same path. Uses t.TempDir so the file is removed at test end.
func TestOpenPersistsToFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "podium.db")

	db1, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatalf("Open (1st): %v", err)
	}

	_, err = db1.Exec(`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "alice@example.com", "hash")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := storage.Open(context.Background(), path, storage.Options{})
	if err != nil {
		t.Fatalf("Open (2nd): %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	var username string
	err = db2.QueryRow(`SELECT username FROM users WHERE username = ?`, "alice").Scan(&username)
	if err != nil {
		t.Fatalf("select user after reopen: %v", err)
	}
	if username != "alice" {
		t.Fatalf("expected alice, got %q", username)
	}
}

// TestOpenAbsolutePathWorks is a regression test for the modernc/sqlite
// SQLITE_CANTOPEN bug exposed by the M7 Dockerfile. Absolute DSNs like
// `/data/podium.db` were being mis-parsed as scheme-relative URIs;
// Open() now coerces them to `file:/data/podium.db` automatically.
func TestOpenAbsolutePathWorks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// t.TempDir() returns an absolute path on Unix; appending a filename
	// gives us an absolute DSN with a leading `/`.
	dsn := filepath.Join(dir, "abs.db")

	db, err := storage.Open(context.Background(), dsn, storage.Options{})
	if err != nil {
		t.Fatalf("Open with absolute path %q: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("ping after absolute-path open: %v", err)
	}
	if v < 1 {
		t.Fatalf("expected user_version >= 1 after migrations, got %d", v)
	}
}

// TestNormalizeDSNTable is a focused unit test on the DSN-coercion helper.
// Exposed via storage.NormalizeDSN so we don't need a _test-only bridge.
func TestNormalizeDSNTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		mem  bool
		want string
	}{
		{"relative path passes through", "data/podium.db", false, "data/podium.db"},
		{"absolute path gets file: prefix", "/data/podium.db", false, "file:/data/podium.db"},
		{"explicit file: passes through", "file:/data/podium.db", false, "file:/data/podium.db"},
		{"file: with query string passes through", "file:/data/p.db?cache=shared", false, "file:/data/p.db?cache=shared"},
		{"memory URI passes through", "file::memory:", true, "file::memory:"},
		{"memory URI in non-memory mode still passes through", "file:test.db?mode=memory", false, "file:test.db?mode=memory"},
		{"scheme:// not double-prefixed", "https://example.com/db", false, "https://example.com/db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := storage.NormalizeDSN(tc.in, tc.mem)
			if got != tc.want {
				t.Errorf("NormalizeDSN(%q, inMemory=%v) = %q; want %q", tc.in, tc.mem, got, tc.want)
			}
		})
	}
}
func TestDefaultEnvironmentsSeeded(t *testing.T) {
	t.Parallel()

	db, err := storage.Open(context.Background(), uniqueMemDSN(t), storage.Options{InMemory: true})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rows, err := db.Query(`SELECT name, namespace FROM environments ORDER BY id`)
	if err != nil {
		t.Fatalf("select environments: %v", err)
	}
	defer rows.Close()

	want := []struct{ name, ns string }{
		{"Development", "podium-dev"},
		{"Staging", "podium-staging"},
		{"Production", "podium-prod"},
	}
	var got []struct{ name, ns string }
	for rows.Next() {
		var n, ns string
		if err := rows.Scan(&n, &ns); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, struct{ name, ns string }{n, ns})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d environments, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("environments[%d]: want %+v, got %+v", i, want[i], got[i])
		}
	}
}
