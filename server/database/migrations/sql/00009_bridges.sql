-- +goose Up
-- What a relay said it supports in its last hello, comma-separated (e.g.
-- "bridge-tcp,bridge-udp"). Empty for relays that never said.
ALTER TABLE relays ADD COLUMN features TEXT NOT NULL DEFAULT '';

-- A bridge is a TCP or UDP port that leads to target_host:target_port on
-- to_relay's network. It listens on from_relay's machine, or on the server
-- itself (a public bridge) when from_relay_id is NULL.
CREATE TABLE bridges (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    protocol      TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    from_relay_id TEXT REFERENCES relays(id),
    listen_host   TEXT NOT NULL DEFAULT '127.0.0.1',
    listen_port   INTEGER NOT NULL,
    to_relay_id   TEXT NOT NULL REFERENCES relays(id),
    target_host   TEXT NOT NULL,
    target_port   INTEGER NOT NULL,
    udp_idle_s    INTEGER NOT NULL DEFAULT 300,
    ip_allowlist  TEXT NOT NULL DEFAULT '',
    -- A public bridge can have a DNS name, on one of the domains: shown as its
    -- address (host:listen_port). Connections are told apart by port, not
    -- name; the name just has to resolve to the server.
    domain_id     TEXT REFERENCES domains(id),
    subdomain     TEXT NOT NULL DEFAULT '',
    host          TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);
CREATE INDEX bridges_from ON bridges(from_relay_id);
CREATE INDEX bridges_to ON bridges(to_relay_id);
CREATE INDEX bridges_domain ON bridges(domain_id);
CREATE UNIQUE INDEX bridges_listen ON bridges(from_relay_id, protocol, listen_host, listen_port) WHERE from_relay_id IS NOT NULL;
CREATE UNIQUE INDEX bridges_public ON bridges(protocol, listen_port) WHERE from_relay_id IS NULL;

-- +goose Down
DROP TABLE bridges;
ALTER TABLE relays DROP COLUMN features;
