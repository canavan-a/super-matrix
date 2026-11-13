# Declarative schema

A schema document describes a server's domains, IP and email groups,
deployments (with their path rules) and bridges in YAML or JSON. The server
**plans** a document (shows what applying it would change) and **applies**
it in one transaction: all of it, or nothing. Keep the document in git and
apply it from CI, from the command line, or by pasting it into the app's
Schema screen.

Relays aren't declared: they're paired by hand, and the document names them
by **slug** (Relays screen). Bridges are keyed by their slug too.

## A document

```yaml
version: 1

domains:
  - "*.example.com"          # wildcard: deployments get a name under it
  - status.example.org       # exact: one deployment's whole name

groups:
  ip:
    office:
      entries: [203.0.113.0/24, 2001:db8::/48]
  email:
    team:
      entries: [ann@example.com, "*@corp.example"]

deployments:
  "...": true                # deployments made in the app stay
  grafana.example.com:
    relay: homelab           # the relay's slug
    target_host: 10.0.0.5
    target_port: 3000
    auth_mode: otp
    email_groups: [team]
    ip_groups: [office]
    ip_allowlist: ["..."]    # the app may add addresses here
    path_rules:
      /admin:
        ip_allowlist: [10.0.0.0/8]
    ui: [enabled]            # the app turns it on and off
  status.example.org:
    relay: homelab
    target_host: 10.0.0.6
    target_port: 80
    auth_mode: none

bridges:
  minecraft:
    public: true
    listen_port: 25565
    to_relay: homelab
    target_host: 10.0.0.7
```

The fields follow the API's, in snake_case. For editor completion and
checking, point your editor at the JSON Schema from
`GET /v1/schema/jsonschema` (for the YAML language server, a
`# yaml-language-server: $schema=…` comment on the first line).

| Deployment field | Default |
|---|---|
| `relay` (slug) | required |
| `domain` | worked out from the hostname; name it (`*.example.com`) only if two fit |
| `protocol` | `http` (`https`, `ssh`) |
| `target_host` | required |
| `target_port` | required (22 for ssh) |
| `tls_skip_verify`, `sni`, `preserve_host` | `false`, `""`, `false` |
| `enabled` | `true` |
| `auth_mode` | `login` (`none`, `otp`, `basic`, `token`) |
| `login_password`, `basic_password` | `{hash: <bcrypt>}`; left out, the app sets them |
| `allow_root_login` | `true` |
| `session_ttl_s` | `604800` (a week) |
| `basic_user` | `""` |
| `otp_emails`, `email_groups`, `ip_allowlist`, `ip_groups` | `[]` |
| `ssh_cli`, `ssh_shell`, `ssh_user` | `true`, `true`, `""` |
| `rate_limit_enabled`, `rate_limit_requests`, `rate_limit_period_s`, `rate_limit_burst` | `false`, `100`, `60`, `0` |
| `path_rules` | none: `{"/prefix": {ip_allowlist: [...]}}` |

Bridges: `name` (the slug), `protocol` (`tcp`), `enabled` (`true`), `public`
(`false`), `from_relay` (required unless public), `listen_host`
(`127.0.0.1`), `listen_port` (required), `to_relay` (required),
`target_host` (required), `target_port` (the listen port), `udp_idle_s`
(300), and for public bridges `ip_allowlist`, `ip_groups`, `domain` and
`subdomain`.

Passwords are only ever bcrypt hashes (`htpasswd -nbBC 10 "" 'secret' | cut
-d: -f2`); there is no way to put a plain password in a document.

## What the schema manages

**Strict by default.** What a document lists is the whole truth:

- an item it lists has exactly the values given, and its missing fields
  take the defaults above;
- a collection it lists (`deployments`, `bridges`, `domains`, `groups.ip`,
  `groups.email`) loses whatever the document doesn't list;
- a collection it leaves out is left alone entirely (`version: 1` alone
  changes nothing).

Three markers hand things back to the app:

| Marker | Where | Means |
|---|---|---|
| `"..."` | an entry of a list | the list keeps entries added in the app; the schema's own entries are kept too, and removing one from the document removes it from the server |
| `"...": true` | a key of a map (`deployments`, `bridges`, `groups.ip`, `groups.email`, `path_rules`) | items made in the app stay; only items the schema made or took charge of are deleted when the document drops them |
| `ui: [field, …]` | an item | those fields are the app's: a value given for one is only used when the item is created |

Quote the marker (`"..."`): bare `...` ends a YAML document.

