package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ContextKey is the type used for storing values inside *http.Request.Context.
// Keeping it package-private avoids collisions with other middleware.
type contextKey int

const (
	ctxUser contextKey = iota + 1
)

// UserFromContext returns the authenticated user stored by WithSession.
// Returns (nil, false) when the request is unauthenticated.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(ctxUser).(User)
	if !ok {
		return nil, false
	}
	return &u, true
}

// withUser returns a new context carrying u. Used by the api middleware.
func withUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxUser, u)
}

// Handler exposes the auth HTTP endpoints. It depends only on *Service so
// the api package (M1.4) can wire it onto the global router.
type Handler struct {
	svc *Service
}

// NewHandler wires the Handler to a Service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Mount registers the auth routes on mux. Mux routing uses Go 1.22+
// "METHOD /path" syntax (spec.md §33 + Go 1.22 net/http ServeMux features).
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/signup", h.signup)
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("GET  /api/auth/me", h.me)
}

// SignupRequest is the JSON body of POST /api/auth/signup.
type SignupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginRequest is the JSON body of POST /api/auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// MeResponse is the JSON body of GET /api/auth/me.
type MeResponse struct {
	User UserDTO `json:"user"`
}

// UserDTO is the JSON-safe representation of a User. It deliberately omits
// password_hash, created_at_iso (we still send it as RFC3339Nano), etc.
// AGENTS.md §19 / §41.
type UserDTO struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toDTO(u User) UserDTO {
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var req SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
		return
	}
	user, err := h.svc.Signup(r.Context(), SignupInput{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidUsername), errors.Is(err, ErrInvalidEmail), errors.Is(err, ErrInvalidPassword):
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		case errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrEmailTaken):
			writeError(w, http.StatusConflict, "conflict", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not create account")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":    toDTO(user),
		"message": "account created; awaiting admin approval",
	})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be JSON")
		return
	}
	sess, err := h.svc.Login(r.Context(), strings.TrimSpace(req.Username), req.Password)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "username or password is incorrect")
		case errors.Is(err, ErrLoginBlocked):
			writeError(w, http.StatusForbidden, "login_blocked", "your account is not approved")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "could not sign in")
		}
		return
	}
	SetSessionCookie(w, sess.Token)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       toDTO(sess.User),
		"expires_at": sess.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	tok, _ := CookieFromRequest(r)
	_ = h.svc.Logout(r.Context(), tok)
	ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// me requires the WithSession middleware in front of it (mounted in M1.4).
// When invoked without a session it returns 401.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "no session")
		return
	}
	writeJSON(w, http.StatusOK, MeResponse{User: toDTO(*u)})
}
