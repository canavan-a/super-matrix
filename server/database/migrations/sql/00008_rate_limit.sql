-- +goose Up
-- Per-deployment, per-client-IP rate limiting at the edge. period_s is the
-- window length in seconds (60 = "per minute", 1 = "per second"); burst, if
-- > 0, caps a short 1s window on top of the sustained rate.
ALTER TABLE deployments ADD COLUMN rate_limit_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN rate_limit_requests INTEGER NOT NULL DEFAULT 100;
ALTER TABLE deployments ADD COLUMN rate_limit_period_s INTEGER NOT NULL DEFAULT 60;
ALTER TABLE deployments ADD COLUMN rate_limit_burst INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE deployments DROP COLUMN rate_limit_burst;
ALTER TABLE deployments DROP COLUMN rate_limit_period_s;
ALTER TABLE deployments DROP COLUMN rate_limit_requests;
ALTER TABLE deployments DROP COLUMN rate_limit_enabled;
