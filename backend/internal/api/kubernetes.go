package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

// K8sHandler exposes the live Kubernetes state endpoints added in M3:
// GET /api/applications/{id}/state and GET /api/namespaces.
//
// State is the polled endpoint the Overview tab hits every ~5s; it
// returns the running deployment name, current/desired replicas, the
// matching pods, and an "available" flag so the UI can show a graceful
// "cluster not configured" message instead of crashing when KUBECONFIG
// is missing.
type K8sHandler struct {
	store  *storage.Queries
	apps   *application.Service
	client *kubernetes.Client // nil when KUBECONFIG was unavailable at boot
	logger *slog.Logger
}

// NewK8sHandler wires the dependencies. client is the real *kubernetes.Client
// from cmd/podium/main.go; it is nil when the boot-time fallback fired.
func NewK8sHandler(store *storage.Queries, apps *application.Service, client *kubernetes.Client, logger *slog.Logger) *K8sHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &K8sHandler{store: store, apps: apps, client: client, logger: logger}
}

// Mount registers the two routes.
func (h *K8sHandler) Mount(mux *http.ServeMux) {
	wrapped := func(handler http.HandlerFunc) http.Handler {
		return RequireAuth(http.HandlerFunc(handler))
	}
	mux.Handle("GET /api/applications/{id}/state", wrapped(h.GetAppState))
	mux.Handle("GET /api/namespaces", wrapped(h.ListNamespaces))
}

// stateResponse is the JSON body returned by GET /api/applications/{id}/state.
// Available=false means "the cluster isn't reachable from this process";
// the rest of the fields are zero values in that case.
type stateResponse struct {
	Available       bool                    `json:"available"`
	Namespace       string                  `json:"namespace"`
	DeploymentName  string                  `json:"deployment_name"`
	CurrentReplicas int                     `json:"current_replicas"`
	DesiredReplicas int                     `json:"desired_replicas"`
	Pods            []kubernetes.PodSummary `json:"pods"`
}

// GetAppState returns live k8s state for the latest deployment of an
// application in the requested namespace (defaults to podium-dev).
// Returns 404 if the app is not owned by the caller.
func (h *K8sHandler) GetAppState(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
		return
	}
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	app, err := h.apps.Get(r.Context(), appID, user.ID)
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "application not found")
			return
		}
		h.logger.Error("get application", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_namespace", "namespace is not DNS-1123")
		return
	}
	env, err := h.store.GetEnvironmentByNamespace(r.Context(), namespace)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown_namespace", "namespace is not known")
		return
	}

	resp := stateResponse{Available: h.client != nil}

	if !resp.Available {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	depName := kubernetes.DeploymentName(app.Name, app.ID)
	ctx := r.Context()

	cur, des, err := h.client.CurrentReplicas(ctx, env.Namespace, depName)
	if err != nil {
		// Deployment might not exist yet (build not finished). Treat
		// as zero replicas — the dashboard can show "starting".
		h.logger.Info("get replicas (will treat as 0/0)", "err", err, "app", appID, "ns", env.Namespace)
	} else {
		resp.CurrentReplicas = cur
		resp.DesiredReplicas = des
	}

	pods, err := h.client.ListPods(ctx, env.Namespace, kubernetes.AppLabelSelector(app.Name))
	if err != nil {
		h.logger.Error("list pods", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	resp.Namespace = env.Namespace
	resp.DeploymentName = depName
	resp.Pods = pods

	writeJSON(w, http.StatusOK, resp)
}

// ListNamespaces returns every row from the environments table. The
// default namespaces appear first because ListEnvironments orders by
// id ascending.
func (h *K8sHandler) ListNamespaces(w http.ResponseWriter, r *http.Request) {
	envs, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		h.logger.Error("list environments", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := make([]storage.Environment, 0, len(envs))
	out = append(out, envs...)
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": out})
}
