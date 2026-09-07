package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func newEnvFixture(t *testing.T) (*Queries, int64, int64) {
	t.Helper()
	db := OpenInMemoryForTest(t)
	q := NewQueries(db)
	ctx := context.Background()

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "a@b", "x")
	uid, _ := res.LastInsertId()
	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'demo', 'u', 8080)`,
		uid)
	appID, _ := res.LastInsertId()
	var envID int64
	db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace='podium-dev'`).Scan(&envID)
	return q, appID, envID
}

func TestEnvVars_UpsertAndList(t *testing.T) {
	q, appID, envID := newEnvFixture(t)
	ctx := context.Background()

	if _, err := q.UpsertEnvVar(ctx, appID, envID, "FOO", "bar", false); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "BAZ", "qux", false); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "DB_PASS", "hunter2", true); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	vars, err := q.ListEnvVars(ctx, appID, envID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 3 {
		t.Fatalf("len=%d want 3", len(vars))
	}
	// Ordered by key: BAZ, DB_PASS, FOO.
	if vars[0].Key != "BAZ" || vars[0].Value != "qux" || vars[0].IsSecret {
		t.Errorf("vars[0]=%+v", vars[0])
	}
	if !vars[1].IsSecret {
		t.Error("DB_PASS should be IsSecret")
	}
	// Update path: re-upsert FOO and confirm value flips.
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "FOO", "bar2", false); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	vars, _ = q.ListEnvVars(ctx, appID, envID)
	for _, v := range vars {
		if v.Key == "FOO" && v.Value != "bar2" {
			t.Errorf("FOO=%q want bar2", v.Value)
		}
	}
}

func TestEnvVar_RedactStripsSecretValues(t *testing.T) {
	q, appID, envID := newEnvFixture(t)
	ctx := context.Background()
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "DB_PASS", "hunter2", true); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "NODE_ENV", "production", false); err != nil {
		t.Fatal(err)
	}
	vars, _ := q.ListEnvVars(ctx, appID, envID)
	if len(vars) != 2 {
		t.Fatalf("len=%d", len(vars))
	}
	for _, v := range vars {
		red := v.Redacted()
		if v.IsSecret && red.Value != "" {
			t.Errorf("Redacted(%s).Value=%q want empty", v.Key, red.Value)
		}
		if !v.IsSecret && red.Value == "" {
			t.Errorf("Redacted(%s).Value should be preserved (got empty)", v.Key)
		}
	}
}

func TestDeleteEnvVar_NotFoundReturnsSqlErrNoRows(t *testing.T) {
	q, appID, envID := newEnvFixture(t)
	err := q.DeleteEnvVar(context.Background(), appID, envID, "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err=%v want sql.ErrNoRows", err)
	}
}

func TestDeleteEnvVar_HappyPath(t *testing.T) {
	q, appID, envID := newEnvFixture(t)
	ctx := context.Background()
	if _, err := q.UpsertEnvVar(ctx, appID, envID, "FOO", "bar", false); err != nil {
		t.Fatal(err)
	}
	if err := q.DeleteEnvVar(ctx, appID, envID, "FOO"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	vars, _ := q.ListEnvVars(ctx, appID, envID)
	if len(vars) != 0 {
		t.Errorf("len after delete=%d want 0", len(vars))
	}
}
