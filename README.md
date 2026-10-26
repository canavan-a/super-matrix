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
- **SSH too.** An `ssh` deployment (`ssh-box.example.com` → relay → `127.0.0.1:22`)
  is reached with plain `ssh` and the `supermatrix` CLI as its ProxyCommand,
  which opens the deployment's sign-in in your browser first, like
  `cloudflared access ssh`. Or use the browser shell on its hostname. Same auth,
  same port 443.
- **Any number of domains.** Wildcard ones (`*.example.com`, each deployment
  picks a subdomain) and exact ones (`status.foo.org`), managed in the app.
- **A certificate per deployment.** Point a wildcard DNS record at the server
  and each deployment gets its own Let's Encrypt certificate on first use. No
  wildcard certificate, no DNS API keys.
- **Auth in front, per deployment:** a Super Matrix sign-in page (its own
  password, the root password, or both), email codes for a list of addresses,
  HTTP basic auth, API tokens only, or public. API tokens
  (`Authorization: Bearer`, optionally expiring) also get past any of the
  sign-ins, for scripts. An IP allow list works with any of them. Credentials are checked and stripped at the server; the service never
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
server/     Go: the server, the relay, an admin CLI and the supermatrix SSH client (one module, SQLite via modernc)
app/        React Native app (Android APK + web), forked from super-chat's
nix/        NixOS modules: services.supermatrix, services.supermatrix-relay and programs.supermatrix
deploy/     systemd units, example config and install.sh for any other Linux; k8s/ manifest
tools/      appart: generates the logo data and launcher icons from art/
art/        the broom (broomledger's logo) and "Super Matrix" in super-chat's lettering
docs/       protocol.md
release/    APK signing and the guided GitHub release (app-v* tags); client.sh builds the CLI for every platform
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

## SSH deployments

Pick **ssh** as a deployment's protocol and give the address the relay should
dial on its own machine, usually `127.0.0.1:22`. The deployment keeps its
hostname, certificate, sign-in and IP allow list; what changes is how it's
reached:

- **Terminal:** the `supermatrix` CLI carries ssh over a WebSocket on 443.
  When it has no session it prints a link and opens the deployment's sign-in
  in your browser, asks you to allow terminal access, and remembers the session
  (`~/.config/supermatrix`) until it would end in the browser too. ssh itself
  stays end to end; the sshd still wants its own key or password.

  ```
  # ~/.ssh/config
  Host ssh-box.example.com
    ProxyCommand supermatrix access %h
  ```

  `supermatrix login --no-browser HOST` is for machines without a browser:
  open the link anywhere and paste back what it shows. For scripts, set
  `SUPERMATRIX_TOKEN_FILE` to one of the deployment's API tokens.
- **Browser shell:** the deployment's own `https://` address is a terminal
  behind its sign-in. It asks for the server's username and password, and
  stores nothing. The host key is trusted the first time and checked after;
  the app can forget it.
- **A local port:** `supermatrix forward --listen 127.0.0.1:2222 HOST`, then
  `ssh -p 2222 user@127.0.0.1` or anything else that speaks to the port. Made
  to run as a service with an API token (below).

Either can be switched off per deployment in the app. Revoking access (a new
password, a removed address or token, a toggle, the allow list) also ends
connections already open, within 15 seconds. The session itself never passes
through the browser: the sign-in hands the CLI a single-use code that only
works with a secret the CLI kept (PKCE). Avoid **Public** for SSH: that puts
nothing but the sshd's own auth in front.

### Installing the client

- **NixOS:**

  ```nix
  imports = [ inputs.super-matrix.nixosModules.client ];
  programs.supermatrix = {
    enable = true;
    sshHosts = [ "ssh-box.example.com" ];  # ProxyCommand entries in the system ssh_config
    forwards.box = {                         # a service: supermatrix-forward-box
      host = "ssh-box.example.com";
      listen = "127.0.0.1:2222";
      tokenFile = "/run/secrets/ssh-box-token";
    };
  };
  ```
- **Any Linux with systemd:** `sudo deploy/install.sh client` installs
  `/usr/local/bin/supermatrix`; `sudo deploy/install.sh forward box
  ssh-box.example.com --listen 127.0.0.1:2222 smt_…` adds a
  `supermatrix-forward@box` service (`deploy/supermatrix-forward@.service`).
- **macOS, Windows, anything else:** `release/client.sh X.Y.Z` builds archives
  for Linux, macOS, Windows and FreeBSD (amd64/arm64), with
  `--publish` for a `client-vX.Y.Z` GitHub release. Or, with Go:
  `cd server && go build -o ~/bin/supermatrix ./cmd/supermatrix`.
- **Docker:** the image has it too:
  `docker run -i --rm --entrypoint supermatrix -e SUPERMATRIX_TOKEN=smt_… supermatrix access ssh-box.example.com`.
- **In dev:** the `supermatrix` command in `nix develop`, with
  `--url http://%h:8080` for `<name>.localhost` deployments.

## The app

`app/` is the same React Native setup as super-chat: Android (sideloaded APK
with the in-app updater and themed launcher icons) and a web build from the
same code, which the server serves on the admin host. Sign in with the server
address and root password; manage relays and deployments; see which relays
are online and whether each target is reachable, live. See
[`app/README.md`](app/README.md) and [`release/README.md`](release/README.md).
