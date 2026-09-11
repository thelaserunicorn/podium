package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/deployment"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

// DeploymentHandler exposes the HTTP surface for the deployment state
// machine: trigger a new build, read a deployment's state, and stream
// its build logs.
type DeploymentHandler struct {
	store  *storage.Queries
	apps   *application.Service
	orch   *deployment.Orchestrator
	logger *slog.Logger
}

// NewDeploymentHandler wires the dependencies. The handler does not
// start any goroutines itself — the orchestrator manages lifecycle.
func NewDeploymentHandler(store *storage.Queries, apps *application.Service, orch *deployment.Orchestrator, logger *slog.Logger) *DeploymentHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeploymentHandler{store: store, apps: apps, orch: orch, logger: logger}
}

// Mount registers the routes under the given mux. Every route is
// wrapped in RequireAuth.
func (h *DeploymentHandler) Mount(mux *http.ServeMux) {
	wrapped := func(handler http.HandlerFunc) http.Handler {
		return RequireAuth(http.HandlerFunc(handler))
	}
	mux.Handle("POST /api/applications/{id}/deploy", wrapped(h.Deploy))
	mux.Handle("POST /api/applications/{id}/scale", wrapped(h.Scale))
	mux.Handle("POST /api/applications/{id}/restart", wrapped(h.Restart))
	mux.Handle("GET /api/deployments/{id}", wrapped(h.GetDeployment))
	mux.Handle("DELETE /api/deployments/{id}", wrapped(h.DeleteDeployment))
	mux.Handle("GET /api/applications/{id}/deployments", wrapped(h.ListDeployments))
	mux.Handle("GET /api/deployments/{id}/logs", wrapped(h.GetLogs))
	mux.Handle("POST /api/deployments/{id}/rollback", wrapped(h.Rollback))
}

// deployRequest is the JSON body for POST /api/applications/{id}/deploy.
type deployRequest struct {
	Namespace string `json:"namespace"`
	Replicas  *int   `json:"replicas,omitempty"`
}

// deployResponse is the JSON body returned by POST /api/applications/{id}/deploy.
type deployResponse struct {
	Deployment *storage.Deployment `json:"deployment"`
}

