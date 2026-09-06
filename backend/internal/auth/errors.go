package auth

import "errors"

// Typed errors returned by Service. Handlers map these to HTTP statuses; the
// rest of the codebase can errors.Is against them. Per AGENTS.md §41 we
// intentionally collapse "user not found" and "wrong password" into
// ErrInvalidCredentials so we never leak which one failed.
var (
	ErrInvalidUsername    = errors.New("auth: invalid username")
	ErrInvalidEmail       = errors.New("auth: invalid email")
	ErrInvalidPassword    = errors.New("auth: password must be at least 8 characters")
	ErrUsernameTaken      = errors.New("auth: username already in use")
	ErrEmailTaken         = errors.New("auth: email already in use")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrLoginBlocked       = errors.New("auth: login is not permitted for this account")
	ErrSessionNotFound    = errors.New("auth: session not found or expired")
	ErrUserNotFound       = errors.New("auth: user not found")
)
