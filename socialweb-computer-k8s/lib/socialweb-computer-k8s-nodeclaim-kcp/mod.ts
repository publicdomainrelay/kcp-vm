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
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import { isConflict, isKcpStatus, isNotFound, type KcpTarget } from "@publicdomainrelay/kcp-client-common";

export interface KcpRequestOptions {
  kcp: KcpClient;
}

export interface KcpNodeClaimStoreOptions extends KcpRequestOptions {
  homesWorkspace: string;
  homesNamespace?: string;
  homesTtlMs?: number;
}

const DEFAULT_HOMES_TTL_MS = 30_000;
const DEFAULT_HOMES_NAMESPACE = "default";
const NODE_CLAIM_HOME_SELECTOR = "socialweb.computer/nodeclaim-home=true";
const NODE_CLAIM_HOME_KEY = "workspace";
const UNREACHABLE_STATUSES = [403, 404];

const WATCH_RETRY_MS = 2_000;

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) return resolve();
    const timer = setTimeout(done, ms);
    function done() {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
    signal.addEventListener("abort", done);
  });
}

const LOGICAL_CLUSTER_ANNOTATION = "kcp.io/cluster";

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
    uid: claim.metadata?.uid,
    resourceVersion: claim.metadata?.resourceVersion,
    deletionTimestamp: claim.metadata?.deletionTimestamp,
    finalizers: claim.metadata?.finalizers ?? [],
    ...(claim.metadata?.annotations?.[NODE_CLAIM_ADOPT_ANNOTATION] === "true" ? { adoptJoinedNode: true } : {}),
    spec: claim.spec,
    status: claim.status ?? {},
  };
}

export function toClusterLink(raw: unknown, workspace: string): ClusterLinkView {
  const link = parseClusterLink(raw);
  const name = link.metadata?.name;
  if (!name) throw new Error("a clusterlink from kcp carries no metadata.name");
  return { workspace, name, spec: link.spec, status: link.status ?? {} };
}

const CONFIGMAP_RESOURCE = { group: "", version: "v1", plural: "configmaps" };

export function createKcpClusterLinkReader(opts: KcpRequestOptions): ClusterLinkReader {
  return {
    async get(workspace, name) {
      const raw = await opts.kcp.get({ resource: CLUSTER_LINK_RESOURCE, workspace }, name);
      return raw === null ? null : toClusterLink(raw, workspace);
    },
  };
}

