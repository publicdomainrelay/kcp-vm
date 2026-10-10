import { parse as parseYaml } from "@std/yaml";
import type { NodeClaim, NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  assertNodeClaimStatus,
  FINALIZER_NODE_CLAIM,
  NODE_CLAIM_RESOURCE,
  parseNodeClaim,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface KcpCredentials {
  server: string;
  token?: string;
  cert?: string;
  key?: string;
  ca?: string;
}

interface KubeconfigDoc {
  "current-context"?: string;
  contexts?: { name: string; context: { cluster: string; user: string } }[];
  clusters?: { name: string; cluster: { server: string; "certificate-authority-data"?: string } }[];
  users?: { name: string; user: { token?: string; "client-certificate-data"?: string; "client-key-data"?: string } }[];
}

function fromBase64(data: string | undefined): string | undefined {
  return data ? new TextDecoder().decode(Uint8Array.from(atob(data), (c) => c.charCodeAt(0))) : undefined;
}

export function parseKubeconfig(text: string, contextName?: string): KcpCredentials {
  const doc = parseYaml(text) as KubeconfigDoc;
  const name = contextName ?? doc["current-context"];
  const context = doc.contexts?.find((c) => c.name === name)?.context;
  if (!context) throw new Error(`kubeconfig has no context ${name ?? "(none)"}`);
  const cluster = doc.clusters?.find((c) => c.name === context.cluster)?.cluster;
  const user = doc.users?.find((u) => u.name === context.user)?.user;
  if (!cluster?.server || !user) throw new Error(`kubeconfig context ${name} names a missing cluster or user`);
  const server = new URL(cluster.server);
  return {
    server: server.origin,
    token: user.token,
    cert: fromBase64(user["client-certificate-data"]),
    key: fromBase64(user["client-key-data"]),
    ca: fromBase64(cluster["certificate-authority-data"]),
  };
}

export interface KcpNodeClaimStoreOptions {
  credentials: KcpCredentials;
  fetch?: typeof fetch;
  timeoutMs?: number;
  homesTtlMs?: number;
}

const DEFAULT_HOMES_TTL_MS = 30_000;

const NO_HOMES_TTL_MS = 0;

const DEFAULT_RETRY_AFTER_MS = 5_000;

const MAX_RETRY_AFTER_MS = 60_000;

function retryAfterMs(res: Response): number {
  const raw = res.headers.get("retry-after");
  const seconds = raw === null ? Number.NaN : Number(raw);
  const ms = Number.isFinite(seconds) ? seconds * 1_000 : Date.parse(raw ?? "") - Date.now();
  const wait = Number.isFinite(ms) && ms > 0 ? ms : DEFAULT_RETRY_AFTER_MS;
  return Math.min(wait, MAX_RETRY_AFTER_MS);
}

export class KcpRequestError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "KcpRequestError";
  }
}

const LOGICAL_CLUSTER_ANNOTATION = "kcp.io/cluster";
const CONFLICT_STATUSES = new Set([409, 422]);

export function toNodeClaim(raw: unknown, fallbackWorkspace?: string): NodeClaim {
  const claim = parseNodeClaim(raw);
  const name = claim.metadata?.name;
  if (!name) throw new Error("a nodeclaim from kcp carries no metadata.name");
  if (!claim.spec) throw new Error(`nodeclaim ${name} carries no spec`);
  const workspace = claim.metadata?.annotations?.[LOGICAL_CLUSTER_ANNOTATION] ?? fallbackWorkspace;
  if (!workspace) throw new Error(`nodeclaim ${name} carries no ${LOGICAL_CLUSTER_ANNOTATION} annotation`);
  return {
    workspace,
    name,
    resourceVersion: claim.metadata?.resourceVersion,
    deletionTimestamp: claim.metadata?.deletionTimestamp,
    finalizers: claim.metadata?.finalizers ?? [],
    spec: claim.spec,
    status: claim.status ?? {},
  };
}

