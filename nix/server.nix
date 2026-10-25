# NixOS module for the Super Matrix server: services.supermatrix.
#
# Like super-chat's module, the server is built by an activation script
# rather than a buildGoModule derivation: keeping a vendorHash in sync by hand
# on every go.sum change costs more than it's worth for a binary with no
# reproducibility requirement. It rebuilds from ${self}/server on every
# activation; goCache keeps that fast after the first run. The web app
# follows the same tradeoff via npmCache.
{ self }:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.supermatrix;
  stateDir = "/var/lib/supermatrix";
  serverBin = "${stateDir}/bin/supermatrix-server";
  adminBin = "${stateDir}/bin/supermatrix-admin";
  goCache = "/var/cache/supermatrix-go";
  npmCache = "/var/cache/supermatrix-npm";
  webDir = "${stateDir}/web";

  # `supermatrix-admin list` etc. against the running service's database.
  adminWrapper = pkgs.writeShellScriptBin "supermatrix-admin" ''
    export SUPERMATRIX_DATA_DIR="${cfg.dataDir}"
    exec ${pkgs.sudo}/bin/sudo -u supermatrix -E ${adminBin} "$@"
  '';

  portOf = a: lib.toInt (lib.last (lib.splitString ":" a));
  # The server's own default: the HTTPS port, or the HTTP one with TLS off.
  quicAddr = if cfg.quic.addr != null then cfg.quic.addr
    else if cfg.tls == "acme" then cfg.httpsAddr else cfg.httpAddr;
  bindsLowPorts = lib.any (a: portOf a < 1024)
    ([ cfg.httpAddr ] ++ lib.optional (cfg.tls == "acme") cfg.httpsAddr
      ++ lib.optional cfg.quic.enable quicAddr);
