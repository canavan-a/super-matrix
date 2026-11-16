-- +goose Up
-- Whether Nix services may be applied to a relay (its machine must also
-- run the Nix agent), and the agent's last host check as JSON ('' for none).
ALTER TABLE relays ADD COLUMN allow_nix_services INTEGER NOT NULL DEFAULT 0;
ALTER TABLE relays ADD COLUMN nix_host TEXT NOT NULL DEFAULT '';

-- A nix_service is a flake (its files inline: flake.nix, flake.lock and any
-- others) and how to run it, applied to relays by applied_nix_services.
-- files is a JSON object of path → content without flake.lock, which is
-- flake_lock: given (lock_pinned) or made by the first relay to build it.
CREATE TABLE nix_services (
    id           TEXT PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    run          TEXT NOT NULL DEFAULT 'package',
    attr         TEXT NOT NULL DEFAULT 'default',
    command      TEXT NOT NULL DEFAULT '[]',
    env          TEXT NOT NULL DEFAULT '{}',
    relay_env    TEXT NOT NULL DEFAULT '',
    restart      TEXT NOT NULL DEFAULT 'always',
    remove_state INTEGER NOT NULL DEFAULT 1,
    files        TEXT NOT NULL DEFAULT '{}',
    flake_lock   TEXT NOT NULL DEFAULT '',
    lock_pinned  INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL,
    updated_at   DATETIME NOT NULL
);

-- An applied_nix_service is a nix_service on one relay, with the relay's
-- overrides (env, merged over the service's) and the phase the relay last
-- reported. Removing one sets removing: the row stays (as tearing-down)
-- until the relay says it's gone, so nix_service_id is NULL once the
-- service itself is deleted, and slug keeps its name.
CREATE TABLE applied_nix_services (
    id             TEXT PRIMARY KEY,
    nix_service_id TEXT REFERENCES nix_services(id) ON DELETE SET NULL,
    relay_id       TEXT NOT NULL REFERENCES relays(id) ON DELETE CASCADE,
    slug           TEXT NOT NULL,
    desired        TEXT NOT NULL DEFAULT 'running',
    env            TEXT NOT NULL DEFAULT '{}',
    removing       INTEGER NOT NULL DEFAULT 0,
    phase          TEXT NOT NULL DEFAULT 'pending',
    phase_since    DATETIME NOT NULL,
    serving        TEXT NOT NULL DEFAULT '',
    previous       TEXT NOT NULL DEFAULT '',
    build_hash     TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    missing_env    TEXT NOT NULL DEFAULT '',
    restarts       INTEGER NOT NULL DEFAULT 0,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL
);
CREATE UNIQUE INDEX applied_nix_services_once ON applied_nix_services(nix_service_id, relay_id) WHERE removing = 0;
CREATE INDEX applied_nix_services_relay ON applied_nix_services(relay_id);

-- nix_service_events is each applied service's history: phase changes,
-- builds, crashes, with the log tail of a failure. The newest 50 per
-- applied service are kept.
CREATE TABLE nix_service_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    applied_id TEXT NOT NULL REFERENCES applied_nix_services(id) ON DELETE CASCADE,
    at         DATETIME NOT NULL,
    kind       TEXT NOT NULL,
    message    TEXT NOT NULL DEFAULT '',
    log        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX nix_service_events_applied ON nix_service_events(applied_id, id);

-- +goose Down
DROP TABLE nix_service_events;
DROP TABLE applied_nix_services;
DROP TABLE nix_services;
ALTER TABLE relays DROP COLUMN nix_host;
ALTER TABLE relays DROP COLUMN allow_nix_services;
