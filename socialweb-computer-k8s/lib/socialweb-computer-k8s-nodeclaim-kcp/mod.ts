import { parse as parseYaml } from "@std/yaml";
import type {
  ClusterLinkReader,
  ClusterLinkView,
  NodeClaim,
  NodeClaimStore,
} from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { NODE_CLAIM_ADOPT_ANNOTATION } from "@publicdomainrelay/socialweb-computer-k8s-common";
import {
  assertNodeClaimStatus,
  CLUSTER_LINK_RESOURCE,
  FINALIZER_NODE_CLAIM,
  GATEWAY_RESOURCE,
  type Gateway,
  MARKET_IDENTITY_RESOURCE,
  type MarketIdentity,
  type MarketIdentitySpec,
  type MarketIdentityStatus,
  type VaultRef,
  NODE_CLAIM_RESOURCE,
  parseClusterLink,
  parseNodeClaim,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface KcpCredentials {
  server: string;
  token?: string;
  tokenFile?: string;
  cert?: string;
  key?: string;
  ca?: string;
}

interface KubeconfigDoc {
  "current-context"?: string;
  contexts?: { name: string; context: { cluster: string; user: string } }[];
  clusters?: { name: string; cluster: { server: string; "certificate-authority-data"?: string } }[];
  users?: {
    name: string;
    user: { token?: string; tokenFile?: string; "client-certificate-data"?: string; "client-key-data"?: string };
  }[];
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
    tokenFile: user.tokenFile,
    cert: fromBase64(user["client-certificate-data"]),
    key: fromBase64(user["client-key-data"]),
    ca: fromBase64(cluster["certificate-authority-data"]),
  };
}

export async function bearerOf(credentials: KcpCredentials): Promise<string | undefined> {
  if (credentials.tokenFile) return (await Deno.readTextFile(credentials.tokenFile)).trim();
  return credentials.token;
}

export interface KcpRequestOptions {
  credentials: KcpCredentials;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

export interface KcpNodeClaimStoreOptions extends KcpRequestOptions {
  virtualWorkspace: string;
}

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
    ...(claim.metadata?.annotations?.[NODE_CLAIM_ADOPT_ANNOTATION] === "true" ? { adoptJoinedNode: true } : {}),
    spec: claim.spec,
    status: claim.status ?? {},
  };
}

type KcpRequest = (url: string, init?: RequestInit) => Promise<unknown>;

function createKcpRequest(opts: KcpRequestOptions): KcpRequest {
  const creds = opts.credentials;
  const timeoutMs = opts.timeoutMs ?? 15_000;
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && (creds.ca || creds.cert)
    ? Deno.createHttpClient({
      caCerts: creds.ca ? [creds.ca] : undefined,
      ...(creds.cert && creds.key ? { cert: creds.cert, key: creds.key } : {}),
    })
    : undefined;

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
    const bearer = await bearerOf(creds);
    if (bearer) headers.set("authorization", `Bearer ${bearer}`);
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

  return request;
}

export function toClusterLink(raw: unknown, workspace: string): ClusterLinkView {
  const link = parseClusterLink(raw);
  const name = link.metadata?.name;
  if (!name) throw new Error("a clusterlink from kcp carries no metadata.name");
  return { workspace, name, spec: link.spec, status: link.status ?? {} };
}

function virtualWorkspaceBase(opts: KcpNodeClaimStoreOptions): string {
  return opts.virtualWorkspace.replace(/\/+$/, "");
}

export function createKcpClusterLinkReader(opts: KcpNodeClaimStoreOptions): ClusterLinkReader {
  const request = createKcpRequest(opts);
  const base = virtualWorkspaceBase(opts);
  const { group, version, plural } = CLUSTER_LINK_RESOURCE;
  return {
    async get(workspace, name) {
      const url = `${base}/clusters/${workspace}/apis/${group}/${version}/${plural}/${encodeURIComponent(name)}`;
      try {
        return toClusterLink(await request(url), workspace);
      } catch (err) {
        if (err instanceof KcpRequestError && err.status === 404) return null;
        throw err;
      }
    },
  };
}

