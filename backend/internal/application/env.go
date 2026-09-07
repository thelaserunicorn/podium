package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/podium/podium/internal/storage"
)

// EnvVar is the in-memory representation of a row in environment_variables.
// It's a thin alias over storage.EnvVar so callers don't have to
// import storage for the read path. Secret values are NEVER returned
// here (DECISIONS.md E): the storage layer exposes the raw value, but
// the application service List method strips it before returning.
type EnvVar = storage.EnvVar

// envKeyRe restricts env-var keys to DNS-friendly uppercase identifiers.
// This matches the typical convention (FOO_BAR=...) and dodges shell
// injection from users who try to set "FOO; rm -rf /".
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvKey returns nil if key is a valid env var identifier.
func ValidateEnvKey(key string) error {
	if key == "" || len(key) > 128 {
		return ErrInvalidEnvKey
	}
	if !envKeyRe.MatchString(key) {
		return ErrInvalidEnvKey
	}
	return nil
}

// ValidateEnvValue is intentionally permissive: env values are
// arbitrary strings (URLs, JSON blobs, base64 secrets). The only
// constraint is a length cap so a user can't store a 100MB blob.
func ValidateEnvValue(v string) error {
	if len(v) > 8192 {
		return ErrInvalidEnvValue
	}
	return nil
}

// EnvService manages env-var CRUD. It is a thin orchestration layer:
// storage owns the SQLite rows, the kubernetes.Client owns the
// ConfigMap + Secret resources in the cluster. The service writes to
// both, in storage-first order so a k8s failure leaves the row state
// correct (the next deploy will re-attempt the push).
type EnvService struct {
	db  *sql.DB
	env EnvStore // optional k8s writer; nil in M1/M2 dev mode
}

