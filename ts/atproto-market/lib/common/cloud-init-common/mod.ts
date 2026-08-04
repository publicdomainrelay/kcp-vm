// Pure cloud-init YAML generation helpers. Zero I/O.
//
// One composer (buildUserData) + a module registry. Every guest personality is
// a UserDataModule producing a UserDataPatch that is merged onto an optional
// caller-supplied base cloud-config. Modules are pure: parse/merge/stringify
// only, no I/O, no crypto, no Deno.

import { parse as yamlParse, stringify as yamlStringify } from "yaml";

/**
 * Unified context for all built-in modules. Superset of the historical
 * CloudInitContext (fedproxy/websocat) and TunnelCloudInitContext (tunnel
 * subscriber). Modules read the fields they need; undefined fields render as
 * empty. Keep per-module doc comments listing which fields they use.
 */
export interface CloudInitContext {
  /** VM name / RBAC role; used as the fedproxy SERVICE name and ttyd rkey. */
  vmName: string;
  /** Full DID (did:plc:...). fedproxy HANDLE for the fedproxy-ssh transport. */
  didPlc?: string;
  /** Bare PLC key (DID without the `did:plc:` prefix). */
  didPlcKey?: string;
  /** Relay FQDN (e.g. xrpc.fedproxy.com). fedproxy ATPRP_URL built from this. */
  relayHost?: string;
  /** Subdomain the relay registered for this requester. */
  xrpcRelaySubdomain?: string;
  /** OpenSSH public key (single line) added to root's authorized_keys. */
  sshAuthorizedKey?: string;

  // tunnel transport
  /** host:port the guest dials outbound to reach the relay dispatcher. */
  ingressProxyHost?: string;
  /** Relay hostname used as the service-auth `aud` (did:web:<audHost>). */
  audHost?: string;
  /** Optional host:port of a local hono-jsr registry (sets Deno's JSR_URL). */
  jsrUrl?: string;
  /** Local TCP port the subscriber bridges relay tunnel bytes to. Default 22. */
  targetPort?: number;
  /** Extra `/etc/hosts` entries ("<ip> <name>") so the guest can dial the dispatcher by name. */
  hostAliases?: string[];

  // fedproxy-web / wootty terminal
  /** Short did:plc identity fedproxy-client uses as its SSH username (DNS-label safe). */
  sshHandle?: string;
  /** Port the terminal/ingress service listens on. Default 8080. */
  listenPort?: number;
  /** Origin serving the wootty-web tarball (e.g. https://ui.fedfork.com). */
  woottyDistUrl?: string;

  // secrets
  /** Base URL of the requester's ephemeral secrets server (its relay ingress URL). */
  secretsUrl?: string;
  /** Route on secretsUrl serving the [{path,value}] bundle. */
  secretsRoute?: string;
  /** Audience the guest must request when exchanging its workload identity token. */
  secretsAud?: string;
  /** Guest path of the bidder-injected accept.json carrying bid_config. */
  secretsAcceptPath?: string;
}

/** Back-compat context for the tunnel-subscriber transport (historical buildTunnelUserData shape). */
export interface TunnelCloudInitContext {
  /** host:port the guest dials outbound to reach the relay dispatcher. */
  ingressProxyHost: string;
  /** Relay hostname used as the service-auth `aud` (did:web:<audHost>). */
  audHost: string;
  /** Optional host:port of a local hono-jsr registry (sets Deno's JSR_URL). */
  jsrUrl?: string;
  /** OpenSSH public key (single line) added to root's authorized_keys. */
  sshAuthorizedKey: string;
  /** Local TCP port the subscriber bridges relay tunnel bytes to. Default 22. */
  targetPort?: number;
  /** Extra `/etc/hosts` entries ("<ip> <name>") so the guest can dial the dispatcher by name. */
  hostAliases?: string[];
}

export interface WriteFileEntry {
  path: string;
  owner?: string;
  permissions?: string;
  content: string;
  [k: string]: unknown;
}

/** Section-shaped YAML patch, merged onto base (and prior modules). */
export interface UserDataPatch {
  apt?: Record<string, unknown>;
  packages?: string[];
  users?: Record<string, unknown>[];
  write_files?: WriteFileEntry[];
  runcmd?: unknown[];
  /** Inserted BEFORE the base runcmd (preserves injectAcceptBundle / provisioning unshift order). */
  runcmdPrepend?: unknown[];
  bootcmd?: unknown[];
  disable_root?: boolean;
  ssh_pwauth?: boolean;
}

