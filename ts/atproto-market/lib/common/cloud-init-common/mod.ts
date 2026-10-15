// Pure cloud-init YAML generation helpers. Zero I/O.
//
// One composer (buildUserData) + a module registry. Every guest personality is
// a UserDataModule producing a UserDataPatch that is merged onto an optional
// caller-supplied base cloud-config. Modules are pure: parse/merge/stringify
// only, no I/O, no crypto, no Deno.

import { parse as yamlParse, stringify as yamlStringify } from "yaml";

/**
 * Context for the built-in modules. Modules read the fields they need;
 * undefined fields render as empty.
 */
export interface CloudInitContext {
  /** VM name / RBAC role. */
  vmName: string;
  /** OpenSSH public key (single line) added to root's authorized_keys. */
  sshAuthorizedKey?: string;

  /** Local TCP port the iroh listener forwards to. Default 22. */
  targetPort?: number;
  /** Extra `/etc/hosts` entries ("<ip> <name>") so the guest can dial the requester and bidder by name. */
  hostAliases?: string[];

  // secrets
  /** Base URL of the requester's ephemeral secrets server (its relay ingress URL). */
  secretsUrl?: string;
  /** Route on secretsUrl serving the [{path,value}] bundle. */
  secretsRoute?: string;
  /** Audience the guest must request when exchanging its workload identity token. */
  secretsAud?: string;
  /** Guest path of the bidder-injected accept.json carrying bid_config. */
  secretsAcceptPath?: string;

  // resolver
  /** Provider's forwarded resolver gateway; first nameserver in /etc/resolv.conf. */
  dnsGateway?: string;
  /** Public resolvers appended after dnsGateway so one gateway hiccup cannot brick the guest. */
  dnsFallbacks?: string[];

  // iroh transport
  /** Pinned dumbpipe release the iroh module installs. Default 0.39.0. */
  dumbpipeVersion?: string;
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
  package_update?: boolean;
  users?: Record<string, unknown>[];
  write_files?: WriteFileEntry[];
  runcmd?: unknown[];
  /** Inserted BEFORE the base runcmd (preserves injectAcceptBundle / provisioning unshift order). */
  runcmdPrepend?: unknown[];
  bootcmd?: unknown[];
  ca_certs?: { trusted: string[] };
  disable_root?: boolean;
  ssh_pwauth?: boolean;
}

export type UserDataModule = (ctx: Partial<CloudInitContext>) => UserDataPatch;

export const RESOLV_CONF_PATH = "/etc/resolv.conf";

/** The pasta gateway every firecracker guest's provider forwards DNS through. */
export const DEFAULT_DNS_GATEWAY = "172.30.0.1";

/** Public resolvers so one gateway hiccup cannot strand a guest mid-provision. */
export const DEFAULT_DNS_FALLBACKS = ["1.1.1.1", "9.9.9.9"];

/** Render an /etc/resolv.conf naming each server once, in the given order. */
export function renderResolver(nameservers: string[]): string {
  const unique = [...new Set(nameservers.filter((n) => n.length > 0))];
  return `${unique.map((n) => `nameserver ${n}`).join("\n")}\noptions timeout:2 attempts:3\n`;
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

const registry = new Map<string, UserDataModule>();

/** Register (or replace) a user-data module by id. */
export function registerUserDataModule(id: string, m: UserDataModule): void {
  registry.set(id, m);
}

export const GUEST_TRANSPORT_MODULE = "iroh";

export const REMOVED_TRANSPORT_MODULES = ["tunnel", "fedproxy-ssh", "fedproxy-web", "wootty"];

export class LegacyTransportError extends Error {
  constructor(readonly transport: string) {
    super(
      `guest transport "${transport}" was removed (legacy-transport): ` +
        `${GUEST_TRANSPORT_MODULE} is the only guest transport`,
    );
  }
}

export class RemovedTransportFlagError extends Error {
  constructor(readonly flag: string) {
    super(`--${flag} was removed (legacy-transport): ${GUEST_TRANSPORT_MODULE} is the only guest transport`);
  }
}

export function removedTransportFlagsIn(
  options: Record<string, unknown>,
  flags: string[],
): RemovedTransportFlagError[] {
  return flags
    .filter((flag) => options[flag.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase())] !== undefined)
    .map((flag) => new RemovedTransportFlagError(flag));
}