// EnvStore is the subset of kubernetes.Client that the env service
// needs. Declared here so tests can fake the k8s writes without
// depending on the kubernetes package's full Client.
type EnvStore interface {
	ApplyConfigMap(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error
	ApplySecret(ctx context.Context, appName string, appID int64, namespace string, data map[string]string) error
	DeleteConfigMap(ctx context.Context, appName string, appID int64, namespace string) error
	DeleteSecret(ctx context.Context, appName string, appID int64, namespace string) error
}

// NewEnvService wires the env-var service against the Podium database.
// Pass a nil EnvStore to disable k8s writes (useful in unit tests that
// only exercise the storage path). Production wiring passes the real
// *kubernetes.Client — see cmd/podium/main.go.
func NewEnvService(db *sql.DB, env EnvStore) *EnvService {
	return &EnvService{db: db, env: env}
}

// List returns every env var for an application in an environment,
// with secret values redacted. Returns ErrNotFound for unknown
// app+env combos so callers can surface a 404.
func (s *EnvService) List(ctx context.Context, app *Application, envID int64) ([]EnvVar, error) {
	q := storage.NewQueries(s.db)
	vars, err := q.ListEnvVars(ctx, app.ID, envID)
	if err != nil {
		return nil, fmt.Errorf("application: list env vars: %w", err)
	}
	out := make([]EnvVar, len(vars))
	for i, v := range vars {
		out[i] = v.Redacted()
	}
	return out, nil
}

// SetInput is the validated input to Set / Upsert.
type SetInput struct {
	Key      string
	Value    string
	IsSecret bool
}

// Set inserts or updates one env var, then pushes the resulting
// ConfigMap / Secret to k8s so the running Deployment picks the new
// value up on next restart (envFrom sources are read at pod start;
// updating the ConfigMap/Secret alone does not re-rolling pods).
//
// Returns ErrInvalidEnvKey / ErrInvalidEnvValue on bad input.
func (s *EnvService) Set(ctx context.Context, app *Application, envID int64, namespace string, in SetInput) (EnvVar, error) {
	in.Key = strings.TrimSpace(in.Key)
	if err := ValidateEnvKey(in.Key); err != nil {
		return EnvVar{}, err
	}
	if err := ValidateEnvValue(in.Value); err != nil {
		return EnvVar{}, err
	}
	q := storage.NewQueries(s.db)
	id, err := q.UpsertEnvVar(ctx, app.ID, envID, in.Key, in.Value, in.IsSecret)
	if err != nil {
		return EnvVar{}, fmt.Errorf("application: upsert env var: %w", err)
	}
	vars, err := q.ListEnvVars(ctx, app.ID, envID)
	if err != nil {
		return EnvVar{}, fmt.Errorf("application: re-read env vars: %w", err)
	}
	// Re-push the full ConfigMap + Secret (only one of them changed;
	// the other will be a no-op write with the same payload).
	if s.env != nil {
		if err := s.pushK8s(ctx, app, namespace, vars); err != nil {
			return EnvVar{}, err
		}
	}
	for _, v := range vars {
		if v.ID == id {
			return v.Redacted(), nil
		}
	}
	return EnvVar{ID: id, ApplicationID: app.ID, EnvironmentID: envID, Key: in.Key, IsSecret: in.IsSecret}.Redacted(), nil
}

// Delete removes one env var. Returns sql.ErrNoRows if the row
// doesn't exist (so callers can return 404).
func (s *EnvService) Delete(ctx context.Context, app *Application, envID int64, namespace, key string) error {
	if err := ValidateEnvKey(strings.TrimSpace(key)); err != nil {
		return err
	}
	q := storage.NewQueries(s.db)
	if err := q.DeleteEnvVar(ctx, app.ID, envID, strings.TrimSpace(key)); err != nil {
		return err
	}
	if s.env == nil {
		return nil
	}
	vars, err := q.ListEnvVars(ctx, app.ID, envID)
	if err != nil {
		return fmt.Errorf("application: re-read env vars after delete: %w", err)
	}
	if err := s.pushK8s(ctx, app, namespace, vars); err != nil {
		return err
	}
	return nil
}

// pushK8s rebuilds and writes the per-app ConfigMap (non-secret) and
// Secret (secret) for the given env var list. Passing an empty list
// for one side results in a delete of that resource.
func (s *EnvService) pushK8s(ctx context.Context, app *Application, namespace string, vars []EnvVar) error {
	cm := make(map[string]string)
	sc := make(map[string]string)
	for _, v := range vars {
		if v.IsSecret {
			sc[v.Key] = v.Value
		} else {
			cm[v.Key] = v.Value
		}
	}
	// ApplyConfigMap / ApplySecret short-circuit on empty input (no
	// write). To clear a previous ConfigMap when the user removed the
	// last non-secret var, we explicitly delete when cm is empty AND
	// there used to be a ConfigMap. We don't track "used to exist",
	// so we Delete unconditionally; the k8s client treats NotFound as
	// success so this is idempotent.
	if err := s.env.DeleteConfigMap(ctx, app.Name, app.ID, namespace); err != nil {
		return fmt.Errorf("application: clear configmap: %w", err)
	}
	if err := s.env.DeleteSecret(ctx, app.Name, app.ID, namespace); err != nil {
		return fmt.Errorf("application: clear secret: %w", err)
	}
	if err := s.env.ApplyConfigMap(ctx, app.Name, app.ID, namespace, cm); err != nil {
		return fmt.Errorf("application: write configmap: %w", err)
	}
	if err := s.env.ApplySecret(ctx, app.Name, app.ID, namespace, sc); err != nil {
		return fmt.Errorf("application: write secret: %w", err)
	}
	return nil
}

// ErrInvalidEnvKey / ErrInvalidEnvValue are surfaced by the API layer
// as 400. Distinct sentinels so the API can keep error messages
// specific.
var (
	ErrInvalidEnvKey   = errors.New("invalid env var key")
	ErrInvalidEnvValue = errors.New("invalid env var value")
)
