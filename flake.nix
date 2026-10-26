{
  description = "Super Matrix: self-hosted tunnels with auth in front. Dev shell + NixOS modules";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      perSystem = flake-utils.lib.eachDefaultSystem (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};

          # The repo root is $PWD when a script starts, so these scripts must
          # be run from the repo root (always `cd` into super-matrix/ before
          # `nix develop`). Every script captures it first, before any `cd`.
          repoRoot = "$root";
          script = name: text: pkgs.writeShellScriptBin name (''
            root="$PWD"
          '' + text);
          dataDir = "${repoRoot}/.data";
          port = "8080";
          # TLS is off in dev. The admin host is localhost, and deployments
          # are <sub>.localhost, which browsers resolve to loopback.
          devEnv = ''
            export SUPERMATRIX_ADMIN_HOST=localhost
            export SUPERMATRIX_BASE_DOMAIN=localhost
            export SUPERMATRIX_TLS=off
            export SUPERMATRIX_HTTP_ADDR=127.0.0.1:${port}
            export SUPERMATRIX_DATA_DIR="${dataDir}"
            export SUPERMATRIX_ROOT_PASSWORD="''${SUPERMATRIX_ROOT_PASSWORD:-supermatrix}"
            if [ -d "${repoRoot}/app/dist" ]; then
              export SUPERMATRIX_STATIC_DIR="${repoRoot}/app/dist"
            fi
          '';

          db-reset = script "db-reset" ''
            set -e
            rm -rf "${dataDir}"
            echo "removed ${dataDir} (a fresh database is created on next start)"
          '';

          # foreground, hot-reload via air (see server/.air.toml)
          run-supermatrix = script "run-supermatrix" ''
            set -e
            mkdir -p "${dataDir}"
            ${devEnv}
            cd "${repoRoot}/server"
            exec ${pkgs.air}/bin/air
          '';

          dev-up = script "dev-up" ''
            set -e
            mkdir -p "${dataDir}/logs"
            ${devEnv}
            (cd "${repoRoot}/server" && \
              ${pkgs.go}/bin/go run ./cmd/supermatrix-server > "${dataDir}/logs/supermatrix.log" 2>&1 &)
            echo "supermatrix: http://localhost:${port} (root password: $SUPERMATRIX_ROOT_PASSWORD)"
            echo "             log: ${dataDir}/logs/supermatrix.log"
            echo "run 'dev-down' to stop it"
          '';

          dev-down = script "dev-down" ''
            pkill -f "cmd/supermatrix-" 2>/dev/null || true
            pkill -f "go-build.*supermatrix" 2>/dev/null || true
            pkill -f "tmp/supermatrix-server" 2>/dev/null || true
            echo "stopped supermatrix (best-effort)"
          '';

          # A relay against the dev server. Add one in the app (Relays → Add)
          # and pass its token: run-relay smr_…
          run-relay = script "run-relay" ''
            set -e
            if [ -z "''${1:-}" ]; then
              echo "usage: run-relay <relay token from the app>" >&2
              exit 2
            fi
            cd "${repoRoot}/server"
            export SUPERMATRIX_SERVER_URL="''${SUPERMATRIX_SERVER_URL:-http://localhost:${port}}"
            export SUPERMATRIX_RELAY_TOKEN="$1"
            exec ${pkgs.go}/bin/go run ./cmd/supermatrix-relay
          '';

          # The SSH client, e.g. as a ProxyCommand against the dev server:
          #   ssh -o ProxyCommand="supermatrix access --url http://%h: %h" you@box.localhost
          supermatrix = script "supermatrix" ''
            set -e
            cd "${repoRoot}/server"
            exec ${pkgs.go}/bin/go run ./cmd/supermatrix "$@"
          '';

          # Same database as the dev server above.
          supermatrix-admin = script "supermatrix-admin" ''
            set -e
            cd "${repoRoot}/server"
            export SUPERMATRIX_DATA_DIR="${dataDir}"
            exec ${pkgs.go}/bin/go run ./cmd/supermatrix-admin "$@"
          '';

          run-app-web = script "run-app-web" ''
            set -e
            cd "${repoRoot}/app"
            exec ${pkgs.nodejs_22}/bin/npm run web
          '';

          run-app-web-build = script "run-app-web-build" ''
            set -e
            cd "${repoRoot}/app"
            exec ${pkgs.nodejs_22}/bin/npm run build:web
          '';

          # Needs the system-wide Android SDK already on this machine's PATH;
          # nothing Android-related is added to this flake on purpose.
          run-app-android = script "run-app-android" ''
            set -e
            cd "${repoRoot}/app"
            exec ${pkgs.nodejs_22}/bin/npm run android
          '';

          # Regenerates the logo data and launcher icons from art/.
          run-appart = script "run-appart" ''
            set -e
            cd "${repoRoot}/tools"
            exec ${pkgs.go}/bin/go run ./appart "$@"
          '';

          run-tests = script "run-tests" ''
            set -e
            (cd "${repoRoot}/server" && ${pkgs.go}/bin/go test ./...)
            (cd "${repoRoot}/app" && ${pkgs.nodejs_22}/bin/npx jest)
            (cd "${repoRoot}/app" && E2E=1 ${pkgs.nodejs_22}/bin/npx jest __tests__/e2e.test.ts --forceExit)
          '';

          printHelp = ''
            echo "super-matrix dev shell"
            echo "  run-supermatrix   # foreground: the server with hot reload (air) on localhost:${port}, TLS off"
            echo "  dev-up / dev-down # start/stop the server headless (logs in .data/logs)"
            echo "  run-relay <token> # a relay against the dev server (token from the app)"
            echo "  db-reset          # delete the local database"
            echo "  supermatrix-admin # stats | list | revoke-sessions"
            echo "  supermatrix       # the SSH client (access / forward / login), e.g. as a ProxyCommand"
            echo "  run-app-web       # the app's web target via Vite on :5173"
            echo "  run-app-web-build # builds app/dist, which the dev server then serves"
            echo "  run-app-android   # the app on a connected device/emulator (needs the system Android SDK)"
            echo "  run-appart        # regenerate logo data + launcher icons from art/"
            echo "  run-tests         # server tests, app tests, then the end-to-end test"
            echo "  dev-help          # re-print this list"
            echo "dev root password: \$SUPERMATRIX_ROOT_PASSWORD or 'supermatrix'"
          '';

          dev-help = pkgs.writeShellScriptBin "dev-help" printHelp;
        in
        {
          devShells.default = pkgs.mkShell {
            buildInputs = [
              pkgs.go
              pkgs.gopls
              pkgs.air
              pkgs.sqlite
              pkgs.nodejs_22
              pkgs.watchman
              db-reset
              run-supermatrix
              dev-up
              dev-down
              run-relay
              supermatrix-admin
              supermatrix
              run-app-web
              run-app-web-build
              run-app-android
              run-appart
              run-tests
              dev-help
            ];
            shellHook = printHelp;
          };
        }
      );

      # No buildGoModule package on purpose, the same tradeoff as super-chat:
      # the modules build from source in an activation script instead of
      # keeping a vendorHash (or npmDepsHash) in sync by hand.
      nixosModules = rec {
        server = import ./nix/server.nix { inherit self; };
        relay = import ./nix/relay.nix { inherit self; };
        client = import ./nix/client.nix { inherit self; };
        default = { imports = [ server relay client ]; };
      };
    in
    perSystem // { inherit nixosModules; };
}
