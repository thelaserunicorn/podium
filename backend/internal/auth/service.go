package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// User statuses and roles. These are persisted as strings in the SQLite
// users.status / users.role columns with CHECK constraints (see
// migrations/0001_init.sql). The constants below are the only blessed
// values — anything else at the API boundary is rejected.
const (
	StatusPending  = "PENDING"
	StatusApproved = "APPROVED"
	StatusRejected = "REJECTED"
	StatusDisabled = "DISABLED"

	RoleAdmin = "ADMIN"
	RoleUser  = "USER"
)

// User is the in-memory representation of a row in the users table.
// The password hash is deliberately not exposed via JSON encoders; the
// handler layer marshals the public fields only.
type User struct {
	ID        int64
	Username  string
	Email     string
	Role      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SignupInput is the validated input to Signup.
type SignupInput struct {
	Username string
	Email    string
	Password string
}

// Session is the in-memory representation of an active session.
type Session struct {
	Token     string
	User      User
	ExpiresAt time.Time
}

// Service is the public entry point for the auth package. Handlers depend on
// the methods, not on the concrete *sql.DB, which makes the service trivial
// to fake in unit tests for higher layers (M1.4+).
type Service struct {
	db *sql.DB
}

// NewService wires the auth Service against the Podium database handle.
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// usernameRe / emailRe are deliberately permissive. They enforce only the
// minimum invariants we need (no whitespace, sane email shape) and leave the
// rest to bcrypt cost and the UNIQUE constraints. Adjust here only if the UI
// starts complaining.
var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)
	emailRe    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

const minPasswordLen = 8

// Signup creates a new user in the PENDING state. The user cannot log in
// until an admin calls ApproveUser. Spec.md §6.
func (s *Service) Signup(ctx context.Context, in SignupInput) (User, error) {
	in.Username = strings.TrimSpace(in.Username)
	in.Email = strings.TrimSpace(in.Email)
	if !usernameRe.MatchString(in.Username) {
		return User{}, ErrInvalidUsername
	}
	if !emailRe.MatchString(in.Email) {
		return User{}, ErrInvalidEmail
	}
	if len(in.Password) < minPasswordLen {
		return User{}, ErrInvalidPassword
	}

	hash, err := HashPassword(in.Password)
	if err != nil {
		return User{}, fmt.Errorf("auth: hash: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, email, password_hash, role, status)
		VALUES (?, ?, ?, ?, ?)
	`, in.Username, in.Email, hash, RoleUser, StatusPending)
	if err != nil {
		// SQLite surfaces UNIQUE violations via the error message; map the
		// two known cases to typed errors. Anything else is a real bug.
		msg := err.Error()
		switch {
		case strings.Contains(msg, "users.username"):
			return User{}, ErrUsernameTaken
		case strings.Contains(msg, "users.email"):
			return User{}, ErrEmailTaken
		default:
			return User{}, fmt.Errorf("auth: insert user: %w", err)
		}
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("auth: last insert id: %w", err)
	}
	return s.userByID(ctx, id)
}

// Login validates username + password and, on success, returns a fresh
// session. Any non-APPROVED status yields ErrLoginBlocked so callers can
// collapse PENDING / REJECTED / DISABLED into one message in the UI.
// Spec.md §7 + DECISIONS.md F.
func (s *Service) Login(ctx context.Context, username, password string) (Session, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return Session{}, ErrInvalidCredentials
	}

	var (
		id     int64
		hash   string
		role   string
		status string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, password_hash, role, status FROM users WHERE username = ?
	`, username).Scan(&id, &hash, &role, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("auth: select user: %w", err)
	}

	if status != StatusApproved {
		return Session{}, ErrLoginBlocked
	}

	if err := VerifyPassword(hash, password); err != nil {
		// Same error as "no such user" so we never reveal which field failed.
		return Session{}, ErrInvalidCredentials
	}

	tok, err := NewSessionToken()
	if err != nil {
		return Session{}, fmt.Errorf("auth: token: %w", err)
	}
	expires := time.Now().Add(sessionTTL).UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)
	`, tok, id, expires.UTC().Format(time.RFC3339Nano)); err != nil {
		return Session{}, fmt.Errorf("auth: insert session: %w", err)
	}

	user, err := s.userByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return Session{Token: tok, User: user, ExpiresAt: expires}, nil
}

// LoadSession resolves a session token to its user. Returns ErrSessionNotFound
// for unknown tokens and expired sessions. Used by the WithSession middleware.
func (s *Service) LoadSession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionNotFound
	}
	var (
		userID     int64
		expiresStr string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT user_id, expires_at FROM sessions WHERE id = ?
	`, token).Scan(&userID, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("auth: select session: %w", err)
	}
	expiresAt := parseRFC3339(expiresStr)
	if !time.Now().Before(expiresAt) {
		// Expired — clean up opportunistically.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, token)
		return Session{}, ErrSessionNotFound
	}
	user, err := s.userByID(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	return Session{Token: token, User: user, ExpiresAt: expiresAt}, nil
}

