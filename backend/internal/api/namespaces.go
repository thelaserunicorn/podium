package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/kubernetes"
	"github.com/podium/podium/internal/storage"
)

// NamespacesHandler owns the namespace management endpoints described
// in the namespaces-page plan. Routes:
//
//	GET    /api/namespaces                — any authenticated user
//	POST   /api/namespaces                — any authenticated user
//	DELETE /api/admin/namespaces/{id}     — admin only
//
// Mount owns its own RequireAuth/AuthAdmin wrappings so the caller
// can't forget one.
type NamespacesHandler struct {
	store  *storage.Queries
	client *kubernetes.Client // nil when KUBECONFIG was unavailable at boot (delete falls back to SQLite-only)
	logger *slog.Logger
}

// NewNamespacesHandler wires the dependencies. client may be nil — the
// delete endpoint degrades to "drop the SQLite row only" when the
// cluster is unreachable (mirrors the same fallback the delete-app
// handler has for resource cleanup).
func NewNamespacesHandler(store *storage.Queries, client *kubernetes.Client, logger *slog.Logger) *NamespacesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &NamespacesHandler{store: store, client: client, logger: logger}
}

// Mount registers all three routes.
func (h *NamespacesHandler) Mount(mux *http.ServeMux) {
	mux.Handle("GET /api/namespaces", RequireAuth(http.HandlerFunc(h.list)))
	mux.Handle("POST /api/namespaces", RequireAuth(http.HandlerFunc(h.create)))
	mux.Handle("DELETE /api/admin/namespaces/{id}", AuthAdmin(http.HandlerFunc(h.delete)))
}

// DefaultNamespaces are the three namespaces Podium seeds on every boot.
// SPEC §10 + DECISIONS.md C. They cannot be deleted through the admin
// endpoint — the check happens by namespace string rather than id
// because deleting by id from a client request could otherwise bypass
// the guard if the underlying rows were re-seeded.
var DefaultNamespaces = []string{"podium-dev", "podium-staging", "podium-prod"}

// namespaceDTO is the JSON shape used by list/create responses. It
// augments storage.Environment with deployment_count so the UI can
// render "12 deployments" without a second roundtrip per row, and
// is_default so the UI can hide the Delete action without
// hard-coding the seeded list on the client.
type namespaceDTO struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	CreatedAt       string `json:"created_at"`
	DeploymentCount int64  `json:"deployment_count"`
	IsDefault       bool   `json:"is_default"`
}

