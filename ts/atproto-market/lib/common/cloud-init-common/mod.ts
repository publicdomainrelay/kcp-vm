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

for _ in \$(seq 1 30); do
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

for _ in \$(seq 1 30); do
  [ -s "\${TOKEN_PATH}" ] && [ -s "\${URL_PATH}" ] && break
  sleep 2
done
[ -s "\${TOKEN_PATH}" ] || { echo "workload identity token never appeared at \${TOKEN_PATH}" >&2; exit 1; }

WID_TOKEN="\$(cat "\${TOKEN_PATH}")"
ISSUER_BASE="\$(cat "\${URL_PATH}")"

# The provider assigned this subject at /v1/oidc/prove from the droplet tags.
# Echo it back verbatim so the exchanged token keeps the same identity.
PAYLOAD="\$(printf '%s' "\${WID_TOKEN}" | cut -d. -f2 | tr '_-' '/+')"
case \$(( \${#PAYLOAD} % 4 )) in
  2) PAYLOAD="\${PAYLOAD}==" ;;
  3) PAYLOAD="\${PAYLOAD}=" ;;
esac
SUBJECT="\$(printf '%s' "\${PAYLOAD}" | base64 -d 2>/dev/null | jq -r .sub)" || SUBJECT=""
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

export const HOST_KEYS_REPORTER_SCRIPT = "/usr/local/bin/send-hostkeys.sh";
export const HOST_KEYS_REPORTER_UNIT = "guest-hostkeys.service";

export interface HostKeysReporterOptions {
  url: string;
  tokenPath: string;
  sshDir?: string;
  attempts?: number;
  retryDelaySec?: number;
}

export function hostKeysReporterScript(opts: HostKeysReporterOptions): string {
  const sshDir = opts.sshDir ?? "/etc/ssh";
  const attempts = opts.attempts ?? 12;
  const delay = opts.retryDelaySec ?? 5;
  return [
    "#!/usr/bin/env bash",
    "set -uo pipefail",
    `URL="${opts.url}"`,
    `TOKEN_PATH="${opts.tokenPath}"`,
    `SSH_DIR="${sshDir}"`,
    'OUT="${TMPDIR:-/tmp}/guest-hostkeys.out"',
    'ERR="${TMPDIR:-/tmp}/guest-hostkeys.err"',
    'ED25519="${SSH_DIR}/ssh_host_ed25519_key"',
    "for _ in $(seq 1 45); do",
    '  [ -f "${ED25519}" ] && [ -f "${ED25519}.pub" ] && [ -s "${TOKEN_PATH}" ] && break',
    "  sleep 2",
    "done",
    'if [ ! -f "${ED25519}" ]; then echo "guest-hostkeys: no ${ED25519}"; exit 1; fi',
    'if [ ! -s "${TOKEN_PATH}" ]; then echo "guest-hostkeys: no provisioning token at ${TOKEN_PATH}"; exit 1; fi',
    'TOKEN="$(cat "${TOKEN_PATH}")"',
    "KEYS='[]'",
    'for PUB in "${SSH_DIR}"/ssh_host_*_key.pub; do',
    '  [ -f "${PUB}" ] || continue',
    '  read -r TYPE BLOB _ < "${PUB}"',
    '  FP="$(ssh-keygen -l -E sha256 -f "${PUB}" | awk \'{print $2}\')"',
    '  KEYS="$(jq -c --arg t "${TYPE}" --arg k "${BLOB}" --arg f "${FP}" \'. + [{type: $t, publicKey: $k, fingerprint: $f}]\' <<<"${KEYS}")"',
    "done",
    'SIG="$(printf \'%s\' "${TOKEN}" | ssh-keygen -Y sign -n prove-sshd -f "${ED25519}")"',
    'if [ -z "${SIG}" ]; then echo "guest-hostkeys: signing the token failed"; exit 1; fi',
    'PAYLOAD="$(jq -nc --argjson keys "${KEYS}" --arg token "${TOKEN}" --arg sig "${SIG}" \'{keys: $keys, token: $token, signature: $sig}\')"',
    'echo "guest-hostkeys: reporting $(jq -r \'[.[] | .type + " " + .fingerprint] | join(", ")\' <<<"${KEYS}")"',
    `for _ in $(seq 1 ${attempts}); do`,
    '  CODE="$(curl -sS -m 10 -o "${OUT}" -w \'%{http_code}\' -X POST "${URL}" -H \'Content-Type: application/json\' -d "${PAYLOAD}" 2>>"${ERR}" || echo 000)"',
    '  echo "guest-hostkeys: http ${CODE} ${URL}"',
    '  if [ "${CODE}" = "200" ]; then echo "guest-hostkeys: reported"; exit 0; fi',
    '  case "${CODE}" in 400|403|409) echo "guest-hostkeys: refused: $(cat "${OUT}" 2>/dev/null)"; exit 1 ;; esac',
    `  sleep ${delay}`,
    "done",
    'echo "guest-hostkeys: gave up; last response: $(cat "${OUT}" 2>/dev/null)"',
    "exit 1",
    "",
  ].join("\n");
}

export function hostKeysReporterModule(opts: HostKeysReporterOptions): UserDataModule {
  const unit = [
    "[Unit]",
    "Description=Report this guest's SSH host keys to its bidder",
    "After=network-online.target ssh.service",
    "Wants=network-online.target",
    "",
    "[Service]",
    "Type=oneshot",
    "RemainAfterExit=yes",
    "TimeoutStartSec=180",
    `ExecStart=${HOST_KEYS_REPORTER_SCRIPT}`,
    "StandardOutput=journal+console",
    "StandardError=journal+console",
    "",
    "[Install]",
    "WantedBy=multi-user.target",
    "",
  ].join("\n");
  return () => ({
    write_files: [
      {
        path: HOST_KEYS_REPORTER_SCRIPT,
        owner: "root:root",
        permissions: "0700",
        content: hostKeysReporterScript(opts),
      },
      { path: `/etc/systemd/system/${HOST_KEYS_REPORTER_UNIT}`, owner: "root:root", permissions: "0644", content: unit },
    ],
    runcmdPrepend: [
      "systemctl daemon-reload",
      `systemctl enable ${HOST_KEYS_REPORTER_UNIT}`,
      `systemctl start --no-block ${HOST_KEYS_REPORTER_UNIT}`,
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
