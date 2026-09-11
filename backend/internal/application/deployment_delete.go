package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// DeleteDeployment removes a single deployment attempt from SQLite
// (soft-delete via deleted_at, see migration 0003) and tears down the
// live Kubernetes resources for the owning application in the
// deployment's namespace.
//
// Why per-app k8s cleanup for a single deployment row: per
// DECISIONS.md E every app has exactly one Kubernetes Deployment per
// namespace (`DeploymentName(appName, appID)`). The `deployments`
// table is a history of *image versions*, not isolated runtime
// objects. Deleting any historical row therefore tears down the same
// live Deployment that every other deployment row in that namespace
// points at. A subsequent deploy in the same namespace will recreate
// the live resources from scratch.
//
// Authorization: ownership is checked against the deployment's
// parent application — if UserID does not own the app, the method
// returns ErrNotFound (AGENTS.md §19: never leak existence).
//
// Errors:
//   - ErrNotFound — unknown id, already soft-deleted, or wrong owner.
//   - any other error is a SQLite or Kubernetes I/O failure.
//
// Ordering:
//  1. Load the deployment. Stop with ErrNotFound for not-found / wrong
//     owner / already-deleted.
//  2. Resolve the namespace via the deployment's environment_id. If
//     the environment row is gone, skip k8s cleanup and proceed to
//     the SQLite soft-delete (the dashboard source of truth).
//  3. Drop the app's k8s resources in that namespace (best-effort:
//     k8s NotFound is success, real errors are logged but not
//     returned because the SQLite soft-delete is the dashboard's
//     source of truth — orphan k8s resources are recoverable, a
//     stuck delete is not).
//  4. Soft-delete the SQLite row.
func (s *Service) DeleteDeployment(ctx context.Context, deploymentID, userID int64) error {
	if s.queries == nil {
		return errors.New("application: queries not configured")
	}

	d, err := s.queries.GetDeployment(ctx, deploymentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("application: load deployment: %w", err)
	}

	app, err := s.Get(ctx, d.ApplicationID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Wrong owner — collapse into not-found to match the
			// cross-user pattern used everywhere else (AGENTS.md §19).
			return ErrNotFound
		}
		return fmt.Errorf("application: authorize deployment: %w", err)
	}

	// Already soft-deleted? treat as not-found — the row is invisible
	// to ListDeployments and a second delete should be a no-op 404.
	if d.DeletedAt.Valid {
		return ErrNotFound
	}

	env, err := s.queries.GetEnvironment(ctx, d.EnvironmentID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("application: load environment: %w", err)
		}
		// env row missing — skip k8s cleanup.
		env = nil
	}

	if s.cleaner != nil && env != nil {
		if err := s.cleaner.DeleteAppResources(ctx, &app, env.Namespace); err != nil {
			slog.Default().Warn("clean k8s resources after deployment delete",
				"deployment_id", deploymentID,
				"app_id", app.ID,
				"namespace", env.Namespace,
				"err", err)
		}
	}

	if err := s.queries.SoftDeleteDeployment(ctx, deploymentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Lost the race against another delete; treat as success.
			return nil
		}
		return fmt.Errorf("application: soft-delete deployment: %w", err)
	}

	// Drop the cached port-forward for this (app, namespace). The k8s
	// Service is gone (or about to be); the cached Forwarder is now
	// pointing at a dead upstream. Evicting here means the next
	// `GET /api/applications/{id}/ingress?ns=<this>` starts a fresh
	// Forwarder against whatever the redeploy creates, instead of
	// handing the browser a stale URL that won't resolve.
	//
	// We evict AFTER the SQLite soft-delete so a Redis-style "evict
	// then crash" race can't leave the user with a deploy row that
	// exists in SQLite but no longer has a backing ingress route —
	// they would already see the URL as "unreachable" before the
	// delete completes, and a concurrent redeploy will recreate both.
	if s.ingress != nil && env != nil {
		s.ingress.Evict(app.ID, env.Namespace)
	}
	return nil
}
