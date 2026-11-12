-- +goose Up
-- An email group is a named list of addresses (and *@domain wildcards) that
-- deployments' email-code sign-in lists can include, so one list of people
-- is kept in one place. entries is newline-separated, like otp_emails.
-- It mirrors ip_groups: deleting a deployment takes its rows along;
-- deleting a group still in use is refused (the foreign key's default NO
-- ACTION), so a sign-in list can never silently lose a group.
CREATE TABLE email_groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    entries    TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX email_groups_name ON email_groups(name COLLATE NOCASE);

CREATE TABLE deployment_email_groups (
    deployment_id  TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    email_group_id TEXT NOT NULL REFERENCES email_groups(id),
    PRIMARY KEY (deployment_id, email_group_id)
);
CREATE INDEX deployment_email_groups_group ON deployment_email_groups(email_group_id);

-- +goose Down
DROP TABLE deployment_email_groups;
DROP TABLE email_groups;
