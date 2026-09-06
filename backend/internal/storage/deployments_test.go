package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestQueries(t *testing.T) *Queries {
	t.Helper()
	dsn := fmt.Sprintf("file:test-%s-%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	db, err := Open(context.Background(), dsn, Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewQueries(db)
}

func seedUserAppEnv(t *testing.T, q *Queries) (userID, appID, envID int64) {
	t.Helper()
	ctx := context.Background()

	res, err := q.db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = res.LastInsertId()

	res, err = q.db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, ?, ?, ?)`,
		userID, "my-api", "https://github.com/x/y", 8080)
	if err != nil {
		t.Fatal(err)
	}
	appID, _ = res.LastInsertId()

	if err := q.db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace = 'podium-dev'`).Scan(&envID); err != nil {
		t.Fatal(err)
	}
	return userID, appID, envID
}

func TestCreateAndGetDeployment(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()

	id, err := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	d, err := q.GetDeployment(ctx, id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.Status != StatusQueued {
		t.Errorf("status=%q want QUEUED", d.Status)
	}
	if d.Image != "my-api:v1" {
		t.Errorf("image=%q", d.Image)
	}
	if d.Version != 1 || d.Replicas != 3 {
		t.Errorf("version=%d replicas=%d", d.Version, d.Replicas)
	}
}

func TestSetDeploymentStatus_SetsStartedAt(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()
	id, _ := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")

	if err := q.SetDeploymentStatus(ctx, id, StatusBuilding, ""); err != nil {
		t.Fatal(err)
	}
	d, _ := q.GetDeployment(ctx, id)
	if d.Status != StatusBuilding {
		t.Errorf("status=%q", d.Status)
	}
	if !d.StartedAt.Valid {
		t.Errorf("started_at should be set")
	}
}

func TestSetDeploymentStatus_SetsFinishedAtOnTerminal(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()
	id, _ := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")

	if err := q.SetDeploymentStatus(ctx, id, StatusFailed, "build_failed"); err != nil {
		t.Fatal(err)
	}
	d, _ := q.GetDeployment(ctx, id)
	if d.Status != StatusFailed {
		t.Errorf("status=%q", d.Status)
	}
	if !d.FinishedAt.Valid {
		t.Errorf("finished_at should be set on FAILED")
	}
	if !d.Reason.Valid || d.Reason.String != "build_failed" {
		t.Errorf("reason=%v", d.Reason)
	}
}

func TestHasActiveDeployment(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()

	// No deployments yet.
	active, err := q.HasActiveDeployment(ctx, appID, envID)
	if err != nil || active {
		t.Fatalf("active=%v err=%v want false,nil", active, err)
	}

	id, _ := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")
	active, _ = q.HasActiveDeployment(ctx, appID, envID)
	if !active {
		t.Errorf("QUEUED deployment should be active")
	}

	if err := q.SetDeploymentStatus(ctx, id, StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	active, _ = q.HasActiveDeployment(ctx, appID, envID)
	if active {
		t.Errorf("RUNNING should not be active")
	}
}

func TestListDeployments_NewestFirst(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()
	q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")
	q.CreateDeployment(ctx, appID, envID, 2, 3, "my-api:v2")
	q.CreateDeployment(ctx, appID, envID, 3, 3, "my-api:v3")

	out, err := q.ListDeployments(ctx, appID, envID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 got %d", len(out))
	}
	if out[0].Version != 3 || out[1].Version != 2 || out[2].Version != 1 {
		t.Errorf("ordering: %d %d %d", out[0].Version, out[1].Version, out[2].Version)
	}
}

func TestAppendAndReadLogLines(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()
	id, _ := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")

	for _, line := range []string{"step 1", "step 2", "step 3"} {
		if err := q.AppendLogLine(ctx, id, line); err != nil {
			t.Fatal(err)
		}
	}

	lines, err := q.LogLinesSince(ctx, id, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0].Line != "step 1" || lines[2].Line != "step 3" {
		t.Errorf("lines: %v", lines)
	}
}

func TestLogLinesSince_CursorWorks(t *testing.T) {
	q := newTestQueries(t)
	_, appID, envID := seedUserAppEnv(t, q)
	ctx := context.Background()
	id, _ := q.CreateDeployment(ctx, appID, envID, 1, 3, "my-api:v1")

	q.AppendLogLine(ctx, id, "old")
	ts, _ := q.LatestLogTimestamp(ctx, id)
	t.Logf("cursor=%q", FormatPodTS(ts))
	t.Logf("old ts parsed=%v", ts)

	time.Sleep(5 * time.Millisecond)

	q.AppendLogLine(ctx, id, "new")
	lines, _ := q.LogLinesSince(ctx, id, ts)
	t.Logf("lines=%+v", lines)
	if len(lines) != 1 || lines[0].Line != "new" {
		t.Errorf("since filter: %v", lines)
	}
}

func TestEnvironmentIDByNamespace(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()

	id, err := q.EnvironmentIDByNamespace(ctx, "podium-dev")
	if err != nil || id == 0 {
		t.Fatalf("podium-dev: id=%d err=%v", id, err)
	}
	_, err = q.EnvironmentIDByNamespace(ctx, "nonexistent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestApplicationNextVersion_Monotonic(t *testing.T) {
	q := newTestQueries(t)
	_, appID, _ := seedUserAppEnv(t, q)
	ctx := context.Background()

	v1, _ := q.ApplicationNextVersion(ctx, appID)
	v2, _ := q.ApplicationNextVersion(ctx, appID)
	v3, _ := q.ApplicationNextVersion(ctx, appID)

	if v1 != 1 || v2 != 2 || v3 != 3 {
		t.Errorf("got %d %d %d want 1 2 3", v1, v2, v3)
	}
}

func zeroTime() (t sql.NullTime) { return }
