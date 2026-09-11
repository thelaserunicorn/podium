package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/logs"
	"github.com/podium/podium/internal/storage"
)

// LogsHandler exposes the M5 diagnostics endpoints:
//
//	GET /api/applications/{id}/logs?pod=<name>&namespace=<ns>
//	GET /api/deployments/{id}/events?namespace=<ns>
//
// Both share the same auth + ownership pattern as the rest of the
// API: cross-user lookups surface as 404 (never leak existence).
//
// The k8s client is optional; when nil the handler still answers
// but returns empty lines / events with an `available: false` flag
// so the UI can render a graceful "cluster not configured" message
// instead of crashing when KUBECONFIG is missing.
type LogsHandler struct {
	store  *storage.Queries
	apps   *application.Service
	client *kubernetes.Client // nil when KUBECONFIG was unavailable at boot
	logger *slog.Logger
}

// NewLogsHandler wires the dependencies.
func NewLogsHandler(store *storage.Queries, apps *application.Service, client *kubernetes.Client, logger *slog.Logger) *LogsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogsHandler{store: store, apps: apps, client: client, logger: logger}
}

// Mount registers the routes under the given mux. Every route is
// wrapped in RequireAuth.
func (h *LogsHandler) Mount(mux *http.ServeMux) {
	wrapped := func(handler http.HandlerFunc) http.Handler {
		return RequireAuth(http.HandlerFunc(handler))
	}
	mux.Handle("GET /api/applications/{id}/logs", wrapped(h.GetAppLogs))
	mux.Handle("GET /api/deployments/{id}/events", wrapped(h.GetDeploymentEvents))
}

// GetAppLogs returns the current container's stdout/stderr for a
// Pod. The query params are `pod` (required, the pod name from
// /state) and `namespace` (defaults to podium-dev). 502 when the
// cluster is unavailable; 404 when the pod has been GC'd by the
// kubelet.
func (h *LogsHandler) GetAppLogs(w http.ResponseWriter, r *http.Request) {
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
	if _, err := h.apps.Get(r.Context(), appID, user.ID); err != nil {
		if errors.Is(err, application.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "application not found")
			return
		}
		h.logger.Error("get application", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	pod := r.URL.Query().Get("pod")
	if pod == "" {
		writeError(w, http.StatusBadRequest, "missing_pod", "pod query param required")
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
	if h.client == nil {
		// No cluster — surface empty lines + available=false so the UI
		// can show its "cluster not configured" state.
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"namespace": namespace,
			"pod":       pod,
			"lines":     []logs.PodLogLine{},
		})
		return
	}
	lines, err := logs.FetchPodLogs(r.Context(), h.client, namespace, pod)
	if err != nil {
		h.logger.Error("fetch pod logs", "err", err, "pod", pod, "ns", namespace)
		writeError(w, http.StatusBadGateway, "pod_logs_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"namespace": namespace,
		"pod":       pod,
		"lines":     lines,
	})
}

// GetDeploymentEvents returns the Kubernetes events for a deployment
// in the given namespace. `involved_object_uid` is optional — when
// present we filter to events about the Deployment's UID; when
// absent we return every event in the namespace (the spec.md §27
// example shape).
func (h *LogsHandler) GetDeploymentEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
		return
	}
	depID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	d, err := h.store.GetDeployment(r.Context(), depID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "deployment not found")
			return
		}
		h.logger.Error("get deployment", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if _, err := h.apps.Get(r.Context(), d.ApplicationID, user.ID); err != nil {
		// Cross-user lookup — same response as 404 (AGENTS.md §19).
		writeError(w, http.StatusNotFound, "not_found", "deployment not found")
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
	if h.client == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"namespace": namespace,
			"events":    []logs.EventRow{},
		})
		return
	}
	uid := r.URL.Query().Get("involved_object_uid")
	events, err := logs.FetchEvents(r.Context(), h.client, namespace, uid)
	if err != nil {
		h.logger.Error("fetch events", "err", err, "ns", namespace)
		writeError(w, http.StatusBadGateway, "events_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"namespace": namespace,
		"events":    events,
	})
}
