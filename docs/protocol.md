| `GET /v1/status` | live relay state: `{relays: {<id>: {online, connectedAt, remote, version, hostname, health, transport, fallback, rttMs, bytesSent, bytesReceived, packetsLost, streams}}}` || `GET /v1/info` | admin host, base domain, TLS on/off, version, the URL relays should use, the QUIC endpoint (or null) |# Super Matrix protocol

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
certificate per hostname: the admin host and each enabled deployment. Its host
policy only allows those names, so an unknown SNI never reaches Let's Encrypt.
HTTP-01 is answered on `:80` (everything else there redirects to https) and
TLS-ALPN-01 on `:443`. Certificates are cached in `DATA_DIR/certs`.

A new or renamed deployment's certificate is fetched at once in the background
("warmed"), with a hello that asks for ECDSA, so it's ready before the first
visitor. Every host is warmed again at startup and every 12 hours, which also
retries failures and refreshes the expiry dates the app shows (`cert_status`,
`cert_expires_at`). autocert renews certificates 30 days before they expire.

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
   relay → server  {"type":"hello","hello":{"version":"0.1.0","hostname":"homelab","fallback":"…"}}
   relay → server  {"type":"health","health":{"<deployment id>":"ok"}}
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

1. **Disabled** deployments answer 404.
2. **IP allow list** (CIDRs). If it's set and the visitor's address isn't on
   it, the answer is 403. The visitor's address is the TCP peer, or with
   `SUPERMATRIX_TRUST_PROXY=1` the rightmost `X-Forwarded-For` on loopback
   connections.
3. **Auth mode:**
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
   - `token`: `Authorization: Bearer smt_…` or `X-Supermatrix-Token: smt_…`,
     looked up by SHA-256. A token's last use is recorded, at most once a
     minute.
     A token can be made with an expiry (`expiresInS`, 5 minutes to 5
     years; none never expires).
   Session length (`session_ttl_s`, 5 minutes to a year) applies to `login`
   and `otp`. The cookie is kept a week past that so an ended session can be
   told apart from none: every 401 from a deployment carries
   `X-Supermatrix-Auth: required` or `expired` (also expired tokens), and in
   `login`/`otp` mode the body is JSON `{"error", "auth", "login"}`, `login`
   being the sign-in URL. Page loads (GET/HEAD wanting HTML) are redirected
   there instead, with `&expired=1` when the session ran out.
   `/_supermatrix/session` answers `{"signedIn": true, "email", "expiresAt"}`
   or a 401, for front ends that want to warn before a session ends.
4. The Super Matrix cookie, `Authorization` (for basic and token mode) and
   `X-Supermatrix-Token` are removed before the request goes upstream.

Sign-in attempts on the login page, and failed basic-auth attempts, are limited
to 10 a minute per address.

## Admin API

The admin host serves this API; every route except `login`, `relay/*` (relay
tokens) and `ws` needs `Authorization: Bearer sms_…`.

| | |
|---|---|
| `POST /v1/login {password}` | root password → `{token, expiresAt}` (30 days); 10 attempts a minute per address |
| `POST /v1/logout` | ends this session |
| `GET /v1/info` | admin host, base domain, TLS on/off, version, the URL relays should use |
| `GET /v1/status` | live relay state: `{relays: {<id>: {online, connectedAt, remote, version, hostname, health}}}` |
| `GET/POST /v1/relays`, `PATCH/DELETE /v1/relays/:id` | relays; `POST` returns the token once. Deleting a relay that still has deployments is refused |
| `POST /v1/relays/:id/rotate` | new token (shown once); the old one stops working and the relay is disconnected |
| `GET/POST /v1/domains {name}`, `DELETE /v1/domains/:id` | domains: `name` is `*.example.com` (wildcard) or `status.foo.org` (exact). A domain in use can't be deleted |
| `GET /v1/domains/:id/dns` | whether the domain (for a wildcard, a random name under it) resolves to the same addresses as the admin host: `{ok, probe, addresses, expected, detail}` |
| `GET/POST /v1/deployments`, `GET/PATCH/DELETE /v1/deployments/:id` | deployments (`domainId` + `subdomain`, which is ignored on an exact domain). Passwords go in as plaintext (`loginPassword`, `basicPassword`, at least 8 characters; `""` clears one) and are stored as bcrypt hashes |
| `POST /v1/deployments/:id/tokens {name, expiresInS?}`, `DELETE …/tokens/:tid` | API tokens; `POST` returns the token once. `expiresInS` 0 or missing never expires |

Sessions are tied to a fingerprint of the root password, so changing the
password file and restarting signs every app out.

**Live status.** `GET /v1/ws` is a WebSocket. The app sends
`{"type":"auth","token":"sms_…"}` first; the server answers with
`{"type":"status","relays":{…}}`, sends it again whenever a relay connects,
disconnects or reports health, and sends `{"type":"changed"}` when relays,
deployments or certificates change, so the app refetches. A bad token closes
the socket with code 4001.

## Storage

SQLite at `DATA_DIR/supermatrix.db` (WAL, foreign keys), with migrations
embedded from `server/database/migrations/sql`. Tables: `domains`, `relays`,
`deployments`, `deployment_tokens`, `admin_sessions`. Every bearer secret
(relay, session and API tokens) is 125 random bits, stored only as its
SHA-256.
