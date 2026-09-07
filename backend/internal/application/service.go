package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/podium/podium/internal/storage"
)

// Application is the in-memory representation of a row in the applications
// table. The Version counter is the monotonic per-app counter from
// DECISIONS.md D; it is bumped by Update and used by the deployment
// orchestrator in M2 as the image tag suffix.
type Application struct {
	ID            int64
	UserID        int64
	Name          string
	RepositoryURL string
	ContainerPort int
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateInput is the validated input to Create. Name + RepositoryURL +
// ContainerPort are required; UserID is the authenticated caller's id.
type CreateInput struct {
	Name          string
	RepositoryURL string
	ContainerPort int
	UserID        int64
}

// UpdateInput supports partial updates. ContainerPort==0 is a sentinel
// meaning "no change" because the validator rejects port 0 (validate.go),
// so 0 is always safe to ignore.
type UpdateInput struct {
	RepositoryURL string
	ContainerPort int
	UserID        int64
}

// Service is the public entry point for the application package.
type Service struct {
	db        *sql.DB
	queries   *storage.Queries   // optional; populated by WithQueries for dashboard status lookups
	cleaner   AppResourceCleaner // optional; populated by WithResourceCleaner for k8s cleanup on Delete and DeleteDeployment
	testUsers *testUserIDs       // see WithUsers — nil in production
}

// testUserIDs holds two known user ids used only by application_test.go.
// Production code never sets this field.
type testUserIDs struct {
	a, b int64
}

// NewService wires the Service against the Podium database handle.
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// WithQueries returns a copy of the Service that holds a *storage.Queries
// reference. The handler uses this to enrich List responses with the
// latest deployment status per app (dashboard badges). Production code
// passes the same Queries that the orchestrator uses.
func (s *Service) WithQueries(q *storage.Queries) *Service {
	clone := *s
	clone.queries = q
	return &clone
}

// WithResourceCleaner returns a copy of the Service that will invoke
// the cleaner to drop Kubernetes resources (Deployment / Service /
// ConfigMap / Secret) for every namespace the deleted app touched.
// Optional — when nil (older callers, unit tests without a cluster),
// Delete remains a pure SQLite operation.
func (s *Service) WithResourceCleaner(c AppResourceCleaner) *Service {
	clone := *s
	clone.cleaner = c
	return &clone
}

// WithUsers returns a copy of the Service that knows about two user ids.
// Tests use this to drive cross-user scenarios without depending on the
// auth package directly. Production code never calls it.
func (s *Service) WithUsers(a, b int64) *Service {
	clone := *s
	clone.testUsers = &testUserIDs{a: a, b: b}
	return &clone
}

// UserAForTest returns the first test user id. Panics if WithUsers was not
// called — the panic is intentional; it surfaces wiring bugs loudly.
func (s *Service) UserAForTest(ctx context.Context) int64 {
	if s.testUsers == nil {
		panic("application.UserAForTest called without WithUsers wiring")
	}
	return s.testUsers.a
}

// UserBForTest returns the second test user id.
func (s *Service) UserBForTest(ctx context.Context) int64 {
	if s.testUsers == nil {
		panic("application.UserBForTest called without WithUsers wiring")
	}
	return s.testUsers.b
}

// Create inserts a new application owned by UserID. Version starts at 0;
// the first deployment increments it to 1.
func (s *Service) Create(ctx context.Context, in CreateInput) (Application, error) {
	in.Name = strings.TrimSpace(in.Name)
	if err := ValidateName(in.Name); err != nil {
		return Application{}, err
	}
	if err := ValidateRepositoryURL(in.RepositoryURL); err != nil {
		return Application{}, err
	}
	if err := ValidatePort(in.ContainerPort); err != nil {
		return Application{}, err
	}
	if in.UserID <= 0 {
		return Application{}, ErrNotFound
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO applications (user_id, name, repository_url, container_port, version)
		VALUES (?, ?, ?, ?, 0)
	`, in.UserID, in.Name, in.RepositoryURL, in.ContainerPort)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Application{}, ErrDuplicateName
		}
		return Application{}, fmt.Errorf("application: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Application{}, fmt.Errorf("application: last insert id: %w", err)
	}
	return s.Get(ctx, id, in.UserID)
}

// Get returns the application identified by id, but only if owned by UserID.
// Cross-user access returns ErrNotFound (AGENTS.md §19).
func (s *Service) Get(ctx context.Context, id, userID int64) (Application, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, repository_url, container_port, version, created_at, updated_at
		  FROM applications WHERE id = ?
	`, id)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("application: select: %w", err)
	}
	if a.UserID != userID {
		return Application{}, ErrNotFound
	}
	return a, nil
}

