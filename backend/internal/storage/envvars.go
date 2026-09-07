package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnvVar is a row from environment_variables. Values for is_secret=true
// rows are not returned by any List/Get endpoint — see EnvVar.Redact.
// DECISIONS.md E: a single is_secret boolean per var.
type EnvVar struct {
	ID            int64     `json:"id"`
	ApplicationID int64     `json:"application_id"`
	EnvironmentID int64     `json:"environment_id"`
	Key           string    `json:"key"`
	Value         string    `json:"value,omitempty"` // populated for non-secret vars only
	IsSecret      bool      `json:"is_secret"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Redacted returns a copy with Value cleared when the row is a secret.
// Use this when serialising for the API so secret values never leak
// (AGENTS.md §19, §41). Non-secret rows pass through unchanged.
func (e EnvVar) Redacted() EnvVar {
	if e.IsSecret {
		e.Value = ""
	}
	return e
}

// ListEnvVars returns every env var for an app in an environment.
// Callers are responsible for Redact() — the raw Value is in the
// returned rows so the kubernetes.Applier can use it.
func (q *Queries) ListEnvVars(ctx context.Context, appID, envID int64) ([]EnvVar, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, application_id, environment_id, key, value, is_secret, created_at, updated_at
		  FROM environment_variables
		 WHERE application_id = ? AND environment_id = ?
		 ORDER BY key ASC
	`, appID, envID)
	if err != nil {
		return nil, fmt.Errorf("storage: list env vars: %w", err)
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var (
			e         EnvVar
			isSecret  int
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(&e.ID, &e.ApplicationID, &e.EnvironmentID, &e.Key, &e.Value, &isSecret, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		e.IsSecret = isSecret == 1
		if t, err := parseTS(createdAt); err == nil {
			e.CreatedAt = t
		}
		if t, err := parseTS(updatedAt); err == nil {
			e.UpdatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertEnvVar inserts or updates an env var row keyed on
// (application_id, environment_id, key). The unique constraint
// guarantees one row per (app, env, key). Returns the row id so
// callers can construct response shapes.
func (q *Queries) UpsertEnvVar(ctx context.Context, appID, envID int64, key, value string, isSecret bool) (int64, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("storage: queries not initialised")
	}
	secret := 0
	if isSecret {
		secret = 1
	}
	res, err := q.db.ExecContext(ctx, `
		INSERT INTO environment_variables
			(application_id, environment_id, key, value, is_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(application_id, environment_id, key) DO UPDATE SET
			value      = excluded.value,
			is_secret  = excluded.is_secret,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, appID, envID, key, value, secret)
	if err != nil {
		return 0, fmt.Errorf("storage: upsert env var: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: last insert id: %w", err)
	}
	return id, nil
}

// DeleteEnvVar removes a single env var by key. Returns sql.ErrNoRows
// if no matching row exists (so callers can surface a 404).
func (q *Queries) DeleteEnvVar(ctx context.Context, appID, envID int64, key string) error {
	if q == nil || q.db == nil {
		return errors.New("storage: queries not initialised")
	}
	res, err := q.db.ExecContext(ctx,
		`DELETE FROM environment_variables WHERE application_id = ? AND environment_id = ? AND key = ?`,
		appID, envID, key)
	if err != nil {
		return fmt.Errorf("storage: delete env var: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