export function createKcpNodeClaimStore(opts: KcpNodeClaimStoreOptions): NodeClaimStore {
  const { kcp } = opts;
  const homesTtlMs = opts.homesTtlMs ?? DEFAULT_HOMES_TTL_MS;
  let homesCache: { at: number; homes: string[] } | null = null;

  async function reachable<T>(read: () => Promise<T>, fallback: T): Promise<T> {
    try {
      return await read();
    } catch (err) {
      if (isKcpStatus(err, ...UNREACHABLE_STATUSES)) return fallback;
      throw err;
    }
  }

  async function homes(): Promise<string[]> {
    if (homesCache && Date.now() - homesCache.at < homesTtlMs) return homesCache.homes;
    const out = await kcp.list(
      { resource: CONFIGMAP_RESOURCE, workspace: opts.homesWorkspace, namespace: opts.homesNamespace ?? DEFAULT_HOMES_NAMESPACE },
      { labelSelector: NODE_CLAIM_HOME_SELECTOR },
    );
    const items = out.items as { data?: Record<string, string> }[];
    const paths = [...new Set(items.map((i) => i.data?.[NODE_CLAIM_HOME_KEY] ?? "").filter(Boolean))].sort();
    homesCache = { at: Date.now(), homes: paths };
    return paths;
  }

  const claimsIn = (workspace: string): KcpTarget => ({ resource: NODE_CLAIM_RESOURCE, workspace });

  async function editFinalizers(claim: NodeClaim, edit: (current: string[]) => string[] | null): Promise<void> {
    const target = claimsIn(claim.workspace);
    for (let attempt = 0; attempt < 3; attempt++) {
      const raw = await kcp.get(target, claim.name);
      if (raw === null) return;
      const fresh = toNodeClaim(raw, claim.workspace);
      const next = edit(fresh.finalizers);
      if (!next) return;
      try {
        await kcp.patch(target, claim.name, {
          type: "json",
          ops: [
            { op: "test", path: "/metadata/resourceVersion", value: fresh.resourceVersion },
            { op: "add", path: "/metadata/finalizers", value: next },
          ],
        });
        return;
      } catch (err) {
        if (isNotFound(err)) return;
        if (!isConflict(err)) throw err;
      }
    }
    throw new Error(`nodeclaim ${claim.workspace}/${claim.name}: finalizers kept changing under three attempts`);
  }

  return {
    async list() {
      const claims: NodeClaim[] = [];
      for (const home of await homes()) {
        const out = await reachable(() => kcp.list(claimsIn(home)), { items: [], resourceVersion: "" });
        for (const item of out.items) claims.push(toNodeClaim(item, home));
      }
      return claims;
    },
    async get(claim) {
      const raw = await kcp.get(claimsIn(claim.workspace), claim.name);
      return raw === null ? null : toNodeClaim(raw, claim.workspace);
    },
    async replaceStatus(claim, status) {
      assertNodeClaimStatus(status);
      await kcpResourceClient(kcp, claimsIn(claim.workspace)).replaceStatus(claim.name, status);
    },
    addFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(FINALIZER_NODE_CLAIM) ? null : [...cur, FINALIZER_NODE_CLAIM]);
    },
    removeFinalizer(claim) {
      return editFinalizers(claim, (cur) => cur.includes(FINALIZER_NODE_CLAIM) ? cur.filter((f) => f !== FINALIZER_NODE_CLAIM) : null);
    },
    watch(signal, onChange) {
      const open = new Map<string, AbortController>();
      const follow = async (home: string, stop: AbortSignal) => {
        while (!stop.aborted) {
          try {
            for await (const event of kcp.watch(claimsIn(home), { signal: stop })) {
              if (event.type === "BOOKMARK") continue;
              onChange();
              if (event.type === "ERROR") break;
            }
          } catch {
            if (stop.aborted) return;
          }
          await pause(WATCH_RETRY_MS, stop);
        }
      };
      void (async () => {
        while (!signal.aborted) {
          const current = await homes().catch(() => null);
          for (const home of current ?? []) {
            if (open.has(home)) continue;
            const stop = new AbortController();
            signal.addEventListener("abort", () => stop.abort(), { once: true });
            open.set(home, stop);
            void follow(home, stop.signal);
          }
          for (const [home, stop] of open) {
            if (current && !current.includes(home)) {
              stop.abort();
              open.delete(home);
            }
          }
          await pause(homesTtlMs, signal);
        }
      })();
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
  const gateways = kcpResourceClient<Gateway>(opts.kcp, { resource: GATEWAY_RESOURCE, workspace: opts.workspace });
  const cacheMs = opts.cacheMs ?? DEFAULT_LISTENER_CACHE_MS;
  let cached: { at: number; url: string } | null = null;
  return async () => {
    if (cached && Date.now() - cached.at < cacheMs) return cached.url;
    const gateway = await gateways.get(opts.name);
    if (!gateway) throw new Error(`gateway ${opts.workspace}:${opts.name} does not exist`);
    const found = gateway.status?.listeners?.find((l) => l.name === opts.listener && l.address);
    if (!found) throw new Error(`gateway ${opts.workspace}:${opts.name} reports no ${opts.listener} listener in its status`);
    cached = { at: Date.now(), url: `${found.kind === "https" ? "https" : "http"}://${found.address}` };
    return cached.url;
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
  const { group, version, kind } = MARKET_IDENTITY_RESOURCE;
  const identities = kcpResourceClient<MarketIdentity>(opts.kcp, { resource: MARKET_IDENTITY_RESOURCE, workspace: opts.workspace });

  async function nameOf(did: string): Promise<string> {
    const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(did)));
    return `account-${Array.from(sum.slice(0, 10), (b) => b.toString(16).padStart(2, "0")).join("")}`;
  }

  async function upsert(name: string, did: string, spec: MarketIdentitySpec): Promise<void> {
    const object: MarketIdentity = {
      apiVersion: `${group}/${version}`,
      kind,
      metadata: { name, annotations: { [ACCOUNT_DID_ANNOTATION]: did } },
      spec,
    };
    const { created } = await identities.createIfAbsent(object);
    if (!created) await identities.patch(name, { type: "merge", body: { spec } });
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
        message: "the application OpenBao holds this account's OAuth session",
        conditions: [{
          type: "SignedIn",
          status: "True",
          reason: "SignedInThroughVault",
          message: "an OAuth session is held by the atproto auth mount at the session ref",
          lastTransitionTime: new Date().toISOString().replace(/\.\d+Z$/, "Z"),
        }],
      };
      await identities.patch(name, { type: "merge", body: { status } }, { subresource: "status" });
      return name;
    },
    async signedOut(did, sessionRef) {
      const name = await nameOf(did);
      try {
        await identities.patch(name, {
          type: "merge",
          body: {
            status: {
              phase: "SessionExpired",
              did,
              sessionRef,
              message: "the application OpenBao holds no OAuth session for this account; sign in again",
              pendingAction: { kind: "signInAgain" },
              conditions: [{
                type: "SignedIn",
                status: "False",
                reason: "SignedOut",
                message: "the session was deleted from the atproto auth mount and its tokens revoked",
                lastTransitionTime: new Date().toISOString().replace(/\.\d+Z$/, "Z"),
              }],
            },
          },
        }, { subresource: "status" });
      } catch (err) {
        if (!isNotFound(err)) throw err;
      }
    },
  };
}
