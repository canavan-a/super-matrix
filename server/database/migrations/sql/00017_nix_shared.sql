-- +goose Up
-- Nix services a relay defines are everyone's, like the admin's: they run
-- on any relay, and nothing about them belongs to the relay that made them.
-- (A relay's services used to be its own until the admin adopted them.)
ALTER TABLE nix_services DROP COLUMN owner_relay_id;

-- +goose Down
ALTER TABLE nix_services ADD COLUMN owner_relay_id TEXT REFERENCES relays(id) ON DELETE CASCADE;
