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
        ${pkgs.go}/bin/go build -buildvcs=false -o ${relayBin}.new ./cmd/supermatrix-relay
        mv -f ${relayBin}.new ${relayBin}
        ${pkgs.systemd}/bin/systemctl try-restart supermatrix-relay.service || true
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
      } // lib.optionalAttrs (cfg.quicAddr != null) {
        SUPERMATRIX_QUIC_ADDR = cfg.quicAddr;
      };
      serviceConfig = {
        ExecStart = relayBin;
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
  };
}
