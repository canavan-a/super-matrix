-- +goose Up
-- Whether a relay may manage its own deployments with its token (the
-- /v1/relay/deployments API, and the CLI on the relay's machine). Off
-- until turned on in the app.
ALTER TABLE relays ADD COLUMN manage_deployments INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE relays DROP COLUMN manage_deployments;
