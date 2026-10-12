// kcpdns preload. The provider passes this with --preload, so it runs before the
// workload's main module in the same process and the patches below are in effect
// for the workload's own calls.
//
// Deno resolves fetch, WebSocket and Deno.connect in Rust, so none of them
// consult Deno.resolveDns and patching that alone would do nothing; each entry
// point is patched separately. Deno.resolveDns is patched too, so a workload
// that asks it directly agrees with the same table.
//
// Known ceiling: anything that bypasses these entry points, such as a native
// library or a subprocess the workload spawns, does not see virtual DNS.

type Table = Record<string, string>;

const DOMAIN: string = (Deno.env.get("KCP_SERVICE_DOMAIN") ?? "kcp.local");
const SUFFIX = ".svc." + DOMAIN;
const TABLE: Table = JSON.parse(Deno.env.get("KCP_DNS_TABLE") ?? "{}");
const TABLE_FILE: string = (() => {
  try {
    return Deno.env.get("KCP_DNS_TABLE_FILE") ?? "";
  } catch {
    return "";
  }
})();
const TOKENS: Record<string, string> = JSON.parse(Deno.env.get("KCP_TOKENS") ?? "{}");
const SERVER: string = Deno.env.get("KCP_SERVER") ?? "";

// The provider rewrites the table file whenever a workload reports the port it
// bound, so a workload started before its peers, or a peer that restarted on a
// new port, is found on the next lookup. The file is read again only when its
// modification time moves; a file this process may not read leaves the table
// it started with in place.
let tableFileStamp = -1;
function refresh(): void {
  if (!TABLE_FILE) return;
  try {
    const stamp = Deno.statSync(TABLE_FILE).mtime?.getTime() ?? 0;
    if (stamp === tableFileStamp) return;
    const next: Table = JSON.parse(Deno.readTextFileSync(TABLE_FILE));
    tableFileStamp = stamp;
    Object.assign(TABLE, next);
  } catch {
    return;
  }
}

// alice -> root:alice, prod.acme -> root:acme:prod. The inverse of the labelling
// the provider applies when it builds the table.
function labelsToCluster(labels: string): string {
  const parts = labels.split(".").filter((p) => p.length > 0).reverse();
  return ["root", ...parts].join(":");
}

const LISTENER_PREFIX = "_";
const UNIX_PREFIX = "unix:";

type Parts = { listener: string; name: string; namespace: string; cluster: string };

function split(host: string): Parts | null {
  if (!host.endsWith(SUFFIX)) return null;
  let bits = host.slice(0, -SUFFIX.length).split(".");
  let listener = "";
  if (bits[0]?.startsWith(LISTENER_PREFIX)) {
    listener = bits[0].slice(LISTENER_PREFIX.length);
    bits = bits.slice(1);
  }
  if (bits.length < 2) return null;
  return { listener, name: bits[0], namespace: bits[1], cluster: labelsToCluster(bits.slice(2).join(".")) };
}

type Target =
  | { kind: "tcp"; host: string; port: string }
  | { kind: "unix"; path: string }
  | { kind: "relay"; url: URL };

function parseTarget(value: string): Target | null {
  if (value.startsWith(UNIX_PREFIX)) return { kind: "unix", path: value.slice(UNIX_PREFIX.length) };
  if (value.includes("://")) {
    try {
      return { kind: "relay", url: new URL(value) };
    } catch {
      return null;
    }
  }
  const colon = value.lastIndexOf(":");
  if (colon < 0) return null;
  let host = value.slice(0, colon);
  if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
  return { kind: "tcp", host, port: value.slice(colon + 1) };
}

type Reported = { name?: unknown; kind?: unknown; address?: unknown };

function listenerValue(l: Reported): string {
  if (typeof l.address !== "string" || !l.address) return "";
  return l.kind === "unix" ? UNIX_PREFIX + l.address : l.address;
}

