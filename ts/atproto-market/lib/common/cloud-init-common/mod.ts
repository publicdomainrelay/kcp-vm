// Pure cloud-init YAML generation helpers. Zero I/O.

import { parse as yamlParse, stringify as yamlStringify } from "yaml";

export interface CloudInitContext {
  /** VM name / RBAC role; used as the fedproxy SERVICE name. */
  vmName: string;
  /** Full DID (did:plc:…). */
  didPlc: string;
  /** Bare PLC key (DID without the `did:plc:` prefix). */
  didPlcKey: string;
  /** Relay FQDN (e.g. xrpc.fedproxy.com). The fedproxy-client ATPRP_URL is built from this. */
  relayHost: string;
  /** Subdomain the relay registered for this requester. */
  xrpcRelaySubdomain: string;
  /** OpenSSH public key (single line) added to root's authorized_keys. */
  sshAuthorizedKey: string;
}

/** Mirror atprp-ssh-relay's flattenLabel. Must stay in sync with cmd/atprp-ssh-relay/main.go:flattenLabel. */
export function flattenLabel(s: string): string {
  return s.replace(/[.:]/g, "-");
}

/**
 * Build the default cloud-config for a VM: OpenSSH reachable over WebSocket.
 *
 * sshd listens on 127.0.0.1:22 (loopback only). websocat bridges
 * ws-listen 127.0.0.1:8080 → tcp 127.0.0.1:22, and fedproxy-client fronts
 * :8080 — so an external SSH client tunnels through the relay over a WebSocket
 * (`ProxyCommand websocat --binary ws://<service>.fedproxy.com`). Root login is
 * key-only; the public key is injected by the requester, which holds the matching
 * private key.
 */
export function buildDefaultUserData(ctx: CloudInitContext): string {
  const { vmName, didPlc, didPlcKey, relayHost, xrpcRelaySubdomain, sshAuthorizedKey } = ctx;
  const xrpcRelayFqdn = `${xrpcRelaySubdomain}.${relayHost}`;
  return `#cloud-config
packages:
  - openssh-server
  - jq
  - curl

# Key-only root login over the websocat tunnel.
disable_root: false
ssh_pwauth: false

write_files:
  - path: /root/.ssh/authorized_keys
    owner: root:root
    permissions: '0600'
    content: |
      ${sshAuthorizedKey}

  - path: /etc/ssh/sshd_config.d/10-websocat.conf
    owner: root:root
    permissions: '0644'
    content: |
      # sshd is only reachable through the websocat→fedproxy tunnel.
      ListenAddress 127.0.0.1
      PermitRootLogin prohibit-password
      PasswordAuthentication no

  - path: /usr/local/bin/setup-websocat.sh
    owner: root:root
    permissions: '0755'
    content: |
      #!/bin/bash
      set -x

      STAMP=/var/lib/setup-websocat.done
      [ -f "\\\${STAMP}" ] && exit 0

      retry() {
        n=0
        delay=5
        until "$@"; do
          n=$((n + 1))
          echo "command failed (attempt $n): $*; retrying in \\\${delay}s" >&2
          sleep "$delay"
        done
      }

      # fedproxy-client (fronts the websocat WebSocket listener).
      _arch=$(uname -m)
      case "$_arch" in x86_64|amd64) _arch=amd64 ;; aarch64|arm64) _arch=arm64 ;; esac
      _os=$(uname -s | tr '[:upper:]' '[:lower:]')
      retry sh -c "curl -sfL 'https://github.com/publicdomainrelay/atproto-reverse-proxy/releases/download/latest/atproto-reverse-proxy_\${_os}_\${_arch}.tar.gz' | tar -xvz -C /usr/local/bin"

      # websocat release binary (musl-static; ws ↔ tcp bridge).
      case "$_arch" in amd64) _ws_arch=x86_64 ;; arm64) _ws_arch=aarch64 ;; esac
      retry sh -c "curl -sfL 'https://github.com/vi/websocat/releases/download/v1.13.0/websocat.\${_ws_arch}-unknown-linux-musl' -o /usr/local/bin/websocat"
      chmod +x /usr/local/bin/websocat

      systemctl enable websocat.service fedproxy-client.service
      systemctl start --no-block websocat.service fedproxy-client.service

      touch "\\\${STAMP}"

  - path: /etc/systemd/system/websocat.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=websocat ws→sshd bridge (fronted by fedproxy-client)
      After=network-online.target sshd.service ssh.service
      Wants=network-online.target

      [Service]
      Type=simple
      User=root
      # WebSocket listener on loopback :8080 → sshd on loopback :22.
      # fedproxy-client (SERVICE=${vmName}, PORT=8080) forwards external WS here.
      ExecStart=/usr/local/bin/websocat --binary ws-l:127.0.0.1:8080 tcp:127.0.0.1:22
      Restart=always
      RestartSec=5
      TimeoutStopSec=10
      StandardOutput=journal
      StandardError=journal

      [Install]
      WantedBy=multi-user.target

  - path: /etc/systemd/system/setup-websocat.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=First-boot websocat setup (install binaries)
      After=network-online.target
      Wants=network-online.target
      ConditionPathExists=/root/secrets/digitalocean.com/serviceaccount/token
      ConditionPathExists=!/var/lib/setup-websocat.done

      [Service]
      Type=oneshot
      User=root
      ExecStart=/usr/local/bin/setup-websocat.sh
      StandardOutput=journal
      StandardError=journal

      [Install]
      WantedBy=multi-user.target

  - path: /etc/systemd/system/setup-websocat.path
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=Watch for DO service-account token then run setup-websocat

      [Path]
      PathExists=/root/secrets/digitalocean.com/serviceaccount/token
      Unit=setup-websocat.service

      [Install]
      WantedBy=multi-user.target

  - path: /etc/systemd/system/fedproxy-client.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=FedProxy Client Service
      After=network-online.target
      Wants=network-online.target

      [Service]
      Type=simple
      User=root
      WorkingDirectory=/root
      Environment="SERVICE=${vmName}"
      # SSH username the relay flattens into the host's handle segment. Pinning
      # it to the did:plc yields <SERVICE>--did-plc-<key>.fedproxy.com.
      Environment="HANDLE=${didPlc}"
      Environment="PORT=8080"
      Environment="ATPRP_URL=https://${xrpcRelayFqdn}"
      Environment="AUTH_PLUGIN=oidc"
      Environment="MARKET_ACCEPT_JSON_PATH=/root/secrets/publicdomainrelay.com/market/accept.json"
      ExecStart=/usr/local/bin/fedproxy-client
      Restart=always
      RestartSec=5
      TimeoutStopSec=10
      StandardOutput=journal
      StandardError=journal

      [Install]
      WantedBy=multi-user.target

runcmd:
  - systemctl daemon-reload
  - systemctl enable --now ssh || systemctl enable --now sshd
  - systemctl enable setup-websocat.path
  - systemctl start --no-block setup-websocat.path
`;
}