/** Resolve a list of ids/module functions to functions; throws on unknown id. */
export function getUserDataModules(ids: Array<string | UserDataModule>): UserDataModule[] {
  return ids.map((idOrFn) => {
    if (typeof idOrFn === "function") return idOrFn;
    if (REMOVED_TRANSPORT_MODULES.includes(idOrFn)) throw new LegacyTransportError(idOrFn);
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
  if (p.package_update !== undefined) out.package_update = p.package_update;
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
  if (p.ca_certs) {
    const have = asArray(asObj(out.ca_certs).trusted);
    out.ca_certs = { ...asObj(out.ca_certs), trusted: [...new Set([...have, ...p.ca_certs.trusted])] };
  }
  if (p.runcmd !== undefined || p.runcmdPrepend !== undefined) {
    out.runcmd = [...(p.runcmdPrepend ?? []), ...asArray(out.runcmd), ...(p.runcmd ?? [])];
  }
  if (p.disable_root !== undefined) out.disable_root = p.disable_root;
  if (p.ssh_pwauth !== undefined) out.ssh_pwauth = p.ssh_pwauth;
  return out;
}

const PACKAGE_NAME = /^[a-z0-9][a-z0-9+.-]*$/;

/**
 * A first-runcmd step that installs only the packages the boot is missing.
 * The node image carries the market's packages already, so on a market node
 * every `dpkg -s` succeeds and no apt runs at all; on a guest nothing was
 * baked for (the container and droplet providers), the same step apt-get
 * updates and installs exactly what is missing.
 */
export function packagesGuardCommand(packages: string[]): string {
  const names = [...new Set(packages)];
  for (const name of names) {
    if (!PACKAGE_NAME.test(name)) {
      throw new Error(
        `the composed user_data asks for the package ${JSON.stringify(name)}, which is not a package ` +
          `name this will put in a shell command. The step it would go into is what installs a ` +
          `guest's packages at boot, so a name that is not one is a step that either installs ` +
          `something else or fails and leaves the guest without the package a unit of its waits on.`,
      );
    }
  }
  const list = names.map((name) => `"${name}"`).join(" ");
  return `sh -c 'missing=""; for p in ${list}; do dpkg -s "$p" >/dev/null 2>&1 || ` +
    `missing="$missing $p"; done; [ -z "$missing" ] || { apt-get update && ` +
    `DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ` +
    `-o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef $missing; }'`;
}

/**
 * Take the packages the modules asked for out of cloud-init's own apt module
 * and put them behind the guard as the document's first runcmd entry:
 * cloud-init's module runs `apt-get update` on every boot whatever the image
 * already carries, and the guard does not.
 */
function guardPackages(base: Record<string, unknown>): Record<string, unknown> {
  const requested = asArray(base.packages).filter((p): p is string => typeof p === "string");
  if (requested.length === 0) return base;
  const out = { ...base };
  delete out.packages;
  out.package_update = false;
  out.runcmd = [packagesGuardCommand(requested), ...asArray(out.runcmd)];
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

function nameserversIn(content: string): string[] {
  const out: string[] = [];
  for (const line of content.split("\n")) {
    const m = /^\s*nameserver\s+(\S+)/.exec(line);
    if (m) out.push(m[1]);
  }
  return out;
}

function resolvWriteCommand(resolver: string): string {
  return `printf '%s' '${resolver.replace(/'/g, `'\\''`)}' > ${RESOLV_CONF_PATH}`;
}

function trailingOnce(arr: unknown[], item: string): unknown[] {
  return [...arr.filter((x) => x !== item), item];
}

/**
 * Force the composed document to end with a resolver that names the gateway
 * first and the fallbacks behind it, whatever earlier writers put at
 * /etc/resolv.conf: merge any nameservers a caller wrote (later write_files
 * entries win on path), then re-assert the file in bootcmd and, last of all, in
 * runcmd so a later cloud-init stage cannot take it away.
 */
function assertResolver(
  base: Record<string, unknown>,
  gateway: string,
  fallbacks: string[],
): Record<string, unknown> {
  const out = { ...base };
  const files = asArray(out.write_files) as WriteFileEntry[];
  const idx = files.findIndex((f) => f && (f as WriteFileEntry).path === RESOLV_CONF_PATH);
  const existing = idx >= 0 ? String(files[idx].content ?? "") : "";
  const extras = nameserversIn(existing).filter((n) => n !== gateway && !fallbacks.includes(n));
  const resolver = renderResolver([gateway, ...fallbacks, ...extras]);
  const entry: WriteFileEntry = {
    path: RESOLV_CONF_PATH,
    owner: "root:root",
    permissions: "0644",
    content: resolver,
  };
  if (idx >= 0) files[idx] = { ...files[idx], ...entry };
  else files.push(entry);
  out.write_files = files;

  const command = resolvWriteCommand(resolver);
  out.bootcmd = trailingOnce(asArray(out.bootcmd), command);
  out.runcmd = trailingOnce(asArray(out.runcmd), command);
  return out;
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
  dnsGateway?: string;
  dnsFallbacks?: string[];
}): string {
  const ctx = opts.ctx ?? {};
  const gateway = opts.dnsGateway ?? ctx.dnsGateway ?? DEFAULT_DNS_GATEWAY;
  const fallbacks = opts.dnsFallbacks ?? ctx.dnsFallbacks ?? DEFAULT_DNS_FALLBACKS;
  let obj = parseBase(opts.base);
  for (const m of getUserDataModules(opts.modules)) {
    obj = applyPatch(obj, m(ctx));
  }
  if (opts.overrides) obj = applyPatch(obj, opts.overrides);
  obj = guardPackages(obj);
  obj = assertResolver(obj, gateway, fallbacks);
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


const secretsModule: UserDataModule = (ctx) => {
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

SECRETS_URL="${ctx.secretsUrl ?? ""}"
SECRETS_ROUTE="${ctx.secretsRoute ?? ""}"
SECRETS_AUD="${ctx.secretsAud ?? ""}"
GUEST_LOGIN="${GUEST_LOGIN_SCRIPT}"

if [ -z "\${SECRETS_URL}" ] || [ -z "\${SECRETS_AUD}" ]; then
  echo "secrets module misconfigured: missing url or aud" >&2
  exit 1
fi

for _ in \$(seq 1 60); do
  [ -x "\${GUEST_LOGIN}" ] && break
  sleep 2
done
[ -x "\${GUEST_LOGIN}" ] || { echo "the guest login helper \${GUEST_LOGIN} never appeared" >&2; exit 1; }

EXCHANGED=""
for attempt in \$(seq 1 10); do
  EXCHANGED="\$("\${GUEST_LOGIN}" token "\${SECRETS_AUD}")" || true
  [ -n "\${EXCHANGED}" ] && break
  echo "workload identity token exchange failed (attempt \${attempt}); retrying" >&2
  sleep 5
done
[ -n "\${EXCHANGED}" ] || { echo "no workload identity token from the bidder's OpenBao for \${SECRETS_AUD}" >&2; exit 1; }

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
          "After=network-online.target guest-login.service",
          "Wants=network-online.target guest-login.service",
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
    runcmdPrepend: [
      "systemctl daemon-reload",
      "systemctl enable setup-secrets.service",
      "systemctl start --no-block setup-secrets.service",
    ],
  };
};

/** Pinned k3s release. Measured against `lima/k3s-bench.yaml` and `docs/kcp-notes.md`. */
const K3S_VERSION = "v1.36.4+k3s1";

/** Deterministic write_files path: the discriminator the provider spec needs, since the composer emits no marker file. */
const K3S_SETUP_PATH = "/usr/local/bin/setup-k3s.sh";

/**
 * k3s -- installs the k3s binary at a pinned release so a market-side worker can
 * join. It does NOT start anything: the agent is the SSH connection's command
 * (ADR 0003) and `k3s server` runs gateway-side (ADR 0012), so the installer runs
 * with SKIP_START and SKIP_ENABLE and this module writes no systemd unit.
 *
 * It carries neither the instance token nor the node name. Both travel in the
 * command (ADR 0016 scopes the token per cluster INSTANCE; ADR 0028 forbids a
 * report surface that would carry it), and cloud-init user_data is readable from
 * inside the guest via the metadata service -- so the token must not be here.
 */
const k3sModule: UserDataModule = () => ({
  write_files: [
    {
      path: K3S_SETUP_PATH,
      owner: "root:root",
      permissions: "0755",
      content: `#!/bin/bash
set -eux -o pipefail

STAMP=/var/lib/setup-k3s.done
[ -f "\${STAMP}" ] && exit 0

if [ -x /usr/local/bin/k3s ] && /usr/local/bin/k3s --version 2>/dev/null | grep -qF '${K3S_VERSION}'; then
  /usr/local/bin/k3s --version
  touch "\${STAMP}"
  exit 0
fi

command -v curl >/dev/null || { apt-get update && apt-get install -y curl ca-certificates; }

curl -sfL https://get.k3s.io -o /tmp/k3s-install.sh
INSTALL_K3S_VERSION=${K3S_VERSION} \\
  INSTALL_K3S_SKIP_START=true \\
  INSTALL_K3S_SKIP_ENABLE=true \\
  sh /tmp/k3s-install.sh

/usr/local/bin/k3s --version

touch "\${STAMP}"
`,
    },
  ],
  runcmd: [K3S_SETUP_PATH],
});

/** Pinned dumbpipe release installed by the iroh transport module. */
export const DUMBPIPE_VERSION_DEFAULT = "0.39.0";

/**
 * iroh — dumbpipe listener in front of the guest's own sshd. The guest listens
 * for iroh (outbound dials only; nothing here opens an inbound port) and the
 * wrapper forwards every bidi stream to 127.0.0.1:<targetPort>. Requires
 * ctx.sshAuthorizedKey; optional ctx.targetPort (default 22), ctx.hostAliases,
 * ctx.dumbpipeVersion (default 0.39.0).
 *
 * The ticket is minted by the guest, not read off it by the host: the wrapper
 * captures the `dumbpipe connect-tcp <ticket>` startup line from stderr, writes
 * the ticket to /run/guest-iroh-ticket and, as `iroh://<ticket>`, to
 * /run/guest-fqdn -- the rendezvous file the on-network reporter every transport
 * already uses publishes to the market, so nothing about how a guest announces
 * itself changes with the transport. IROH_SECRET is generated once and persisted
 * root-only so the endpoint id survives restarts.
 */
const irohModule: UserDataModule = (ctx) => {
  const targetPort = ctx.targetPort ?? 22;
  const dumbpipeVersion = ctx.dumbpipeVersion ?? DUMBPIPE_VERSION_DEFAULT;
  const aliases = (ctx.hostAliases ?? []).filter((a) => /^[\w.:-]+\s+[\w.-]+$/.test(a));

  const writeFiles: WriteFileEntry[] = [
    {
      path: "/root/.ssh/authorized_keys",
      owner: "root:root",
      permissions: "0600",
      content: `${ctx.sshAuthorizedKey ?? ""}\n`,
    },
    {
      path: "/etc/ssh/sshd_config.d/10-iroh.conf",
      owner: "root:root",
      permissions: "0644",
      content: [
        "# Key-only root login; reached over the guest's own iroh endpoint (the",
        "# compute-provider harness also TCP-probes :22 directly for readiness).",
        "PermitRootLogin prohibit-password",
        "PasswordAuthentication no",
      ].join("\n") + "\n",
    },
    {
      path: "/usr/local/bin/setup-iroh.sh",
      owner: "root:root",
      permissions: "0755",
      content: `#!/bin/bash
set -euo pipefail

STAMP=/var/lib/setup-iroh.done
[ -f "\${STAMP}" ] && exit 0

DUMBPIPE_VERSION="${dumbpipeVersion}"

if [ -x /usr/local/bin/dumbpipe ] && [ "$(cat /usr/local/bin/dumbpipe.version 2>/dev/null)" = "\${DUMBPIPE_VERSION}" ]; then
  touch "\${STAMP}"
  exit 0
fi

# dumbpipe ships linux tarballs for x86_64 and aarch64 only.
case "$(uname -m)" in
  x86_64|amd64) _arch=x86_64 ;;
  aarch64|arm64) _arch=aarch64 ;;
  *) echo "unsupported machine type for dumbpipe: $(uname -m)" >&2; exit 1 ;;
esac

URL="https://github.com/n0-computer/dumbpipe/releases/download/v\${DUMBPIPE_VERSION}/dumbpipe-v\${DUMBPIPE_VERSION}-linux-\${_arch}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "\${TMP}"' EXIT

# Bounded retry: the archive carries only ./dumbpipe, so the whole archive is
# extracted into TMP and the ./dumbpipe it produced is moved into place.
for attempt in $(seq 1 5); do
  if curl -fsSL "\${URL}" -o "\${TMP}/dumbpipe.tar.gz" \\
      && tar -xzf "\${TMP}/dumbpipe.tar.gz" -C "\${TMP}"; then
    install -m 0755 "\${TMP}/dumbpipe" /usr/local/bin/dumbpipe
    touch "\${STAMP}"
    exit 0
  fi
  echo "dumbpipe install failed (attempt \${attempt}): \${URL}" >&2
  sleep 5
done

echo "dumbpipe install failed after 5 attempts: \${URL}" >&2
exit 1
`,
    },
    {
      path: "/usr/local/bin/iroh-listener.sh",
      owner: "root:root",
      permissions: "0755",
      content: `#!/bin/bash
set -euo pipefail

# One persistent endpoint secret: regenerating it would change the endpoint id
# and therefore the ticket on every restart.
SECRET_FILE=/root/.iroh-secret
if [ ! -s "\${SECRET_FILE}" ]; then
  umask 077
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \\n' > "\${SECRET_FILE}"
  chmod 0600 "\${SECRET_FILE}"
fi
IROH_SECRET="$(cat "\${SECRET_FILE}")"
export IROH_SECRET

TICKET_FILE=/run/guest-iroh-ticket
LOG=/run/guest-iroh.log
rm -f "\${TICKET_FILE}" "\${LOG}"

# No --verbose: it appends a second, short-ticket line. The last connect-tcp
# line's final field is the full ticket.
/usr/local/bin/dumbpipe listen-tcp --host 127.0.0.1:${targetPort} 2>>"\${LOG}" &
DUMBPIPE_PID=$!

TICKET=""
for _ in $(seq 1 150); do
  if [ -s "\${LOG}" ]; then
    TICKET="$(grep -a 'connect-tcp' "\${LOG}" | tail -n 1 | awk '{print $NF}')"
  fi
  if [ -n "\${TICKET}" ]; then
    break
  fi
  if ! kill -0 "\${DUMBPIPE_PID}" 2>/dev/null; then
    break
  fi
  sleep 1
done

if [ -n "\${TICKET}" ]; then
  printf 'iroh://%s\\n' "\${TICKET}" > "\${TICKET_FILE}"
  chmod 0600 "\${TICKET_FILE}"
  printf 'iroh://%s\\n' "\${TICKET}" > /run/guest-fqdn.tmp
  mv -f /run/guest-fqdn.tmp /run/guest-fqdn
fi

wait "\${DUMBPIPE_PID}"
`,
    },
    {
      path: "/etc/systemd/system/iroh.service",
      owner: "root:root",
      permissions: "0644",
      content: [
        "[Unit]",
        "Description=iroh dumbpipe listener in front of local sshd",
        "After=network-online.target",
        "Wants=network-online.target",
        "",
        "[Service]",
        "Type=simple",
        "User=root",
        "ExecStart=/usr/local/bin/iroh-listener.sh",
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
  ];

  return {
    apt: { preserve_sources_list: true },
    packages: ["openssh-server"],
    disable_root: false,
    ssh_pwauth: false,
    bootcmd: aliases.map((a) =>
      ["sh", "-c", `grep -qxF '${a}' /etc/hosts || echo '${a}' >> /etc/hosts`]
    ),
    write_files: writeFiles,
    runcmd: [
      "/usr/local/bin/setup-iroh.sh",
      "systemctl daemon-reload",
      "systemctl enable iroh.service",
      "systemctl start --no-block iroh.service",
      "systemctl enable --now --no-block ssh || systemctl enable --now --no-block sshd",
    ],
  };
};

export const GUEST_LOGIN_SCRIPT = "/usr/local/bin/guest-login.sh";
export const GUEST_LOGIN_UNIT = "guest-login.service";
export const GUEST_LOGIN_TOKEN_PATH = "/root/secrets/openbao/token";
export const GUEST_LOGIN_NAMESPACE = "prove-sshd";
export const GUEST_LOGIN_DEFAULT_MOUNT = "sshsig";

export interface GuestLoginOptions {
  url: string;
  role: string;
  bindSecret: string;
  mount?: string;
  sshDir?: string;
  tokenPath?: string;
  attempts?: number;
  retryDelaySec?: number;
}

export function guestLoginScript(opts: GuestLoginOptions): string {
  const sshDir = opts.sshDir ?? "/etc/ssh";
  const tokenPath = opts.tokenPath ?? GUEST_LOGIN_TOKEN_PATH;
  const attempts = opts.attempts ?? 30;
  const delay = opts.retryDelaySec ?? 4;
  const mount = opts.mount ?? GUEST_LOGIN_DEFAULT_MOUNT;
  return `#!/usr/bin/env bash
set -uo pipefail
URL="${opts.url}"
ROLE="${opts.role}"
MOUNT="${mount}"
BIND_SECRET="${opts.bindSecret}"
TOKEN_PATH="${tokenPath}"
ED25519="${sshDir}/ssh_host_ed25519_key"
OUT="\$(mktemp)"
trap 'rm -f "\${OUT}"' EXIT
wait_key() {
  for _ in \$(seq 1 150); do
    [ -f "\${ED25519}" ] && [ -f "\${ED25519}.pub" ] && return 0
    sleep 2
  done
  echo "guest-login: no \${ED25519}" >&2
  return 1
}
post() {
  local step="\$1" body="\$2" code
  for _ in \$(seq 1 ${attempts}); do
    code="\$(curl -sS -m 20 -o "\${OUT}" -w '%{http_code}' -X POST "\${URL}/auth/\${MOUNT}/\${step}" -H 'Content-Type: application/json' -d "\${body}" 2>/dev/null || echo 000)"
    case "\${code}" in
      200|204) return 0 ;;
      4??) echo "guest-login: \${step} refused (\${code}): \$(cat "\${OUT}" 2>/dev/null)" >&2; return 1 ;;
    esac
    echo "guest-login: \${step} http \${code}, retrying" >&2
    sleep ${delay}
  done
  echo "guest-login: \${step} gave up" >&2
  return 1
}
sign() {
  printf '%s' "\$1" | ssh-keygen -Y sign -n ${GUEST_LOGIN_NAMESPACE} -f "\${ED25519}"
}
bind() {
  local host_key sig
  host_key="\$(awk '{print \$1" "\$2}' "\${ED25519}.pub")"
  sig="\$(sign "\${BIND_SECRET}")"
  [ -n "\${sig}" ] || { echo "guest-login: signing the bind secret failed" >&2; return 1; }
  post bind "\$(jq -nc --arg r "\${ROLE}" --arg s "\${BIND_SECRET}" --arg k "\${host_key}" --arg g "\${sig}" '{role: \$r, bind_secret: \$s, host_key: \$k, signature: \$g}')" || return 1
  echo "guest-login: bound \$(jq -r '.data.host_key_fingerprint // empty' "\${OUT}") to role \${ROLE}" >&2
}
login() {
  local challenge sig token
  post challenge "\$(jq -nc --arg r "\${ROLE}" '{role: \$r}')" || return 1
  challenge="\$(jq -r '.data.challenge // empty' "\${OUT}")"
  [ -n "\${challenge}" ] || { echo "guest-login: no challenge in \$(cat "\${OUT}")" >&2; return 1; }
  sig="\$(sign "\${challenge}")"
  post login "\$(jq -nc --arg r "\${ROLE}" --arg c "\${challenge}" --arg g "\${sig}" '{role: \$r, challenge: \$c, signature: \$g}')" || return 1
  token="\$(jq -r '.auth.client_token // empty' "\${OUT}")"
  [ -n "\${token}" ] || { echo "guest-login: no token in the login reply" >&2; return 1; }
  install -d -m 0700 "\$(dirname "\${TOKEN_PATH}")"
  ( umask 077; printf '%s\\n' "\${token}" > "\${TOKEN_PATH}" )
  echo "guest-login: logged in to the bidder's OpenBao as \$(jq -r '.auth.metadata.role // empty' "\${OUT}"), policies \$(jq -c '.auth.policies' "\${OUT}"), ttl \$(jq -r '.auth.lease_duration' "\${OUT}")s" >&2
}
role_for() {
  printf 'guest-%s-%s' "\${ROLE}" "\$(printf '%s' "\$1" | sha256sum | cut -c1-16)"
}
token() {
  local name code
  name="\$(role_for "\$1")"
  [ -s "\${TOKEN_PATH}" ] || login || return 1
  for _ in \$(seq 1 ${attempts}); do
    code="\$(curl -sS -m 20 -o "\${OUT}" -w '%{http_code}' -H "X-Vault-Token: \$(cat "\${TOKEN_PATH}")" "\${URL}/identity/oidc/token/\${name}" 2>/dev/null || echo 000)"
    case "\${code}" in
      200) jq -r '.data.token // empty' "\${OUT}"; return 0 ;;
      403) login || sleep ${delay} ;;
      *) sleep ${delay} ;;
    esac
  done
  echo "guest-login: no token for \$1 from the bidder's OpenBao (last http \${code})" >&2
  return 1
}
case "\${1:-}" in
  "") wait_key && { bind || echo "guest-login: the bind was refused; logging in with the key already bound" >&2; login; } ;;
  login) wait_key && login ;;
  token) wait_key && token "\${2:?an audience}" ;;
  *) echo "usage: guest-login.sh [login|token <audience>]" >&2; exit 2 ;;
esac
`;
}

export function guestLoginModule(opts: GuestLoginOptions): UserDataModule {
  const unit = [
    "[Unit]",
    "Description=Log this guest in to its bidder's OpenBao by its ssh host key",
    "After=network-online.target ssh.service",
    "Wants=network-online.target",
    "",
    "[Service]",
    "Type=oneshot",
    "RemainAfterExit=yes",
    "TimeoutStartSec=600",
    `ExecStart=${GUEST_LOGIN_SCRIPT}`,
    "StandardOutput=journal+console",
    "StandardError=journal+console",
    "",
    "[Install]",
    "WantedBy=multi-user.target",
    "",
  ].join("\n");
  return () => ({
    write_files: [
      { path: GUEST_LOGIN_SCRIPT, owner: "root:root", permissions: "0700", content: guestLoginScript(opts) },
      { path: `/etc/systemd/system/${GUEST_LOGIN_UNIT}`, owner: "root:root", permissions: "0644", content: unit },
    ],
    runcmdPrepend: [
      "systemctl daemon-reload",
      `systemctl enable ${GUEST_LOGIN_UNIT}`,
      `systemctl start --no-block ${GUEST_LOGIN_UNIT}`,
    ],
  });
}

export const POWEROFF_ENDS_VM_MODULE = "poweroff-ends-vm";
export const POWEROFF_ENDS_VM_PATH = "/usr/lib/systemd/system-shutdown/socialweb-poweroff-ends-vm.shutdown";

export const poweroffEndsVmModule: UserDataModule = () => ({
  write_files: [{
    path: POWEROFF_ENDS_VM_PATH,
    owner: "root:root",
    permissions: "0755",
    content: "#!/bin/sh\n" +
      "case \"$1\" in poweroff|halt) ;; *) exit 0 ;; esac\n" +
      "[ -e /sys/class/dmi/id/sys_vendor ] && exit 0\n" +
      "echo b > /proc/sysrq-trigger\n",
  }],
});

registerUserDataModule("secrets", secretsModule);
registerUserDataModule("k3s", k3sModule);
registerUserDataModule("iroh", irohModule);
registerUserDataModule(POWEROFF_ENDS_VM_MODULE, poweroffEndsVmModule);
