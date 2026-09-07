package api

import (
	"net/http"

	"github.com/podium/podium/internal/application"
	"github.com/podium/podium/internal/auth"
)

// MountAuth mounts the auth endpoints with the appropriate public/private
// routing. signup, login, healthz are public; /api/auth/me is wrapped in
// RequireAuth so a stale cookie or a missing one returns 401.
func MountAuth(mux *http.ServeMux, h *auth.Handler) {
	mux.HandleFunc("POST /api/auth/signup", h.Signup)
	mux.HandleFunc("POST /api/auth/login", h.Login)
	mux.HandleFunc("POST /api/auth/logout", h.Logout)
	mux.Handle("GET /api/auth/me", RequireAuth(http.HandlerFunc(h.Me)))
}

// MountApplications mounts the application endpoints, all wrapped in
// RequireAuth. Per spec.md §33, every /api/applications/* route requires a
// session; cross-user access returns 404 (not 403) via the application
// service.
func MountApplications(mux *http.ServeMux, h *application.Handler) {
	wrapped := func(handler http.HandlerFunc) http.Handler {
		return RequireAuth(http.HandlerFunc(handler))
	}
	mux.Handle("GET /api/applications", wrapped(h.List))
	mux.Handle("POST /api/applications", wrapped(h.Create))
	mux.Handle("GET /api/applications/{id}", wrapped(h.Get))
	mux.Handle("PUT /api/applications/{id}", wrapped(h.Update))
	mux.Handle("DELETE /api/applications/{id}", wrapped(h.Delete))
}
