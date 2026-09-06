package application

import "errors"

// Typed errors returned by Service. Handlers map these to HTTP statuses.
//
// Note the deliberate use of ErrNotFound (not ErrForbidden) on cross-user
// access. Per AGENTS.md §19 we must not leak whether an application id
// exists for a different user.
var (
	ErrInvalidName      = errors.New("application: invalid name (must be 3-63 chars, [a-z0-9-], no leading/trailing dash)")
	ErrInvalidRepoURL   = errors.New("application: invalid repository URL (must be http(s)://)")
	ErrInvalidPort      = errors.New("application: container_port must be between 1 and 65535")
	ErrInvalidNamespace = errors.New("application: invalid namespace (must be DNS-1123)")
	ErrNotFound         = errors.New("application: not found")
	ErrDuplicateName    = errors.New("application: name already in use")
)
