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

function split(host: string): { name: string; namespace: string; cluster: string } | null {
  if (!host.endsWith(SUFFIX)) return null;
  const bits = host.slice(0, -SUFFIX.length).split(".");
  if (bits.length < 2) return null;
  return { name: bits[0], namespace: bits[1], cluster: labelsToCluster(bits.slice(2).join(".")) };
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
  const res = await fetch(url, { headers: { authorization: `Bearer ${token}` } });
  if (!res.ok) return null;
  const pod = await res.json();
  const reported = typeof pod?.status?.address === "string" ? pod.status.address : "";
  const addr = reported || addressOf(pod?.spec?.env ?? {});
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
function rewrite(raw: string, addr: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  const [host, port] = addr.split(":");
  url.hostname = host;
  url.port = port ?? "";
  return url.toString();
}

function hostOf(raw: string): string {
  try {
    return new URL(raw).hostname;
  } catch {
    return "";
  }
}

const realFetch = globalThis.fetch;
globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const host = typeof raw === "string" ? hostOf(raw) : "";
  // addressFor returns null for a name outside the suffix, and throws for one
  // inside it that cannot be resolved: swallowing that would turn a name we own
  // into an opaque DNS failure.
  const target = host ? await addressFor(host) : null;
  if (!target) return realFetch(input as RequestInfo, init);
  const next = rewrite(raw, target);
  if (!next) return realFetch(input as RequestInfo, init);
  // A Request carries its own method, headers and body; rebuilding it on the
  // rewritten URL keeps all three, where passing only the URL would send a
  // bodiless GET in its place.
  if (input instanceof Request) {
    const request = new Request(next, input);
    if (!request.headers.has("host")) request.headers.set("host", host);
    return realFetch(request, init);
  }
  const headers = new Headers(
    init?.headers ?? (typeof input === "object" && "headers" in input ? input.headers : undefined),
  );
  if (!headers.has("host")) headers.set("host", host);
  return realFetch(next, { ...(init ?? {}), headers });
}) as typeof fetch;

for (const name of ["connect", "connectTls"] as const) {
  const real = Deno[name].bind(Deno);
  // @ts-ignore the two signatures differ only in their options type
  Deno[name] = async (options: { hostname?: string; port?: number } & Record<string, unknown>) => {
    const host = options?.hostname;
    if (typeof host === "string" && host.endsWith(SUFFIX)) {
      const addr = await addressFor(host);
      if (addr) {
        const [h, p] = addr.split(":");
        options = { ...options, hostname: h, port: Number(p) };
      }
    }
    return real(options as never);
  };
}

const realResolveDns = Deno.resolveDns;
Deno.resolveDns = (async (name: string, recordType: string) => {
  if (typeof name === "string" && name.endsWith(SUFFIX)) {
    const addr = await addressFor(name);
    if (addr && (recordType === "A" || recordType === "AAAA")) return [addr.split(":")[0]];
    if (addr) return [];
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
    const host = (() => {
      try {
        return new URL(raw).hostname;
      } catch {
        return "";
      }
    })();
    if (host.endsWith(SUFFIX)) refresh();
    const addr = host.endsWith(SUFFIX) ? TABLE[host] : null;
    const next = addr ? rewrite(raw, addr) : null;
    super(next ?? raw, protocols);
  }
} as typeof WebSocket;
