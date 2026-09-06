// Package application implements CRUD over the applications table — the
// per-user deployable projects Podium manages. See spec.md §9, §34 and
// AGENTS.md §14.
//
// Layering (per AGENTS.md §11):
//
//	HTTP handlers (handler.go)
//	    → Service (service.go)
//	        → SQL store (storage.Open)
//
// Handlers stay thin; the Service enforces ownership (returning ErrNotFound,
// not 403, on cross-user access — AGENTS.md §19), validation, and the
// monotonic per-app version counter (DECISIONS.md D).
//
// Environment variables for an application live in env.go. The M1 slice
// provides the table skeleton only — full Secret/ConfigMap wiring lands in
// M4 (PLAN.md M4).
package application
