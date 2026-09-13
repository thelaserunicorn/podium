package application

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/auth"
	"github.com/podium/podium/internal/storage"
)

// Handler exposes the application REST endpoints. The handler depends on
// Service for all business logic; the only thing it does is parse JSON,
// pull the authenticated user id from context, and map errors to status.
// Routing is owned by api.MountApplications.
type Handler struct {
	svc *Service
}

// NewHandler wires the Handler to a Service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// ApplicationDTO is the JSON-safe representation of an Application.
//
// `latest_status` is the deployment status of the most recent deploy
// across all namespaces, or null when the app has never been deployed.
// The dashboard uses it for the per-app badge and the Running/Failed
// count cards (so they reflect reality, not the M3 placeholder).
type ApplicationDTO struct {
	ID     int64 `json:"id"`
	UserID int64 `json:"user_id"`
	// OwnerUsername is populated by the admin-aware service methods so
	// the Applications tab can show "alice / my-api" when an admin is
	// viewing apps they don't own. Empty for normal user views.
	OwnerUsername string               `json:"owner_username,omitempty"`
	Name          string               `json:"name"`
	RepositoryURL string               `json:"repository_url"`
	ContainerPort int                  `json:"container_port"`
	Version       int                  `json:"version"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
	LatestStatus  *DeploymentStatusDTO `json:"latest_status,omitempty"`
}

// DeploymentStatusDTO is a minimal, dashboard-friendly snapshot of the
// most recent deployment for an app. It omits the long log field the
// full Deployment row carries.
type DeploymentStatusDTO struct {
	DeploymentID int64  `json:"deployment_id"`
	Status       string `json:"status"`
	Version      int    `json:"version"`
	Namespace    string `json:"namespace"`
	CreatedAt    string `json:"created_at"`
}

func toDTO(a Application) ApplicationDTO {
	return ApplicationDTO{
		ID:            a.ID,
		UserID:        a.UserID,
		OwnerUsername: a.OwnerUsername,
		Name:          a.Name,
		RepositoryURL: a.RepositoryURL,
		ContainerPort: a.ContainerPort,
		Version:       a.Version,
		CreatedAt:     a.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt:     a.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

func statusDTO(ls storage.LatestStatus) DeploymentStatusDTO {
	return DeploymentStatusDTO{
		DeploymentID: ls.DeploymentID,
		Status:       string(ls.Status),
		Version:      ls.Version,
		Namespace:    ls.Namespace,
		CreatedAt:    ls.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

// CreateRequest / UpdateRequest are the JSON bodies of the POST / PUT routes.
type CreateRequest struct {
	Name          string `json:"name"`
	RepositoryURL string `json:"repository_url"`
	ContainerPort int    `json:"container_port"`
}

type UpdateRequest struct {
	Name          string `json:"name"`
	RepositoryURL string `json:"repository_url"`
	ContainerPort int    `json:"container_port"`
}

// callerUserID is the canonical way handlers ask "who is calling?". It
// reads the auth.User the WithSession middleware (api package) puts in the
// context. Returns (0, false) when the middleware is not wired — callers
// map that to 401.
func callerUserID(r *http.Request) (int64, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		return 0, false
	}
	return u.ID, true
}

// callerFromRequest returns the full Caller (id + admin flag) for
// admin-aware service methods. Returns (_, false) when the session
// middleware is not wired — handlers map that to 401.
func callerFromRequest(r *http.Request) (Caller, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u == nil {
		return Caller{}, false
	}
	return Caller{UserID: u.ID, IsAdmin: u.Role == auth.RoleAdmin}, true
}

// writeJSON / writeError are tiny shared helpers. Kept private to this
// package so each handler package can evolve its error envelope without
// affecting the others.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "application not found")
	case errors.Is(err, ErrDuplicateName):
		writeError(w, http.StatusConflict, "duplicate_name", err.Error())
	case errors.Is(err, ErrHasDeployments):
		writeError(w, http.StatusConflict, "has_deployments", err.Error())
	case errors.Is(err, ErrInvalidName), errors.Is(err, ErrInvalidRepoURL), errors.Is(err, ErrInvalidPort):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not process request")
	}
}

// List handles GET /api/applications.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	c, ok := callerFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	apps, err := h.svc.ListForCaller(r.Context(), c)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list applications")
		return
	}
	out := make([]ApplicationDTO, 0, len(apps))
	if len(apps) > 0 {
		// Enrich each app with the latest deployment status so the
		// dashboard can render running/failed counts and per-row
		// badges without an extra round-trip per app.
		ids := make([]int64, len(apps))
		for i, a := range apps {
			ids[i] = a.ID
		}
		statuses, err := h.svc.LatestStatuses(r.Context(), ids)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "could not load latest statuses")
			return
		}
		for _, a := range apps {
			dto := toDTO(a)
			if ls, ok := statuses[a.ID]; ok {
				sdto := statusDTO(ls)
				dto.LatestStatus = &sdto
			}
			out = append(out, dto)
		}
	} else {
		for _, a := range apps {
			out = append(out, toDTO(a))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": out})
}

// Create handles POST /api/applications.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	uid, ok := callerUserID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
		return
	}
	a, err := h.svc.Create(r.Context(), CreateInput{
		Name:          req.Name,
		RepositoryURL: req.RepositoryURL,
		ContainerPort: req.ContainerPort,
		UserID:        uid,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"application": toDTO(a)})
}

// Get handles GET /api/applications/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	c, ok := callerFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	a, err := h.svc.GetForCaller(r.Context(), id, c)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application": toDTO(a)})
}

// Update handles PUT /api/applications/{id}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	c, ok := callerFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
		return
	}
	a, err := h.svc.Update(r.Context(), id, UpdateInput{
		Name:          req.Name,
		RepositoryURL: req.RepositoryURL,
		ContainerPort: req.ContainerPort,
		UserID:        c.UserID,
		IsAdmin:       c.IsAdmin,
	})
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application": toDTO(a)})
}

// Delete handles DELETE /api/applications/{id}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	c, ok := callerFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	if err := h.svc.DeleteForCaller(r.Context(), id, c); err != nil {
		mapErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
