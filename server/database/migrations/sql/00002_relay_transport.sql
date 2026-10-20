-- +goose Up
-- How each relay last connected (quic | websocket), and why it last wasn't
-- on QUIC, for when it's offline; live values come from the hub.
ALTER TABLE relays ADD COLUMN transport TEXT NOT NULL DEFAULT '';
ALTER TABLE relays ADD COLUMN fallback TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE relays DROP COLUMN fallback;
ALTER TABLE relays DROP COLUMN transport;
