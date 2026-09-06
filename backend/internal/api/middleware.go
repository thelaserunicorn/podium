package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/podium/podium/internal/auth"
)

// Deps carries the dependencies the middleware needs. Keeping them in one
// struct means `New(...)` doesn't grow a long signature as we add features.
type Deps struct {
	Auth   *auth.Service
	Logger *slog.Logger
}

// New wraps mux with the standard middleware chain: RequestLog → Recover →
// WithSession → RequireAuth-or-pass. Routes registered on mux after this
// wrap will be inside the chain. Order matters:
//
//  1. RequestLog first so it sees every request, including panics.
//  2. Recover next so panics are turned into 500s without taking the
//     server down.
//  3. WithSession runs on every request so /api/auth/me can use the user
//     placed in the context.
//
// RequireAuth / RequireAdmin are wrappers callers put around individual
// routes — they don't apply globally because public routes (signup, login,
// healthz) must remain accessible.
func New(mux *http.ServeMux, deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	var h http.Handler = mux
	h = withRequestLog(deps.Logger)(h)
	h = withRecover(deps.Logger)(h)
	h = withSession(deps.Auth)(h)
	return h
}

// withRequestLog logs one structured line per request.
func withRequestLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			logger.Info("http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Duration("dur", time.Since(start)),
			)
		})
	}
}

// statusRecorder wraps http.ResponseWriter so middleware can observe the
// final status code (Go's stdlib does not expose it otherwise).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

// withRecover turns panics into 500s and logs the stack. Without this a
// single buggy handler would crash the whole server.
func withRecover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					logger.Error("panic",
						slog.Any("value", rv),
						slog.String("path", r.URL.Path),
						slog.String("stack", string(debug.Stack())),
					)
					writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// withSession resolves the session cookie (if any) and puts the auth.User in
// the request context. It does NOT block unauthenticated requests — that is
// RequireAuth's job. Per AGENTS.md §6 sessions use HttpOnly cookies.
func withSession(authSvc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, ok := auth.CookieFromRequest(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			sess, err := authSvc.LoadSession(r.Context(), tok)
			if err != nil {
				// Stale / invalid cookies are silently dropped — the request
				// continues as anonymous. We could clear the cookie here, but
				// doing it on every miss risks wiping good sessions on race
				// with concurrent logouts. The browser will eventually evict
				// it on MaxAge expiry.
				if !errors.Is(err, auth.ErrSessionNotFound) {
					slog.Default().Warn("session load", slog.String("err", err.Error()))
				}
				next.ServeHTTP(w, r)
				return
			}
			ctx := auth.WithUser(r.Context(), sess.User)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAuth aborts with 401 if the request is unauthenticated. Use as
// `api.RequireAuth(yourHandler)` for any route that needs a session.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFromContext(r.Context())
		if !ok || u == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin aborts with 403 if the request is not by an admin. Use after
// RequireAuth (or with both wrapped together — see AuthAdmin).
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFromContext(r.Context())
		if !ok || u == nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "login required")
			return
		}
		if u.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuthAdmin composes RequireAuth + RequireAdmin. Convenience for routes
// that need both.
func AuthAdmin(next http.Handler) http.Handler { return RequireAuth(RequireAdmin(next)) }

// helpers used across this package ---------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// noCancelContext is used by handlers that should not abort early when the
// client disconnects (e.g. log streaming). Kept here so it can be wired
// without importing context from many places.
func noCancelContext(parent context.Context) context.Context {
	return context.WithoutCancel(parent)
}
