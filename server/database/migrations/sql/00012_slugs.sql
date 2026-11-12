-- +goose Up
-- A slug is a relay's or bridge's stable, friendly ID: lowercase letters,
-- digits and dashes (a DNS label), unique per table. Config files name
-- relays and bridges by it, so it must not change when the display name
-- does. Existing rows are given one from their name by database.BackfillSlugs
-- at startup (collapsing runs of other characters is easier in Go than in
-- SQLite).
--
-- The unique indexes skip '' so an older binary, which doesn't know about
-- slugs and inserts the column default, can still create relays and bridges
-- after a downgrade; the next startup of a newer one fills them in.
ALTER TABLE relays ADD COLUMN slug TEXT NOT NULL DEFAULT '';
ALTER TABLE bridges ADD COLUMN slug TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX relays_slug ON relays(slug) WHERE slug != '';
CREATE UNIQUE INDEX bridges_slug ON bridges(slug) WHERE slug != '';

-- +goose Down
DROP INDEX bridges_slug;
DROP INDEX relays_slug;
ALTER TABLE bridges DROP COLUMN slug;
ALTER TABLE relays DROP COLUMN slug;
