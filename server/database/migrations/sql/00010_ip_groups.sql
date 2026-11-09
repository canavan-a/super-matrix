-- +goose Up
-- An IP group is a named set of addresses and CIDR ranges that deployments'
-- and public bridges' allow lists can include, so one list is kept in one
-- place. entries is newline-separated, like ip_allowlist.
CREATE TABLE ip_groups (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    entries    TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX ip_groups_name ON ip_groups(name COLLATE NOCASE);

-- Which groups each allow list includes. Deleting a deployment or bridge
-- takes its rows along; deleting a group still in use is refused (the
-- foreign key's default NO ACTION), so an allow list can never silently
-- lose a group.
CREATE TABLE deployment_ip_groups (
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    ip_group_id   TEXT NOT NULL REFERENCES ip_groups(id),
    PRIMARY KEY (deployment_id, ip_group_id)
);
CREATE INDEX deployment_ip_groups_group ON deployment_ip_groups(ip_group_id);

CREATE TABLE bridge_ip_groups (
    bridge_id   TEXT NOT NULL REFERENCES bridges(id) ON DELETE CASCADE,
    ip_group_id TEXT NOT NULL REFERENCES ip_groups(id),
    PRIMARY KEY (bridge_id, ip_group_id)
);
CREATE INDEX bridge_ip_groups_group ON bridge_ip_groups(ip_group_id);

-- +goose Down
DROP TABLE bridge_ip_groups;
DROP TABLE deployment_ip_groups;
DROP TABLE ip_groups;