function addressOf(env: Record<string, string>): string | null {
  let args: string[] = [];
  try {
    args = JSON.parse(env.SERVICE_ARGS ?? "[]");
  } catch {
    return null;
  }
  let serviceEnv: Record<string, string> = {};
  try {
    serviceEnv = JSON.parse(env.SERVICE_ENV ?? "{}");
  } catch {
    return null;
  }
  const flag = (f: string) => {
    const i = args.indexOf(f);
    return i >= 0 ? args[i + 1] : undefined;
  };
  const port = flag("--port") ?? serviceEnv.PORT;
  const bind = flag("--hostname") ?? serviceEnv.HOSTNAME ?? "127.0.0.1";
  if (!port || port === "0") return null;
  return (bind === "0.0.0.0" || bind === "::" ? "127.0.0.1" : bind) + ":" + port;
}

function reportedValue(pod: { spec?: { env?: Record<string, string> }; status?: { address?: unknown; listeners?: unknown } }, listener: string): string | null {
  const listeners: Reported[] = Array.isArray(pod?.status?.listeners) ? pod.status.listeners as Reported[] : [];
  if (listener) {
    const found = listeners.find((l) => l?.name === listener);
    return found ? listenerValue(found) || null : null;
  }
  const reported = typeof pod?.status?.address === "string" ? pod.status.address : "";
  if (reported) return reported;
  const first = listeners.map(listenerValue).find((v) => v);
  return first || addressOf(pod?.spec?.env ?? {});
}

// A miss falls back to the kcp API, using a token minted in the target
// workspace: a token from another workspace is not authorised there, which is
// why the provider injects one per workspace rather than a single token.
async function discover(host: string): Promise<string | null> {
  const parts = split(host);
  if (!parts || !SERVER) return null;
  const token = TOKENS[parts.cluster];
  if (!token) return null;
  const current = SERVER.match(/\/clusters\/([^/]+)/)?.[1];
  const base = current ? SERVER.replace(`/clusters/${current}`, `/clusters/${parts.cluster}`) : SERVER;
  const url = `${base}/apis/deno.computer/v1alpha1/namespaces/${parts.namespace}/denopods/${parts.name}`;
  const res = await realFetch(url, { headers: { authorization: `Bearer ${token}` } });
  if (!res.ok) return null;
  const addr = reportedValue(await res.json(), parts.listener);
  if (addr) TABLE[host] = addr;
  return addr;
}

async function addressFor(host: string): Promise<string | null> {
  if (!host.endsWith(SUFFIX)) return null;
  refresh();
  const known = TABLE[host];
  if (known) return known;
  const found = await discover(host);
  if (!found) throw new Error(`kcpdns: ${host} is not in the table and could not be discovered`);
  return found;
}

async function targetFor(host: string): Promise<Target | null> {
  const value = await addressFor(host);
  return value ? parseTarget(value) : null;
}

function syncTargetFor(host: string): Target | null {
  if (!host.endsWith(SUFFIX)) return null;
  refresh();
  const value = TABLE[host];
  return value ? parseTarget(value) : null;
}

// Rewrites only the authority, keeping the path, the query and the scheme
// intact. The scheme matters: downgrading https to http here would silently
// defeat the TLS the leaves serve, so it is preserved and the certificate is
// what makes the connection trustworthy.
//
// The host becomes an address, which the leaf's certificate covers because
// every leaf carries 127.0.0.1 as a subject alternative name. That means the
// client authenticates the address rather than the name, which is the price of
// having no way to make the name resolve; the Host header is therefore set back
// to the original name by the caller, so a service that derives its identity
// from Host still sees the name it was configured with.
function rewrite(raw: string, target: Target, websocket: boolean): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  switch (target.kind) {
    case "tcp":
      url.hostname = target.host.includes(":") ? `[${target.host}]` : target.host;
      url.port = target.port ?? "";
      return url.toString();
    case "unix":
      url.hostname = "localhost";
      url.port = "";
      return url.toString();
    case "relay": {
      const secure = target.url.protocol === "https:" || target.url.protocol === "wss:";
      url.protocol = websocket ? (secure ? "wss:" : "ws:") : (secure ? "https:" : "http:");
      url.host = target.url.host;
      const prefix = target.url.pathname.replace(/\/$/, "");
      if (prefix) url.pathname = prefix + url.pathname;
      return url.toString();
    }
  }
}

