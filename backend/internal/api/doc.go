// Package api wires the Podium HTTP surface: middleware, the global router,
// and cross-cutting handlers (admin user management). The domain packages
// (auth, application, …) own their own endpoints; this package is just glue.
//
// See spec.md §33 (HTTP API), §34 (Authorization), §41 (Security), and
// AGENTS.md §11 (API design) and §14 (ownership / authorization).
package api
