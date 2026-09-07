-- 0002_envvars_timestamps.sql — add created_at / updated_at to
-- environment_variables. The M1 schema (0001_init.sql) left them
-- out to keep the table minimal; M4 needs them for the API
-- response shape so the UI can show "added X minutes ago".

ALTER TABLE environment_variables ADD COLUMN created_at TEXT NOT NULL
    DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
ALTER TABLE environment_variables ADD COLUMN updated_at TEXT NOT NULL
    DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