function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : [];
}

/**
 * Merge the default websocat/fedproxy-client provisioning into a caller-supplied
 * cloud-config, instead of generating a fresh one. Same mechanism the
 * compute-providers use to patch user_data (injectAcceptBundle): parse the base
 * YAML, append our packages/write_files/runcmd, restringify. buildDefaultUserData
 * is the single source of truth for what we inject — its output is parsed and its
 * sections concatenated onto the base. Scalar toggles (disable_root, ssh_pwauth)
 * are forced to our key-only-root values. The base's own packages, files, and
 * commands are preserved.
 */
export function patchDefaultUserData(
  baseUserData: string,
  ctx: CloudInitContext,
): string {
  const ours = yamlParse(
    buildDefaultUserData(ctx).replace(/^#cloud-config\s*/i, ""),
  ) as Record<string, unknown>;

  let base: Record<string, unknown> = {};
  try {
    const parsed = baseUserData
      ? yamlParse(baseUserData.replace(/^#cloud-config\s*/i, ""))
      : null;
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      base = parsed as Record<string, unknown>;
    }
  } catch {
    /* fall through with empty base */
  }

  const merged: Record<string, unknown> = { ...base };
  merged.packages = [...asArray(base.packages), ...asArray(ours.packages)];
  merged.write_files = [...asArray(base.write_files), ...asArray(ours.write_files)];
  merged.runcmd = [...asArray(base.runcmd), ...asArray(ours.runcmd)];
  merged.disable_root = ours.disable_root;
  merged.ssh_pwauth = ours.ssh_pwauth;

  return "#cloud-config\n" + yamlStringify(merged, { lineWidth: 0 });
}

export interface TunnelCloudInitContext {
  /** host:port the guest dials outbound to reach the relay dispatcher. */
  ingressProxyHost: string;
  /** Relay hostname used as the service-auth `aud` (did:web:<audHost>). */
  audHost: string;
  /** secp256k1 private key hex; the relay verifies the registration nonce sig. */
  privateKeyHex: string;
  /** host:port of the local hono-jsr registry; emitted as Deno's JSR_URL. */
  jsrUrl: string;
  /** OpenSSH public key (single line) added to root's authorized_keys. */
  sshAuthorizedKey: string;
  /** Local TCP port the subscriber bridges relay tunnel bytes to. Default 22. */
  targetPort?: number;
}

/**
 * Sibling of buildDefaultUserData that replaces the fedproxy-client transport
 * with the xrpc tunnel-subscriber. sshd listens on :22; the
 * subscriber dials the relay outbound, registers its DID subdomain, and bridges
 * raw relay tunnel bytes straight to sshd (no guest websocat — the subscriber
 * speaks raw TCP). The agent is pulled at boot via `deno run jsr:` from the
 * local hono-jsr registry (Deno's JSR_URL override); `deno` is already on the
 * compute-provider runner image PATH.
 */
export function buildTunnelUserData(ctx: TunnelCloudInitContext): string {
  const { ingressProxyHost, audHost, privateKeyHex, jsrUrl, sshAuthorizedKey } = ctx;
  const targetPort = ctx.targetPort ?? 22;
  return `#cloud-config
packages:
  - openssh-server
  - jq
  - curl
  - unzip

disable_root: false
ssh_pwauth: false

write_files:
  - path: /root/.ssh/authorized_keys
    owner: root:root
    permissions: '0600'
    content: |
      ${sshAuthorizedKey}

  - path: /etc/ssh/sshd_config.d/10-tunnel.conf
    owner: root:root
    permissions: '0644'
    content: |
      # Key-only root login; reached through the xrpc relay tunnel (the
      # compute-provider harness also TCP-probes :22 directly for readiness).
      PermitRootLogin prohibit-password
      PasswordAuthentication no

  - path: /etc/systemd/system/tunnel-subscriber.service
    owner: root:root
    permissions: '0644'
    content: |
      [Unit]
      Description=xrpc tunnel subscriber (ssh-over-relay)
      After=network-online.target sshd.service ssh.service
      Wants=network-online.target

      [Service]
      Type=simple
      User=root
      Environment="JSR_URL=http://${jsrUrl}/"
      Environment="DENO_DIR=/var/lib/deno"
      ExecStart=deno run -A jsr:@publicdomainrelay/hono-did-key-ingress-proxy-tunnel-subscriber --ingress-proxy-host ${ingressProxyHost} --aud-host ${audHost} --private-key-hex ${privateKeyHex} --target-host 127.0.0.1 --target-port ${targetPort}
      Restart=always
      RestartSec=5
      TimeoutStopSec=10
      StandardOutput=journal
      StandardError=journal

      [Install]
      WantedBy=multi-user.target

runcmd:
  - systemctl daemon-reload
  - systemctl enable --now ssh || systemctl enable --now sshd
  - systemctl enable --now tunnel-subscriber.service
`;
}

export function buildIrohUserData(ctx: TunnelCloudInitContext): string {
  const sshAuthorizedKey = ctx.sshAuthorizedKey ?? "";
  return `#cloud-config
packages:
  - openssh-server
  - jq
  - curl

disable_root: false
ssh_pwauth: false

write_files:
  - path: /root/.ssh/authorized_keys
    owner: root:root
    permissions: '0600'
    content: |
      ${sshAuthorizedKey}

  - path: /etc/ssh/sshd_config.d/10-iroh.conf
    owner: root:root
    permissions: '0644'
    content: |
      PermitRootLogin prohibit-password
      PasswordAuthentication no

  - path: /etc/systemd/system/iroh.service
    permissions: '0644'
    content: |
      [Unit]
      Description=iroh P2P endpoint
      After=network-online.target sshd.service
      [Service]
      Type=simple
      ExecStart=/usr/local/bin/iroh endpoint --bind 0.0.0.0:9876
      Restart=always
      RestartSec=5
      [Install]
      WantedBy=multi-user.target

runcmd:
  - systemctl daemon-reload
  - systemctl enable --now ssh
  - systemctl enable --now iroh.service
`;
}