export type UserDataModule = (ctx: Partial<CloudInitContext>) => UserDataPatch;

/** Mirror atprp-ssh-relay's flattenLabel. Must stay in sync with cmd/atprp-ssh-relay/main.go:flattenLabel. */
export function flattenLabel(s: string): string {
  return s.replace(/[.:]/g, "-");
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

const registry = new Map<string, UserDataModule>();

/** Register (or replace) a user-data module by id. */
export function registerUserDataModule(id: string, m: UserDataModule): void {
  registry.set(id, m);
}

/** Resolve a list of ids/module functions to functions; throws on unknown id. */
export function getUserDataModules(ids: Array<string | UserDataModule>): UserDataModule[] {
  return ids.map((idOrFn) => {
    if (typeof idOrFn === "function") return idOrFn;
    const m = registry.get(idOrFn);
    if (!m) throw new Error(`unknown user-data module: ${idOrFn}`);
    return m;
  });
}

export function listUserDataModules(): string[] {
  return [...registry.keys()];
}

// ---------------------------------------------------------------------------
// Composer
// ---------------------------------------------------------------------------

function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : [];
}

function asObj(v: unknown): Record<string, unknown> {
  return v && typeof v === "object" && !Array.isArray(v) ? v as Record<string, unknown> : {};
}

function dedupeByKey<T>(arr: T[], key: (x: T) => unknown): T[] {
  const indexOf = new Map<unknown, number>();
  const out: T[] = [];
  for (const item of arr) {
    const k = key(item);
    if (k !== undefined && indexOf.has(k)) {
      // Later wins, keeps first position (cloud-init last-wins semantics).
      out[indexOf.get(k)!] = item;
    } else {
      if (k !== undefined) indexOf.set(k, out.length);
      out.push(item);
    }
  }
  return out;
}

function applyPatch(base: Record<string, unknown>, p: UserDataPatch): Record<string, unknown> {
  const out = { ...base };
  if (p.apt) out.apt = { ...asObj(out.apt), ...p.apt };
  if (p.packages) out.packages = [...new Set([...asArray(out.packages), ...p.packages])];
  if (p.users) {
    out.users = dedupeByKey([...asArray(out.users), ...p.users], (u) => (u as { name?: unknown }).name);
  }
  if (p.write_files) {
    out.write_files = dedupeByKey(
      [...asArray(out.write_files), ...p.write_files],
      (w) => (w as { path?: unknown }).path,
    );
  }
  if (p.bootcmd) out.bootcmd = [...new Set([...asArray(out.bootcmd), ...p.bootcmd])];
  if (p.runcmd !== undefined || p.runcmdPrepend !== undefined) {
    out.runcmd = [...(p.runcmdPrepend ?? []), ...asArray(out.runcmd), ...(p.runcmd ?? [])];
  }
  if (p.disable_root !== undefined) out.disable_root = p.disable_root;
  if (p.ssh_pwauth !== undefined) out.ssh_pwauth = p.ssh_pwauth;
  return out;
}

