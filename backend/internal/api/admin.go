package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/podium/podium/internal/auth"
)

// AdminHandler exposes the admin user-management endpoints:
// GET /api/admin/users, POST /api/admin/users/{id}/{approve,reject,disable}.
// Spec.md §8 + §33.
type AdminHandler struct {
	svc *auth.Service
}

// NewAdminHandler wires the admin handler against the auth service.
func NewAdminHandler(svc *auth.Service) *AdminHandler { return &AdminHandler{svc: svc} }

// Mount registers the admin routes. RequireAuth + RequireAdmin are wrapped
// around each route here so the caller can't forget.
func (h *AdminHandler) Mount(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/users", AuthAdmin(http.HandlerFunc(h.listUsers)))
	mux.Handle("POST /api/admin/users/{id}/approve", AuthAdmin(http.HandlerFunc(h.approve)))
	mux.Handle("POST /api/admin/users/{id}/reject", AuthAdmin(http.HandlerFunc(h.reject)))
	mux.Handle("POST /api/admin/users/{id}/disable", AuthAdmin(http.HandlerFunc(h.disable)))
	mux.Handle("DELETE /api/admin/users/{id}", AuthAdmin(http.HandlerFunc(h.delete)))
}

// UserDTO is the JSON-safe representation used by the admin list endpoint.
// Kept here (not imported from auth) so the auth package stays net/http-free.
type UserDTO struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toUserDTO(u auth.User) UserDTO {
	return UserDTO{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Role:      u.Role,
		Status:    u.Status,
		CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		UpdatedAt: u.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
}

func (h *AdminHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list users")
		return
	}
	out := make([]UserDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (h *AdminHandler) approve(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.svc.ApproveUser, nil)
}
func (h *AdminHandler) reject(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.svc.RejectUser, nil)
}
func (h *AdminHandler) disable(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.svc.DisableUser, forbidSelf)
}

// delete handles DELETE /api/admin/users/{id}. Hard-deletes the target user.
// FK cascades wipe applications / deployments / sessions / build logs.
// An admin cannot delete themselves — the AuthAdmin middleware guarantees
// the user is in context, and forbidSelf returns 400 before the service
// is touched.
func (h *AdminHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	caller, ok := auth.UserFromContext(r.Context())
	if !ok || caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
		return
	}
	if err := h.svc.DeleteUser(r.Context(), caller.ID, id); err != nil {
		switch {
		case errors.Is(err, auth.ErrCannotDeleteSelf):
			writeError(w, http.StatusBadRequest, "cannot_delete_self", "admins cannot delete themselves")
		case errors.Is(err, auth.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "not_found", "user not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not delete user")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// lifecycle is the shared body of approve/reject/disable. The action
// function returns ErrUserNotFound for unknown ids; we map that to 404.
// selfCheck (optional) is invoked with (callerID, targetID) and may return
// a sentinel error to short-circuit with a 400 — used to prevent admins
// from disabling themselves.
func (h *AdminHandler) lifecycle(w http.ResponseWriter, r *http.Request, action func(ctx context.Context, id int64) error, selfCheck func(callerID, targetID int64) error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return
	}
	if selfCheck != nil {
		caller, ok := auth.UserFromContext(r.Context())
		if !ok || caller == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		if err := selfCheck(caller.ID, id); err != nil {
			writeError(w, http.StatusBadRequest, "cannot_target_self", err.Error())
			return
		}
	}
	if err := action(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, auth.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "not_found", "user not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not update user")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// forbidSelf returns an error if the caller is targeting their own user
// row. The error message is safe to surface to the caller.
func forbidSelf(callerID, targetID int64) error {
	if callerID == targetID {
		return errors.New("admins cannot target themselves")
	}
	return nil
}
