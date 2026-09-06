package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeploymentStatus is the set of valid values for deployments.status.
// Per spec.md §11.
type DeploymentStatus string

const (
	StatusQueued    DeploymentStatus = "QUEUED"
	StatusBuilding  DeploymentStatus = "BUILDING"
	StatusBuilt     DeploymentStatus = "BUILT"
	StatusDeploying DeploymentStatus = "DEPLOYING"
	StatusStarting  DeploymentStatus = "STARTING"
	StatusRunning   DeploymentStatus = "RUNNING"
	StatusFailed    DeploymentStatus = "FAILED"
)

// Deployment is a single row from the `deployments` table.
type Deployment struct {
	ID            int64            `json:"id"`
	ApplicationID int64            `json:"application_id"`
	EnvironmentID int64            `json:"environment_id"`
	Version       int              `json:"version"`
	Image         string           `json:"image"`
	Replicas      int              `json:"replicas"`
	Status        DeploymentStatus `json:"status"`
	Reason        sql.NullString   `json:"reason,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	StartedAt     sql.NullTime     `json:"started_at,omitempty"`
	FinishedAt    sql.NullTime     `json:"finished_at,omitempty"`
}

// LogLine is one row from deploy_log_lines. Returned to the UI for
// the build-log streaming endpoint.
type LogLine struct {
	ID           int64     `json:"id"`
	DeploymentID int64     `json:"deployment_id"`
	TS           time.Time `json:"ts"`
	Line         string    `json:"line"`
}

// Environment is a row from the environments table. In Podium the
// `namespace` column is the Kubernetes namespace string (DECISIONS.md
// C); `name` is the human-friendly label ("Development", "Staging",
// "Production") used only for the three defaults. Custom namespaces
// created on demand (M3) have name == namespace.
type Environment struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrDeploymentBusy is returned when a second deploy for the same app
// is fired while one is already in-flight (DECISIONS.md B).
var ErrDeploymentBusy = errors.New("deployment already in progress")

// CreateDeployment inserts a new deployment row in QUEUED state and
// returns the assigned ID and version. The caller passes the next
// version (monotonic per app, see ApplicationService.NextVersion).
func (q *Queries) CreateDeployment(ctx context.Context, appID, envID int64, version, replicas int, image string) (int64, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("storage: queries not initialised")
	}
	res, err := q.db.ExecContext(ctx, `
		INSERT INTO deployments
			(application_id, environment_id, version, image, replicas, status)
		VALUES (?, ?, ?, ?, ?, 'QUEUED')
	`, appID, envID, version, image, replicas)
	if err != nil {
		return 0, fmt.Errorf("storage: insert deployment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: LastInsertId: %w", err)
	}
	return id, nil
}

// SetDeploymentStatus transitions a deployment to a new status. The
// reason is optional and is persisted as `deployments.reason`. For
// FAILED, the reason is short and human-readable (e.g. "build_timeout").
// Timestamps are computed in Go (rather than via SQL strftime) so the
// format we write matches the format we read with parseTS.
func (q *Queries) SetDeploymentStatus(ctx context.Context, deploymentID int64, status DeploymentStatus, reason string) error {
	if q == nil || q.db == nil {
		return errors.New("storage: queries not initialised")
	}
	now := FormatPodTS(time.Now())
	switch status {
	case StatusBuilding:
		// Preserve an existing started_at (in case of re-entry); only
		// stamp it the first time we enter BUILDING.
		_, err := q.db.ExecContext(ctx,
			`UPDATE deployments SET status = ?, reason = NULL, started_at = COALESCE(started_at, ?) WHERE id = ?`,
			string(status), now, deploymentID)
		return err
	case StatusFailed, StatusRunning:
		_, err := q.db.ExecContext(ctx,
			`UPDATE deployments SET status = ?, reason = ?, finished_at = ? WHERE id = ?`,
			string(status), nullIfEmpty(reason), now, deploymentID)
		return err
	default:
		_, err := q.db.ExecContext(ctx,
			`UPDATE deployments SET status = ?, reason = NULL WHERE id = ?`,
			string(status), deploymentID)
		return err
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetDeployment loads a deployment by ID, or returns sql.ErrNoRows.
func (q *Queries) GetDeployment(ctx context.Context, id int64) (*Deployment, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	row := q.db.QueryRowContext(ctx, `
		SELECT id, application_id, environment_id, version, image, replicas,
		       status, reason, created_at, started_at, finished_at
		FROM deployments WHERE id = ?
	`, id)
	d := &Deployment{}
	var status string
	var createdAt string
	var startedAt, finishedAt sql.NullString
	var reason sql.NullString
	if err := row.Scan(&d.ID, &d.ApplicationID, &d.EnvironmentID, &d.Version, &d.Image,
		&d.Replicas, &status, &reason, &createdAt, &startedAt, &finishedAt); err != nil {
		return nil, err
	}
	d.Status = DeploymentStatus(status)
	d.Reason = reason
	if t, err := parseTS(createdAt); err == nil {
		d.CreatedAt = t
	}
	if startedAt.Valid {
		if t, err := parseTS(startedAt.String); err == nil {
			d.StartedAt = sql.NullTime{Time: t, Valid: true}
		}
	}
	if finishedAt.Valid {
		if t, err := parseTS(finishedAt.String); err == nil {
			d.FinishedAt = sql.NullTime{Time: t, Valid: true}
		}
	}
	return d, nil
}

// ListDeployments returns the deployment history for an app in a given
// environment, newest first.
func (q *Queries) ListDeployments(ctx context.Context, appID, envID int64) ([]*Deployment, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, application_id, environment_id, version, image, replicas,
		       status, reason, created_at, started_at, finished_at
		FROM deployments
		WHERE application_id = ? AND environment_id = ?
		ORDER BY version DESC
	`, appID, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Deployment
	for rows.Next() {
		d := &Deployment{}
		var status string
		var createdAt string
		var startedAt, finishedAt sql.NullString
		var reason sql.NullString
		if err := rows.Scan(&d.ID, &d.ApplicationID, &d.EnvironmentID, &d.Version, &d.Image,
			&d.Replicas, &status, &reason, &createdAt, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		d.Status = DeploymentStatus(status)
		d.Reason = reason
		if t, err := parseTS(createdAt); err == nil {
			d.CreatedAt = t
		}
		if startedAt.Valid {
			if t, err := parseTS(startedAt.String); err == nil {
				d.StartedAt = sql.NullTime{Time: t, Valid: true}
			}
		}
		if finishedAt.Valid {
			if t, err := parseTS(finishedAt.String); err == nil {
				d.FinishedAt = sql.NullTime{Time: t, Valid: true}
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// podTSLayout is the canonical timestamp layout we use throughout
// Podium. SQLite's strftime('%Y-%m-%dT%H:%M:%fZ', 'now') produces
// variable-width fractional seconds (1–6 digits), so we adopt the
// same convention in Go: trim trailing zeros from the 6-digit form.
const podTSLayout = "2006-01-02T15:04:05.000000Z"

func parseTS(s string) (time.Time, error) {
	if t, err := time.Parse(podTSLayout, s); err == nil {
		return t, nil
	}
	// Variable-precision fraction (what SQLite emits).
	if t, err := time.Parse("2006-01-02T15:04:05.999999Z", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// FormatPodTS returns the canonical Podium wire format for time.Time,
// trimmed to match SQLite's variable-width fractional seconds.
func FormatPodTS(t time.Time) string {
	s := t.UTC().Format(podTSLayout)
	// Strip trailing zeros from the fractional seconds but keep at
	// least one digit before the Z.
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return s
	}
	z := strings.IndexByte(s[dot:], 'Z')
	if z < 0 {
		return s
	}
	frac := s[dot+1 : dot+z]
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return s[:dot] + "Z"
	}
	return s[:dot+1] + frac + "Z"
}

// HasActiveDeployment reports whether the given app+env already has a
// non-terminal deployment in flight. Used to enforce the 409 rule from
// DECISIONS.md B.
func (q *Queries) HasActiveDeployment(ctx context.Context, appID, envID int64) (bool, error) {
	if q == nil || q.db == nil {
		return false, errors.New("storage: queries not initialised")
	}
	var n int
	err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM deployments
		WHERE application_id = ? AND environment_id = ?
		  AND status NOT IN ('RUNNING', 'FAILED')
	`, appID, envID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// AppendLogLine writes one build-log line for a deployment.
func (q *Queries) AppendLogLine(ctx context.Context, deploymentID int64, line string) error {
	if q == nil || q.db == nil {
		return errors.New("storage: queries not initialised")
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO deploy_log_lines (deployment_id, line) VALUES (?, ?)
	`, deploymentID, line)
	return err
}

// LogLinesSince returns every log line for the deployment with ts > since.
// The cursor is compared as RFC3339Nano.
func (q *Queries) LogLinesSince(ctx context.Context, deploymentID int64, since time.Time) ([]LogLine, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	sinceStr := ""
	if !since.IsZero() {
		sinceStr = FormatPodTS(since)
	}
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, deployment_id, ts, line
		FROM deploy_log_lines
		WHERE deployment_id = ? AND (? = '' OR ts > ?)
		ORDER BY ts ASC, id ASC
	`, deploymentID, sinceStr, sinceStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogLine
	for rows.Next() {
		var l LogLine
		var ts string
		if err := rows.Scan(&l.ID, &l.DeploymentID, &ts, &l.Line); err != nil {
			return nil, err
		}
		if t, err := parseTS(ts); err == nil {
			l.TS = t
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LatestStatus is the status of the most recent deployment for a given
// application — across all namespaces. Returned by LatestDeploymentStatuses
// so the dashboard can show one badge per app without an N+1 roundtrip.
type LatestStatus struct {
	DeploymentID int64
	Status       DeploymentStatus
	Version      int
	Namespace    string
	CreatedAt    time.Time
}

// LatestDeploymentStatuses returns the status of the latest deployment
// (by created_at, then id tiebreak) for each application in appIDs,
// across all environments. An id with no deployments is omitted from
// the map; callers treat that as "never deployed".
//
// Implemented as one SQLite query that joins each id to the row with
// the maximum (created_at, id) tuple. Avoids the N+1 that per-app
// ListDeployments would incur for a dashboard view.
func (q *Queries) LatestDeploymentStatuses(ctx context.Context, appIDs []int64) (map[int64]LatestStatus, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	out := map[int64]LatestStatus{}
	if len(appIDs) == 0 {
		return out, nil
	}

	// Build the IN-list with placeholders; modernc.org/sqlite binds
	// args positionally so we can hand-build the slice safely.
	placeholders := make([]string, len(appIDs))
	args := make([]any, 0, len(appIDs)+1)
	for i, id := range appIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	in := "application_id IN (" + strings.Join(placeholders, ",") + ")"

	q1 := `
		SELECT d.application_id, d.id, d.status, d.version, e.namespace, d.created_at
		FROM deployments d
		JOIN environments e ON e.id = d.environment_id
		WHERE d.id = (
			SELECT d2.id FROM deployments d2
			WHERE d2.application_id = d.application_id
			ORDER BY d2.created_at DESC, d2.id DESC
			LIMIT 1
		)
		AND ` + in

	rows, err := q.db.QueryContext(ctx, q1, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: latest deployment statuses: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			appID, depID          int64
			status, ns, createdAt string
			version               int
		)
		if err := rows.Scan(&appID, &depID, &status, &version, &ns, &createdAt); err != nil {
			return nil, fmt.Errorf("storage: scan latest status: %w", err)
		}
		ls := LatestStatus{
			DeploymentID: depID,
			Status:       DeploymentStatus(status),
			Version:      version,
			Namespace:    ns,
		}
		if t, err := parseTS(createdAt); err == nil {
			ls.CreatedAt = t
		}
		out[appID] = ls
	}
	return out, rows.Err()
}

// LatestLogTimestamp returns the most recent log ts for the deployment,
// or zero time if there are no log lines yet.
func (q *Queries) LatestLogTimestamp(ctx context.Context, deploymentID int64) (time.Time, error) {
	if q == nil || q.db == nil {
		return time.Time{}, errors.New("storage: queries not initialised")
	}
	var ts sql.NullString
	err := q.db.QueryRowContext(ctx, `
		SELECT MAX(ts) FROM deploy_log_lines WHERE deployment_id = ?
	`, deploymentID).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return parseTS(ts.String)
}

// EnvironmentIDByNamespace returns the id for a Kubernetes namespace.
// Used when the frontend picks an environment for a deploy.
func (q *Queries) EnvironmentIDByNamespace(ctx context.Context, namespace string) (int64, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("storage: queries not initialised")
	}
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM environments WHERE namespace = ?`, namespace).Scan(&id)
	return id, err
}

// GetEnvironmentByNamespace returns the full Environment row for a
// namespace, or sql.ErrNoRows if no such row exists. Used by the API
// layer to translate a kube namespace string into the env id + label.
func (q *Queries) GetEnvironmentByNamespace(ctx context.Context, namespace string) (*Environment, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	row := q.db.QueryRowContext(ctx,
		`SELECT id, name, namespace, created_at FROM environments WHERE namespace = ?`, namespace)
	var e Environment
	var createdAt string
	if err := row.Scan(&e.ID, &e.Name, &e.Namespace, &createdAt); err != nil {
		return nil, err
	}
	if t, err := parseTS(createdAt); err == nil {
		e.CreatedAt = t
	}
	return &e, nil
}

// GetEnvironment loads an environment row by id, returning sql.ErrNoRows
// for unknown ids. Used by the kubernetes.Applier to resolve a
// deployment's envID into the namespace string.
func (q *Queries) GetEnvironment(ctx context.Context, id int64) (*Environment, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	row := q.db.QueryRowContext(ctx,
		`SELECT id, name, namespace, created_at FROM environments WHERE id = ?`, id)
	var e Environment
	var createdAt string
	if err := row.Scan(&e.ID, &e.Name, &e.Namespace, &createdAt); err != nil {
		return nil, err
	}
	if t, err := parseTS(createdAt); err == nil {
		e.CreatedAt = t
	}
	return &e, nil
}

// EnsureEnvironment inserts a row for a custom Kubernetes namespace
// (DECISIONS.md C) if one does not already exist, then returns the
// id. The columns `name` and `namespace` are both UNIQUE — for custom
// rows we set `name = namespace` so the UI's freeform entry doubles
// as the displayed label.
func (q *Queries) EnsureEnvironment(ctx context.Context, namespace string) (int64, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("storage: queries not initialised")
	}
	if _, err := q.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO environments (name, namespace) VALUES (?, ?)`,
		namespace, namespace); err != nil {
		return 0, fmt.Errorf("storage: insert environment: %w", err)
	}
	return q.EnvironmentIDByNamespace(ctx, namespace)
}

// ListEnvironments returns every environment row, oldest first (so the
// three default namespaces appear at the top of the UI list).
func (q *Queries) ListEnvironments(ctx context.Context) ([]Environment, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("storage: queries not initialised")
	}
	rows, err := q.db.QueryContext(ctx,
		`SELECT id, name, namespace, created_at FROM environments ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("storage: list environments: %w", err)
	}
	defer rows.Close()
	var out []Environment
	for rows.Next() {
		var e Environment
		var createdAt string
		if err := rows.Scan(&e.ID, &e.Name, &e.Namespace, &createdAt); err != nil {
			return nil, fmt.Errorf("storage: scan environment: %w", err)
		}
		if t, err := parseTS(createdAt); err == nil {
			e.CreatedAt = t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplicationNextVersion bumps the per-application version counter
// atomically and returns the new value. Uses SQLite's implicit
// transaction so two concurrent deploys for the same app see distinct
// versions (DECISIONS.md D).
func (q *Queries) ApplicationNextVersion(ctx context.Context, appID int64) (int, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("storage: queries not initialised")
	}
	// SQLite serialises writes through the implicit txn on UPDATE.
	res, err := q.db.ExecContext(ctx,
		`UPDATE applications SET version = version + 1, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		appID)
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows == 0 {
		return 0, sql.ErrNoRows
	}
	var v int
	if err := q.db.QueryRowContext(ctx, `SELECT version FROM applications WHERE id = ?`, appID).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}
