-- +goose Up
-- A path rule layers an extra IP allowlist onto one path prefix (and
-- everything under it) of a deployment, on top of the deployment's own
-- ip_allowlist: it can only narrow who may reach that path, never widen
-- who may reach the deployment as a whole.
CREATE TABLE path_rules (
    id            TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    path_prefix   TEXT NOT NULL,
    ip_allowlist  TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);
CREATE INDEX path_rules_deployment ON path_rules(deployment_id);
CREATE UNIQUE INDEX path_rules_prefix ON path_rules(deployment_id, path_prefix);

-- +goose Down
DROP TABLE path_rules;
