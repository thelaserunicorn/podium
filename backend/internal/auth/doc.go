// Package auth handles passwords, sessions, signup, login, and admin user
// lifecycle (approve / reject / disable) for the Podium control plane.
//
// Layering (per AGENTS.md §11):
//
//	HTTP handlers (handler.go)
//	    → Service (service.go)
//	        → SQL store (storage.Open)
//
// Handlers stay thin; the Service holds the bcrypt + status-check rules;
// sessions live in the sessions table created by migrations/0001_init.sql.
//
// See spec.md §6–§8, DECISIONS.md F, AGENTS.md §6, §19, §41.
package auth
