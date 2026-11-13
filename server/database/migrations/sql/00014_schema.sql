-- +goose Up
-- settings is a small key/value store for server-wide switches the app can
-- flip (schema_override_until: until when schema-managed values may be
-- edited by hand).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- api_tokens are long-lived bearer tokens (sma_…) for automation, like CI
-- applying a schema. scope says what one may do; a token with a scope this
-- binary doesn't know is refused. Changing the root password doesn't revoke
-- them (CI keeps working); they're revoked one by one.
CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL,
    created_at   DATETIME NOT NULL,
    last_used_at DATETIME
);

-- schema_state is the last applied schema document (one row, id 1). No row
-- means no schema was ever applied: nothing is locked.
CREATE TABLE schema_state (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    revision   INTEGER NOT NULL,
    document   TEXT NOT NULL,
    doc_hash   TEXT NOT NULL,
    applied_at DATETIME NOT NULL,
    applied_by TEXT NOT NULL DEFAULT ''
);

-- schema_owned is what the last apply took charge of. A row with field ''
-- is a whole resource (kind, resource_id); a row with a field (its path in
-- the document, e.g. ip_allowlist) and entry '' is a value only the schema
-- sets; a row with an entry is one schema-owned entry of a list the UI may
-- add to. kind 'collection' with resource_id the collection's name marks a
-- collection the schema lists in full. No foreign keys: rows for resources
-- deleted behind the schema's back (by an older binary, say) are ignored
-- and cleaned up on the next apply.
CREATE TABLE schema_owned (
    kind        TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    field       TEXT NOT NULL DEFAULT '',
    entry       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (kind, resource_id, field, entry)
);

-- +goose Down
DROP TABLE schema_owned;
DROP TABLE schema_state;
DROP TABLE api_tokens;
DROP TABLE settings;