function parseBase(base: string | null | undefined): Record<string, unknown> {
  if (!base) return {};
  try {
    const parsed = yamlParse(base.replace(/^#cloud-config\s*/i, ""));
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : {};
  } catch {
    return {};
  }
}

/**
 * Build a #cloud-config string: optional caller-supplied base cloud-config,
 * then the given modules applied in order (later modules win on scalar toggles
 * and duplicate write_file paths; arrays append), then optional overrides.
 * Precedence: base < modules < overrides. The composer owns the #cloud-config
 * header — modules must not emit it.
 */
export function buildUserData(opts: {
  ctx?: Partial<CloudInitContext>;
  base?: string | null;
  modules: Array<string | UserDataModule>;
  overrides?: Partial<UserDataPatch>;
}): string {
  const ctx = opts.ctx ?? {};
  let obj = parseBase(opts.base);
  for (const m of getUserDataModules(opts.modules)) {
    obj = applyPatch(obj, m(ctx));
  }
  if (opts.overrides) obj = applyPatch(obj, opts.overrides);
  return "#cloud-config\n" + yamlStringify(obj, { lineWidth: 0 });
}

// ---------------------------------------------------------------------------
// Built-in modules
// ---------------------------------------------------------------------------

/** Inject a provider accept bundle (accept.json) + parent-dir runcmd. */
export function acceptBundleModule(
  acceptPathVm: string,
  bundle: Record<string, unknown>,
): UserDataModule {
  const parent = acceptPathVm.split("/").slice(0, -1).join("/");
  return () => ({
    write_files: [{
      path: acceptPathVm,
      owner: "root:root",
      permissions: "0600",
      content: JSON.stringify(bundle, null, 2),
    }],
    runcmdPrepend: [["sh", "-c", `install -d -m 0700 -o root -g root ${parent}`]],
  });
}

/**
 * tunnel — xrpc tunnel-subscriber transport. Guest sshd on :22; the subscriber
 * dials the relay outbound, registers its DID subdomain, bridges raw relay
 * tunnel bytes to sshd (no guest websocat). Requires ctx.ingressProxyHost,
 * ctx.audHost, ctx.sshAuthorizedKey. Optional: ctx.jsrUrl, ctx.targetPort,
 * ctx.hostAliases.
 */
const tunnelModule: UserDataModule = (ctx) => {
  const targetPort = ctx.targetPort ?? 22;
  const aliases = (ctx.hostAliases ?? []).filter((a) => /^[\w.:-]+\s+[\w.-]+$/.test(a));
  return {
    apt: { preserve_sources_list: true },
    disable_root: false,
    ssh_pwauth: false,
    bootcmd: aliases.map((a) =>
      ["sh", "-c", `grep -qxF '${a}' /etc/hosts || echo '${a}' >> /etc/hosts`]
    ),
    write_files: [
      {
        path: "/root/.ssh/authorized_keys",
        owner: "root:root",
        permissions: "0600",
        content: `${ctx.sshAuthorizedKey ?? ""}\n`,
      },
      {
        path: "/etc/ssh/sshd_config.d/10-tunnel.conf",
        owner: "root:root",
        permissions: "0644",
        content: [
          "# Key-only root login; reached through the xrpc relay tunnel (the",
          "# compute-provider harness also TCP-probes :22 directly for readiness).",
          "PermitRootLogin prohibit-password",
          "PasswordAuthentication no",
        ].join("\n") + "\n",
      },
      {
        path: "/etc/systemd/system/tunnel-subscriber.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=xrpc tunnel subscriber (ssh-over-relay)",
          "After=network-online.target sshd.service ssh.service",
          "Wants=network-online.target",
          "",
          "[Service]",
          "Type=simple",
          "User=root",
          ...(ctx.jsrUrl ? [`Environment="JSR_URL=http://${ctx.jsrUrl}/"`] : []),
          `ExecStart=/usr/local/bin/deno run -A jsr:@publicdomainrelay/hono-did-key-ingress-proxy-tunnel-subscriber --ingress-proxy-host ${ctx.ingressProxyHost ?? ""} --aud-host ${ctx.audHost ?? ""} --private-key-from-sshd-host-key /etc/ssh/ssh_host_ed25519_key --fqdn-file /run/guest-fqdn --target-host 127.0.0.1 --target-port ${targetPort}`,
          "Restart=always",
          "RestartSec=5",
          "TimeoutStopSec=10",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
    ],
    runcmd: [
      ["sh", "-c", "command -v deno || { apt-get update && apt-get install -y curl unzip; curl -fsSL https://deno.land/install.sh | DENO_INSTALL=/usr/local sh; chmod 755 /usr/local/bin/deno; }"],
      "systemctl daemon-reload",
      "systemctl enable --now ssh || systemctl enable --now sshd",
      "systemctl enable --now tunnel-subscriber.service",
    ],
  };
};

/**
 * fedproxy-ssh — websocat + fedproxy-client transport. sshd reachable through a
 * websocat ws->sshd bridge on loopback :8080, fronted by fedproxy-client.
 * Requires ctx.vmName, ctx.didPlc, ctx.relayHost, ctx.xrpcRelaySubdomain,
 * ctx.sshAuthorizedKey.
 */
const fedproxySshModule: UserDataModule = (ctx) => {
  const xrpcRelayFqdn = `${ctx.xrpcRelaySubdomain ?? ""}.${ctx.relayHost ?? ""}`;
  return {
    apt: { preserve_sources_list: true },
    disable_root: false,
    ssh_pwauth: false,
    write_files: [
      {
        path: "/root/.ssh/authorized_keys",
        owner: "root:root",
        permissions: "0600",
        content: `${ctx.sshAuthorizedKey ?? ""}\n`,
      },
      {
        path: "/etc/ssh/sshd_config.d/10-websocat.conf",
        owner: "root:root",
        permissions: "0644",
        content: [
          "# sshd reachable through websocat->fedproxy tunnel (loopback only).",
          "ListenAddress 127.0.0.1",
          "PermitRootLogin prohibit-password",
          "PasswordAuthentication no",
        ].join("\n") + "\n",
      },
      {
        path: "/usr/local/bin/setup-websocat.sh",
        owner: "root:root",
        permissions: "0755",
        content: `#!/bin/bash
set -x

STAMP=/var/lib/setup-websocat.done
[ -f "\${STAMP}" ] && exit 0

retry() {
  n=0
  delay=5
  until "$@"; do
    n=$((n + 1))
    echo "command failed (attempt $n): $*; retrying in \${delay}s" >&2
    sleep "$delay"
  done
}

# fedproxy-client (fronts the websocat WebSocket listener).
_arch=$(uname -m)
case "$_arch" in x86_64|amd64) _arch=amd64 ;; aarch64|arm64) _arch=arm64 ;; esac
_os=$(uname -s | tr '[:upper:]' '[:lower:]')
retry sh -c "curl -sfL 'https://github.com/publicdomainrelay/atproto-reverse-proxy/releases/download/latest/atproto-reverse-proxy_\${_os}_\${_arch}.tar.gz' | tar -xvz -C /usr/local/bin"

# websocat release binary (musl-static; ws <-> tcp bridge).
case "$_arch" in amd64) _ws_arch=x86_64 ;; arm64) _ws_arch=aarch64 ;; esac
retry sh -c "curl -sfL 'https://github.com/vi/websocat/releases/download/v1.13.0/websocat.\${_ws_arch}-unknown-linux-musl' -o /usr/local/bin/websocat"
chmod +x /usr/local/bin/websocat

systemctl enable websocat.service fedproxy-client.service
systemctl start --no-block websocat.service fedproxy-client.service

touch "\${STAMP}"
`,
      },
      {
        path: "/etc/systemd/system/websocat.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=websocat ws->sshd bridge (fronted by fedproxy-client)",
          "After=network-online.target sshd.service ssh.service",
          "Wants=network-online.target",
          "",
          "[Service]",
          "Type=simple",
          "User=root",
          `# WebSocket listener on loopback :8080 -> sshd on loopback :22.`,
          `# fedproxy-client (SERVICE=${ctx.vmName ?? ""}, PORT=8080) forwards external WS here.`,
          "ExecStart=/usr/local/bin/websocat --binary ws-l:127.0.0.1:8080 tcp:127.0.0.1:22",
          "Restart=always",
          "RestartSec=5",
          "TimeoutStopSec=10",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
      {
        path: "/etc/systemd/system/setup-websocat.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=First-boot websocat setup (install binaries)",
          "After=network-online.target",
          "Wants=network-online.target",
          "ConditionPathExists=/root/secrets/digitalocean.com/serviceaccount/token",
          "ConditionPathExists=!/var/lib/setup-websocat.done",
          "",
          "[Service]",
          "Type=oneshot",
          "User=root",
          "ExecStart=/usr/local/bin/setup-websocat.sh",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
      {
        path: "/etc/systemd/system/setup-websocat.path",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=Watch for DO service-account token then run setup-websocat",
          "",
          "[Path]",
          "PathExists=/root/secrets/digitalocean.com/serviceaccount/token",
          "Unit=setup-websocat.service",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
      {
        path: "/etc/systemd/system/fedproxy-client.service",
        owner: "root:root",
        permissions: "0644",
        content: fedproxyClientUnit({
          vmName: ctx.vmName ?? "",
          handle: ctx.didPlc ?? "",
          port: "8080",
          atprpUrl: `https://${xrpcRelayFqdn}`,
        }),
      },
    ],
    runcmd: [
      "systemctl daemon-reload",
      "systemctl enable --now ssh || systemctl enable --now sshd",
      "systemctl enable setup-websocat.path",
      "systemctl start --no-block setup-websocat.path",
    ],
  };
};

/** fedproxy-client.service unit body shared by the fedproxy-ssh and fedproxy-web modules. */
function fedproxyClientUnit(o: { vmName: string; handle: string; port: string; atprpUrl: string }): string {
  return [
    "[Unit]",
    "Description=FedProxy Client Service",
    "After=network-online.target",
    "Wants=network-online.target",
    "",
    "[Service]",
    "Type=simple",
    "User=root",
    "WorkingDirectory=/root",
    `Environment="SERVICE=${o.vmName}"`,
    "# SSH username the relay flattens into the host's handle segment.",
    `Environment="HANDLE=${o.handle}"`,
    `Environment="PORT=${o.port}"`,
    `Environment="ATPRP_URL=${o.atprpUrl}"`,
    "Environment=\"AUTH_PLUGIN=oidc\"",
    "Environment=\"MARKET_ACCEPT_JSON_PATH=/root/secrets/publicdomainrelay.com/market/accept.json\"",
    "ExecStart=/usr/local/bin/fedproxy-client",
    "Restart=always",
    "RestartSec=5",
    "TimeoutStopSec=10",
    "StandardOutput=journal",
    "StandardError=journal",
    "",
    "[Install]",
    "WantedBy=multi-user.target",
    "",
  ].join("\n");
}

/**
 * fedproxy-web — fedproxy-client ingress only (no sshd, no websocat): fronting
 * a browser terminal (pair with the wootty module). Requires ctx.vmName,
 * ctx.relayHost, ctx.xrpcRelaySubdomain. Uses ctx.sshHandle (falls back to
 * ctx.didPlc) and ctx.listenPort (default 8080).
 */
const fedproxyWebModule: UserDataModule = (ctx) => {
  const xrpcRelayFqdn = `${ctx.xrpcRelaySubdomain ?? ""}.${ctx.relayHost ?? ""}`;
  const handle = ctx.sshHandle ?? ctx.didPlc ?? "";
  const port = String(ctx.listenPort ?? 8080);
  return {
    write_files: [{
      path: "/etc/systemd/system/fedproxy-client.service",
      owner: "root:root",
      permissions: "0644",
      content: fedproxyClientUnit({
        vmName: ctx.vmName ?? "",
        handle,
        port,
        atprpUrl: `https://${xrpcRelayFqdn}`,
      }),
    }],
  };
};

/**
 * wootty — WooTTY browser-terminal service + OIDC token handoff. Requires
 * ctx.vmName, ctx.didPlc, ctx.didPlcKey, ctx.relayHost, ctx.xrpcRelaySubdomain.
 * Optional: ctx.woottyDistUrl (default https://ui.fedfork.com), ctx.listenPort.
 * Pair with fedproxy-web. The setup script mints a get-ttyd-password scoped
 * token via the provider issuer (/v1/oidc/issue) and fetches the terminal
 * password from the browser relay getRecord (com.fedproxy.ttydCredentials).
 */
const woottyModule: UserDataModule = (ctx) => {
  const xrpcRelayFqdn = `${ctx.xrpcRelaySubdomain ?? ""}.${ctx.relayHost ?? ""}`;
  const woottyDistUrl = ctx.woottyDistUrl ?? "https://ui.fedfork.com";
  const port = ctx.listenPort ?? 8080;
  return {
    packages: ["tmux"],
    users: [{
      name: "agent",
      gecos: "Policy Engine Agent",
      primary_group: "agent",
      groups: ["users"],
      shell: "/bin/bash",
      sudo: "ALL=(ALL) NOPASSWD:ALL",
      lock_passwd: true,
      no_user_group: false,
    }],
    disable_root: false,
    ssh_pwauth: false,
    write_files: [
      {
        path: "/usr/local/bin/setup-wootty.sh",
        owner: "root:root",
        permissions: "0755",
        content: `#!/bin/bash
set -x

STAMP=/var/lib/setup-wootty.done
[ -f "\${STAMP}" ] && exit 0

# Identity of the requesting user, wired through from the SPA.
DID_PLC="${ctx.didPlc ?? ""}"
DID_PLC_KEY="${ctx.didPlcKey ?? ""}"

URL=$(cat /root/secrets/digitalocean.com/serviceaccount/base_url)
TEAM_UUID=$(cat /root/secrets/digitalocean.com/serviceaccount/team_uuid)
ID_TOKEN=$(cat /root/secrets/digitalocean.com/serviceaccount/token)

# Scope the minted token to exactly the ttyd-password role for this VM.
SUBJECT="actx:\${TEAM_UUID}:plc:\${DID_PLC_KEY}:role:get-ttyd-password-${ctx.vmName ?? ""}"

TOKEN=$(curl -sf \\
  -H "Authorization: Bearer \${ID_TOKEN}" \\
  -d@<(jq -n -c \\
    --arg aud "api://ATProto?actx=\${DID_PLC}" \\
    --arg sub "\${SUBJECT}" \\
    --arg ttl 300 \\
    '{aud: \$aud, sub: \$sub, ttl: (\$ttl | fromjson)}') \\
  "\${URL}/v1/oidc/issue" \\
  | jq -r .token)

XRPC_RELAY_FQDN="${xrpcRelayFqdn}"

# Fetch the WooTTY auth token from the browser relay. The relay's handler
# OIDC-validates \${TOKEN} (full JWKS verify) before returning the record. The
# record's .value.password is the single WooTTY auth token (one bearer token).
mkdir -p /etc/wootty
chown agent:agent /etc/wootty
chmod 750 /etc/wootty
PASSWORD=$(curl -sf \\
  -H "Authorization: Bearer \${TOKEN}" \\
  "https://\${XRPC_RELAY_FQDN}/xrpc/com.atproto.repo.getRecord?collection=com.fedproxy.ttydCredentials&rkey=${ctx.vmName ?? ""}" \\
  | jq -r .value.password)

# wootty.service runs as User=agent and reads this as an EnvironmentFile; the
# SPA shows the user this same token (and opens the terminal with it in the URL
# hash, which WooTTY exchanges once for the wootty_auth cookie).
printf 'WOOTTY_AUTH_TOKEN=%s\\n' "\${PASSWORD}" > /etc/wootty/wootty.env
chown agent:agent /etc/wootty/wootty.env
chmod 600 /etc/wootty/wootty.env

retry() {
  n=0
  delay=5
  until "$@"; do
    n=$((n + 1))
    echo "command failed (attempt $n): $*; retrying in \${delay}s" >&2
    sleep "$delay"
  done
}

# Detect OS and architecture for the correct release archive.
_os=$(uname -s | tr '[:upper:]' '[:lower:]')
_arch=$(uname -m)
case "$_arch" in x86_64|amd64) _arch=amd64 ;; aarch64|arm64) _arch=arm64 ;; esac

retry sh -c "curl -sfL 'https://github.com/publicdomainrelay/atproto-reverse-proxy/releases/download/latest/atproto-reverse-proxy_\${_os}_\${_arch}.tar.gz' | tar -xvz -C /usr/local/bin"

# Install the woottyd browser-terminal daemon (no apt package; pull the release
# binary, mirroring the atproto-reverse-proxy pattern above).
retry sh -c "curl -sfL 'https://github.com/icoretech/wootty/releases/download/wootty-v0.2.17/woottyd_0.2.17_\${_os}_\${_arch}.tar.gz' | tar -xvz -C /usr/local/bin woottyd"
chmod +x /usr/local/bin/woottyd

# The woottyd release binary ships NO web UI; build the wootty-web assets
# ourselves and serve the tarball from the SPA origin. Extract it and point
# WOOTTY_STATIC_DIR at it (wootty.service reads that env).
mkdir -p /usr/local/share/wootty
retry sh -c "curl -sfL '${woottyDistUrl}/wootty-web-dist.tar.gz' | tar -xz -C /usr/local/share/wootty"

systemctl enable wootty fedproxy-client.service
systemctl start --no-block wootty fedproxy-client.service

touch "\${STAMP}"
`,
      },
      {
        path: "/etc/systemd/system/setup-wootty.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=First-boot WooTTY setup (fetch token, install woottyd, publish SSH key)",
          "After=network-online.target",
          "Wants=network-online.target",
          "ConditionPathExists=/root/secrets/digitalocean.com/serviceaccount/token",
          "ConditionPathExists=!/var/lib/setup-wootty.done",
          "",
          "[Service]",
          "Type=oneshot",
          "User=root",
          "ExecStart=/usr/local/bin/setup-wootty.sh",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
      {
        path: "/etc/systemd/system/setup-wootty.path",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=Watch for DO service-account token then run setup-wootty",
          "",
          "[Path]",
          "PathExists=/root/secrets/digitalocean.com/serviceaccount/token",
          "Unit=setup-wootty.service",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
      {
        path: "/etc/systemd/system/wootty.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=Policy Engine Service (WooTTY browser terminal)",
          "After=network-online.target",
          "Wants=network-online.target",
          "",
          "[Service]",
          "Type=simple",
          "User=agent",
          "Group=agent",
          "# Loopback bind; fedproxy-client fronts it. WOOTTY_AUTH_TOKEN comes from the",
          "# env file written by setup-wootty.sh. WOOTTY_COMMAND=tmux preserves the",
          "# ttyd-over-tmux behavior.",
          "EnvironmentFile=/etc/wootty/wootty.env",
          "Environment=\"WOOTTY_HOST=127.0.0.1\"",
          `Environment="WOOTTY_PORT=${port}"`,
          "Environment=\"WOOTTY_COMMAND=tmux\"",
          "# Web UI assets fetched by setup-wootty.sh (release binary embeds none).",
          "Environment=\"WOOTTY_STATIC_DIR=/usr/local/share/wootty/wootty-web\"",
          "ExecStart=/usr/local/bin/woottyd run",
          "Restart=always",
          "RestartSec=5",
          "TimeoutStopSec=10",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
    ],
    runcmd: [
      "systemctl daemon-reload",
      "systemctl enable setup-wootty.path",
      "systemctl start --no-block setup-wootty.path",
    ],
  };
};

/**
 * secrets — fetch an operator-supplied secrets bundle from the requester's
 * ephemeral secrets server and write each entry to disk. Requires ctx.secretsUrl,
 * ctx.secretsRoute, ctx.secretsAud. Optional: ctx.secretsAcceptPath.
 *
 * Provider-specific values (token path, issuer base URL, exchange route) are read
 * at runtime from bid_config inside the bidder-injected accept.json, so nothing
 * about the winning provider has to be known when this cloud-config is built --
 * which happens before the RFP is even sent. The guest echoes back the subject
 * from its own provisioning token rather than re-rendering a template, so the
 * exchanged token carries exactly the identity the provider assigned it.
 *
 * Fails loud: the unit does not swallow errors, so units ordered After= it do not
 * start with missing secrets.
 */
const secretsModule: UserDataModule = (ctx) => {
  const acceptPath = ctx.secretsAcceptPath ?? "/root/secrets/publicdomainrelay.com/market/accept.json";
  return {
    packages: ["jq", "curl"],
    write_files: [
      {
        path: "/usr/local/bin/setup-secrets.sh",
        owner: "root:root",
        permissions: "0700",
        content: `#!/usr/bin/env bash
set -euo pipefail

STAMP=/var/lib/setup-secrets.done
[ -f "\${STAMP}" ] && exit 0

ACCEPT_JSON="${acceptPath}"
SECRETS_URL="${ctx.secretsUrl ?? ""}"
SECRETS_ROUTE="${ctx.secretsRoute ?? ""}"
SECRETS_AUD="${ctx.secretsAud ?? ""}"

if [ -z "\${SECRETS_URL}" ] || [ -z "\${SECRETS_AUD}" ]; then
  echo "secrets module misconfigured: missing url or aud" >&2
  exit 1
fi

for _ in \$(seq 1 60); do
  [ -f "\${ACCEPT_JSON}" ] && break
  sleep 2
done
[ -f "\${ACCEPT_JSON}" ] || { echo "accept.json never appeared at \${ACCEPT_JSON}" >&2; exit 1; }

# The bidder writes bid_config as a strongRef wrapper ({uri, cid, value}); older
# bidders inlined the record. Accept either.
WIF="\$(jq -c '.bid_config.value // .bid_config // {}' "\${ACCEPT_JSON}")"
TOKEN_PATH="\$(printf '%s' "\${WIF}" | jq -r '.token_path // empty')"
URL_PATH="\$(printf '%s' "\${WIF}" | jq -r '.url_path // empty')"
URL_ROUTE="\$(printf '%s' "\${WIF}" | jq -r '.url_route // "/v1/oidc/issue"')"
[ -n "\${TOKEN_PATH}" ] || { echo "accept.json has no bid_config token_path" >&2; exit 1; }
[ -n "\${URL_PATH}" ] || { echo "accept.json has no bid_config url_path" >&2; exit 1; }

for _ in \$(seq 1 60); do
  [ -s "\${TOKEN_PATH}" ] && [ -s "\${URL_PATH}" ] && break
  sleep 2
done
[ -s "\${TOKEN_PATH}" ] || { echo "workload identity token never appeared at \${TOKEN_PATH}" >&2; exit 1; }

WID_TOKEN="\$(cat "\${TOKEN_PATH}")"
ISSUER_BASE="\$(cat "\${URL_PATH}")"

# The provider assigned this subject at /v1/oidc/prove from the droplet tags.
# Echo it back verbatim so the exchanged token keeps the same identity.
SUBJECT="\$(printf '%s' "\${WID_TOKEN}" | cut -d. -f2 \\
  | tr '_-' '/+' | sed -e 's/\$/==/' | base64 -d 2>/dev/null | jq -r .sub)"
[ -n "\${SUBJECT}" ] && [ "\${SUBJECT}" != "null" ] || { echo "could not read sub from workload identity token" >&2; exit 1; }

EXCHANGED=""
for attempt in \$(seq 1 10); do
  EXCHANGED="\$(curl -sf \\
    -H "Authorization: Bearer \${WID_TOKEN}" \\
    -H "Content-Type: application/json" \\
    -d "\$(jq -nc --arg aud "\${SECRETS_AUD}" --arg sub "\${SUBJECT}" '{aud: \$aud, sub: \$sub, ttl: 300}')" \\
    "\${ISSUER_BASE}\${URL_ROUTE}" | jq -r '.token // empty')" || true
  [ -n "\${EXCHANGED}" ] && break
  echo "token exchange failed (attempt \${attempt}); retrying" >&2
  sleep 5
done
[ -n "\${EXCHANGED}" ] || { echo "token exchange failed against \${ISSUER_BASE}\${URL_ROUTE}" >&2; exit 1; }

BUNDLE=""
for attempt in \$(seq 1 10); do
  BUNDLE="\$(curl -sf -H "Authorization: Bearer \${EXCHANGED}" "\${SECRETS_URL}\${SECRETS_ROUTE}")" || true
  [ -n "\${BUNDLE}" ] && break
  echo "secrets fetch failed (attempt \${attempt}); retrying" >&2
  sleep 5
done
[ -n "\${BUNDLE}" ] || { echo "secrets fetch failed against \${SECRETS_URL}\${SECRETS_ROUTE}" >&2; exit 1; }

umask 077
COUNT="\$(printf '%s' "\${BUNDLE}" | jq 'length')"
i=0
while [ "\${i}" -lt "\${COUNT}" ]; do
  SECRET_PATH="\$(printf '%s' "\${BUNDLE}" | jq -r ".[\${i}].path")"
  install -d -m 0700 -o root -g root "\$(dirname "\${SECRET_PATH}")"
  printf '%s' "\${BUNDLE}" | jq -j ".[\${i}].value" > "\${SECRET_PATH}"
  chown root:root "\${SECRET_PATH}"
  chmod 0600 "\${SECRET_PATH}"
  i=\$((i + 1))
done

echo "wrote \${COUNT} secrets"
touch "\${STAMP}"
`,
      },
      {
        path: "/etc/systemd/system/setup-secrets.service",
        owner: "root:root",
        permissions: "0644",
        content: [
          "[Unit]",
          "Description=Fetch compute contract secrets via workload identity exchange",
          "After=network-online.target provisioning-token.service",
          "Wants=network-online.target",
          "ConditionPathExists=!/var/lib/setup-secrets.done",
          "",
          "[Service]",
          "Type=oneshot",
          "RemainAfterExit=yes",
          "User=root",
          "ExecStart=/usr/local/bin/setup-secrets.sh",
          "StandardOutput=journal",
          "StandardError=journal",
          "",
          "[Install]",
          "WantedBy=multi-user.target",
          "",
        ].join("\n"),
      },
    ],
    runcmd: [
      "systemctl daemon-reload",
      "systemctl enable setup-secrets.service",
      "systemctl start --no-block setup-secrets.service",
    ],
  };
};

registerUserDataModule("tunnel", tunnelModule);
registerUserDataModule("fedproxy-ssh", fedproxySshModule);
registerUserDataModule("fedproxy-web", fedproxyWebModule);
registerUserDataModule("wootty", woottyModule);
registerUserDataModule("secrets", secretsModule);

// ---------------------------------------------------------------------------
// Back-compat wrappers (deprecated)
// ---------------------------------------------------------------------------

/**
 * @deprecated Use buildUserData({ ctx, modules: ["fedproxy-ssh"] }). Kept for
 * one release so existing callers (and the hono-compute-provider integration
 * test) keep compiling. Fixes baked in: ListenAddress 127.0.0.1 and correct
 * ${STAMP} escaping (previously double-escaped to a literal `\${STAMP}`).
 */
export function buildDefaultUserData(ctx: CloudInitContext): string {
  return buildUserData({ ctx, modules: ["fedproxy-ssh"] });
}

/**
 * @deprecated Use buildUserData({ ctx, modules: ["tunnel"] }). Kept for one
 * release so existing callers keep compiling.
 */
export function buildTunnelUserData(ctx: TunnelCloudInitContext): string {
  return buildUserData({ ctx, modules: ["tunnel"] });
}

/** Inject JSR_URL into the tunnel-subscriber systemd unit in an existing cloud-init YAML. */
export function injectJsrUrl(userData: string, jsrUrl: string): string {
  return userData.replace(
    /(ExecStart=\S*deno run .*tunnel-subscriber)/,
    `Environment="JSR_URL=${jsrUrl}"\n      $1`,
  );
}
