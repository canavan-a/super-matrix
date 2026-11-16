# NixOS module for a Super Matrix relay: services.supermatrix-relay.
#
# The relay runs next to the services you expose. It dials out to the
# server, so it needs no open ports; which services it forwards is set in
# the app. Built from source in an activation script, like the server.
{ self }:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.supermatrix-relay;
  stateDir = "/var/lib/supermatrix-relay";
  relayBin = "${stateDir}/bin/supermatrix-relay";
  goCache = "/var/cache/supermatrix-go";
  nixSocket = "/run/supermatrix-nix/agent.sock";
in
{
  options.services.supermatrix-relay = {
    enable = lib.mkEnableOption "a Super Matrix relay";

    serverUrl = lib.mkOption {
      type = lib.types.str;
      example = "https://matrix.example.com";
      description = "The server's admin URL.";
    };

    transport = lib.mkOption {
      type = lib.types.enum [ "auto" "quic" "websocket" ];
      default = "auto";
      description = ''
        "auto": QUIC when the network lets UDP through, otherwise the
        WebSocket (and back to QUIC once it works again). "quic" or
        "websocket" pin one. The app shows which a relay is using, and why.
      '';
    };

    quicAddr = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "matrix.example.com:443";
      description = "Overrides the QUIC host:port the server advertises.";
    };

    localApi = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:3388";
      example = "off";
      description = ''
        Where the relay serves its local API, with no auth (loopback
        addresses only), or "off". The supermatrix-relay CLI on this machine
        uses it to manage the relay's deployments once "Manage its
        deployments" is on for the relay in the app. Anything that can run
        programs here can then do the same.
      '';
    };

    nixServices = {
      enable = lib.mkEnableOption ''
        the Nix agent, so Super Matrix can build and run Nix services on
        this machine (each as smx-<slug>.service). They run as root: whoever
        controls your Super Matrix server can then run programs here. Nix
        services also have to be turned on for the relay in the app'';

      envDir = lib.mkOption {
        type = lib.types.str;
        default = "/etc/supermatrix/nix-services";
        description = ''
          Where each Nix service's own variables go, as <slug>.env
          (KEY=value lines). The service's relay_env names the ones it needs;
          it won't start until they're set. Not managed by this module.
        '';
      };
    };

    tokenFile = lib.mkOption {
      type = lib.types.str;
      example = "/run/secrets/supermatrix-relay-token";
      description = ''
        File holding the relay token the app showed when you added this
        relay (smr_…). Passed via LoadCredential: give a runtime path as a
        string, not a Nix path literal.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.tmpfiles.rules = [
      "d ${stateDir} 0755 root root -"
      "d ${stateDir}/bin 0755 root root -"
      "d ${goCache} 0755 root root -"
    ] ++ lib.optionals cfg.nixServices.enable [
      "d ${cfg.nixServices.envDir} 0700 root root -"
    ];

    # The relay binary is also the CLI for its local API.
    environment.systemPackages = [
      (pkgs.writeShellScriptBin "supermatrix-relay" ''
        export SUPERMATRIX_LOCAL_ADDR="''${SUPERMATRIX_LOCAL_ADDR:-${cfg.localApi}}"
        # For `supermatrix-relay nix …` (as root): it talks to the server with
        # the relay's token.
        export SUPERMATRIX_SERVER_URL="''${SUPERMATRIX_SERVER_URL:-${cfg.serverUrl}}"
        export SUPERMATRIX_RELAY_TOKEN_FILE="''${SUPERMATRIX_RELAY_TOKEN_FILE:-${cfg.tokenFile}}"
        exec ${relayBin} "$@"
      '')
    ];

    system.activationScripts.supermatrixRelayBuild = {
      deps = [ ];
      text = ''
        export PATH=${pkgs.go}/bin:${pkgs.bash}/bin:$PATH
        export HOME=${goCache}
        export GOCACHE=${goCache}/build
        export GOPATH=${goCache}/path
        export CGO_ENABLED=0
        mkdir -p ${stateDir}/bin ${goCache}
        cd ${self}/server
        ${pkgs.go}/bin/go build -trimpath -buildvcs=false -o ${relayBin}.new ./cmd/supermatrix-relay
        # Restarting drops every connection the relay is carrying, so only
        # do it for a binary that actually changed. -trimpath keeps the
        # ${self} store path out of the build, so an unrelated commit (or a
        # switch with nothing new) builds byte-identical output.
        if ${pkgs.diffutils}/bin/cmp -s ${relayBin}.new ${relayBin}; then
          rm -f ${relayBin}.new
        else
          mv -f ${relayBin}.new ${relayBin}
          ${pkgs.systemd}/bin/systemctl try-restart supermatrix-relay.service || true
          # The Nix services themselves are units of their own: restarting
          # the agent leaves them running.
          ${pkgs.systemd}/bin/systemctl try-restart supermatrix-nix-agent.service || true
        fi
      '';
    };

    # Larger UDP buffers for QUIC (see the server module).
    boot.kernel.sysctl = lib.mkIf (cfg.transport != "websocket") {
      "net.core.rmem_max" = lib.mkDefault 7500000;
      "net.core.wmem_max" = lib.mkDefault 7500000;
    };

    systemd.services.supermatrix-relay = {
      description = "Super Matrix relay";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        SUPERMATRIX_SERVER_URL = cfg.serverUrl;
        SUPERMATRIX_RELAY_TOKEN_FILE = "%d/token";
        SUPERMATRIX_TRANSPORT = cfg.transport;
        SUPERMATRIX_LOCAL_ADDR = cfg.localApi;
        SUPERMATRIX_NIX_SOCKET = if cfg.nixServices.enable then nixSocket else "off";
      } // lib.optionalAttrs (cfg.quicAddr != null) {
        SUPERMATRIX_QUIC_ADDR = cfg.quicAddr;
      };
      serviceConfig = {
        ExecStart = relayBin;
        # The agent's socket is the group's.
        SupplementaryGroups = lib.optionals cfg.nixServices.enable [ "supermatrix-nix" ];
        Restart = "always";
        RestartSec = 2;
        DynamicUser = true;
        LoadCredential = [ "token:${cfg.tokenFile}" ];
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
        CapabilityBoundingSet = "";
        UMask = "0077";
      };
    };

    # The Nix agent: root, apart from the relay, which only talks to it over
    # its socket. It writes each service's unit to /run/systemd/system (so a
    # switch leaves them alone) and writes them again after a reboot.
    users.groups.supermatrix-nix = lib.mkIf cfg.nixServices.enable { };
    systemd.services.supermatrix-nix-agent = lib.mkIf cfg.nixServices.enable {
      description = "Super Matrix Nix agent";
      after = [ "network-online.target" "nix-daemon.socket" ];
      wants = [ "network-online.target" ];
      before = [ "supermatrix-relay.service" ];
      wantedBy = [ "multi-user.target" ];
      # nix (and git, for flake inputs from git), systemctl, journalctl and
      # bash for dev shells.
      path = [ config.nix.package pkgs.git pkgs.bash pkgs.coreutils config.systemd.package ];
      environment = {
        SUPERMATRIX_NIX_SOCKET = nixSocket;
        SUPERMATRIX_NIX_GROUP = "supermatrix-nix";
        SUPERMATRIX_NIX_ENV_DIR = cfg.nixServices.envDir;
      };
      serviceConfig = {
        ExecStart = "${relayBin} nix-agent";
        Restart = "always";
        RestartSec = 2;
        Group = "supermatrix-nix";
        RuntimeDirectory = "supermatrix-nix";
        RuntimeDirectoryMode = "0750";
        RuntimeDirectoryPreserve = "yes";
        StateDirectory = "supermatrix-nix";
        StateDirectoryMode = "0700";
      };
    };
  };
}