// Deploy handles POST /api/applications/{id}/deploy. It validates
// ownership, allocates the next version, creates a QUEUED deployment
// row, and kicks off the orchestrator goroutine.
//
// Returns:
//   - 201 Created with { deployment } on success.
//   - 400 if the namespace is invalid.
//   - 404 if the app does not exist or is not owned by the caller.
//   - 409 if another deployment for this app+namespace is already in flight.
func (h *DeploymentHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid application id")
		return
	}

	app, err := h.apps.Get(r.Context(), appID, user.ID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "application not found")
			return
		}
		h.logger.Error("get application", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	var req deployRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = "podium-dev" // MVP default; switcher lands in M6
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace: "+err.Error())
		return
	}

	replicas := 3
	if req.Replicas != nil {
		replicas = *req.Replicas
	}
	if replicas < 1 || replicas > 5 {
		writeJSONError(w, http.StatusBadRequest, "replicas must be 1..5")
		return
	}

	envID, err := h.store.EnvironmentIDByNamespace(r.Context(), namespace)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("lookup environment", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		// DECISIONS.md C: the user typed a custom namespace; create it
		// on demand. The actual Kubernetes namespace is created by the
		// orchestrator's applier when it runs; here we just record the
		// SQLite row so ListDeployments / state lookups can join on it.
		envID, err = h.store.EnsureEnvironment(r.Context(), namespace)
		if err != nil {
			h.logger.Error("create environment", "err", err, "namespace", namespace)
			writeJSONError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	active, err := h.store.HasActiveDeployment(r.Context(), appID, envID)
	if err != nil {
		h.logger.Error("check active deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if active {
		writeJSONError(w, http.StatusConflict, "deployment_already_in_progress")
		return
	}

	// Bump the version counter first so the deployment row carries
	// the right tag. SQLite serialises the UPDATE.
	version, err := h.store.ApplicationNextVersion(r.Context(), appID)
	if err != nil {
		h.logger.Error("bump version", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	image := fmt.Sprintf("%s:v%d", app.Name, version)

	deploymentID, err := h.store.CreateDeployment(r.Context(), appID, envID, version, replicas, image)
	if err != nil {
		h.logger.Error("create deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	// Kick off the orchestrator. We don't wait for the build — the
	// client polls /api/deployments/{id} and /api/deployments/{id}/logs.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := h.orch.Run(ctx, deploymentID, appID, app.Name, app.RepositoryURL, envID, replicas); err != nil {
			h.logger.Info("deployment finished with error",
				"deployment_id", deploymentID,
				"err", err)
		}
	}()

	d, err := h.store.GetDeployment(context.Background(), deploymentID)
	if err != nil {
		h.logger.Error("read back deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, deployResponse{Deployment: d})
}

// GetDeployment returns the current state of a single deployment.
// Returns 404 if the deployment doesn't exist or belongs to another
// user (we never leak existence, AGENTS.md §19).
func (h *DeploymentHandler) GetDeployment(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	d, err := h.store.GetDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "deployment not found")
			return
		}
		h.logger.Error("get deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	// Authorize: deployment belongs to one of the caller's apps.
	app, err := h.apps.Get(r.Context(), d.ApplicationID, user.ID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "deployment not found")
		return
	}
	_ = app
	writeJSON(w, http.StatusOK, map[string]any{"deployment": d})
}

// DeleteDeployment handles DELETE /api/deployments/{id}. It soft-deletes
// the SQLite row (stamps deleted_at) and tears down the owning
// application's Kubernetes resources in the deployment's namespace
// (Deployment / Service / ConfigMap / Secret). Per DECISIONS.md E,
// every app has exactly one Kubernetes Deployment per namespace — the
// SQLite row is a version history, not an isolated runtime object, so
// deleting any row tears down the same live Deployment.
//
// Returns:
//   - 204 No Content on success.
//   - 400 for a non-integer id.
//   - 404 if the deployment doesn't exist, is already deleted, or
//     belongs to another user (we never leak existence).
//   - 500 for unexpected I/O failures.
func (h *DeploymentHandler) DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.apps.DeleteDeployment(r.Context(), id, user.ID); err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "deployment not found")
			return
		}
		h.logger.Error("delete deployment", "err", err, "deployment_id", id)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scaleRequest is the JSON body for POST /api/applications/{id}/scale.
type scaleRequest struct {
	Namespace string `json:"namespace"`
	Replicas  *int   `json:"replicas"`
}

// Scale handles POST /api/applications/{id}/scale. It validates
// ownership and the replica range (1..5 per spec.md §19) and
// delegates to the orchestrator. Returns:
//
//   - 200 OK with { deployment_name, current_replicas, desired_replicas }
//     on success.
//   - 400 for invalid namespace / replica count.
//   - 404 if the app doesn't exist or isn't owned by the caller.
//   - 502 when the k8s applier errored (no kubeconfig, deployment
//     missing, etc.).
func (h *DeploymentHandler) Scale(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	app, err := h.apps.Get(r.Context(), appID, user.ID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "application not found")
			return
		}
		h.logger.Error("get application", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	var req scaleRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace: "+err.Error())
		return
	}
	if req.Replicas == nil {
		writeJSONError(w, http.StatusBadRequest, "replicas required")
		return
	}
	replicas := *req.Replicas
	if replicas < 1 || replicas > 5 {
		writeJSONError(w, http.StatusBadRequest, "replicas must be 1..5")
		return
	}

	if err := h.orch.Scale(r.Context(), &app, namespace, replicas); err != nil {
		h.logger.Error("scale deployment", "err", err, "app", appID, "ns", namespace)
		writeJSONError(w, http.StatusBadGateway, "scale_failed: "+err.Error())
		return
	}

	depName := kubernetes.DeploymentName(app.Name, app.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"application_id":   app.ID,
		"namespace":        namespace,
		"deployment_name":  depName,
		"desired_replicas": replicas,
	})
}

// restartRequest is the JSON body for POST /api/applications/{id}/restart.
type restartRequest struct {
	Namespace string `json:"namespace"`
}

// Restart handles POST /api/applications/{id}/restart. Same
// ownership / namespace validation as Scale; delegates to the
// orchestrator. The orchestrator deletes the pods; the Deployment
// controller recreates them. UI should poll the state endpoint to
// see the new pods come up.
func (h *DeploymentHandler) Restart(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	app, err := h.apps.Get(r.Context(), appID, user.ID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "application not found")
			return
		}
		h.logger.Error("get application", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	var req restartRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace: "+err.Error())
		return
	}

	if err := h.orch.Restart(r.Context(), &app, namespace); err != nil {
		h.logger.Error("restart deployment", "err", err, "app", appID, "ns", namespace)
		writeJSONError(w, http.StatusBadGateway, "restart_failed: "+err.Error())
		return
	}

	depName := kubernetes.DeploymentName(app.Name, app.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"application_id":  app.ID,
		"namespace":       namespace,
		"deployment_name": depName,
		"restarted_at":    storage.FormatPodTS(time.Now()),
	})
}