What the schema manages is **locked** in the app (and for relays managing
their own deployments): editing it, deleting a managed item, renaming one, or
adding an item to a collection the document lists in full is refused with
`… is managed by schema rev N`. Resending a value unchanged is fine.

**Override mode** (Settings) unlocks everything for an emergency fix, for an
hour by default. Changes made meanwhile show up in the next plan as drift,
and the next apply puts the document's values back.

## Plan, apply, export

API routes (a session, or an API token with the `schema` scope):

| | |
|---|---|
| `GET /v1/schema` | `{revision, appliedAt, appliedBy, docHash, strict, override, versions}`; revision 0: never applied |
| `POST /v1/schema/plan` | body: the document. `{ops, changed, creates, updates, deletes, planHash, baseRevision, docVersion}`; each op is `{action, kind, key, changes, drift}`, action `create`, `update`, `delete` or `adopt` (an existing item the schema takes charge of) |
| `POST /v1/schema/apply` | body: the document; `X-Plan-Hash` (or `?planHash=`) applies only if the plan is still that one, else 409 with the new plan. `{applied, revision, plan}` |
| `GET /v1/schema/export` | the server as a document: every collection in full, current values, no markers. `?source=applied` returns the last applied document as it was sent; `?format=json` for JSON |
| `GET /v1/schema/jsonschema` | the document format as JSON Schema |

A problem with a document is a 400 listing every one found:
`{"error": "…", "errors": [{"path": "deployments.grafana.example.com.relay", "msg": "no relay with the slug \"nope\""}]}`.

### Getting started on an existing server

Nothing is managed until the first apply, and the app works as it always
has. To start, export the server, commit that, and apply it: it changes
nothing, only taking charge (`adopt`) of what's there. Then loosen it with
markers where the app should keep a say.

```sh
supermatrix-admin schema export -server https://admin.example.com -token sma_… > supermatrix.yaml
```

## API tokens

Make one in the app (Settings → API tokens) or with
`POST /v1/api-tokens {"name": "ci"}`; it's shown once. A token can use the
schema routes and nothing else. Changing the root password doesn't revoke
tokens, so CI keeps working; delete one to revoke it.

## The command line

```sh
export SUPERMATRIX_SERVER=https://admin.example.com SUPERMATRIX_TOKEN=sma_…
supermatrix-admin schema plan -f supermatrix.yaml     # exit 2 when it would change something
supermatrix-admin schema apply -f supermatrix.yaml    # prints the plan, then applies exactly that
supermatrix-admin schema export > supermatrix.yaml    # -source applied for the last applied document
```

Unlike the other `supermatrix-admin` commands, these go through the running
server over HTTP: the same checks as the app, effective at once, no restart.
(The direct-to-database commands, `rate-limit` and `path-rules`, don't check
schema locks; what they change shows up as drift in the next plan.)

## CI

Plan on pull requests, apply on the main branch. With GitHub Actions:

```yaml
name: supermatrix
on:
  pull_request:
    paths: [supermatrix.yaml]
  push:
    branches: [main]
    paths: [supermatrix.yaml]
jobs:
  schema:
    runs-on: ubuntu-latest
    env:
      SUPERMATRIX_SERVER: https://admin.example.com
      SUPERMATRIX_TOKEN: ${{ secrets.SUPERMATRIX_TOKEN }}
    steps:
      - uses: actions/checkout@v4
      - name: Plan
        if: github.event_name == 'pull_request'
        run: |
          curl -fsS -X POST "$SUPERMATRIX_SERVER/v1/schema/plan" \
            -H "Authorization: Bearer $SUPERMATRIX_TOKEN" \
            -H "Content-Type: application/yaml" \
            --data-binary @supermatrix.yaml | jq .
      - name: Apply
        if: github.event_name == 'push'
        run: |
          curl -fsS -X POST "$SUPERMATRIX_SERVER/v1/schema/apply" \
            -H "Authorization: Bearer $SUPERMATRIX_TOKEN" \
            -H "Content-Type: application/yaml" \
            --data-binary @supermatrix.yaml | jq .
```

With the `supermatrix-admin` binary on the runner, replace the curl calls
with `supermatrix-admin schema plan -f supermatrix.yaml` (exit 2 marks a
plan with changes; allow it with `|| [ $? = 2 ]`) and `… schema apply`.

## Versions

Every document starts with `version: N`. A server reads a range of versions
(`schemaVersions` in `/v1/info`) and converts older documents as it reads
them; a document newer than the server is refused rather than half applied.
A field added in a later version is left to the app when a document is
older than that version, so upgrading the server never resets values an
old document doesn't know about. Export always writes the server's current
version.
