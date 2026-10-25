# super-matrix

Your own replacement for Cloudflare Tunnels or Pangolin. A small server with a
public address gives each of your services its own hostname and certificate,
with sign-in in front, and reaches them through **relays** that dial out from
wherever the services run. No open ports at home, no third party in the path.

- **Relays dial out.** A relay is one small binary next to your services (a
  home server, a Pi, a laptop). It connects to the server over QUIC, or over a
  WebSocket on networks that block UDP, and needs no inbound ports or public
  IP. The app shows which each relay is on, why, and its round-trip time. Which services it forwards is set
  in the app, not on the device.
- **A deployment is a hostname.** `grafana.example.com` → relay `homelab` →
  `http://127.0.0.1:3000`. HTTP, WebSockets and streaming all pass through;
  `https` targets work too, self-signed ones included if you allow it.
- **Any number of domains.** Wildcard ones (`*.example.com`, each deployment
  picks a subdomain) and exact ones (`status.foo.org`), managed in the app.
- **A certificate per deployment.** Point a wildcard DNS record at the server
  and each deployment gets its own Let's Encrypt certificate on first use. No
  wildcard certificate, no DNS API keys.
- **Auth in front, per deployment:** a Super Matrix sign-in page (its own
  password, the root password, or both), email codes for a list of addresses,
  HTTP basic auth, API tokens (`Authorization: Bearer`, optionally expiring),
  or public. An IP allow list works with any of
  them. Credentials are checked and stripped at the server; the service never
  sees them.
- **One root password** manages everything, from the app on Android or the web
  app the server serves.
- **Your data in SQLite** on the server: relays, deployments, hashed tokens.

## How it works

```
 browser ──https──▶ supermatrix-server ◀══ QUIC, udp/443 ═══ supermatrix-relay ──▶ http://127.0.0.1:3000
                    *.example.com        (or wss, tcp/443)    (dials out)
                    certs, auth, routing
```

The server terminates TLS, finds the deployment by hostname, checks its auth,
and opens a stream to the deployment's relay over its QUIC connection (or
WebSocket). The relay only dials targets the server has assigned it. Details:
[`docs/protocol.md`](docs/protocol.md).

## Layout

```
server/     Go: the server, the relay and an admin CLI (one module, SQLite via modernc)
app/        React Native app (Android APK + web), forked from super-chat's
nix/        NixOS modules: services.supermatrix and services.supermatrix-relay
deploy/     systemd units, example config and install.sh for any other Linux; k8s/ manifest
tools/      appart: generates the logo data and launcher icons from art/
art/        the broom (broomledger's logo) and "Super Matrix" in super-chat's lettering
docs/       protocol.md
release/    APK signing and the guided GitHub release (app-v* tags)
```

## Running it

```sh
nix develop          # prints the dev commands
dev-up               # server on http://localhost:8080, TLS off, root password "supermatrix"
run-app-web          # web client on :5173; sign in to http://localhost:8080
run-relay smr_…      # a relay, with the token the app shows when you add one
run-tests            # Go tests, app tests, then a full end-to-end run
```

In dev, deployments are `<name>.localhost:8080`, which browsers resolve to
your machine.

### DNS

At your DNS provider, point the admin host and each domain you add at the
server:

```
matrix.example.com    A     203.0.113.10     # the admin host: API, web app, relays
*.example.com         A     203.0.113.10     # a wildcard domain: any deployment on it
status.foo.org        A     203.0.113.10     # an exact domain: one deployment
```

Domains are added in the app (the globe at the top), as many as you like.
The app shows the records to add and checks each domain resolves to this
server. `SUPERMATRIX_BASE_DOMAIN` (or `baseDomain` in the NixOS module) just
adds the first one.

Ports 80/tcp and 443/tcp must reach the server (Let's Encrypt validates over
them), and 443/udp for relays over QUIC. Without UDP, relays use the WebSocket
on 443/tcp.
Each new deployment uses one certificate from Let's Encrypt's limit of 50 a
week per registered domain; test with staging first.

### On NixOS