export function createKcpNodeClaimStore(opts: KcpNodeClaimStoreOptions): NodeClaimStore {
  const request = createKcpRequest(opts);
  const base = virtualWorkspaceBase(opts);

  function clusterPath(workspace: string, resource: string): string {
    return `${base}/clusters/${workspace}/apis/${NODE_CLAIM_RESOURCE.group}/${NODE_CLAIM_RESOURCE.version}/${NODE_CLAIM_RESOURCE.plural}${resource}`;
  }

  async function itemUrl(claim: Pick<NodeClaim, "workspace" | "name">, sub = ""): Promise<string> {
    return clusterPath(claim.workspace, `/${encodeURIComponent(claim.name)}${sub}`);
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
      const out = await request(clusterPath("*", "")) as { items?: unknown[] };
      return (out.items ?? []).map((item) => toNodeClaim(item));
    },
    async get(claim) {
      try {
        return toNodeClaim(await request(await itemUrl(claim)), claim.workspace);
      } catch (err) {
        if (err instanceof KcpRequestError && err.status === 404) return null;
        throw err;
      }
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

export interface KcpListenerUrlOptions extends KcpRequestOptions {
  workspace: string;
  name: string;
  listener: string;
  cacheMs?: number;
}

const DEFAULT_LISTENER_CACHE_MS = 5_000;

export function createKcpGatewayListenerUrl(opts: KcpListenerUrlOptions): () => Promise<string> {
  const request = createKcpRequest(opts);
  const { group, version, plural } = GATEWAY_RESOURCE;
  const url = `${opts.credentials.server}/clusters/${opts.workspace}/apis/${group}/${version}/${plural}/${encodeURIComponent(opts.name)}`;
  const cacheMs = opts.cacheMs ?? DEFAULT_LISTENER_CACHE_MS;
  let cached: { at: number; url: string } | null = null;
  return async () => {
    if (cached && Date.now() - cached.at < cacheMs) return cached.url;
    const gateway = await request(url) as Gateway;
    const found = gateway.status?.listeners?.find((l) => l.name === opts.listener && l.address);
    if (!found) throw new Error(`gateway ${opts.workspace}:${opts.name} reports no ${opts.listener} listener in its status`);
    cached = { at: Date.now(), url: `${found.kind === "https" ? "https" : "http"}://${found.address}` };
    return cached.url;
  };
}

export function createKcpFetch(credentials: KcpCredentials): typeof fetch {
  const client = credentials.ca || credentials.cert
    ? Deno.createHttpClient({
      caCerts: credentials.ca ? [credentials.ca] : undefined,
      ...(credentials.cert && credentials.key ? { cert: credentials.cert, key: credentials.key } : {}),
    })
    : undefined;
  return async (input, init) => {
    const headers = new Headers(init?.headers);
    const bearer = await bearerOf(credentials);
    if (bearer) headers.set("authorization", `Bearer ${bearer}`);
    return await fetch(input, { ...init, headers, ...(client ? { client } : {}) } as RequestInit);
  };
}

export interface KcpMarketIdentitiesOptions extends KcpRequestOptions {
  workspace: string;
}

export interface AccountSignIn {
  did: string;
  handle?: string;
  pdsUrl?: string;
  sessionRef: VaultRef;
}

export interface KcpMarketIdentities {
  nameOf(did: string): Promise<string>;
  signedIn(account: AccountSignIn): Promise<string>;
  signedOut(did: string, sessionRef: VaultRef): Promise<void>;
}

const ACCOUNT_DID_ANNOTATION = "socialweb.computer/did";

export function createKcpMarketIdentities(opts: KcpMarketIdentitiesOptions): KcpMarketIdentities {
  const request = createKcpRequest(opts);
  const { group, version, plural, kind } = MARKET_IDENTITY_RESOURCE;
  const base = `${opts.credentials.server}/clusters/${opts.workspace}/apis/${group}/${version}/${plural}`;

  async function nameOf(did: string): Promise<string> {
    const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(did)));
    return `account-${Array.from(sum.slice(0, 10), (b) => b.toString(16).padStart(2, "0")).join("")}`;
  }

  async function mergePatch(url: string, body: unknown): Promise<void> {
    await request(url, {
      method: "PATCH",
      headers: { "content-type": "application/merge-patch+json" },
      body: JSON.stringify(body),
    });
  }

  async function upsert(name: string, did: string, spec: MarketIdentitySpec): Promise<void> {
    const object: MarketIdentity = {
      apiVersion: `${group}/${version}`,
      kind,
      metadata: { name, annotations: { [ACCOUNT_DID_ANNOTATION]: did } },
      spec,
    };
    try {
      await request(base, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(object) });
    } catch (err) {
      if (!(err instanceof KcpRequestError) || err.status !== 409) throw err;
      await mergePatch(`${base}/${name}`, { spec });
    }
  }

  return {
    nameOf,
    async signedIn(account) {
      const name = await nameOf(account.did);
      await upsert(name, account.did, {
        mode: "oauthDelegated",
        handle: account.handle,
        pdsUrl: account.pdsUrl,
        sessionRef: account.sessionRef,
      });
      const status: MarketIdentityStatus = {
        phase: "Ready",
        did: account.did,
        handle: account.handle,
        pdsUrl: account.pdsUrl,
        sessionRef: account.sessionRef,
        message: "the web app holds this account's OAuth session in its vault",
        conditions: [{
          type: "SignedIn",
          status: "True",
          reason: "SessionDeposited",
          message: "an OAuth session is in the vault at the session ref",
          lastTransitionTime: new Date().toISOString().replace(/\.\d+Z$/, "Z"),
        }],
      };
      await mergePatch(`${base}/${name}/status`, { status });
      return name;
    },
    async signedOut(did, sessionRef) {
      const name = await nameOf(did);
      try {
        await mergePatch(`${base}/${name}/status`, {
          status: {
            phase: "SessionExpired",
            did,
            sessionRef,
            message: "the web app holds no OAuth session for this account; sign in again",
            pendingAction: { kind: "signInAgain" },
          },
        });
      } catch (err) {
        if (!(err instanceof KcpRequestError) || err.status !== 404) throw err;
      }
    },
  };
}