function hostOf(raw: string): string {
  try {
    return new URL(raw).hostname;
  } catch {
    return "";
  }
}

const unixClients = new Map<string, Deno.HttpClient>();
function unixClient(path: string): Deno.HttpClient {
  let client = unixClients.get(path);
  if (!client) {
    client = Deno.createHttpClient({ proxy: { transport: "unix", path } } as never);
    unixClients.set(path, client);
  }
  return client;
}

const realFetch = globalThis.fetch;
globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const host = typeof raw === "string" ? hostOf(raw) : "";
  // addressFor returns null for a name outside the suffix, and throws for one
  // inside it that cannot be resolved: swallowing that would turn a name we own
  // into an opaque DNS failure.
  const target = host ? await targetFor(host) : null;
  if (!target) return realFetch(input as RequestInfo, init);
  const next = rewrite(raw, target, false);
  if (!next) return realFetch(input as RequestInfo, init);
  const keepName = target.kind !== "relay";
  const extra = target.kind === "unix" ? { client: unixClient(target.path) } : {};
  // A Request carries its own method, headers and body; rebuilding it on the
  // rewritten URL keeps all three, where passing only the URL would send a
  // bodiless GET in its place.
  if (input instanceof Request) {
    const request = new Request(next, input);
    if (keepName && !request.headers.has("host")) request.headers.set("host", host);
    return realFetch(request, { ...(init ?? {}), ...extra } as RequestInit);
  }
  const headers = new Headers(
    init?.headers ?? (typeof input === "object" && "headers" in input ? input.headers : undefined),
  );
  if (keepName && !headers.has("host")) headers.set("host", host);
  return realFetch(next, { ...(init ?? {}), headers, ...extra } as RequestInit);
}) as typeof fetch;

for (const name of ["connect", "connectTls"] as const) {
  const real = Deno[name].bind(Deno);
  // @ts-ignore the two signatures differ only in their options type
  Deno[name] = async (options: { hostname?: string; port?: number } & Record<string, unknown>) => {
    const host = options?.hostname;
    if (typeof host === "string" && host.endsWith(SUFFIX)) {
      const target = await targetFor(host);
      if (target?.kind === "tcp") {
        options = { ...options, hostname: target.host, port: Number(target.port) };
      } else if (target?.kind === "relay") {
        const port = target.url.port || (target.url.protocol === "https:" || target.url.protocol === "wss:" ? "443" : "80");
        options = { ...options, hostname: target.url.hostname, port: Number(port) };
      } else if (target?.kind === "unix") {
        if (name === "connectTls") throw new Error(`kcpdns: ${host} is a unix listener and cannot be reached over tls`);
        return Deno.connect({ transport: "unix", path: target.path });
      }
    }
    return real(options as never);
  };
}

const realResolveDns = Deno.resolveDns;
Deno.resolveDns = (async (name: string, recordType: string) => {
  if (typeof name === "string" && name.endsWith(SUFFIX)) {
    const target = await targetFor(name);
    if (target?.kind === "relay") return realResolveDns(target.url.hostname, recordType as never);
    if (target?.kind === "tcp" && (recordType === "A" || recordType === "AAAA")) return [target.host];
    return [];
  }
  return realResolveDns(name, recordType as never);
}) as typeof Deno.resolveDns;

// ponytail: WebSocket is constructed synchronously, so it can only use the table
// the provider injected and cannot await a discovery round trip. A name that is
// not in the table is passed through unchanged and will fail to resolve, which
// is a clear enough error; making it work would need a wrapper class deferring
// the connection, which is not worth it while the table is complete.
const realWebSocket = globalThis.WebSocket;
globalThis.WebSocket = class extends realWebSocket {
  constructor(url: string | URL, protocols?: string | string[]) {
    const raw = typeof url === "string" ? url : url.href;
    const target = syncTargetFor(hostOf(raw));
    const next = target && target.kind !== "unix" ? rewrite(raw, target, true) : null;
    super(next ?? raw, protocols);
  }
} as typeof WebSocket;
