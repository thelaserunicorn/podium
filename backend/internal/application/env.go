package application

import (
	"context"
	"database/sql"
	"fmt"
)

// EnvVar is the in-memory representation of a row in environment_variables.
// Full CRUD wiring lands in M4 (PLAN.md M4). This file is a deliberate stub
// so M1 can compile against the schema without dragging in Secret/ConfigMap
// Kubernetes logic that depends on the k8s client (M3).
type EnvVar struct {
	ID            int64
	ApplicationID int64
	EnvironmentID int64
	Key           string
	Value         string
	IsSecret      bool
}

// EnvService is the placeholder for the M4 env-var slice. Its zero value
// works; production wiring happens in M4 once the kubernetes package lands.
type EnvService struct {
	db *sql.DB
}

// NewEnvService wires the env-var service. Only used by M4.
func NewEnvService(db *sql.DB) *EnvService { return &EnvService{db: db} }

// List returns every env var for an application. The placeholder returns
// ErrNotImplemented so M1 callers can detect that this surface is not yet
// wired (instead of getting an empty slice and silently misbehaving).
func (s *EnvService) List(ctx context.Context, applicationID int64) ([]EnvVar, error) {
	return nil, fmt.Errorf("application.EnvService.List: not implemented in M1 (lands in M4): %w", sql.ErrNoRows)
}
