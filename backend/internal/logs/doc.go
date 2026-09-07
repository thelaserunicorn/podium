// Package logs is the read-side of Podium's diagnostics surface. It
// exposes three services used by the API layer in M5:
//
//   - PodLogs reads the current container's stdout/stderr from a Pod
//     through the Kubernetes API. Per DECISIONS.md B, we do NOT pass
//     --previous, so a crashing container's logs are not retrievable
//     after a restart — that matches the spec's "recent log output"
//     wording and is the simplest implementation that works.
//
//   - BuildLogs reads the demuxed Docker build output that the
//     orchestrator streamed into the SQLite deploy_log_lines table
//     during the M2 build stage. It is purely a wrapper around the
//     storage layer's LogLinesSince cursor.
//
//   - Events reads Kubernetes events for a namespace, optionally
//     filtered to a specific object UID. Used by the deployment
//     detail panel to surface "Pulled / Created / Started / Unhealthy"
//     signals Podium doesn't otherwise have visibility into.
//
// Pod logs are NOT persisted to SQLite (DECISIONS.md §10 / AGENTS.md
// §10). They live only in the cluster. If the cluster forgets them
// (kubelet rotates, pod gets evicted), they're gone — that is the
// right behaviour for a single-week IDP.
package logs