```nix
# flake.nix
inputs.super-matrix.url = "github:canavan-a/super-matrix";

# the server's configuration
imports = [ inputs.super-matrix.nixosModules.server ];
services.supermatrix = {
  enable = true;
  adminHost = "matrix.example.com";
  baseDomain = "example.com";
  rootPasswordFile = "/etc/supermatrix/root-password";  # a runtime path, not a Nix path
  # environmentFile = "/etc/supermatrix/env";  # SUPERMATRIX_SMTP_* for email codes
  acmeEmail = "you@example.com";
  # acmeStaging = true;  # while testing
  # quic.enable = false;  # relays then always use the WebSocket
};

# each relay's configuration (the app shows this when you add a relay)
imports = [ inputs.super-matrix.nixosModules.relay ];
services.supermatrix-relay = {
  enable = true;
  serverUrl = "https://matrix.example.com";
  tokenFile = "/etc/supermatrix/relay-token";
  # transport = "websocket";  # default "auto": QUIC, else the WebSocket
};
```

Like super-chat, nothing is built as a derivation: an activation script runs
`go build` (and `npm ci && vite build` for the web app) from the flake's
source, so there's no vendorHash or npmDepsHash to keep in sync. The server
runs as its own user with systemd hardening, binding 80/443 through
`CAP_NET_BIND_SERVICE` only. The modules also raise the UDP buffer limits
(`net.core.rmem_max`/`wmem_max`) that quic-go needs to go fast. `supermatrix-admin list` works on the host and
shows how each relay last connected.

### On any other Linux (systemd)

```sh
git clone https://github.com/canavan-a/super-matrix && cd super-matrix
sudo deploy/install.sh server            # builds; prints a generated root password
sudoedit /etc/supermatrix/server.env     # admin host, base domain
sudo systemctl enable --now supermatrix-server

# on each relay machine
sudo deploy/install.sh relay smr_…       # the token from the app
sudoedit /etc/supermatrix/relay.env      # SUPERMATRIX_SERVER_URL
sudo systemctl enable --now supermatrix-relay
```

Configuration is environment variables (`deploy/server.env.example` lists
them); secrets are files passed in with systemd's `LoadCredential`. Re-run
`install.sh` after `git pull` to upgrade.

### In a container / Kubernetes

The `Dockerfile` builds one image with the server, the relay, the admin CLI
and the web app. The server runs as a non-root user on 8080 (HTTP), 8443/tcp
(HTTPS) and 8443/udp (QUIC), with its state in `/data`:

```sh
docker build -t supermatrix .
docker run -d -v supermatrix:/data -p 80:8080 -p 443:8443/tcp -p 443:8443/udp \
  -e SUPERMATRIX_ADMIN_HOST=matrix.example.com \
  -e SUPERMATRIX_QUIC_PUBLIC=matrix.example.com:443 \
  -e SUPERMATRIX_ROOT_PASSWORD_FILE=/run/secrets/root-password \
  -v /etc/supermatrix/root-password:/run/secrets/root-password:ro supermatrix

# a relay, from the same image
docker run -d --entrypoint supermatrix-relay \
  -e SUPERMATRIX_SERVER_URL=https://matrix.example.com \
  -e SUPERMATRIX_RELAY_TOKEN_FILE=/run/secrets/relay-token \
  -v /etc/supermatrix/relay-token:/run/secrets/relay-token:ro supermatrix
```

`deploy/k8s/supermatrix.yaml` runs the server on k3s behind its bundled
Traefik: TLS passes through by SNI (so the server still gets its own
per-host certificates), port 80 goes through a plain Ingress, and QUIC uses a
UDP hostPort. Each domain added in the app also needs adding to its
`HostSNI` match and port-80 rules.

## The app

`app/` is the same React Native setup as super-chat: Android (sideloaded APK
with the in-app updater and themed launcher icons) and a web build from the
same code, which the server serves on the admin host. Sign in with the server
address and root password; manage relays and deployments; see which relays
are online and whether each target is reachable, live. See
[`app/README.md`](app/README.md) and [`release/README.md`](release/README.md).