export function createKcpNodeClaimStore(opts: KcpNodeClaimStoreOptions): NodeClaimStore {
  const creds = opts.credentials;
  const timeoutMs = opts.timeoutMs ?? 15_000;
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && (creds.ca || creds.cert)
    ? Deno.createHttpClient({
      caCerts: creds.ca ? [creds.ca] : undefined,
      ...(creds.cert && creds.key ? { cert: creds.cert, key: creds.key } : {}),
    })
    : undefined;

  const homesTtlMs = opts.homesTtlMs ?? DEFAULT_HOMES_TTL_MS;
  let homesCache: { at: number; homes: string[] } | null = null;
  let readsPausedUntil = 0;

  async function request(url: string, init: RequestInit = {}): Promise<unknown> {
    const method = (init.method ?? "GET").toUpperCase();
    const reading = method === "GET";
    if (reading && Date.now() < readsPausedUntil) {
      throw new KcpRequestError(
        429,
        `${method} ${new URL(url).pathname}: held back until ${new Date(readsPausedUntil).toISOString()} because kcp rate limited this client and the Retry-After it sent has not elapsed`,
      );
    }
    const headers = new Headers(init.headers);
    headers.set("accept", "application/json");
    if (creds.token) headers.set("authorization", `Bearer ${creds.token}`);
    const res = await baseFetch(url, {
      ...init,
      headers,
      signal: AbortSignal.timeout(timeoutMs),
      ...(client ? { client } : {}),
    } as RequestInit);
    const text = await res.text();
    if (!res.ok) {
      if (res.status === 429) readsPausedUntil = Date.now() + retryAfterMs(res);
      throw new KcpRequestError(res.status, `${init.method ?? "GET"} ${new URL(url).pathname}: ${res.status} ${text.slice(0, 300)}`);
    }
    return text ? JSON.parse(text) : null;
  }

  function clusterPath(workspace: string, resource: string): string {
    return `${creds.server}/clusters/${workspace}/apis/${NODE_CLAIM_RESOURCE.group}/${NODE_CLAIM_RESOURCE.version}/${NODE_CLAIM_RESOURCE.plural}${resource}`;
  }

  async function itemUrl(claim: Pick<NodeClaim, "workspace" | "name">, sub = ""): Promise<string> {
    return clusterPath(claim.workspace, `/${encodeURIComponent(claim.name)}${sub}`);
  }

  interface WorkspaceList {
    items?: { metadata: { name: string }; spec?: { type?: { name?: string } } }[];
  }

  async function childrenOf(workspace: string): Promise<string[]> {
    const out = await request(
      `${creds.server}/clusters/${workspace}/apis/tenancy.kcp.io/v1alpha1/workspaces`,
    ) as WorkspaceList;
    return (out.items ?? []).map((w) => w.metadata.name);
  }

  async function homes(): Promise<string[]> {
    if (homesCache && Date.now() - homesCache.at < homesTtlMs) return homesCache.homes;
    const paths: string[] = [];
    for (const account of await childrenOf("root")) {
      const accountPath = `root:${account}`;
      for (const home of await childrenOf(accountPath)) paths.push(`${accountPath}:${home}`);
    }
    if (homesTtlMs > NO_HOMES_TTL_MS) homesCache = { at: Date.now(), homes: paths };
    return paths;
  }

  async function jsonPatch(url: string, ops: unknown[]): Promise<unknown> {
    return await request(url, {
      method: "PATCH",
      headers: { "content-type": "application/json-patch+json" },
      body: JSON.stringify(ops),
    });
  }

  async function editFinalizers(claim: NodeClaim, edit: (current: string[]) => string[] | null): Promise<void> {
    const url = await itemUrl(claim);
    for (let attempt = 0; attempt < 3; attempt++) {
      let fresh: NodeClaim;
      try {
        fresh = toNodeClaim(await request(url), claim.workspace);
      } catch (err) {
        if (err instanceof KcpRequestError && err.status === 404) return;
        throw err;
      }
      const next = edit(fresh.finalizers);
      if (!next) return;
      try {
        await jsonPatch(url, [
          { op: "test", path: "/metadata/resourceVersion", value: fresh.resourceVersion },
          { op: "add", path: "/metadata/finalizers", value: next },
        ]);
        return;
      } catch (err) {
        if (err instanceof KcpRequestError && err.status === 404) return;
        if (!(err instanceof KcpRequestError && CONFLICT_STATUSES.has(err.status))) throw err;
      }
    }
    throw new Error(`nodeclaim ${claim.workspace}/${claim.name}: finalizers kept changing under three attempts`);
  }

  return {
    async list() {
      const claims: NodeClaim[] = [];
      for (const home of await homes()) {
        const out = await request(clusterPath(home, "")) as { items?: unknown[] };
        for (const item of out.items ?? []) claims.push(toNodeClaim(item, home));
      }
      return claims;
    },
    async replaceStatus(claim, status) {
      assertNodeClaimStatus(status);
      await jsonPatch(await itemUrl(claim, "/status"), [{ op: "add", path: "/status", value: status }]);
    },
    addFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(FINALIZER_NODE_CLAIM) ? null : [...cur, FINALIZER_NODE_CLAIM]);
    },
    removeFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(FINALIZER_NODE_CLAIM) ? cur.filter((f) => f !== FINALIZER_NODE_CLAIM) : null);
    },
  };
}
