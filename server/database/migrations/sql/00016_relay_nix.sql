-- +goose Up
-- Whether a relay may apply Nix services it defined to itself, and manage
-- them there, with its token (they run as root on its machine). Defining
-- one needs no permission: it does nothing until it's applied.
ALTER TABLE relays ADD COLUMN manage_nix_services INTEGER NOT NULL DEFAULT 0;

-- owner_relay_id: the relay that defined the service with its token (NULL
-- for the admin's and the schema's). It can only run on that relay until
-- it's adopted, and goes with the relay.
ALTER TABLE nix_services ADD COLUMN owner_relay_id TEXT REFERENCES relays(id) ON DELETE CASCADE;

-- pending_lock: a flake.lock a relay made for a service it doesn't own,
-- waiting to be accepted (pending_lock_from is that relay's ID). Until
-- then the server keeps the lock it has.
ALTER TABLE nix_services ADD COLUMN pending_lock TEXT NOT NULL DEFAULT '';
ALTER TABLE nix_services ADD COLUMN pending_lock_from TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nix_services DROP COLUMN pending_lock_from;
ALTER TABLE nix_services DROP COLUMN pending_lock;
ALTER TABLE nix_services DROP COLUMN owner_relay_id;
ALTER TABLE relays DROP COLUMN manage_nix_services;
