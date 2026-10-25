-- +goose Up
-- Email-code sign-in: the addresses that may get a code, one per line.
ALTER TABLE deployments ADD COLUMN otp_emails TEXT NOT NULL DEFAULT '';
-- API tokens can expire; NULL never does.
ALTER TABLE deployment_tokens ADD COLUMN expires_at DATETIME;

-- +goose Down
ALTER TABLE deployment_tokens DROP COLUMN expires_at;
ALTER TABLE deployments DROP COLUMN otp_emails;