// GetByID returns the application identified by id without an ownership
// check. The deployment orchestrator and the kubernetes.Applier call this
// after the API handler has already authorized ownership, so the userID
// gate would be redundant (and would break for admin cross-user reads
// like the orchestrator background goroutine, which has no userID).
func (s *Service) GetByID(ctx context.Context, id int64) (Application, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, repository_url, container_port, version, created_at, updated_at
		  FROM applications WHERE id = ?
	`, id)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	if err != nil {
		return Application{}, fmt.Errorf("application: select: %w", err)
	}
	return a, nil
}

// List returns every application owned by UserID, ordered by id.
func (s *Service) List(ctx context.Context, userID int64) ([]Application, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, name, repository_url, container_port, version, created_at, updated_at
		  FROM applications
		 WHERE user_id = ?
		 ORDER BY id
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("application: list: %w", err)
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, fmt.Errorf("application: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestStatuses returns a map[applicationID] -> LatestStatus for the
// supplied app ids, used by the dashboard to render status badges.
// Returns an empty map (and no error) when the service was constructed
// without WithQueries — older callers and tests don't need it.
func (s *Service) LatestStatuses(ctx context.Context, appIDs []int64) (map[int64]storage.LatestStatus, error) {
	if s.queries == nil || len(appIDs) == 0 {
		return map[int64]storage.LatestStatus{}, nil
	}
	return s.queries.LatestDeploymentStatuses(ctx, appIDs)
}

// Update applies a partial update and bumps Version by 1.
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (Application, error) {
	if in.RepositoryURL != "" {
		if err := ValidateRepositoryURL(in.RepositoryURL); err != nil {
			return Application{}, err
		}
	}
	if in.ContainerPort != 0 {
		if err := ValidatePort(in.ContainerPort); err != nil {
			return Application{}, err
		}
	}

	current, err := s.Get(ctx, id, in.UserID)
	if err != nil {
		return Application{}, err
	}

	newURL := current.RepositoryURL
	if in.RepositoryURL != "" {
		newURL = in.RepositoryURL
	}
	newPort := current.ContainerPort
	if in.ContainerPort != 0 {
		newPort = in.ContainerPort
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE applications
		   SET repository_url = ?,
		       container_port = ?,
		       version        = version + 1,
		       updated_at     = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ?
		   AND user_id = ?
	`, newURL, newPort, id, in.UserID)
	if err != nil {
		return Application{}, fmt.Errorf("application: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Application{}, fmt.Errorf("application: rows affected: %w", err)
	}
	if n == 0 {
		return Application{}, ErrNotFound
	}
	return s.Get(ctx, id, in.UserID)
}

