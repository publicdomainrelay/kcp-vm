import { parse as parseYaml } from "@std/yaml";
import type { NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  NODECLAIM_FINALIZER,
  NODECLAIM_GROUP,
  NODECLAIM_RESOURCE,
  NODECLAIM_VERSION,
  type NodeClaim,
  type NodeClaimSpec,
  type NodeClaimStatus,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

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
  providerPath?: string;
  exportName?: string;
  virtualWorkspaceUrl?: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

interface RawClaim {
  metadata: {
    name: string;
    resourceVersion?: string;
    deletionTimestamp?: string;
    finalizers?: string[];
    annotations?: Record<string, string>;
  };
  spec: NodeClaimSpec;
  status?: NodeClaimStatus;
}

export class KcpRequestError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "KcpRequestError";
  }
}

const LOGICAL_CLUSTER_ANNOTATION = "kcp.io/cluster";
const CONFLICT_STATUSES = new Set([409, 422]);

export function toNodeClaim(raw: RawClaim, fallbackWorkspace?: string): NodeClaim {
  const workspace = raw.metadata.annotations?.[LOGICAL_CLUSTER_ANNOTATION] ?? fallbackWorkspace;
  if (!workspace) throw new Error(`nodeclaim ${raw.metadata.name} carries no ${LOGICAL_CLUSTER_ANNOTATION} annotation`);
  return {
    workspace,
    name: raw.metadata.name,
    resourceVersion: raw.metadata.resourceVersion,
    deletionTimestamp: raw.metadata.deletionTimestamp,
    finalizers: raw.metadata.finalizers ?? [],
    spec: raw.spec,
    status: raw.status ?? {},
  };
}

export function createKcpNodeClaimStore(opts: KcpNodeClaimStoreOptions): NodeClaimStore {
  const creds = opts.credentials;
  const providerPath = opts.providerPath ?? "root:socialweb-provider";
  const exportName = opts.exportName ?? "clusters";
  const timeoutMs = opts.timeoutMs ?? 15_000;
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && (creds.ca || creds.cert)
    ? Deno.createHttpClient({
      caCerts: creds.ca ? [creds.ca] : undefined,
      ...(creds.cert && creds.key ? { cert: creds.cert, key: creds.key } : {}),
    })
    : undefined;
  let vwBase: string | undefined = opts.virtualWorkspaceUrl?.replace(/\/+$/, "");

  async function request(url: string, init: RequestInit = {}): Promise<unknown> {
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
    if (!res.ok) throw new KcpRequestError(res.status, `${init.method ?? "GET"} ${new URL(url).pathname}: ${res.status} ${text.slice(0, 300)}`);
    return text ? JSON.parse(text) : null;
  }

  async function virtualWorkspace(): Promise<string> {
    if (vwBase) return vwBase;
    const slice = await request(
      `${creds.server}/clusters/${providerPath}/apis/apis.kcp.io/v1alpha1/apiexportendpointslices/${exportName}`,
    ).catch(() => null) as { status?: { endpoints?: { url: string }[] } } | null;
    vwBase = slice?.status?.endpoints?.[0]?.url?.replace(/\/+$/, "") ??
      `${creds.server}/services/apiexport/${providerPath}/${exportName}`;
    return vwBase;
  }

  async function itemUrl(claim: Pick<NodeClaim, "workspace" | "name">, sub = ""): Promise<string> {
    return `${await virtualWorkspace()}/clusters/${claim.workspace}/apis/${NODECLAIM_GROUP}/${NODECLAIM_VERSION}/${NODECLAIM_RESOURCE}/${encodeURIComponent(claim.name)}${sub}`;
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
        fresh = toNodeClaim(await request(url) as RawClaim, claim.workspace);
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
      const out = await request(
        `${await virtualWorkspace()}/clusters/*/apis/${NODECLAIM_GROUP}/${NODECLAIM_VERSION}/${NODECLAIM_RESOURCE}`,
      ) as { items?: RawClaim[] };
      return (out.items ?? []).map((item) => toNodeClaim(item));
    },
    async replaceStatus(claim, status) {
      await jsonPatch(await itemUrl(claim, "/status"), [{ op: "add", path: "/status", value: status }]);
    },
    addFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(NODECLAIM_FINALIZER) ? null : [...cur, NODECLAIM_FINALIZER]);
    },
    removeFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(NODECLAIM_FINALIZER) ? cur.filter((f) => f !== NODECLAIM_FINALIZER) : null);
    },
  };
}