in
{
  options.services.supermatrix = {
    enable = lib.mkEnableOption "the Super Matrix relay server";

    adminHost = lib.mkOption {
      type = lib.types.str;
      example = "matrix.example.com";
      description = ''
        Hostname of the admin API, the web app, and where relays connect.
        Point an A/AAAA record for it at this machine.
      '';
    };

    baseDomain = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "example.com";
      description = ''
        Optional first domain: on a server with no domains yet, *.<baseDomain>
        is added as a wildcard domain. Domains (any number of wildcard ones
        like *.example.com, and exact hostnames like status.foo.org) are
        managed in the app after that. Each deployment still gets its own
        certificate; no wildcard certificate is used.
      '';
    };

    rootPasswordFile = lib.mkOption {
      type = lib.types.str;
      example = "/run/secrets/supermatrix-root";
      description = ''
        File holding the root password for the app (and, where allowed,
        deployment logins). Passed via LoadCredential: give a runtime path as
        a string, not a Nix path literal (which would copy it into the store).
      '';
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "/etc/supermatrix/env";
      description = ''
        File with the SMTP account email-code deployments send sign-in codes
        from: SUPERMATRIX_SMTP_EMAIL=… and SUPERMATRIX_SMTP_APP_PASSWORD=…
        (an app password), plus SUPERMATRIX_SMTP_HOST=… and
        SUPERMATRIX_SMTP_PORT=… for anything but Gmail (default
        smtp.gmail.com, 587). Without it, email-code sign-in can't send
        codes; everything else works. Give a runtime path as a string, not a
        Nix path literal (which would copy it into the store).
      '';
    };

    tls = lib.mkOption {
      type = lib.types.enum [ "acme" "off" ];
      default = "acme";
      description = ''
        "acme": terminate TLS here with a Let's Encrypt certificate per host
        (HTTP-01 on httpAddr, TLS-ALPN-01 on httpsAddr). "off": plain HTTP on
        httpAddr only, e.g. behind another TLS proxy.
      '';
    };

    acmeEmail = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Optional contact address for Let's Encrypt expiry notices.";
    };

    acmeStaging = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Use Let's Encrypt's staging CA (untrusted certs, generous rate limits) while testing.";
    };

    httpAddr = lib.mkOption {
      type = lib.types.str;
      default = ":80";
      description = "HTTP listener: ACME HTTP-01 challenges and redirects to https (or everything, with tls = \"off\").";
    };

    httpsAddr = lib.mkOption {
      type = lib.types.str;
      default = ":443";
      description = "HTTPS listener.";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "${stateDir}/data";
      description = "Where the SQLite database, certificates and the cookie key live.";
    };

    trustProxy = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Believe the rightmost X-Forwarded-For on loopback connections. Only
        turn on with tls = "off" behind a proxy on this machine.
      '';
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Open the HTTP and HTTPS ports (TCP), and the QUIC port (UDP).";
    };

    quic = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Accept relays over QUIC (UDP). Relays use it when they can and
          fall back to the WebSocket on networks that block UDP; the app
          shows which each relay is on, and why.
        '';
      };
      addr = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = ":443";
        description = "UDP listener for QUIC. Default: the same port as httpsAddr (httpAddr with tls = \"off\").";
      };
      publicAddr = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "matrix.example.com:8443";
        description = "host:port relays are told to dial, when a NAT or firewall maps another port. Default: adminHost and the listener's port.";
      };
    };

    web.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Build the app's web client and serve it on the admin host.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.supermatrix = {
      isSystemUser = true;
      group = "supermatrix";
      home = stateDir;
    };
    users.groups.supermatrix = { };

    systemd.tmpfiles.rules = [
      "d ${stateDir} 0750 supermatrix supermatrix -"
      "d ${stateDir}/bin 0755 root root -"
      "d ${cfg.dataDir} 0700 supermatrix supermatrix -"
      "d ${goCache} 0755 root root -"
    ] ++ lib.optionals cfg.web.enable [
      "d ${npmCache} 0755 root root -"
    ];

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall (
      map portOf ([ cfg.httpAddr ] ++ lib.optional (cfg.tls == "acme") cfg.httpsAddr)
    );
    networking.firewall.allowedUDPPorts = lib.mkIf (cfg.openFirewall && cfg.quic.enable) [ (portOf quicAddr) ];

    # quic-go wants larger UDP socket buffers than the kernel default for
    # high-bandwidth links (https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes).
    boot.kernel.sysctl = lib.mkIf cfg.quic.enable {
      "net.core.rmem_max" = lib.mkDefault 7500000;
      "net.core.wmem_max" = lib.mkDefault 7500000;
    };

    system.activationScripts.supermatrixBuild = {
      deps = [ ];
      text = ''
        export PATH=${pkgs.go}/bin:${pkgs.bash}/bin:${pkgs.nodejs_22}/bin:$PATH
        export HOME=${goCache}
        export GOCACHE=${goCache}/build
        export GOPATH=${goCache}/path
        # modernc sqlite is pure Go; no C toolchain needed.
        export CGO_ENABLED=0
        mkdir -p ${stateDir}/bin ${goCache}
        cd ${self}/server
        ${pkgs.go}/bin/go build -buildvcs=false -o ${serverBin}.new ./cmd/supermatrix-server
        mv -f ${serverBin}.new ${serverBin}
        ${pkgs.go}/bin/go build -buildvcs=false -o ${adminBin}.new ./cmd/supermatrix-admin
        mv -f ${adminBin}.new ${adminBin}
      '' + lib.optionalString cfg.web.enable ''
        # ${self}/app is in the read-only store and npm ci writes
        # node_modules into the project, so build from a scratch copy.
        export npm_config_cache=${npmCache}
        appBuild=${stateDir}/app-build
        rm -rf "$appBuild"
        mkdir -p "$appBuild"
        cp -r ${self}/app/. "$appBuild"/
        chmod -R u+w "$appBuild"
        cd "$appBuild"
        ${pkgs.nodejs_22}/bin/npm ci
        ${pkgs.nodejs_22}/bin/npm run build:web
        rm -rf ${webDir}.new
        cp -r dist ${webDir}.new
        chown -R supermatrix:supermatrix ${webDir}.new
        rm -rf ${webDir}
        mv -f ${webDir}.new ${webDir}
      '' + ''
        ${pkgs.systemd}/bin/systemctl try-restart supermatrix.service || true
      '';
    };

    environment.systemPackages = [ adminWrapper ];

    systemd.services.supermatrix = {
      description = "Super Matrix server";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        SUPERMATRIX_ADMIN_HOST = cfg.adminHost;
        SUPERMATRIX_TLS = cfg.tls;
        SUPERMATRIX_HTTP_ADDR = cfg.httpAddr;
        SUPERMATRIX_HTTPS_ADDR = cfg.httpsAddr;
        SUPERMATRIX_ACME_EMAIL = cfg.acmeEmail;
        SUPERMATRIX_DATA_DIR = cfg.dataDir;
        SUPERMATRIX_ROOT_PASSWORD_FILE = "%d/root-password";
        SUPERMATRIX_TRUST_PROXY = if cfg.trustProxy then "1" else "0";
        SUPERMATRIX_QUIC = if cfg.quic.enable then "on" else "off";
        SUPERMATRIX_QUIC_ADDR = quicAddr;
      } // lib.optionalAttrs (cfg.baseDomain != null) {
        SUPERMATRIX_BASE_DOMAIN = cfg.baseDomain;
      } // lib.optionalAttrs (cfg.quic.publicAddr != null) {
        SUPERMATRIX_QUIC_PUBLIC = cfg.quic.publicAddr;
      } // lib.optionalAttrs cfg.acmeStaging {
        SUPERMATRIX_ACME_DIRECTORY = "staging";
      } // lib.optionalAttrs cfg.web.enable {
        SUPERMATRIX_STATIC_DIR = webDir;
      };
      serviceConfig = {
        ExecStart = serverBin;
        Restart = "always";
        RestartSec = 2;
        User = "supermatrix";
        Group = "supermatrix";
        LoadCredential = [ "root-password:${cfg.rootPasswordFile}" ];
        EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
        # 80/443 without running as root.
        AmbientCapabilities = lib.optional bindsLowPorts "CAP_NET_BIND_SERVICE";
        CapabilityBoundingSet = lib.optional bindsLowPorts "CAP_NET_BIND_SERVICE";
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        ReadWritePaths = [ cfg.dataDir ];
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
        UMask = "0077";
        LimitNOFILE = 65536;
      };
    };
  };
}
