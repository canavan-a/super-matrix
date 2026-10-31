# NixOS module for the supermatrix CLI: programs.supermatrix.
#
# The client for SSH deployments: `supermatrix access` is ssh's
# ProxyCommand (signing in through the browser when needed), and
# `supermatrix forward` keeps a local port open to one, as a service. Built
# from source in an activation script, like the server and the relay.
{ self }:
{ config, lib, pkgs, ... }:
let
  cfg = config.programs.supermatrix;
  stateDir = "/var/lib/supermatrix-client";
  clientBin = "${stateDir}/bin/supermatrix";
  goCache = "/var/cache/supermatrix-go";

  wrapper = pkgs.writeShellScriptBin "supermatrix" ''
    exec ${clientBin} "$@"
  '';

  forwardOpts = { name, ... }: {
    options = {
      host = lib.mkOption {
        type = lib.types.str;
        example = "ssh-box.example.com";
        description = "The SSH deployment's hostname.";
      };
      url = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "http://box.localhost:8080";
        description = "The deployment's URL, when it isn't https://<host>.";
      };
      listen = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1:2222";
        description = "Local address the forward listens on.";
      };
      tokenFile = lib.mkOption {
        type = lib.types.str;
        example = "/run/secrets/ssh-box-token";
        description = ''
          File holding one of the deployment's API tokens (smt_…), made in
          the app. Passed via LoadCredential: give a runtime path as a
          string, not a Nix path literal.
        '';
      };
    };
  };
in
{
  options.programs.supermatrix = {
    enable = lib.mkEnableOption "the supermatrix CLI, for Super Matrix SSH deployments";

    sshHosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "ssh-box.example.com" ];
      description = ''
        SSH deployments to reach through `supermatrix access`: each gets a
        ProxyCommand in the system ssh_config, so plain `ssh user@host`
        works (it opens the browser to sign in when there's no session).
      '';
    };

    forwards = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule forwardOpts);
      default = { };
      example = lib.literalExpression ''
        {
          box = {
            host = "ssh-box.example.com";
            listen = "127.0.0.1:2222";
            tokenFile = "/run/secrets/ssh-box-token";
          };
        }
      '';
      description = ''
        Local ports kept open to SSH deployments, one systemd service each
        (supermatrix-forward-<name>), signed in with an API token. Then
        `ssh -p 2222 user@127.0.0.1`, or point other tools at the port.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.tmpfiles.rules = [
      "d ${stateDir} 0755 root root -"
      "d ${stateDir}/bin 0755 root root -"
      "d ${goCache} 0755 root root -"
    ];

    system.activationScripts.supermatrixClientBuild = {
      deps = [ ];
      text = ''
        export PATH=${pkgs.go}/bin:${pkgs.bash}/bin:$PATH
        export HOME=${goCache}
        export GOCACHE=${goCache}/build
        export GOPATH=${goCache}/path
        export CGO_ENABLED=0
        mkdir -p ${stateDir}/bin ${goCache}
        cd ${self}/server
        ${pkgs.go}/bin/go build -trimpath -buildvcs=false -o ${clientBin}.new ./cmd/supermatrix
        # Only swap in (and restart the forwards for) a binary that actually
        # changed; see the relay module.
        if ${pkgs.diffutils}/bin/cmp -s ${clientBin}.new ${clientBin}; then
          rm -f ${clientBin}.new
        else
          mv -f ${clientBin}.new ${clientBin}
      '' + lib.concatMapStrings (name: ''
          ${pkgs.systemd}/bin/systemctl try-restart supermatrix-forward-${name}.service || true
      '') (lib.attrNames cfg.forwards) + ''
        fi
      '';
    };

    environment.systemPackages = [ wrapper ];

    programs.ssh.extraConfig = lib.concatMapStrings (h: ''
      Host ${h}
        ProxyCommand ${clientBin} access %h
    '') cfg.sshHosts;

    systemd.services = lib.mapAttrs' (name: f: lib.nameValuePair "supermatrix-forward-${name}" {
      description = "Super Matrix forward: ${f.listen} → ${f.host}";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        SUPERMATRIX_URL = if f.url != null then f.url else "https://${f.host}";
        SUPERMATRIX_LISTEN = f.listen;
        SUPERMATRIX_TOKEN_FILE = "%d/token";
      };
      serviceConfig = {
        ExecStart = "${clientBin} forward";
        Restart = "always";
        RestartSec = 2;
        DynamicUser = true;
        LoadCredential = [ "token:${f.tokenFile}" ];
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
    }) cfg.forwards;
  };
}