// Logout deletes the session row. Idempotent: deleting an unknown token is a
// no-op (caller still clears the cookie).
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, token)
	if err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// ApproveUser flips a PENDING user to APPROVED. Admin-only by convention —
// the HTTP handler enforces the role check (see RequireAdmin in api/middleware).
func (s *Service) ApproveUser(ctx context.Context, userID int64) error {
	return s.setStatus(ctx, userID, StatusApproved)
}

// RejectUser flips a PENDING user to REJECTED. Terminal per DECISIONS.md F.
func (s *Service) RejectUser(ctx context.Context, userID int64) error {
	return s.setStatus(ctx, userID, StatusRejected)
}

// DisableUser flips an APPROVED user to DISABLED. Reversible by ApproveUser.
func (s *Service) DisableUser(ctx context.Context, userID int64) error {
	return s.setStatus(ctx, userID, StatusDisabled)
}

// DeleteUser hard-deletes a user row. Caller-supplied callerID is checked
// against the target so an admin cannot delete themselves and lock out
// Podium; passing the same id for both returns ErrCannotDeleteSelf. All
// child rows (applications, deployments, environment_variables, sessions,
// deploy_log_lines) cascade away via the ON DELETE CASCADE foreign keys
// declared in migrations/0001_init.sql. Returns ErrUserNotFound when the
// target id does not exist. Admin-only by convention — the HTTP handler
// enforces the role check.
func (s *Service) DeleteUser(ctx context.Context, callerID, targetID int64) error {
	if callerID == targetID {
		return ErrCannotDeleteSelf
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, targetID)
	if err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserStatus is the lower-level setter used by tests and by future
// admin endpoints that need finer control. It validates the status string
// against the allowed set so a typo cannot persist an invalid value.
func (s *Service) SetUserStatus(ctx context.Context, userID int64, status string) error {
	switch status {
	case StatusPending, StatusApproved, StatusRejected, StatusDisabled:
	default:
		return fmt.Errorf("auth: invalid status %q", status)
	}
	return s.setStatus(ctx, userID, status)
}

func (s *Service) setStatus(ctx context.Context, userID int64, status string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE users
		   SET status = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ?
	`, status, userID)
	if err != nil {
		return fmt.Errorf("auth: update status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ListUsers returns every user ordered by id. Used by the admin user list
// endpoint. Spec.md §8.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, email, role, status, created_at, updated_at
		  FROM users
		 ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("auth: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created, updated string
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Status, &created, &updated); err != nil {
			return nil, fmt.Errorf("auth: scan user: %w", err)
		}
		u.CreatedAt = parseRFC3339(created)
		u.UpdatedAt = parseRFC3339(updated)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Service) userByID(ctx context.Context, id int64) (User, error) {
	var u User
	var created, updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, email, role, status, created_at, updated_at
		  FROM users WHERE id = ?
	`, id).Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: select user by id: %w", err)
	}
	u.CreatedAt = parseRFC3339(created)
	u.UpdatedAt = parseRFC3339(updated)
	return u, nil
}

// SeedAdmin upserts an admin user from the given env-var-derived credentials.
// If username is empty the call is a no-op (operator has not configured an
// admin). When username matches an existing admin row, the password hash is
// rotated so env-var changes take effect on the next boot. Spec.md §6.
func SeedAdmin(ctx context.Context, db *sql.DB, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil
	}
	if len(password) < minPasswordLen {
		return fmt.Errorf("auth: seed admin password must be at least %d characters", minPasswordLen)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("auth: seed admin hash: %w", err)
	}

	// Upsert via INSERT ... ON CONFLICT. modernc.org/sqlite supports this.
	_, err = db.ExecContext(ctx, `
		INSERT INTO users (username, email, password_hash, role, status)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash = excluded.password_hash,
			role          = 'ADMIN',
			status        = 'APPROVED',
			updated_at    = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`, username, username+"@podium.local", hash, RoleAdmin, StatusApproved)
	if err != nil {
		return fmt.Errorf("auth: seed admin upsert: %w", err)
	}
	return nil
}

// parseRFC3339 tolerates the millisecond/microsecond suffixes that
// strftime('%Y-%m-%dT%H:%M:%fZ', 'now') produces.
func parseRFC3339(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