// rollbackRequest is the JSON body for POST /api/deployments/{id}/rollback.
// target_version=0 means "the most recent successful deployment in this
// app+namespace pair". Any positive integer means "that exact version".
type rollbackRequest struct {
	TargetVersion int `json:"target_version"`
}

// Rollback handles POST /api/deployments/{id}/rollback. It redeploys a
// prior image in the same namespace as the supplied deployment without
// rebuilding (DECISIONS.md D). The new row gets a fresh version counter
// (per-app monotonic) but its image bytes are the target's.
//
// Returns:
//   - 201 Created with { deployment } on success.
//   - 400 for an unknown target version.
//   - 404 if the deployment doesn't exist or belongs to another user.
//   - 409 if another deployment is already in flight for this
//     app+namespace pair (mirrors POST /deploy's behaviour).
func (h *DeploymentHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	deploymentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	d, err := h.store.GetDeployment(r.Context(), deploymentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "deployment not found")
			return
		}
		h.logger.Error("get deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	// Authorize: deployment belongs to one of the caller's apps. We
	// never leak existence (AGENTS.md §19) — a wrong-owner lookup
	// surfaces as 404 too.
	app, err := h.apps.Get(r.Context(), d.ApplicationID, user.ID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "deployment not found")
		return
	}

	var req rollbackRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	if req.TargetVersion < 0 {
		writeJSONError(w, http.StatusBadRequest, "target_version must be >= 0")
		return
	}

	// Reject concurrent rollback when there's already an active
	// deployment in the same (app, namespace) pair. Without this,
	// two simultaneous "Roll back" clicks would race the version
	// counter and create duplicate rows.
	active, err := h.store.HasActiveDeployment(r.Context(), app.ID, d.EnvironmentID)
	if err != nil {
		h.logger.Error("check active deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if active {
		writeJSONError(w, http.StatusConflict, "deployment_already_in_progress")
		return
	}

	rollbackID, err := h.orch.Rollback(r.Context(), &app, d.EnvironmentID, req.TargetVersion)
	if err != nil {
		// LatestSuccessfulDeployment / DeploymentAtVersion return
		// sql.ErrNoRows when the target is missing — surface that as
		// a 400 ("unknown version") so the UI can show a friendly
		// error rather than 500.
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "unknown_target_version")
			return
		}
		h.logger.Error("rollback deployment", "err", err, "deployment_id", deploymentID, "target_version", req.TargetVersion)
		writeJSONError(w, http.StatusBadGateway, "rollback_failed: "+err.Error())
		return
	}

	newDep, err := h.store.GetDeployment(r.Context(), rollbackID)
	if err != nil {
		h.logger.Error("read back rollback deployment", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, http.StatusCreated, deployResponse{Deployment: newDep})
}

// ListDeployments returns the deployment history for an app in a
// namespace (defaults to podium-dev).
func (h *DeploymentHandler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	if _, err := h.apps.Get(r.Context(), appID, user.ID); err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "application not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "podium-dev"
	}
	envID, err := h.store.EnvironmentIDByNamespace(r.Context(), namespace)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unknown namespace")
		return
	}
	out, err := h.store.ListDeployments(r.Context(), appID, envID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": out})
}

// logLineJSON is the wire shape of a single log line in the response.
type logLineJSON struct {
	TS   string `json:"ts"`
	Line string `json:"line"`
}

// GetLogs returns build log lines for a deployment, optionally
// filtered to lines with ts > since (RFC3339).
func (h *DeploymentHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	d, err := h.store.GetDeployment(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "deployment not found")
		return
	}
	if _, err := h.apps.Get(r.Context(), d.ApplicationID, user.ID); err != nil {
		writeJSONError(w, http.StatusNotFound, "deployment not found")
		return
	}

	var since time.Time
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		t, err := time.Parse(time.RFC3339Nano, sinceStr)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid since")
			return
		}
		since = t
	}

	lines, err := h.store.LogLinesSince(r.Context(), id, since)
	if err != nil {
		h.logger.Error("read log lines", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	out := make([]logLineJSON, 0, len(lines))
	for _, l := range lines {
		out = append(out, logLineJSON{TS: storage.FormatPodTS(l.TS), Line: l.Line})
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	writeError(w, status, code, "")
}
