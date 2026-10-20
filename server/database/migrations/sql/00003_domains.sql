-- +goose NO TRANSACTION
-- +goose Up

-- Domains deployments can live on, managed in the app. A wildcard domain
-- (*.example.com, stored as "example.com") gives each deployment a
-- subdomain; an exact one ("status.foo.org") is taken whole by one
-- deployment. Either way every hostname still gets its own certificate.
--
-- deployments is rebuilt (SQLite can't drop the old UNIQUE on subdomain):
-- the unique thing is now the full host. Rows from before this get their
-- domain and host at startup, from SUPERMATRIX_BASE_DOMAIN (database.Seed).
-- Foreign keys are off during the swap so dropping the old table doesn't
-- cascade into deployment_tokens.
PRAGMA foreign_keys = OFF;

BEGIN;

CREATE TABLE domains (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    wildcard   INTEGER NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE TABLE deployments_new (
    id               TEXT PRIMARY KEY,
    relay_id         TEXT NOT NULL REFERENCES relays(id),
    domain_id        TEXT REFERENCES domains(id),
    subdomain        TEXT NOT NULL DEFAULT '',
    host             TEXT,
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

INSERT INTO deployments_new (
    id, relay_id, subdomain, protocol, target_host, target_port, tls_skip_verify, sni, preserve_host,
    enabled, auth_mode, login_pass_hash, allow_root_login, session_ttl_s, basic_user, basic_pass_hash,
    ip_allowlist, cert_status, cert_expires_at, cert_error, created_at, updated_at)
SELECT
    id, relay_id, subdomain, protocol, target_host, target_port, tls_skip_verify, sni, preserve_host,
    enabled, auth_mode, login_pass_hash, allow_root_login, session_ttl_s, basic_user, basic_pass_hash,
    ip_allowlist, cert_status, cert_expires_at, cert_error, created_at, updated_at
FROM deployments;

DROP TABLE deployments;
ALTER TABLE deployments_new RENAME TO deployments;
CREATE INDEX deployments_relay ON deployments(relay_id);
CREATE INDEX deployments_domain ON deployments(domain_id);
-- NULL until Seed fills it in for rows from before domains existed.
CREATE UNIQUE INDEX deployments_host ON deployments(host);

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

BEGIN;

CREATE TABLE deployments_old (
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

-- Exact-domain deployments have no subdomain to go back to; they're lost.
INSERT INTO deployments_old
SELECT id, relay_id, subdomain, protocol, target_host, target_port, tls_skip_verify, sni, preserve_host,
    enabled, auth_mode, login_pass_hash, allow_root_login, session_ttl_s, basic_user, basic_pass_hash,
    ip_allowlist, cert_status, cert_expires_at, cert_error, created_at, updated_at
FROM deployments WHERE subdomain <> '' GROUP BY subdomain;

DROP TABLE deployments;
ALTER TABLE deployments_old RENAME TO deployments;
CREATE INDEX deployments_relay ON deployments(relay_id);
DROP TABLE domains;

COMMIT;

PRAGMA foreign_keys = ON;
