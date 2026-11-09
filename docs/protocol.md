# Super Matrix protocol

How the server, relays, visitors and the app talk. The code is the reference:
`server/tunnel` (relay link), `server/edge` (routing and auth), `server/api`
(admin API).

## Hosts

The server answers for two kinds of hostname, decided by the `Host` header (and
the TLS SNI):

| Host | What |
|---|---|
| `SUPERMATRIX_ADMIN_HOST` | `/health`, `/v1/*` (admin API, relay endpoint, live status), and the web app |
| a deployment's host: `<subdomain>.<wildcard domain>` or an exact domain | that deployment |
| anything else | 404, and no certificate |

**Domains** are managed in the app and stored in SQLite. A wildcard domain
(`*.example.com`) gives each deployment on it a subdomain; an exact domain
(`status.foo.org`) is one deployment's whole host. Any number of each can be
added, and a deployment's host is unique across all of them. If
`SUPERMATRIX_BASE_DOMAIN` is set, it becomes the first wildcard domain on a
server that has none. Wildcard domains never mean wildcard certificates: each
host gets its own.

## Certificates

With `SUPERMATRIX_TLS=acme`, `golang.org/x/crypto/acme/autocert` gets one
certificate per hostname: the admin host and each deployment (disabled ones too, so their page loads). Its host
policy only allows those names, so an unknown SNI never reaches Let's Encrypt.
HTTP-01 is answered on `:80` (everything else there redirects to https) and
TLS-ALPN-01 on `:443`. Certificates are cached in `DATA_DIR/certs`.

A new or renamed deployment's certificate is fetched at once in the background
("warmed"), with a hello that asks for ECDSA, so it's ready before the first
visitor. Every host is warmed again at startup and every 12 hours, which also
retries failures and refreshes the expiry dates the app shows (`cert_status`,
`cert_expires_at`). autocert renews certificates 30 days before they expire.

