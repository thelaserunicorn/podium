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
	// ErrHasDeployments is returned by Service.Update when the caller
	// tries to rename an application that already has at least one
	// deployment row. The application name is baked into the Kubernetes
	// Deployment / Service names (DECISIONS.md E), so silently renaming
	// an app with live history would orphan the cluster resources.
	// Mapped to 409 Conflict by the handler. URL and port updates are
	// not affected by this restriction.
	ErrHasDeployments = errors.New("application: cannot rename — has existing deployments")
)
