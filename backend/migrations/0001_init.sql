-- 0001_init.sql — Podium MVP schema. See PLAN.md "Storage" and spec.md §29.
--
-- Precedence when this disagrees with spec.md: DECISIONS.md > AGENTS.md > spec.md.
-- Relevant decisions:
--   C. Environments ARE namespaces; we model them in one table with `namespace`
--      as the unique key. The three defaults are seeded here so the rest of
--      the system can treat `environment_id` as a FK into namespaces.
--   D. `version` is a monotonic counter per application starting at 1.
--   E. Environment variables carry a single `is_secret` boolean; values for
--      secret vars are never returned by any GET endpoint (AGENTS.md §19, §41).
--   F. Sessions are opaque random tokens stored server-side; the cookie only
--      holds the token.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('ADMIN', 'USER')),
    status        TEXT NOT NULL CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'DISABLED')),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE applications (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    repository_url TEXT NOT NULL,
    container_port INTEGER NOT NULL,
    -- version is a monotonic counter per app, used as the image tag suffix
    -- (deployment #1 → image <app>:v1). Per DECISIONS.md D.
    version        INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (user_id, name)
);

CREATE TABLE environments (
    -- Each row is a Kubernetes namespace. `name` is the human-readable label
    -- shown in the UI ("Development"); `namespace` is the DNS-1123 Kubernetes
    -- namespace name ("podium-dev"). Per DECISIONS.md C.
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    namespace  TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- The three default namespaces from DECISIONS.md C. Idempotent on re-run via
-- INSERT OR IGNORE. Custom namespaces created by users land in this same
-- table (validation happens in the application service, not here).
INSERT OR IGNORE INTO environments (name, namespace) VALUES
    ('Development', 'podium-dev'),
    ('Staging',     'podium-staging'),
    ('Production',  'podium-prod');

CREATE TABLE deployments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    application_id  INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    environment_id  INTEGER NOT NULL REFERENCES environments(id),
    version         INTEGER NOT NULL,
    image           TEXT NOT NULL,
    replicas        INTEGER NOT NULL,
    status          TEXT NOT NULL CHECK (status IN (
        'QUEUED', 'BUILDING', 'BUILT', 'DEPLOYING',
        'STARTING', 'RUNNING', 'FAILED'
    )),
    reason          TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at      TEXT,
    finished_at     TEXT
);
CREATE INDEX idx_deployments_app_env ON deployments(application_id, environment_id);

CREATE TABLE environment_variables (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    application_id INTEGER NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    environment_id INTEGER NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    key            TEXT NOT NULL,
    value          TEXT NOT NULL,
    is_secret      INTEGER NOT NULL DEFAULT 0 CHECK (is_secret IN (0, 1)),
    UNIQUE (application_id, environment_id, key)
);

CREATE TABLE sessions (
    -- Opaque random token (32 bytes from crypto/rand, base64url-encoded by
    -- the auth package). The HttpOnly cookie just holds this string.
    id         TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

-- Build logs only. Pod logs are NEVER written here (DECISIONS.md B + AGENTS.md §10).
CREATE TABLE deploy_log_lines (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    ts            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    line          TEXT NOT NULL
);
CREATE INDEX idx_deploy_log_lines_deployment_ts ON deploy_log_lines(deployment_id, ts);
