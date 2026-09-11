package application

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/podium/podium/internal/storage"
)

// recordingEnvStore implements EnvStore. Tests assert on the writes
// the EnvService makes so the k8s push path can be exercised without
// a real clientset.
type recordingEnvStore struct {
	mu         sync.Mutex
	configMaps []map[string]string
	secrets    []map[string]string
	deletedCM  int
	deletedSec int
	returnErr  error
}

func (r *recordingEnvStore) writeEnv(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.returnErr != nil {
		return r.returnErr
	}
	clone := make(map[string]string, len(data))
	for k, v := range data {
		clone[k] = v
	}
	r.configMaps = append(r.configMaps, clone)
	return nil
}

func (r *recordingEnvStore) ApplyConfigMap(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error {
	return r.writeEnv(ctx, appName, appID, namespace, data)
}

func (r *recordingEnvStore) ApplySecret(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.returnErr != nil {
		return r.returnErr
	}
	clone := make(map[string]string, len(data))
	for k, v := range data {
		clone[k] = v
	}
	r.secrets = append(r.secrets, clone)
	return nil
}

func (r *recordingEnvStore) DeleteConfigMap(ctx context.Context, appName string, appID int64, namespace string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedCM++
	return nil
}

func (r *recordingEnvStore) DeleteSecret(ctx context.Context, appName string, appID int64, namespace string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedSec++
	return nil
}

func newEnvServiceFixture(t *testing.T) (*EnvService, *Application, int64, *recordingEnvStore) {
	t.Helper()
	db := storage.OpenInMemoryForTest(t)
	ctx := context.Background()

	res, _ := db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, status) VALUES (?, ?, ?, 'USER', 'APPROVED')`,
		"alice", "a@b", "x")
	uid, _ := res.LastInsertId()
	res, _ = db.ExecContext(ctx,
		`INSERT INTO applications (user_id, name, repository_url, container_port) VALUES (?, 'demo', 'https://github.com/x/y', 8080)`,
		uid)
	appID, _ := res.LastInsertId()

	var envID int64
	db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace='podium-dev'`).Scan(&envID)

	store := &recordingEnvStore{}
	svc := NewEnvService(db, store)
	return svc, &Application{ID: appID, Name: "demo", UserID: uid}, envID, store
}

func TestEnvService_SetAndListRedactsSecret(t *testing.T) {
	svc, app, envID, store := newEnvServiceFixture(t)
	ctx := context.Background()

	if _, err := svc.Set(ctx, app, envID, "podium-dev", SetInput{Key: "NODE_ENV", Value: "production"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := svc.Set(ctx, app, envID, "podium-dev", SetInput{Key: "DB_PASS", Value: "hunter2", IsSecret: true}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	vars, err := svc.List(ctx, app, envID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vars) != 2 {
		t.Fatalf("len=%d", len(vars))
	}
	for _, v := range vars {
		if v.IsSecret && v.Value != "" {
			t.Errorf("Redacted(%s).Value=%q want empty", v.Key, v.Value)
		}
		if !v.IsSecret && v.Value == "" {
			t.Errorf("Redacted(%s).Value should be preserved", v.Key)
		}
	}

	// The store should have received both writes (the Secret write
	// goes through DeleteSecret + ApplySecret in pushK8s).
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.configMaps) != 2 {
		t.Errorf("configMap writes=%d want 2", len(store.configMaps))
	}
	if len(store.secrets) != 2 {
		t.Errorf("secret writes=%d want 2", len(store.secrets))
	}
}

func TestEnvService_SetRejectsBadKey(t *testing.T) {
	svc, app, envID, _ := newEnvServiceFixture(t)
	for _, key := range []string{"", "1FOO", "FOO BAR", "FOO;BAR", strings.Repeat("a", 129)} {
		_, err := svc.Set(context.Background(), app, envID, "podium-dev", SetInput{Key: key, Value: "v"})
		if !errors.Is(err, ErrInvalidEnvKey) {
			t.Errorf("key=%q err=%v want ErrInvalidEnvKey", key, err)
		}
	}
}

func TestEnvService_SetRejectsLongValue(t *testing.T) {
	svc, app, envID, _ := newEnvServiceFixture(t)
	_, err := svc.Set(context.Background(), app, envID, "podium-dev", SetInput{Key: "FOO", Value: strings.Repeat("x", 8193)})
	if !errors.Is(err, ErrInvalidEnvValue) {
		t.Errorf("err=%v want ErrInvalidEnvValue", err)
	}
}

func TestEnvService_DeletePropagatesToK8s(t *testing.T) {
	svc, app, envID, store := newEnvServiceFixture(t)
	ctx := context.Background()
	if _, err := svc.Set(ctx, app, envID, "podium-dev", SetInput{Key: "FOO", Value: "bar"}); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	beforeDeletes := store.deletedCM + store.deletedSec
	store.mu.Unlock()

	if err := svc.Delete(ctx, app, envID, "podium-dev", "FOO"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	vars, _ := svc.List(ctx, app, envID)
	if len(vars) != 0 {
		t.Errorf("len after delete=%d", len(vars))
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deletedCM+store.deletedSec <= beforeDeletes {
		t.Errorf("expected Delete* calls; cm=%d sec=%d", store.deletedCM, store.deletedSec)
	}
}

func TestEnvService_DeleteUnknownKeyReturnsErrNoRows(t *testing.T) {
	svc, app, envID, _ := newEnvServiceFixture(t)
	err := svc.Delete(context.Background(), app, envID, "podium-dev", "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err=%v want sql.ErrNoRows", err)
	}
}

func TestEnvService_PropagatesK8sError(t *testing.T) {
	svc, app, envID, store := newEnvServiceFixture(t)
	store.returnErr = errors.New("k8s unavailable")
	_, err := svc.Set(context.Background(), app, envID, "podium-dev", SetInput{Key: "FOO", Value: "bar"})
	if err == nil {
		t.Fatal("expected error from k8s push failure")
	}
}
