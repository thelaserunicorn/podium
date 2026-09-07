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

// TestDefaultEnvironmentsSeeded verifies that the three default namespaces
// from DECISIONS.md C are inserted by 0001_init.sql.
// Per DECISIONS.md C, environments ARE namespaces; we model them as a single
// table with `namespace` as the unique key.
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
