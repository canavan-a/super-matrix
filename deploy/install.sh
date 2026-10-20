#!/usr/bin/env bash
# Install Super Matrix on a plain systemd Linux box (no Nix).
#
#   sudo deploy/install.sh server          # server + web app (needs go, npm)
#   sudo deploy/install.sh server --no-web # server only (needs go)
#   sudo deploy/install.sh relay [TOKEN]   # relay (needs go); TOKEN from the app
#
# Re-running upgrades the binaries and keeps existing config and secrets.
# Builds from this checkout, so `git pull` first to upgrade.
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
etc=/etc/supermatrix
bin=/usr/local/bin

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 is required ($2)"; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
[ $# -ge 1 ] || die "usage: install.sh server [--no-web] | relay [TOKEN]"
need go "https://go.dev/dl/, 1.26+"
need systemctl "systemd"

build() {
  echo "building $1…"
  (cd "$repo/server" && CGO_ENABLED=0 go build -buildvcs=false -o "$bin/$1.new" "./cmd/$1")
  mv -f "$bin/$1.new" "$bin/$1"
}

install -d -m 0755 "$etc"

# quic-go wants larger UDP socket buffers than most kernels default to.
udp_buffers() {
  printf 'net.core.rmem_max=7500000\nnet.core.wmem_max=7500000\n' > /etc/sysctl.d/60-supermatrix-quic.conf
  sysctl -q -p /etc/sysctl.d/60-supermatrix-quic.conf || true
}

case "$1" in
server)
  web=1
  [ "${2:-}" = "--no-web" ] && web=0
  build supermatrix-server
  udp_buffers
  build supermatrix-admin

  if [ "$web" = 1 ]; then
    need npm "Node.js 22+, or pass --no-web"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    echo "building the web app…"
    cp -r "$repo/app/." "$tmp/"
    (cd "$tmp" && npm ci --no-audit --no-fund && npm run build:web)
    install -d -m 0755 /usr/local/share/supermatrix
    rm -rf /usr/local/share/supermatrix/web.new
    cp -r "$tmp/dist" /usr/local/share/supermatrix/web.new
    rm -rf /usr/local/share/supermatrix/web
    mv /usr/local/share/supermatrix/web.new /usr/local/share/supermatrix/web
  fi

  if ! id supermatrix >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/supermatrix --shell /usr/sbin/nologin supermatrix
  fi
  if [ ! -f "$etc/server.env" ]; then
    install -m 0640 "$repo/deploy/server.env.example" "$etc/server.env"
    [ "$web" = 1 ] || sed -i 's|^SUPERMATRIX_STATIC_DIR=|#SUPERMATRIX_STATIC_DIR=|' "$etc/server.env"
    fresh_env=1
  fi
  if [ ! -f "$etc/root-password" ]; then
    umask 077
    pw="$(head -c 18 /dev/urandom | base64 | tr -d '/+=')"
    printf '%s\n' "$pw" > "$etc/root-password"
    chmod 0600 "$etc/root-password"
    echo
    echo "root password (also in $etc/root-password): $pw"
  fi
  install -m 0644 "$repo/deploy/supermatrix-server.service" /etc/systemd/system/
  systemctl daemon-reload
  if [ "${fresh_env:-0}" = 1 ]; then
    echo
    echo "Edit $etc/server.env (admin host, base domain), then:"
    echo "  systemctl enable --now supermatrix-server"
  else
    systemctl try-restart supermatrix-server || true
    echo "upgraded; supermatrix-server restarted if it was running"
  fi
  echo
  echo "DNS: A/AAAA records for the admin host and *.<base domain> → this machine."
  echo "Open 80/tcp and 443/tcp (Let's Encrypt, visitors, relays) and 443/udp (relays over QUIC)."
  ;;

relay)
  build supermatrix-relay
  udp_buffers
  if [ ! -f "$etc/relay.env" ]; then
    install -m 0644 "$repo/deploy/relay.env.example" "$etc/relay.env"
    fresh_env=1
  fi
  if [ -n "${2:-}" ]; then
    umask 077
    printf '%s\n' "$2" > "$etc/relay-token"
    chmod 0600 "$etc/relay-token"
  fi
  [ -f "$etc/relay-token" ] || echo "Put the relay token from the app in $etc/relay-token (or re-run with it)."
  install -m 0644 "$repo/deploy/supermatrix-relay.service" /etc/systemd/system/
  systemctl daemon-reload
  if [ "${fresh_env:-0}" = 1 ]; then
    echo "Set SUPERMATRIX_SERVER_URL in $etc/relay.env, then:"
    echo "  systemctl enable --now supermatrix-relay"
  else
    systemctl try-restart supermatrix-relay || true
    echo "upgraded; supermatrix-relay restarted if it was running"
  fi
  ;;

*)
  die "usage: install.sh server [--no-web] | relay [TOKEN]"
  ;;
esac
