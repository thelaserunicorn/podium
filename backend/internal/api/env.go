package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// EnvHandler exposes per-(application, namespace) environment variable
// CRUD. Three endpoints land here:
//
//	GET    /api/applications/{id}/env?namespace=<ns>
//	POST   /api/applications/{id}/env
//	DELETE /api/applications/{id}/env/{key}?namespace=<ns>
//
// Secret values are NEVER returned by GET — the EnvService redacts
// them before the handler writes the response (DECISIONS.md E,
// AGENTS.md §19 / §41).
type EnvHandler struct {
	store  *storage.Queries
	apps   *application.Service
	env    *application.EnvService
	logger *slog.Logger
}

func NewEnvHandler(store *storage.Queries, apps *application.Service, env *application.EnvService, logger *slog.Logger) *EnvHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &EnvHandler{store: store, apps: apps, env: env, logger: logger}
}

func (h *EnvHandler) Mount(mux *http.ServeMux) {
	wrapped := func(handler http.HandlerFunc) http.Handler {
		return RequireAuth(http.HandlerFunc(handler))
	}
	mux.Handle("GET /api/applications/{id}/env", wrapped(h.List))
	mux.Handle("POST /api/applications/{id}/env", wrapped(h.Set))
	mux.Handle("DELETE /api/applications/{id}/env/{key}", wrapped(h.Delete))
}

// envVarJSON is the wire shape of an env var in API responses. Secret
// rows have value omitted (empty string + is_secret=true).
type envVarJSON struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	IsSecret  bool   `json:"is_secret"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toEnvVarJSON(v application.EnvVar) envVarJSON {
	return envVarJSON{
		ID:        v.ID,
		Key:       v.Key,
		Value:     v.Value,
		IsSecret:  v.IsSecret,
		CreatedAt: storage.FormatPodTS(v.CreatedAt),
		UpdatedAt: storage.FormatPodTS(v.UpdatedAt),
	}
}

// setEnvRequest is the JSON body for POST /api/applications/{id}/env.
type setEnvRequest struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	IsSecret  bool   `json:"is_secret"`
}

// List handles GET /api/applications/{id}/env?namespace=<ns>. Returns
// 404 if the app is not owned by the caller.
func (h *EnvHandler) List(w http.ResponseWriter, r *http.Request) {
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
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	env, err := h.store.GetEnvironmentByNamespace(r.Context(), namespace)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unknown namespace")
		return
	}
	vars, err := h.env.List(r.Context(), &app, env.ID)
	if err != nil {
		h.logger.Error("list env vars", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	out := make([]envVarJSON, 0, len(vars))
	for _, v := range vars {
		out = append(out, toEnvVarJSON(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"env_vars": out})
}

// Set handles POST /api/applications/{id}/env. Validates the key,
// value, and namespace; upserts the row and pushes the new
// ConfigMap / Secret to the cluster.
func (h *EnvHandler) Set(w http.ResponseWriter, r *http.Request) {
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
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	var req setEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Namespace == "" {
		req.Namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(req.Namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	env, err := h.store.GetEnvironmentByNamespace(r.Context(), req.Namespace)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unknown namespace")
		return
	}

	saved, err := h.env.Set(r.Context(), &app, env.ID, req.Namespace, application.SetInput{
		Key:      req.Key,
		Value:    req.Value,
		IsSecret: req.IsSecret,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrInvalidEnvKey):
			writeJSONError(w, http.StatusBadRequest, "invalid env var key")
		case errors.Is(err, application.ErrInvalidEnvValue):
			writeJSONError(w, http.StatusBadRequest, "invalid env var value")
		default:
			h.logger.Error("set env var", "err", err)
			writeJSONError(w, http.StatusBadGateway, "env_var_failed: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env_var": toEnvVarJSON(saved)})
}

// Delete handles DELETE /api/applications/{id}/env/{key}?namespace=<ns>.
func (h *EnvHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
		writeJSONError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	key := r.PathValue("key")
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "podium-dev"
	}
	if err := application.ValidateNamespaceName(namespace); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid namespace")
		return
	}
	env, err := h.store.GetEnvironmentByNamespace(r.Context(), namespace)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "unknown namespace")
		return
	}
	if err := h.env.Delete(r.Context(), &app, env.ID, namespace, key); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "env var not found")
		case errors.Is(err, application.ErrInvalidEnvKey):
			writeJSONError(w, http.StatusBadRequest, "invalid env var key")
		default:
			h.logger.Error("delete env var", "err", err)
			writeJSONError(w, http.StatusBadGateway, "env_var_delete_failed: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": key})
}
