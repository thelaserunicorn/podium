-- 0003_deployment_deleted_at.sql — soft-delete column for the M5
-- deployment-delete flow. The Kubernetes Deployment is removed in
-- cluster; the SQLite row is kept (audit trail) but flagged with
-- deleted_at so list endpoints can hide it from the user.

ALTER TABLE deployments ADD COLUMN deleted_at TEXT;

-- Index supports the most common list query (per-app-per-ns, active
-- first, newest first). The status filter is left to the application
-- layer because we want to keep the index small.
CREATE INDEX idx_deployments_app_env_deleted ON deployments(application_id, environment_id, deleted_at);