// toNamespaceDTO builds the wire shape. The deployment count is
// computed via a per-row CountDeploymentsByEnv. N+1 is acceptable for
// an admin list view; the page is one query per row and the row count
// is at most a few dozen in any realistic setup.
func (h *NamespacesHandler) toNamespaceDTO(ctx context.Context, e storage.Environment) (namespaceDTO, error) {
	createdAt := ""
	if !e.CreatedAt.IsZero() {
		createdAt = e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	n, err := h.store.CountDeploymentsByEnv(ctx, e.ID)
	if err != nil {
		return namespaceDTO{}, err
	}
	return namespaceDTO{
		ID:              e.ID,
		Name:            e.Name,
		Namespace:       e.Namespace,
		CreatedAt:       createdAt,
		DeploymentCount: n,
		IsDefault:       isDefaultNamespace(e.Namespace),
	}, nil
}

// isDefaultNamespace reports whether name is one of the three seeded
// defaults. Used by the UI to hide the Delete action and by the
// server-side guard in delete().
func isDefaultNamespace(name string) bool {
	for _, d := range DefaultNamespaces {
		if d == name {
			return true
		}
	}
	return false
}

// list handles GET /api/namespaces. Returns every row from the
// environments table, oldest first (so the three defaults appear at
// the top — matches the pre-existing K8sHandler.ListNamespaces shape).
func (h *NamespacesHandler) list(w http.ResponseWriter, r *http.Request) {
	envs, err := h.store.ListEnvironments(r.Context())
	if err != nil {
		h.logger.Error("list environments", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := make([]namespaceDTO, 0, len(envs))
	for _, e := range envs {
		dto, err := h.toNamespaceDTO(r.Context(), e)
		if err != nil {
			h.logger.Error("count deployments by env", "err", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "")
			return
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": out})
}

// createRequest is the JSON body for POST /api/namespaces. Only
// `Namespace` is needed — Name is set to the same value by
// EnsureEnvironment (matching the convention for custom namespaces).
type createRequest struct {
	Namespace string `json:"namespace"`
}

// create handles POST /api/namespaces. Validates DNS-1123, refuses
// duplicates with 409, then ensures both the K8s namespace object
// (if reachable) and the SQLite row before returning the new DTO.
// Any authenticated user can call this — global namespace semantics
// per DECISIONS.md C.
func (h *NamespacesHandler) create(w http.ResponseWriter, r *http.Request) {
	var in createRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not parse request body")
		return
	}
	if in.Namespace == "" {
		writeError(w, http.StatusBadRequest, "missing_namespace", "namespace is required")
		return
	}
	if err := application.ValidateNamespaceName(in.Namespace); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_namespace", "namespace is not DNS-1123")
		return
	}
	if _, err := h.store.GetEnvironmentByNamespace(r.Context(), in.Namespace); err == nil {
		writeError(w, http.StatusConflict, "namespace_exists", "namespace already exists")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		h.logger.Error("lookup namespace", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	// Create the K8s namespace object first so a failure surfaces as a
	// 502 (cluster problem) and we don't end up with a SQLite row that
	// points at no real namespace. The cluster-down fallback writes
	// the SQLite row anyway so the UI still works for admins.
	if h.client != nil {
		if err := h.client.EnsureNamespace(r.Context(), in.Namespace); err != nil {
			h.logger.Error("ensure namespace", "err", err, "namespace", in.Namespace)
			writeError(w, http.StatusBadGateway, "namespace_create_failed", "could not create namespace in cluster")
			return
		}
	} else {
		h.logger.Warn("skipping k8s ensure; cluster unavailable", "namespace", in.Namespace)
	}

	id, err := h.store.EnsureEnvironment(r.Context(), in.Namespace)
	if err != nil {
		h.logger.Error("ensure environment row", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	env, err := h.store.GetEnvironment(r.Context(), id)
	if err != nil {
		h.logger.Error("load environment", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	dto, err := h.toNamespaceDTO(r.Context(), *env)
	if err != nil {
		h.logger.Error("count deployments after create", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"namespace": dto})
}

// delete handles DELETE /api/admin/namespaces/{id}. Refuses to delete
// one of the three seeded defaults (400 cannot_delete_default). On
// the happy path:
//  1. bulk-delete every deployment row referencing the env (log lines
//     cascade via the FK in migrations/0001_init.sql)
//  2. call kubernetes.Client.DeleteNamespace (idempotent; skips when
//     client is nil — same fallback the rest of the codebase uses)
//  3. DeleteEnvironment (env_vars cascade via FK)
//
// Other users' deployments and env-var rows are intentionally wiped
// — the user picked the destructive force-delete option when
// planning the page, because namespaces are global (no user_id) per
// DECISIONS.md C.
func (h *NamespacesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	env, err := h.store.GetEnvironment(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "namespace not found")
			return
		}
		h.logger.Error("load environment", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if isDefaultNamespace(env.Namespace) {
		writeError(w, http.StatusBadRequest, "cannot_delete_default", "cannot delete a default namespace")
		return
	}

	if _, err := h.store.DeleteDeploymentsByEnv(r.Context(), id); err != nil {
		h.logger.Error("delete deployments by env", "err", err, "env_id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete deployments")
		return
	}
	if h.client != nil {
		if err := h.client.DeleteNamespace(r.Context(), env.Namespace); err != nil {
			h.logger.Error("delete k8s namespace", "err", err, "namespace", env.Namespace)
			writeError(w, http.StatusBadGateway, "namespace_delete_failed", "could not delete namespace in cluster")
			return
		}
	}
	if err := h.store.DeleteEnvironment(r.Context(), id); err != nil {
		h.logger.Error("delete environment row", "err", err, "env_id", id)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not delete namespace row")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"namespace": env.Namespace,
	})
}