// Delete removes the application and (via FK ON DELETE CASCADE) any related
// deployments / env vars. Returns ErrNotFound if id is unknown or not owned.
//
// When the service was constructed with a resource cleaner, Delete
// also drops the Kubernetes Deployment / Service / ConfigMap / Secret
// for every namespace the app touched. The cleaner is invoked AFTER
// the SQLite row is gone so a k8s failure does not leave the user
// looking at an app that still exists in the dashboard; the worst
// case is orphaned cluster resources, which the user can address by
// re-creating the app later (a re-create will not collide because
// the resource name is derived from the new app id).
//
// The list of namespaces to clean up is captured BEFORE the SQLite
// DELETE because the FK ON DELETE CASCADE removes the deployment +
// env_var rows whose join produces that list.
func (s *Service) Delete(ctx context.Context, id, userID int64) error {
	// Load first so we have the app (with Name + ID) to pass to the
	// cleaner, AND so we can short-circuit with ErrNotFound for the
	// "wrong owner" case before touching anything.
	app, err := s.Get(ctx, id, userID)
	if err != nil {
		return err
	}

	// Snapshot the namespaces the app touched BEFORE the DELETE —
	// FK ON DELETE CASCADE will reap the deployments / env_vars rows
	// that EnvironmentsForApp joins against. We need this list in two
	// situations:
	//
	//  1. The cleaner is wired — we call it once per namespace below.
	//  2. The cleaner is NOT wired (e.g. Podium booted before the kind
	//     cluster was reachable, so main.go never called
	//     WithResourceCleaner) but the app had prior deployments.
	//     Without the warning in case 2 the user deletes an app from
	//     the dashboard and the Deployment / Service / ConfigMap /
	//     Secret are silently orphaned in the cluster. The only signal
	//     something went wrong is a future `kubectl get all` showing
	//     resources for a non-existent app. We log loudly so the
	//     operator knows to either restart Podium after kind is up, or
	//     manually clean up the orphans.
	var envs []storage.Environment
	if s.queries != nil {
		envs, err = s.queries.EnvironmentsForApp(ctx, app.ID)
		if err != nil {
			// Don't fail the delete for a metadata read miss. The
			// SQLite row will still be removed; the cleaner simply
			// won't be invoked.
			envs = nil
		}
	}

	res, err := s.db.ExecContext(ctx, `
		DELETE FROM applications WHERE id = ? AND user_id = ?
	`, id, userID)
	if err != nil {
		return fmt.Errorf("application: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("application: rows affected: %w", err)
	}
	if n == 0 {
		// Race: someone else just deleted it. Treat as not-found.
		return ErrNotFound
	}

	if s.cleaner == nil {
		if len(envs) > 0 {
			// App had prior deployments / env vars but no cleaner is
			// wired — surface the orphan-risk loudly so an operator
			// can either restart Podium after the cluster is up or
			// manually run `kubectl delete` for the namespaces below.
			slog.Default().Warn("app delete skipped k8s cleanup: cleaner not wired",
				"app_id", app.ID,
				"app_name", app.Name,
				"namespaces", namespaceNames(envs),
				"hint", "Podium booted before the cluster was reachable; restart after `kind create cluster`")
		}
		return nil
	}
	for i := range envs {
		if cerr := s.cleaner.DeleteAppResources(ctx, &app, envs[i].Namespace); cerr != nil {
			// Best-effort: log via the default logger and continue.
			// The SQLite row is already gone; orphaned cluster
			// resources are recoverable, a stuck delete isn't.
			slog.Default().Warn("clean k8s resources after app delete",
				"app_id", app.ID, "namespace", envs[i].Namespace, "err", cerr)
		}
	}
	return nil
}

// namespaceNames projects the environment rows down to just their
// Kubernetes namespace strings for log output.
func namespaceNames(envs []storage.Environment) []string {
	out := make([]string, len(envs))
	for i, e := range envs {
		out[i] = e.Namespace
	}
	return out
}

// rowScanner lets Get / List share scanApplication.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanApplication(r rowScanner) (Application, error) {
	var (
		a       Application
		created string
		updated string
	)
	if err := r.Scan(&a.ID, &a.UserID, &a.Name, &a.RepositoryURL, &a.ContainerPort, &a.Version, &created, &updated); err != nil {
		return Application{}, err
	}
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05.000000Z",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
