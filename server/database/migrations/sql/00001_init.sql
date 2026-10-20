-- +goose Up

-- A relay is a runner on some device that dials out to this server and
-- forwards traffic to the targets of its deployments.
CREATE TABLE relays (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   DATETIME NOT NULL,
    last_seen_at DATETIME,
    last_ip      TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    hostname     TEXT NOT NULL DEFAULT ''
);

-- A deployment exposes <subdomain>.<base domain> through a relay to
-- protocol://target_host:target_port, behind one auth mode.
CREATE TABLE deployments (
    id               TEXT PRIMARY KEY,
    relay_id         TEXT NOT NULL REFERENCES relays(id),
    subdomain        TEXT NOT NULL UNIQUE,
    protocol         TEXT NOT NULL DEFAULT 'http',
    target_host      TEXT NOT NULL,
    target_port      INTEGER NOT NULL,
    tls_skip_verify  INTEGER NOT NULL DEFAULT 0,
    sni              TEXT NOT NULL DEFAULT '',
    preserve_host    INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1,
    auth_mode        TEXT NOT NULL DEFAULT 'login',
    login_pass_hash  TEXT NOT NULL DEFAULT '',
    allow_root_login INTEGER NOT NULL DEFAULT 1,
    session_ttl_s    INTEGER NOT NULL DEFAULT 604800,
    basic_user       TEXT NOT NULL DEFAULT '',
    basic_pass_hash  TEXT NOT NULL DEFAULT '',
    ip_allowlist     TEXT NOT NULL DEFAULT '',
    cert_status      TEXT NOT NULL DEFAULT 'pending',
    cert_expires_at  DATETIME,
    cert_error       TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX deployments_relay ON deployments(relay_id);

-- API tokens for deployments in "token" auth mode; only hashes are kept.
CREATE TABLE deployment_tokens (
    id            TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    token_hash    TEXT NOT NULL UNIQUE,
    created_at    DATETIME NOT NULL,
    last_used_at  DATETIME
);
CREATE INDEX deployment_tokens_deployment ON deployment_tokens(deployment_id);

-- Root logins from the app. password_fp ties a session to the root password
-- it was made with, so changing the password signs everyone out.
CREATE TABLE admin_sessions (
    token_hash  TEXT PRIMARY KEY,
    password_fp TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    expires_at  DATETIME NOT NULL,
    user_agent  TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE admin_sessions;
DROP TABLE deployment_tokens;
DROP TABLE deployments;
DROP TABLE relays;
