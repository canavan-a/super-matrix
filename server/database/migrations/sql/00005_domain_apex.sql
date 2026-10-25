-- +goose NO TRANSACTION
-- +goose Up

-- An exact domain can sit alongside the wildcard of the same name
-- ("foo.org" and "*.foo.org"): the wildcard's hosts are always
-- <label>.foo.org, never foo.org itself. domains is rebuilt since SQLite
-- can't drop the UNIQUE on name; foreign keys are off during the swap so
-- deployments.domain_id keeps pointing at the new table.
PRAGMA foreign_keys = OFF;

BEGIN;

CREATE TABLE domains_new (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    wildcard   INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE (name, wildcard)
);

INSERT INTO domains_new (id, name, wildcard, created_at)
SELECT id, name, wildcard, created_at FROM domains;

DROP TABLE domains;
ALTER TABLE domains_new RENAME TO domains;

COMMIT;

PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;

BEGIN;

-- Exact domains that share a name with a wildcard go, with their deployments.
CREATE TEMP TABLE twins AS
SELECT id FROM domains WHERE wildcard = 0 AND name IN (SELECT name FROM domains WHERE wildcard = 1);
DELETE FROM deployment_tokens WHERE deployment_id IN (SELECT id FROM deployments WHERE domain_id IN (SELECT id FROM twins));
DELETE FROM deployments WHERE domain_id IN (SELECT id FROM twins);

CREATE TABLE domains_old (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    wildcard   INTEGER NOT NULL,
    created_at DATETIME NOT NULL
);

INSERT INTO domains_old (id, name, wildcard, created_at)
SELECT id, name, wildcard, created_at FROM domains WHERE id NOT IN (SELECT id FROM twins);

DROP TABLE domains;
ALTER TABLE domains_old RENAME TO domains;
DROP TABLE twins;

COMMIT;

PRAGMA foreign_keys = ON;