With TLS on, every response from the admin host and the deployments carries
`Strict-Transport-Security: max-age=31536000` (no `includeSubDomains`: other
names under a domain may not be this server's), unless the service behind a
deployment sends its own.

## The relay link

A relay connects over **QUIC** when the network lets UDP through, and over a
**WebSocket** (TCP) when it doesn't. Both carry the same session: one control
stream plus one stream per proxied connection. QUIC gives every stream its own
loss recovery (a dropped packet stalls only the stream it belonged to, not
every visitor) and survives the relay changing address. The WebSocket gets
through anything that allows HTTPS, HTTP proxies included.

### Choosing a transport

1. The relay asks `GET /v1/relay/transports` (with `Authorization: Bearer
   smr_…`, over the same HTTPS as the WebSocket):

   ```
   {"quic":{"addr":"matrix.example.com:443","serverName":"matrix.example.com"}}
   {"quic":null}                                   QUIC is off on the server
   ```

   With `SUPERMATRIX_TLS=off` the server has no public certificate, so QUIC
   uses a self-signed one (`DATA_DIR/quic-cert.pem`) and the answer adds its
   `"pin":"sha256:<hex>"`, which the relay checks instead of a CA.
2. `SUPERMATRIX_TRANSPORT` on the relay decides what happens next:
   - `auto` (the default): try QUIC for up to 5 s, then the WebSocket.
     It goes straight to the WebSocket when an HTTP proxy is configured
     (`HTTPS_PROXY`), the server has QUIC off, or QUIC failed in the last
     5 minutes. While on the WebSocket it tries QUIC again every 5 minutes.
     When that works, the relay moves over without dropping anything: the server
     sends new connections over QUIC, and the old session stays open for the
     connections it already carries. It closes when they finish, or after
     10 minutes.
   - `quic`: QUIC only; failures are retried with backoff.
   - `websocket`: never QUIC.
3. A relay on the WebSocket says why in its hello (`fallback`), e.g. `no answer
   on udp matrix.example.com:443 within 5s (UDP blocked?)`, `an HTTP proxy is
   configured, which QUIC can't use` or `transport set to websocket`.

### QUIC

UDP on `SUPERMATRIX_QUIC_ADDR` (default: the HTTPS port, or the HTTP port
with TLS off), TLS 1.3 with ALPN `supermatrix-relay/1` and the admin host's
certificate. `SUPERMATRIX_QUIC_PUBLIC` overrides the `host:port` advertised to
relays (default: the admin host and the listener's port). `SUPERMATRIX_QUIC=off`
turns it off. If the UDP port can't be bound, the server logs it and carries on
without QUIC.

1. The relay opens the first stream and writes `{"token":"smr_…"}`. The server
   answers `{}`, or `{"error":"…"}` and closes the connection with application
   error `0x101`.
2. The same stream is the control stream from then on. Each data stream is
   opened by the server.
3. Keepalives every 10 s; a connection that's silent for 30 s is dead. Stream
   windows grow to 16 MiB, the connection's to 64 MiB.

### WebSocket

1. The relay opens a WebSocket to `wss://ADMIN_HOST/v1/relay/connect` with
   `Authorization: Bearer smr_…`. The server looks up the relay by the token's
   SHA-256; an unknown token gets 401 before the upgrade.
2. Both ends run [yamux](https://github.com/hashicorp/yamux) over the
   WebSocket's binary messages, with a keepalive every 20 s and stream windows
   up to 4 MiB. The server is the yamux client and opens the control stream.

### The session

1. **Control stream.** Both directions carry newline-delimited JSON:

   ```
   server → relay  {"type":"targets","targets":[{"id":"…","protocol":"http","host":"127.0.0.1","port":3000,"tlsSkipVerify":false,"sni":""}]}
   server → relay  {"type":"listeners","listeners":[{"id":"…","protocol":"tcp","host":"127.0.0.1","port":5432}]}
   relay → server  {"type":"hello","hello":{"version":"0.1.0","hostname":"homelab","fallback":"…","features":["bridge-tcp","bridge-udp"]}}
   relay → server  {"type":"health","health":{"<deployment or bridge id>":"ok"}}
   ```

   `targets` is sent on connect and whenever the relay's deployments change;
   it's always the full set. The relay probes every target (a TCP connect,
   plus the TLS handshake for https targets) when its targets change and every
   30 s, and reports the results as `health`: `"ok"` or the error. It sends
   `hello` again when its reason for not using QUIC changes.
2. **Data streams.** For each upstream connection the server opens a stream
   and writes one line, `{"d":"<deployment id>"}`. The relay looks the ID up in
   the targets it was given. It never dials an address the stream names, so a
   compromised server session can only reach targets assigned to that relay.
   It answers with one line, `{}` if it connected or `{"error":"…"}`, and from
   then on the stream is a raw byte pipe to the target. For `https` targets the
   relay does the TLS (optionally without verifying, with an optional SNI).
3. A relay that connects again replaces its previous session. The old one
   drains as described above. Rotating a relay's token, or deleting the
   relay, drops its session. The relay reconnects with exponential backoff
   (1 s up to 60 s).

### Visibility

- `GET /v1/status` (and the live `/v1/ws` status, re-sent every 10 s while a
  relay is online) gives each relay's `transport`, `fallback`, `rttMs` (QUIC's
  smoothed RTT, or yamux pings), `bytesSent` / `bytesReceived` (from the
  server's side), `packetsLost` (QUIC), and `streams` (open upstream
  connections, pooled idle ones included).
- `GET /v1/relays` and `supermatrix-admin list` keep the last `transport` and
  `fallback` for relays that are offline.
- `GET /v1/info` has `quic`: what relays are told, or `null`.
- Both the server and the relay log which transport each session uses, and
  why QUIC failed.

## Proxying

`httputil.ReverseProxy` sends the request as plain HTTP/1.1 over a data
stream. Upgrades (WebSockets) and streamed responses pass through. Idle
connections are pooled per deployment and relay session (so a reconnected
relay never gets the old session's idle connections), and the pool is dropped
whenever deployments change.

The upstream request gets `X-Forwarded-For` / `X-Real-IP` (the visitor),
`X-Forwarded-Host` (the public hostname) and `X-Forwarded-Proto`. The `Host`
header is the target's `host[:port]`, or the public hostname when
`preserve_host` is on.

## Edge auth

Checked in this order:

1. **Disabled** deployments answer 503 with a "Service disabled" page (and `Retry-After`), whatever the auth or credentials; the relay isn't given their target. Disabling is per deployment (`PATCH {"enabled": false}`, `supermatrix-relay disable HOST`); the sign-in settings are kept for when it's turned back on.
2. **IP allow list** (CIDRs). If it's set and the visitor's address isn't on
   it, the answer is 403. The visitor's address is the TCP peer, or with
   `SUPERMATRIX_TRUST_PROXY=1` the rightmost `X-Forwarded-For` on loopback
   connections.
3. **Rate limit**, per deployment and per client address (the same address
   the IP allow list checks). Off by default; when on, a sustained rate
   (requests per second or per minute) and, optionally, a short extra burst
   allowance on top of it. An address over either gets 429 with
   `Retry-After` (the sustained window's length, in seconds). Each
   deployment's budget is its own — it isn't shared with other deployments
   on the same domain.
4. **Auth mode:**
   - `none`: public.
   - `login`: `/_supermatrix/login` on the deployment's own host shows a
     sign-in page. It accepts the deployment's password (bcrypt), and the root
     password if `allow_root_login` is on. Success sets a cookie
     (`__Host-supermatrix`: HttpOnly, Secure, SameSite=Lax, scoped to that host
     only) holding `deployment|expiry|kind` and an HMAC-SHA256 over that plus
     the password it was made with. Changing the password, or turning root
     login off, ends those sessions. Browsers without a session are redirected
     (303) to the sign-in page; other requests get 401. `/_supermatrix/logout`
     clears the cookie. The signing key is `DATA_DIR/cookie.key`.
   - `otp`: the same sign-in path, by email. The visitor enters an address;
     if it's on the deployment's list a 6-digit code is emailed over SMTP
     (`SUPERMATRIX_SMTP_EMAIL`, `_APP_PASSWORD`, `_HOST` default
     smtp.gmail.com, `_PORT` default 587; with TLS off and no SMTP the code
     is logged instead). The page looks the same whether or not the address
     is listed. Codes live in memory, hashed, for 10 minutes and 5 guesses;
     `/_supermatrix/verify` takes the code and sets the cookie, with kind `o`
     and the address. Taking an address off the list ends its sessions. The
     service gets the address in `X-Supermatrix-User` (any client-sent value
     is dropped first). Codes asked for are limited to 5 per 10 minutes per
     client address, 3 per 10 minutes per deployment and email, and 100 an
     hour in all.
   - `basic`: HTTP basic auth against a username and bcrypt hash. Successful
     checks are cached for 10 minutes so bcrypt doesn't run on every request.
   - `token`: only API tokens (below) get in.

   API tokens: `Authorization: Bearer smt_…` or `X-Supermatrix-Token: smt_…`,
   looked up by SHA-256. In every mode but `none`, one of the deployment's
   tokens gets the request in instead of the mode's own sign-in. A token
   that fails gets a 401 in every mode, never the sign-in page, with
   `X-Supermatrix-Auth: token_expired` (it ran out) or `token_invalid` (not
   one of the deployment's) and a JSON body `{"error", "auth"}`. The
   exception is an unknown Bearer token outside `token` mode: it's left
   alone (it may be the service's own) and the mode decides. A token's last use is recorded, at most once a
   minute. A token can be made with an expiry (`expiresInS`, 5 minutes to
   5 years; none never expires).
   Session length (`session_ttl_s`, 5 minutes to a year) applies to `login`
   and `otp`. The cookie is kept a week past that so an ended session can be
   told apart from none: every 401 from a deployment carries
   `X-Supermatrix-Auth: required` or `expired` (or a token state above), and in
   `login`/`otp` mode the body is JSON `{"error", "auth", "login"}`, `login`
   being the sign-in URL. Page loads (GET/HEAD wanting HTML) are redirected
   there instead, with `&expired=1` when the session ran out.
   `/_supermatrix/session` answers `{"signedIn": true, "email", "expiresAt"}`
   or a 401, for front ends that want to warn before a session ends.
5. The Super Matrix cookie, `Authorization` (when it held basic-auth
   credentials or a deployment token) and `X-Supermatrix-Token` (when it
   held a deployment token) are removed before the request goes upstream.

Sign-in attempts on the login page, and failed basic-auth attempts, are limited
to 10 a minute per address.

## SSH deployments

A deployment with protocol `ssh` isn't proxied as HTTP. The relay side is the
same as ever: the target (`host:port` on the relay's network, usually
`127.0.0.1:22`) is dialed as plain TCP and the data stream is a byte pipe, so
any relay version serves it. The edge checks the allow list and auth mode as
for any deployment (`none`, `login`, `otp` or `token`; `basic` is refused),
then serves these on the deployment's host:

| Path | |
|---|---|
| `/_supermatrix/login`, `verify`, `logout`, `session` | the sign-in pages, as above |
| `/_supermatrix/cli?port=P&state=S&challenge=C` | the CLI's consent page (needs a browser sign-in) |
| `/_supermatrix/cli/exchange` | POST `code` + `verifier` → `{"session"}`, for the CLI |
| `/_supermatrix/ssh` | WebSocket: binary messages are a raw pipe to the target. Off with `sshCli` |
| `/` | the browser shell, or with `sshShell` off a page showing the ssh config |
| `/_supermatrix/shell` | the browser shell's WebSocket. Off with `sshShell` |
| `/_supermatrix/assets/*` | the shell's script and the vendored xterm.js (public) |

Both WebSockets refuse an `Origin` other than the deployment's own host (the
CLI sends none). Relay and target errors on `/_supermatrix/ssh` are a 502
with `{"error"}` before the upgrade.

**The CLI** (`server/cmd/supermatrix`) dials `wss://HOST/_supermatrix/ssh`
with its saved session as the cookie (`Cookie: __Host-supermatrix=…`), or
with an API token in `X-Supermatrix-Token`. A 401 with a `login` URL sends
it through the sign-in. That works like OAuth's authorization code flow with
PKCE, so the session never passes through the browser:

1. The CLI makes a random `state` and `verifier` and listens on
   `127.0.0.1:<random port>`. It then opens
   `/_supermatrix/cli?port=<port>&state=<state>&challenge=<base64url(SHA-256(verifier))>`
   in the browser. Signed-out browsers go through the sign-in page first,
   which returns there.
2. The page asks to allow terminal access as the signed-in user. It needs a
   browser sign-in: a request that got in with an API token is refused.
   Allowing is a same-origin POST (refused if `Origin` is another host or
   `Sec-Fetch-Site` is `cross-site`). It mints a session value exactly like the
   cookie, with the same kind, address and expiry as the browser's session,
   and keeps it in memory under a random single-use code for 2 minutes. Then
   it redirects to `http://127.0.0.1:<port>/callback?state=…&code=…`. With
   `port=0` it shows the code to paste instead.
3. The CLI checks `state`, then POSTs `code` and `verifier` to
   `/_supermatrix/cli/exchange` (no session needed). A code works once, on
   the deployment that made it, only with the verifier whose hash came with
   it, and only while the session behind it is still good. A wrong verifier
   uses the code up too, and failures count toward the 10-a-minute limit per
   address. The answer is `{"session"}`, which the CLI saves in
   `$SUPERMATRIX_CONFIG_DIR/sessions.json` (0600) before dialing again.

So a code left in the browser's history, or read off the screen by someone
else, is worthless. The CLI refuses plain `http://` except to localhost, so
sessions and tokens never cross a network in the clear.

Because it is the cookie value, a CLI session ends like a browser one:
when it expires, when the password changes, when root login is turned off or
when the address leaves the email list. `supermatrix forward` serves the same
pipe on a local TCP port, one WebSocket per accepted connection. It warns
when that port listens on anything but loopback.

**Open connections** (pipes and shells) are checked again every 15 s with
the same tests a new one gets, against the deployment as it is then:
enabled, still `ssh`, the toggle still on, the address still on the allow
list, and the session or API token still valid. Revoking access in any of
those ways ends them. New pipes and shells are limited to 30 a minute per
client address, which also slows password guessing through the shell.

**The browser shell** is xterm.js on `/_supermatrix/shell`. Binary messages
are terminal bytes; text messages are JSON, `{"type":"resize","cols","rows"}`
from the page and `{"type":"exit","message"}` from the server. The server is
the SSH client (`x/crypto/ssh`) over a data stream from the relay. It asks in
the terminal for a username (suggesting `sshUser`), then for a password or
keyboard-interactive answers, and keeps none of them. Its WebSocket takes messages of at most 64 KiB. The host key is trusted
the first time (saved as `ssh_host_key`) and must match after that. Changing
the relay or target forgets it, as does PATCH `sshHostKey: ""`. The shell
closes when the Super Matrix session ends.

## Admin API

The admin host serves this API; every route except `login`, `relay/*` (relay
tokens, below) and `ws` needs `Authorization: Bearer sms_…`.

| | |
|---|---|
| `POST /v1/login {password}` | root password → `{token, expiresAt}` (30 days); 10 attempts a minute per address |
| `POST /v1/logout` | ends this session |
| `GET /v1/info` | admin host, base domain, TLS on/off, version, the URL relays should use, the QUIC endpoint (or null), `publicPorts` (`""` when public bridges are off) |
| `GET /v1/status` | live relay state: `{relays: {<id>: {online, connectedAt, remote, version, hostname, health, transport, fallback, rttMs, bytesSent, bytesReceived, packetsLost, streams, features}}, public: {<bridge id>: "ok" | "<listen error>"}}` (`public` only with public bridges on) |
| `GET/POST /v1/relays`, `PATCH/DELETE /v1/relays/:id` | relays, with the `features` they last said they support and how many `bridges` use them; `POST` returns the token once. Deleting a relay that still has deployments or bridges is refused |
| `POST /v1/relays/:id/rotate` | new token (shown once); the old one stops working and the relay is disconnected |
| `GET/POST /v1/domains {name}`, `DELETE /v1/domains/:id` | domains: `name` is `*.example.com` (wildcard) or `status.foo.org` (exact), with how many `deployments` and public `bridges` use them. A domain in use can't be deleted |
| `GET /v1/domains/:id/dns` | whether the domain (for a wildcard, a random name under it) resolves to the same addresses as the admin host: `{ok, probe, addresses, expected, detail}` |
| `GET/POST /v1/deployments`, `GET/PATCH/DELETE /v1/deployments/:id` | deployments (`domainId` + `subdomain`, which is ignored on an exact domain). Passwords go in as plaintext (`loginPassword`, `basicPassword`, at least 8 characters; `""` clears one) and are stored as bcrypt hashes. `protocol` is `http`, `https` or `ssh`; ssh deployments also have `sshCli`, `sshShell`, `sshUser` and the read-only `sshHostKey` (PATCH `""` forgets it). `rateLimitEnabled`, `rateLimitRequests`, `rateLimitPeriodS` (1 or 60), `rateLimitBurst` set the per-client-IP rate limit (see "Edge auth"). `ipAllowlist` (addresses or CIDRs) and `ipGroups` (IP group IDs; PATCH replaces them) together make the IP allow list: an address on either gets in, and empty both lets everyone in |
| `POST /v1/deployments/:id/tokens {name, expiresInS?}`, `DELETE …/tokens/:tid` | API tokens; `POST` returns the token once. `expiresInS` 0 or missing never expires |
| `GET/POST /v1/bridges`, `GET/PATCH/DELETE /v1/bridges/:id` | bridges (below): `name`, `protocol` (`tcp`/`udp`), `enabled`, `public` or `fromRelayId` + `listenHost` (default `127.0.0.1`), `listenPort`, `toRelayId`, `targetHost`, `targetPort`, `udpIdleS` (30 s to 24 h, default 300), and for public bridges `ipAllowlist`, `ipGroups` and an optional DNS name (`domainId` + `subdomain`, `""` for none). Read-only: `host`, `publicAddress` (`host:port`, or the admin host's), `listenError`. 409 for a port clash, or a relay that doesn't support the protocol |
| `GET /v1/bridges/:id/dns` | whether a public bridge's DNS name resolves to this server, as for domains |
| `GET/POST /v1/ip-groups`, `GET/PATCH/DELETE /v1/ip-groups/:id` | IP groups: named sets of addresses and CIDRs (`name`, unique ignoring case; `entries`, 1 to 1000, stored normalized, so `10.0.0.5` comes back as `10.0.0.5/32`) that allow lists include by ID. Read-only: `deployments` and `bridges`, how many allow lists include it. Editing one applies to every allow list with it at once; deleting one still in use is a 409 |

Sessions are tied to a fingerprint of the root password, so changing the
password file and restarting signs every app out.

**Live status.** `GET /v1/ws` is a WebSocket. The app sends
`{"type":"auth","token":"sms_…"}` first; the server answers with
`{"type":"status","relays":{…}}`, sends it again whenever a relay connects,
disconnects or reports health, and sends `{"type":"changed"}` when relays,
deployments or certificates change, so the app refetches. A bad token closes
the socket with code 4001.

## Bridges

A bridge is a TCP or UDP port that leads to `targetHost:targetPort` on the
`to` relay's network. It listens on the `from` relay's machine, or for a
public bridge on the server. The traffic rides the relay links above:

```
app ─▶ relay A :5432 ══ stream {"b":id} ══▶ server ══ stream {"d":id} ══▶ relay B ─▶ 127.0.0.1:5432
```

**Features.** A relay lists what it supports in its hello, `"features":
["bridge-tcp","bridge-udp"]`; relays from before bridges list none. The
server keeps the last list on the relay (`features` in `/v1/relays`). A
bridge can't be created, turned on, or moved to other relays or another
protocol unless both relays listed `bridge-<protocol>` (409). The server only
sends a session the bridge targets and listeners its hello allows, and sends
them again when a hello changes that.

**The far end** (relay B) gets the bridge as an ordinary target,
`{"id":"<bridge id>","protocol":"tcp"|"udp","host","port","idleS"}`, and
serves it on a data stream like any deployment. So it still only dials what
the server assigned it. `tcp` is a byte pipe. Health is a TCP connect for
`tcp`, and always `"ok"` for `udp`, which has nothing to connect to.

**The listening end** (relay A) gets `{"type":"listeners","listeners":[…]}`,
always the full set, and listens on exactly those. A port it can't bind is
retried every health round and reported as `"listen: <error>"` under the
bridge's ID in `health`, otherwise `"ok"`. Listeners outlive reconnects; they
open their streams on whichever session is current. For each TCP connection,
the relay opens a stream and writes `{"b":"<bridge id>"}`. The server checks
that the bridge exists, is on, listens on that relay, and that the relay
listed the feature, then opens a stream to relay B with `{"d":"<bridge id>"}`
and answers relay A `{}` (or `{"error"}`) once relay B has. From then on, the
two streams are joined.

**UDP** uses one stream per bridge, opened on the first packet (and again if
it fails), carrying frames in both directions:

```
[4 bytes flow id][2 bytes length][payload]     big-endian
```

Relay A gives each sender (address and port) a flow ID. Relay B dials a
UDP socket to the target for each flow ID it sees, and labels what comes back
on it with the same ID. Relay A sends each reply to the flow's sender. Each
side forgets a flow after `idleS` without a packet either way (default 300 s),
and keeps at most 1024 at once, dropping the one quiet longest. A flow that
relay B has forgotten just gets a new socket, and so a new source port, on
its next packet. The stream is reliable and ordered, so packets aren't lost
or reordered on the way, but they can be delayed.

**Public bridges.** With `SUPERMATRIX_PUBLIC_PORTS` set (ports and ranges,
e.g. `3478,20000-20100`), a bridge with `public: true` listens on the server,
on `SUPERMATRIX_PUBLIC_ADDR` (default all addresses), at a port in that set.
The server's own HTTP, HTTPS and QUIC ports are refused. The server plays
relay A's part and opens each stream straight to relay B. The IP allow list
is checked for each new connection or UDP sender, as the bridge is then.
One address may hold 32 TCP connections or 64 UDP senders on a bridge,
4096 in all. Bind errors show as `listenError` on the bridge and in
`/v1/status`'s `public`; ports are retried every 30 s.

## Relays managing their deployments

A relay can manage its own deployments once **Manage its deployments** is on
for it in the app (`PATCH /v1/relays/:id {"manageDeployments": true}`; off
by default). It authenticates with its relay token, `Authorization: Bearer
smr_…`, the same as for the relay link.

| | |
|---|---|
| `GET /v1/relay/me` | the relay as in `GET /v1/relays`, including `manageDeployments`; works with the permission off |
| `GET /v1/relay/domains` | the domains, read-only |
| `GET/POST /v1/relay/deployments`, `GET/PATCH/DELETE /v1/relay/deployments/:id` | as the admin routes, scoped to this relay |
| `POST /v1/relay/deployments/:id/tokens`, `DELETE …/tokens/:tid` | as the admin routes, on this relay's deployments |

Everything but `/me` answers 403 while the permission is off, and turning it
off takes effect on the next request. A relay sees only deployments on
itself (another relay's answer 404, like ones that don't exist); a new one
lands on the relay whatever `relayId` says, and a `relayId` of any other
relay is refused (403), so it can't move one away or take one over. It
can't change domains, relays or the server. The server logs every change a
relay makes.

**On the relay's machine.** While it runs, `supermatrix-relay` serves this
API on `127.0.0.1:3388` (`SUPERMATRIX_LOCAL_ADDR`, `off` turns it off) with
no auth: `/me`, `/domains` and `/deployments…` are passed to
`/v1/relay/…` with the relay's token added, so the token never leaves the
service. It only listens on loopback addresses, and refuses requests with an
`Origin` header (web pages) or a non-loopback `Host` (DNS rebinding). Anything
else that can run programs on the machine can use it. The same binary is its
CLI: `supermatrix-relay status | domains | list | get | create | update |
delete | token`.

## Storage

SQLite at `DATA_DIR/supermatrix.db` (WAL, foreign keys), with migrations
embedded from `server/database/migrations/sql`. Tables: `domains`, `relays`,
`deployments`, `deployment_tokens`, `bridges`, `admin_sessions`. Every bearer secret
(relay, session and API tokens) is 125 random bits, stored only as its
SHA-256.
