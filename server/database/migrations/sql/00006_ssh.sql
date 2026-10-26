-- +goose Up
-- SSH deployments: whether the CLI proxy and the browser shell are on, the
-- username the shell suggests, and the target's host key (trusted on first
-- use, "" until then).
ALTER TABLE deployments ADD COLUMN ssh_cli INTEGER NOT NULL DEFAULT 1;
ALTER TABLE deployments ADD COLUMN ssh_shell INTEGER NOT NULL DEFAULT 1;
ALTER TABLE deployments ADD COLUMN ssh_user TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN ssh_host_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE deployments DROP COLUMN ssh_host_key;
ALTER TABLE deployments DROP COLUMN ssh_user;
ALTER TABLE deployments DROP COLUMN ssh_shell;
ALTER TABLE deployments DROP COLUMN ssh_cli;
