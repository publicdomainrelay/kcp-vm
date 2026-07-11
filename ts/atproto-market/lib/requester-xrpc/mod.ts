// Compute requester implementation — PDS creation, contract flow orchestration,
// SSH session management, websocat bootstrapping, relay-based bidder discovery.
// All I/O lives here: fetch, Deno.Command, Deno.makeTempDir, WebSocket, crypto.

import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import { getPdsEndpoint } from "@atproto/common-web";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, DenoKvStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { Signer } from "@publicdomainrelay/atproto-repo-abc";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { PlcClient, createGenesisOp, PlcNotFoundError } from "@publicdomainrelay/did-plc";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import {
  loadOrGenerateKeypair,
  attestationFor,
  toStorableEntry,
  createSubmitBidHandler,
  listRecordsPublic,
  createSubmitEventHandler,
  createRecordResolver,
  verifyRecordSignatures,
  verifyRemoteProof,
} from "@publicdomainrelay/market-atproto";
import { OAuthClient } from "@atproto/oauth-client";
import { webCryptoRuntime, memoryStateStore, jsonSessionStore } from "@publicdomainrelay/atproto-oauth-helpers";
import { stripResolved, atUriAuthority } from "@publicdomainrelay/market-abc";
import type { InlineAttestation, AttestationKeypair, SubmitBidCallback } from "@publicdomainrelay/market-atproto";
import {
  COMPUTE_VM_NSID,
  RFP_NSID,
  ACCEPT_NSID,
  BID_NSID,
  RECEIPT_NSID,
  OFFERING_NSID,
  EVENT_NSID,
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_ONNETWORK_NSID,
  COMPUTE_EVENTS_VM_REGISTER_IDENTITY_NSID,
  SUBMIT_RFP_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_EVENT_NSID,
  SUBMIT_RFP_LXM,
  SUBMIT_ACCEPT_LXM,
  SUBMIT_EVENT_LXM,
  VOUCH_NSID,
  RELAYS_NSID,
} from "@publicdomainrelay/market-common";
import type { StrongRef } from "@publicdomainrelay/market-common";
import { DYNAMIC } from "@publicdomainrelay/market-policy-abc";
import { buildDefaultUserData, patchDefaultUserData, buildTunnelUserData, flattenLabel, type CloudInitContext, type TunnelCloudInitContext } from "@publicdomainrelay/cloud-init-common";
import {
  FEDPROXY_RBAC_NSID,
  buildSshKeyRbacRecord,
} from "@publicdomainrelay/fedproxy-rbac-common";
import type {
  RequesterPDS,
  PDSOptions,
  CollectedBid,
  ContractFlowOptions,
  ContractFlowResult,
  SshSessionProvider,
} from "@publicdomainrelay/requester-abc";
import type { LoggerInterface, StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { ServeHandle, IngressRef } from "@publicdomainrelay/serve";
import { ASSOCIATE_CONFIRM_NSID, BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";
import { createTangledGraphVouchResolver } from "@publicdomainrelay/trust-graph-tangled-graph";
import { createBadgeBlueKeysDelegatedTrustResolver } from "@publicdomainrelay/delegated-trust-badge-blue-keys";
import { verifyServiceAuth } from "@publicdomainrelay/market-atproto";

// ---------------------------------------------------------------------------
// Extended types (impl details beyond the abc contract)
// ---------------------------------------------------------------------------

// deno-lint-ignore no-explicit-any
type HonoApp = any;

export interface RequesterPDSImpl extends RequesterPDS {
  app: HonoApp;
  signer: Signer;
  keypair: Secp256k1Keypair;
  api: RepoApi;
}

// ---------------------------------------------------------------------------
// bidder discovery via relay (replaces discoverBiddersFromRegistries)
// ---------------------------------------------------------------------------

export async function discoverBiddersFromRelay(opts: {
  relayUrl: string;
  collection: string;
  log?: LoggerInterface;
  timeoutMs?: number;
}): Promise<string[]> {
  const { relayUrl, collection, log, timeoutMs } = opts;
  try {
    const url = `${relayUrl.replace(/\/+$/, "")}/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}&limit=1000`;
    log?.info("relay_discovery_query", { url, collection });
    const res = await fetch(url, { signal: timeoutMs ? AbortSignal.timeout(timeoutMs) : undefined });
    if (!res.ok) {
      log?.warn("relay_discovery_http_error", { relayUrl, status: res.status, collection });
      return [];
    }
    const data = await res.json() as { repos?: Array<{ did: string }> };
    const dids = [...new Set((data.repos ?? []).map((r) => r.did).filter(Boolean))];
    log?.info("relay_discovery_result", { relayUrl, collection, count: dids.length });
    return dids;
  } catch (err) {
    log?.warn("relay_discovery_error", { relayUrl, collection, error: String(err) });
    return [];
  }
}

/**
 * Query multiple atproto relays for bidders. Each relay failure is logged but
 * non-blocking — results from all successful relays are unioned.
 */
export async function discoverBiddersFromRelays(opts: {
  relayUrls: string[];
  collection: string;
  log?: LoggerInterface;
  timeoutMs?: number;
}): Promise<string[]> {
  const { relayUrls, collection, log, timeoutMs } = opts;
  if (relayUrls.length === 0) return [];
  const results = await Promise.all(
    relayUrls.map((url) => discoverBiddersFromRelay({ relayUrl: url, collection, log, timeoutMs })),
  );
  const all = results.flat();
  return [...new Set(all)];
}

/**
 * Relay visibility result — whether at least one relay that supports
 * listReposByCollection has indexed the bidder's offering.
 */
export interface RelayVisibilityResult {
  ok: boolean;
  /** Relays that support listReposByCollection (returned 200, not 404). */
  capableRelays: string[];
  /** Relays that returned the bidder's DID in the collection index. */
  indexedBy: string[];
  /** Relays that failed probing or don't support the endpoint. */
  failures: Array<{ url: string; reason: string }>;
}

/**
 * Verify the bidder's offering is discoverable through at least one relay
 * that supports listReposByCollection. Probes each relay to detect capability
 * (200 = supported, 404 = skip), then polls capable relays until the bidder's
 * DID appears in the collection index or the poll budget expires.
 *
 * Does NOT call requestCrawl — registration is handled by the caller
 * (hono-bidder's registerPdsWithRelay). This function runs after beginServe()
 * when the offering record exists in the PDS repo.
 */
export async function verifyRelayVisibility(opts: {
  relayUrls: string[];
  bidderDid: string;
  collection: string;
  log?: LoggerInterface;
  pollTimeoutMs?: number;
  pollIntervalMs?: number;
}): Promise<RelayVisibilityResult> {
  const { relayUrls, bidderDid, collection, log, pollTimeoutMs = 15_000, pollIntervalMs = 2_000 } = opts;
  const failures: RelayVisibilityResult["failures"] = [];

  // Phase 1: Probe which relays support listReposByCollection
  const collectionPath = `/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}`;
  const capableRelays: string[] = [];
  for (const url of relayUrls) {
    try {
      const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
      if (res.ok) {
        capableRelays.push(url);
        log?.info("relay_capable", { url });
      } else if (res.status === 404) {
        log?.info("relay_no_collection_support", { url });
        failures.push({ url, reason: "listReposByCollection not supported (404)" });
      } else {
        failures.push({ url, reason: `HTTP ${res.status}` });
      }
    } catch (err) {
      failures.push({ url, reason: String(err) });
    }
  }

  if (capableRelays.length === 0) {
    log?.warn("relay_no_capable_relays", { total: relayUrls.length, failures });
    return { ok: false, capableRelays: [], indexedBy: [], failures };
  }
  log?.info("relay_capable_relays", { count: capableRelays.length, relays: capableRelays });

  // Phase 2: Poll capable relays until bidder's DID appears
  const deadline = Date.now() + pollTimeoutMs;
  const indexedBy: string[] = [];
  while (Date.now() < deadline && indexedBy.length === 0) {
    for (const url of capableRelays) {
      try {
        const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
        if (!res.ok) continue;
        const body = await res.json() as { repos?: Array<{ did: string }> };
        if (body.repos?.some((r) => r.did === bidderDid)) {
          indexedBy.push(url);
        }
      } catch { /* probe failed, try next relay */ }
    }
    if (indexedBy.length === 0) {
      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }
  }

  const ok = indexedBy.length > 0;
  if (!ok) {
    log?.warn("relay_visibility_timeout", { capableRelays, pollTimeoutMs, failures });
  } else {
    log?.info("relay_visibility_confirmed", { indexedBy, capableRelays });
  }
  return { ok, capableRelays, indexedBy, failures };
}

/**
 * Auto-discover relay URLs from $ATPROTO_DID's repo records. Reads
 * com.publicdomainrelay.temp.market.relays records, extracts the `relays`
 * string arrays, deduplicates them, and returns the union.
 * Falls back to an empty array if $ATPROTO_DID is unset, the DID can't be
 * resolved, or no relay records exist.
 */
export async function autoDiscoverRelayUrls(opts: {
  atprotoDid?: string;
  log?: LoggerInterface;
}): Promise<string[]> {
  const did = opts.atprotoDid ?? Deno.env.get("ATPROTO_DID");
  if (!did) return [];
  const log = opts.log;
  log?.info("relay_autodiscover_lookup", { did });
  try {
    const resolver = new IdResolver();
    const doc = await resolver.did.resolve(did);
    if (!doc) {
      log?.warn("relay_autodiscover_did_unresolvable", { did });
      return [];
    }
    const pdsUrl = getPdsEndpoint(doc);
    if (!pdsUrl) {
      log?.warn("relay_autodiscover_no_pds", { did });
      return [];
    }
    const records = await listRecordsAll(pdsUrl, did, RELAYS_NSID);
    const urls: string[] = [];
    for (const r of records) {
      const relays = (r.value as Record<string, unknown>).relays;
      if (Array.isArray(relays)) {
        for (const item of relays) {
          if (typeof item === "string" && item.trim() && (item.startsWith("https://") || item.startsWith("http://"))) urls.push(item.trim());
        }
      }
    }
    const deduped = [...new Set(urls)];
    log?.info("relay_autodiscover_result", { did, sources: records.length, urls: deduped.length });
    return deduped;
  } catch (err) {
    log?.warn("relay_autodiscover_error", { did, error: String(err) });
    return [];
  }
}

// ---------------------------------------------------------------------------
// createRequesterPDS — adapted from hono-bidder pattern + reference server.ts
// ---------------------------------------------------------------------------

export async function createRequesterPDS(
  opts: PDSOptions,
): Promise<RequesterPDSImpl> {
  const logger: StructuredLoggerInterface = opts.logger;
  const serve = opts.serve;
  const privateKeyHex = opts.privateKeyHex ?? "";
  const plcDirectoryUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const ingressProxyHost = opts.ingressProxyHost ?? "xrpc.fedproxy.com";
  const label = opts.label ?? "requester";

  // ── keypair ──────────────────────────────────────────────────────────

  const keypair = privateKeyHex
    ? await Secp256k1Keypair.import(privateKeyHex)
    : await Secp256k1Keypair.create({ exportable: true });

  const privateKeyHexFinal = privateKeyHex ||
    Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

  // ── attestation keypair ───────────────────────────────────────────────

  const attestationKp = await loadOrGenerateKeypair(privateKeyHexFinal);

  // ── did:plc registration ─────────────────────────────────────────────

  const plc = new PlcClient({ baseUrl: plcDirectoryUrl });
  const signingKeyDid = keypair.did();
  const epHost = ingressProxyHost.replace(/:\d+$/, "");

  const { did, op } = await createGenesisOp({
    rotationKeys: [signingKeyDid],
    verificationMethods: {
      atproto: signingKeyDid,
      attestation: attestationKp.did(),
    },
    alsoKnownAs: [
      `at://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
    ],
    services: {
      atproto_pds: {
        type: "AtprotoPersonalDataServer",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
      pdr_temp_market: {
        type: "PDRTempMarket",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
      pdr_temp_compute_event: {
        type: "PDRTempComputeEvent",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
      requester_associate: {
        type: "PDRRequesterAssociate",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  try {
    await plc.resolve(did);
    logger.info("did_plc_already_registered", { did, label });
  } catch (err) {
    if (err instanceof PlcNotFoundError) {
      logger.info("did_plc_registering", { did, label });
      await plc.submitOp(did, op);
      logger.info("did_plc_registered", { did, label });
    } else {
      throw err;
    }
  }

  // ── signer ───────────────────────────────────────────────────────────

  const signer: Signer = {
    did: () => did,
    sign: (bytes) => keypair.sign(bytes),
  };

  // ── pending bids ─────────────────────────────────────────────────────

  const pendingBids: Map<string, CollectedBid[]> = new Map();

  // ── contract state (vm identity tracking) ───────────────────────────

  interface ContractState {
    receiptUri: string;
    receiptCid: string;
    winnerDid: string;
    identities: string[];  // active compute identities (did:key)
    revoked: string[];     // previously active, now revoked
  }
  const activeContracts = new Map<string, ContractState>();

  // ── association confirmation (webapp calls this before RFP) ─────────
  let resolveAssociateCalled: ((callerDid: string) => void) | null = null;
  const associateCalled = new Promise<string>((r) => { resolveAssociateCalled = r; });
  let resolveAssociationApproved: (() => void) | null = null;
  let rejectAssociationApproved: ((err: Error) => void) | null = null;
  const associationApproved = new Promise<void>((resolve, reject) => {
    resolveAssociationApproved = resolve;
    rejectAssociationApproved = reject;
  });

  // ── repo factory ─────────────────────────────────────────────────────

  const baseOrigin = `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${ingressProxyHost}`;

  const store = opts.storagePath
    ? await DenoKvStorage.create(opts.storagePath)
    : new MemoryStorage();

  const { app, api } = createRepoFactory({
    storage: store,
    signer,
    baseOrigin,
    didWebServices: [
      { id: "pdr_temp_market", type: "PDRTempMarket" },
      { id: "pdr_temp_compute_event", type: "PDRTempComputeEvent" },
      { id: "requester_associate", type: "PDRRequesterAssociate" },
    ],
  });

  // ── request/response logging middleware ──────────────────────────────

  app.use("*", async (c: { req: { method: string; url: string }; res: { status: number; clone(): { text(): Promise<string> } } }, next: () => Promise<void>) => {
    const method = c.req.method;
    const path = new URL(c.req.url).pathname;
    const start = Date.now();
    await next();
    const status = c.res.status;
    const durationMs = Date.now() - start;
    const event = status >= 400 ? "response_error" : "response";
    logger.info(event, { method, path, status, durationMs, label });
  });

  // ── relay (WS connect deferred to serve.beginServe -> relay.onServe) ──

  const skipIngress = opts.skipIngress ?? false;
  const relay = skipIngress
    ? { ingressRef: "", ingressUrl: "", ingressHost: "", close() {}, onServe: async () => {} } as IngressRef
    : createIngress({ logger, ingressProxyHost, signer, keypair, label });

  // ── submitBid handler ────────────────────────────────────────────────

  const idResolver = new IdResolver({ plcUrl: plcDirectoryUrl });

  const onBid: SubmitBidCallback = ({ uri, cid, record, issuerDid }) => {
    const rfpUri = (record.rfp as StrongRef | undefined)?.uri;
    if (!rfpUri) return;
    const queue = pendingBids.get(rfpUri) ?? [];
    queue.push({ did: issuerDid ?? "unknown", uri, cid, record: record as unknown as Record<string, unknown> });
    pendingBids.set(rfpUri, queue);
    logger.info("submitBid_queued", { callerDid: issuerDid, uri, rfpUri, label });
  };

  const bidHandler = createSubmitBidHandler({
    deps: {
      hostname: (req: Request) => {
        const host = req.headers.get("host") ?? req.headers.get("x-forwarded-host");
        return host ? host.split(":")[0] : (relay.ingressHost || ingressProxyHost);
      },
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
    },
    serviceIds: ["pdr_temp_market"],
    onBid,
  });
  app.post(`/xrpc/${SUBMIT_BID_NSID}`, (c: { req: { raw: Request } }) => bidHandler(c.req.raw));

  // ── submitEvent handler ──────────────────────────────────────────────

  const submitEventHandler = createSubmitEventHandler({
    deps: {
      hostname: (req) => relay.ingressHost || ingressProxyHost,
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
      log: ((level: string, message: string, meta?: Record<string, unknown>) => {
        const l = level as "info" | "warn" | "error" | "debug";
        logger[l]?.(message, meta ?? {});
      }) as unknown as (level: string, message: string, meta?: Record<string, unknown>) => void,
    },
    callbacks: {
      pdr_temp_compute_event: {
        // Handle vm.registerIdentity events
        "com.publicdomainrelay.temp.compute.events.vm.registerIdentity": async (ctx) => {
          const evt = ctx.event as any;
          const receiptKey = `${evt.receipt.uri}#${evt.receipt.cid}`;
          const identity = evt.payload?.computeIdentity;
          if (!identity) return;

          let state = activeContracts.get(receiptKey);
          if (!state) {
            state = { receiptUri: evt.receipt.uri, receiptCid: evt.receipt.cid, winnerDid: ctx.issuerDid, identities: [], revoked: [] };
            activeContracts.set(receiptKey, state);
          }
          // Rotate: old identities become revoked, new becomes active
          if (state.identities.length > 0) state.revoked.push(...state.identities);
          state.identities = [identity];
          logger.info("registerIdentity: updated contract state", { receiptKey, identity });
        },
        // Handle vm.onNetwork events
        "com.publicdomainrelay.temp.compute.events.vm.onNetwork": async (ctx) => {
          const evt = ctx.event as any;
          logger.info("vm.onNetwork received", { receiptKey: `${evt.receipt.uri}#${evt.receipt.cid}` });
        },
      },
    },
    background: true,
  });
  app.post(`/xrpc/${SUBMIT_EVENT_NSID}`, (c) => submitEventHandler(c.req.raw));

  // ── associateConfirm (webapp calls to confirm requester association) ──
  app.post(`/xrpc/${ASSOCIATE_CONFIRM_NSID}`, async (c) => {
    const authHeader = c.req.header("Authorization");
    if (!authHeader) return c.json({ error: "Unauthorized" }, 401);
    try {
      const auth = await verifyServiceAuth({
        authHeader,
        hostname: relay.ingressHost || ingressProxyHost,
        lxm: ASSOCIATE_CONFIRM_NSID,
        serviceIds: ["requester_associate"],
        extraAudienceDids: [did],
        idResolver,
      });
      resolveAssociateCalled?.(auth.issuerDid);
      // Wait for CLI user to approve/reject before responding to webapp
      await associationApproved;
      // Persist association in our own repo so it survives restarts
      // (mirrors bidder pattern: badgeBlueKeys with challenge=self, keyId=caller)
      await api.applyWrites(did, [{
        action: "create",
        collection: BADGE_BLUE_KEYS_NSID,
        rkey: TID.next().toString(),
        record: {
          $type: BADGE_BLUE_KEYS_NSID,
          keyId: auth.issuerDid,
          challenge: did,
          service: "requester_associate",
          createdAt: new Date().toISOString(),
        },
      }]);
      return c.json({ ok: true, requesterDid: did });
    } catch (err) {
      return c.json({ error: String(err) }, 401);
    }
  });

  // ── mount the repo app + relay on the shared serve handle ────────────

  serve.app.route("/", app as never);
  serve.addRelay(relay);

  // ── helpers ──────────────────────────────────────────────────────────

  async function createRepoRecord(
    collection: string,
    record: Record<string, unknown>,
  ): Promise<{ uri: string; cid: string }> {
    const rkey = TID.next().toString();
    await api.applyWrites(did, [{ action: "create", collection, rkey, record }]);
    const rec = await api.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function createSignedRepoRecord(
    collection: string,
    record: Record<string, unknown>,
    aKp: { did(): string; privateKey: { bytes: Uint8Array; toBytes?(): Uint8Array } },
    issuer?: string,
  ): Promise<{ uri: string; cid: string }> {
    const rkey = TID.next().toString();
    const att = attestationFor(aKp as unknown as AttestationKeypair, issuer);
    const entry = await att.sign({ record, repository: did }) as InlineAttestation;
    const signed = { ...record, signatures: [toStorableEntry(entry)] };
    await api.applyWrites(did, [{ action: "create", collection, rkey, record: signed }]);
    const rec = await api.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function resolveBidderEndpoint(
    endpointUrl: string,
  ): Promise<{ targetUrl: string; audDid: string } | null> {
    if (endpointUrl.startsWith("http://") || endpointUrl.startsWith("https://")) {
      return {
        targetUrl: `${endpointUrl.replace(/\/+$/, "")}/xrpc`,
        audDid: `did:web:${new URL(endpointUrl).host}`,
      };
    }
    if (endpointUrl.startsWith("did:")) {
      const didPart = endpointUrl.split("#")[0];
      const svcDoc = await idResolver.did.resolve(didPart);
      const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : "pdr_temp_market";
      const svc = (svcDoc?.service ?? []).find((s: { id: string }) => s.id === `#${svcId}`);
      const svcEndpoint = (svc as { serviceEndpoint?: string } | undefined)?.serviceEndpoint;
      if (!svcEndpoint) return null;
      const svcHost = new URL(svcEndpoint).host;
      return {
        targetUrl: `${svcEndpoint.replace(/\/+$/, "")}/xrpc`,
        audDid: `did:web:${svcHost}`,
      };
    }
    return null;
  }

  async function callBidder(
    targetBase: string,
    nsid: string,
    lxm: string,
    audDid: string,
    body: Record<string, unknown>,
  ): Promise<{ status: number; ok: boolean; body: unknown }> {
    const token = await signServiceAuth(signer, { aud: audDid, lxm });
    const url = `${targetBase}/${nsid}`;
    const fetchBody = JSON.stringify(body);
    console.log(JSON.stringify({ event: "callBidder_pre", url, bodyType: typeof fetchBody, bodyLen: fetchBody.length, tokenLen: token.length }));
    const res = await fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body: fetchBody,
      signal: AbortSignal.timeout(10_000),
    });
    const resText = await res.text();
    let resBody: unknown;
    try { resBody = JSON.parse(resText); } catch { resBody = resText; }
    return { status: res.status, ok: res.ok, body: resBody };
  }

  return {
    did,
    app,
    signer,
    keypair,
    api,
    serve,
    relay,
    get ingressRef(): string { return relay.ingressRef; },
    get ingressUrl(): string { return relay.ingressUrl; },
    get ingressHost(): string { return relay.ingressHost; },
    get relaySubdomain(): string { return relay.ingressHost; },
    beginServe: () => serve.beginServe(),
    pendingBids,
    createRepoRecord,
    createSignedRepoRecord,
    resolveBidderEndpoint,
    callBidder,
    attestationKp,
    privateKeyHex: privateKeyHexFinal,
    associateCalled,
    approveAssociation: () => { resolveAssociationApproved?.(); },
    rejectAssociation: (err: Error) => { rejectAssociationApproved?.(err); },
    dispose: async () => { store.close(); },
  };
}

// ---------------------------------------------------------------------------
// SSH session provider
// ---------------------------------------------------------------------------

function sshTunnelArgs(
  privateKeyPath: string,
  fqdn: string,
  proxyCmdOverride?: string,
): string[] {
  return [
    "-o", `ProxyCommand=${proxyCmdOverride ?? `websocat --binary wss://${fqdn}`}`,
    "-o", `IdentityFile=${privateKeyPath}`,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "LogLevel=ERROR",
  ];
}

export function createSshSessionProvider(
  logger?: StructuredLoggerInterface,
  opts?: { proxyCommandFn?: (fqdn: string) => string },
): SshSessionProvider {
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
  async function generateKeypair(
    vmName: string,
  ): Promise<{ publicKey: string; privateKeyPath: string }> {
    const dir = await Deno.makeTempDir({ prefix: `ssh-${vmName}-` });
    const privateKeyPath = `${dir}/id_ed25519`;
    const cmd = new Deno.Command("ssh-keygen", {
      args: ["-t", "ed25519", "-N", "", "-C", `root@${vmName}`, "-f", privateKeyPath],
      stdout: "null",
      stderr: "piped",
    });
    const { code, stderr } = await cmd.output();
    if (code !== 0) {
      throw new Error(`ssh-keygen failed: ${new TextDecoder().decode(stderr)}`);
    }
    const publicKey = (await Deno.readTextFile(`${privateKeyPath}.pub`)).trim();
    return { publicKey, privateKeyPath };
  }

  async function pollReady(
    privateKeyPath: string,
    fqdn: string,
    timeoutMs: number,
  ): Promise<boolean> {
    const proxyCmd = opts?.proxyCommandFn?.(fqdn);
    const deadline = Date.now() + timeoutMs;
    let attempt = 0;
    while (Date.now() < deadline) {
      attempt++;
      const cmd = new Deno.Command("ssh", {
        args: [
          ...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd),
          "-o", "BatchMode=yes",
          "-o", "ConnectTimeout=10",
          `root@${fqdn}`,
          "true",
        ],
        stdout: "null",
        stderr: "piped",
      });
      const { code, stderr } = await cmd.output();
      if (code === 0) {
        log("vm_ssh_ready", { fqdn, attempt });
        return true;
      }
      log("vm_ssh_poll", { fqdn, attempt, code, error: new TextDecoder().decode(stderr).trim().slice(0, 200) });
      await new Promise((r) => setTimeout(r, 5000));
    }
    log("vm_ssh_timeout", { fqdn, timeoutMs });
    return false;
  }

  async function runSession(
    privateKeyPath: string,
    fqdn: string,
    program: string,
  ): Promise<number> {
    const proxyCmd = opts?.proxyCommandFn?.(fqdn);
    const interactive = Deno.stdin.isTerminal();
    const args = [...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd)];
    if (interactive) {
      args.push("-tt", `root@${fqdn}`);
    } else {
      args.push(`root@${fqdn}`, program);
    }
    const cmd = new Deno.Command("ssh", { args, stdin: "inherit", stdout: "inherit", stderr: "inherit" });
    const child = cmd.spawn();
    const { code } = await child.status;
    return code;
  }

  return { generateKeypair, pollReady, runSession };
}

// ---------------------------------------------------------------------------
// direct SSH (no relay tunnel — used with --no-ingress-proxy)
// ---------------------------------------------------------------------------

async function pollDirectSsh(host: string, port: number, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const conn = await Deno.connect({ hostname: host, port });
      conn.close();
      return true;
    } catch {
      await new Promise((r) => setTimeout(r, 5000));
    }
  }
  return false;
}

async function runDirectSsh(privateKeyPath: string, host: string, program: string): Promise<number> {
  const args = [
    "-o", `IdentityFile=${privateKeyPath}`,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "LogLevel=ERROR",
  ];
  if (Deno.stdin.isTerminal()) {
    args.push("-tt", `root@${host}`);
  } else {
    args.push(`root@${host}`, program);
  }
  const cmd = new Deno.Command("ssh", { args, stdin: "inherit", stdout: "inherit", stderr: "inherit" });
  const child = cmd.spawn();
  const { code } = await child.status;
  return code;
}

// ---------------------------------------------------------------------------
// websocat bootstrap
// ---------------------------------------------------------------------------

export async function ensureWebsocat(logger?: StructuredLoggerInterface): Promise<void> {
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
  const which = new Deno.Command("which", { args: ["websocat"], stdout: "null", stderr: "null" });
  if ((await which.output()).code === 0) {
    log("websocat_found", { source: "system" });
    return;
  }

  const plat = Deno.build.os;
  const arch = Deno.build.arch;
  const triple: Record<string, Record<string, string>> = {
    linux: { x86_64: "x86_64-unknown-linux-musl", aarch64: "aarch64-unknown-linux-musl" },
    darwin: { x86_64: "x86_64-apple-darwin", aarch64: "aarch64-apple-darwin" },
  };
  const target = triple[plat]?.[arch];
  if (!target) {
    log("websocat_unsupported", { plat, arch });
    return;
  }

  const version = "v1.14.0";
  const url = `https://github.com/vi/websocat/releases/download/${version}/websocat.${target}`;

  const dir = await Deno.makeTempDir({ prefix: "websocat-" });
  const binPath = `${dir}/websocat`;
  log("websocat_downloading", { url });

  const resp = await fetch(url);
  if (!resp.ok || !resp.body) {
    log("websocat_download_failed", { status: resp.status });
    return;
  }

  const file = await Deno.open(binPath, { write: true, create: true, mode: 0o755 });
  await resp.body.pipeTo(file.writable);
  log("websocat_downloaded", { path: binPath });

  Deno.env.set("PATH", `${dir}:${Deno.env.get("PATH") ?? ""}`);
  log("websocat_path_updated", { dir });
}

// ---------------------------------------------------------------------------
// runComputeContract — adapted from reference server.ts
// ---------------------------------------------------------------------------

export async function runComputeContract(
  pds: RequesterPDS,
  opts: ContractFlowOptions & {
    sshProvider?: SshSessionProvider;
    relayUrls?: string[];
    relayUrl?: string; // deprecated: use relayUrls
    signer?: Signer;
    offeringWatcherDids?: () => string[];
    logger?: StructuredLoggerInterface;
    payloadFactory?: () => Promise<{ uri: string; cid: string }>;
    /** ATProto event streams client for cross-party firehose event discovery. */
    eventStreams?: import("@publicdomainrelay/atproto-event-streams-client").ATProtoEventStreamsClient;
  } = {},
): Promise<ContractFlowResult> {
  const vmName = opts.vmName ?? `compute-${randomHex8()}`;
  const bidWindowSec = opts.bidWindowSec ?? 30;
  const skipSsh = opts.skipSsh ?? false;
  const execProgram = opts.execProgram ?? "bash";
  const keepVm = opts.keepVm ?? false;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 300;
  const extraBidderDids = opts.extraBidderDids ?? [];
  const denyBidderDids = opts.denyBidderDids ?? [];
  const policyMode = opts.policyMode;
  const policyEngineEndpoint = opts.policyEngineEndpoint;
  const sshProvider = opts.sshProvider ?? createSshSessionProvider(
    opts.logger,
    { proxyCommandFn: opts.sshProxyCommandFn },
  );
  const relayUrl = opts.relayUrl;
  const relayUrls = opts.relayUrls ?? (relayUrl ? [relayUrl] : []);
  const signer = opts.signer;

  // Firehose watcher for ALL market collections — starts before RFP creation
  // so we don't miss bids/accepts/events. Active until VM deletion.
  const firehoseDiscoveredBids = new Map<string, CollectedBid[]>();
  let bidWatcher: { close(): void } | undefined;
  let _rfpUri = "";

  const logger = opts.logger;
  const eventStreams = opts.eventStreams;
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));

  // Start firehose watcher BEFORE RFP creation — watch BID + ACCEPT + RECEIPT.
  if (eventStreams) {
    const watched = [BID_NSID, ACCEPT_NSID, EVENT_NSID, COMPUTE_EVENTS_VM_ONNETWORK_NSID];
    bidWatcher = eventStreams.watch({
      wantedCollections: watched,
      onEvent: (e) => {
        if (e.operation !== "create") return;
        if (!_rfpUri) return;
        // BID_NSID: collect bids referencing our RFP
        if (e.collection === BID_NSID) {
        (async () => {
          try {
            const doc = await idResolver.did.resolve(e.did);
            if (!doc) return;
            const pdsUrl = getPdsEndpoint(doc);
            if (!pdsUrl) return;
            const recordUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(e.did)}&collection=${encodeURIComponent(e.collection)}&rkey=${e.rkey}`;
            const res = await fetch(recordUrl);
            const data = await res.json();
            const val = data.value as Record<string, unknown> | undefined;
            if (!val) return;
            const rfpRef = val.rfp as { uri?: string } | undefined;
            if (rfpRef?.uri !== _rfpUri) return;
            const queue = firehoseDiscoveredBids.get(_rfpUri) ?? [];
            queue.push({ did: e.did, uri: data.uri as string, cid: data.cid as string, record: val });
            firehoseDiscoveredBids.set(_rfpUri, queue);
            log("firehose_bid_discovered", { bidUri: data.uri, rfpUri: _rfpUri, bidderDid: e.did });
          } catch { /* best-effort */ }
        })();
        }
        // EVENT_NSID: extract vm.onNetwork IP for direct SSH when ingress proxy unavailable
        if (e.collection === EVENT_NSID) {
          (async () => {
            try {
              const doc = await idResolver.did.resolve(e.did);
              if (!doc) return;
              const pdsUrl = getPdsEndpoint(doc);
              if (!pdsUrl) return;
              const recordUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(e.did)}&collection=${encodeURIComponent(e.collection)}&rkey=${e.rkey}`;
              const res = await fetch(recordUrl);
              const data = await res.json();
              const val = data.value as Record<string, unknown> | undefined;
              if (!val) return;
              // Check receipt matches our contract
              const receiptRef = val.receipt as { uri?: string } | undefined;
              if (!receiptRef?.uri) return;
              // Resolve payload to check if it's a vm.onNetwork record
              const payloadRef = val.payload as { uri?: string } | undefined;
              if (!payloadRef?.uri) return;
              const payloadColl = payloadRef.uri.split("/")[3];
              if (payloadColl !== COMPUTE_EVENTS_VM_ONNETWORK_NSID) return;
              // Fetch the vm.onNetwork record for the IP
              const [payloadRepo] = [payloadRef.uri.split("/")[2]];
              const rkey = payloadRef.uri.split("/").pop()!;
              const payloadUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(payloadRepo)}&collection=${encodeURIComponent(payloadColl)}&rkey=${rkey}`;
              const payloadRes = await fetch(payloadUrl);
              const payloadData = await payloadRes.json();
              const address = (payloadData.value as Record<string, unknown>)?.address as string | undefined;
              if (address) {
                directVmHost = address;
                log("vm_ip_discovered", { address, eventUri: data.uri });
              }
            } catch { /* best-effort */ }
          })();
        }
        // COMPUTE_EVENTS_VM_ONNETWORK_NSID: direct vm.onNetwork record (no EVENT_NSID wrapper)
        if (e.collection === COMPUTE_EVENTS_VM_ONNETWORK_NSID) {
          (async () => {
            try {
              const doc = await idResolver.did.resolve(e.did);
              if (!doc) return;
              const pdsUrl = getPdsEndpoint(doc);
              if (!pdsUrl) return;
              const recordUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(e.did)}&collection=${encodeURIComponent(e.collection)}&rkey=${e.rkey}`;
              const res = await fetch(recordUrl);
              const data = await res.json();
              const address = (data.value as Record<string, unknown>)?.address as string | undefined;
              if (address) {
                directVmHost = address;
                log("vm_ip_discovered_direct", { address, uri: data.uri });
              }
            } catch { /* best-effort */ }
          })();
        }
        // REGISTER_IDENTITY: extract iroh nodeId
        if (e.collection === COMPUTE_EVENTS_VM_REGISTER_IDENTITY_NSID) {
          (async () => {
            try {
              const doc = await idResolver.did.resolve(e.did);
              if (!doc) return;
              const pdsUrl = getPdsEndpoint(doc);
              if (!pdsUrl) return;
              const recordUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(e.did)}&collection=${encodeURIComponent(e.collection)}&rkey=${e.rkey}`;
              const res = await fetch(recordUrl);
              const data = await res.json();
              const identity = (data.value as Record<string, unknown>)?.computeIdentity as Record<string, unknown> | undefined;
              if (identity?.nodeId) {
                log("iroh_node_id_firehose", { nodeId: identity.nodeId });
                pds.resolveIrohNodeId?.(String(identity.nodeId));
              }
            } catch { /* best-effort */ }
          })();
        }
      },
      log: logger,
    });
    log("firehose_market_watch_started", { relayCount: eventStreams.relays.length, jetstreamCount: eventStreams.jetstreams.length, collections: watched });
  }

  // Relay WS connect happens in serve.beginServe(); ingressRef is set by then.
  const ingressRef = pds.relay.ingressRef;
  const ingressProxyHost = opts.ingressProxyHost ??
    (pds.relaySubdomain.includes(".")
      ? pds.relaySubdomain.substring(pds.relaySubdomain.indexOf(".") + 1)
      : "xrpc.fedproxy.com");
  const relaySubdomain = pds.relaySubdomain.endsWith("." + ingressProxyHost)
    ? pds.relaySubdomain.slice(0, pds.relaySubdomain.length - ingressProxyHost.length - 1)
    : pds.relaySubdomain.split(".")[0];
  const fedingressHost = opts.fedingressHost ?? "fedproxy.com";

  log("relay_ready_for_rfp", { ingressRef });

  let cloudInit = "";
  let privateKeyPath = "";
  let vmFqdn = "";
  let directVmHost: string | undefined; // discovered from vm.onNetwork firehose event for direct SSH
  let guestDidPlc = "";
  let guestPrivateKeyHex = "";
  const plcGuest = new PlcClient({ baseUrl: "https://plc.directory" });

  if (!skipSsh) {
    // ── guest identity: generate secp256k1 keypair → did:plc for short subdomain ──
    const guestKeypair = await Secp256k1Keypair.create({ exportable: true });
    guestPrivateKeyHex = Array.from(await guestKeypair.export())
      .map((b) => b.toString(16).padStart(2, "0")).join("");
    const guestSigningKeyDid = guestKeypair.did(); // did:key:z...

    // Derive did:plc from guest keypair (short subdomain, fits DNS 63-char limit)
    const guestSubdomain = guestSigningKeyDid.replaceAll(":", "-").toLowerCase();
    const { did: guestDid, op: guestOp } = await createGenesisOp({
      rotationKeys: [guestSigningKeyDid],
      verificationMethods: { atproto: guestSigningKeyDid },
      services: {
        atproto_pds: {
          type: "AtprotoPersonalDataServer",
          endpoint: `https://${guestSubdomain}.${ingressProxyHost}`,
        },
      },
      sign: (bytes) => guestKeypair.sign(bytes),
    });
    guestDidPlc = guestDid;

    // Register did:plc on PLC directory
    try {
      await plcGuest.resolve(guestDidPlc);
      log("guest_did_plc_already_registered", { did: guestDidPlc });
    } catch (err) {
      if (err instanceof PlcNotFoundError) {
        await plcGuest.submitOp(guestDidPlc, guestOp);
        log("guest_did_plc_registered", { did: guestDidPlc });
      } else {
        throw err;
      }
    }

    // Compute FQDN from guest's did:plc subdomain
    const guestDidPlcSubdomain = guestDidPlc.replaceAll(":", "-").toLowerCase();
    vmFqdn = `${guestDidPlcSubdomain}.${ingressProxyHost}`;

    const ssh = await sshProvider.generateKeypair(vmName);
    privateKeyPath = ssh.privateKeyPath;
    log("ssh_keypair_generated", {
      privateKeyPath,
      publicKey: ssh.publicKey,
      vmFqdn,
      guestDidPlc,
      hint: `ssh -i ${privateKeyPath} -o ProxyCommand='websocat --binary - ws-c:tcp:${ingressProxyHost}:80 --ws-c-uri=ws://${guestDidPlcSubdomain}.${ingressProxyHost}/xrpc/com.fedproxy.temp.xrpc.tunnel' root@${vmFqdn}`,
    });

    // Choose transport: tunnel-subscriber when ingress relay exists, fedproxy-client otherwise
    const hasIngress = ingressRef && ingressRef.length > 0;
    if (hasIngress) {
      const txCtx: TunnelCloudInitContext = {
        ingressProxyHost,
        audHost: ingressProxyHost,
        privateKeyHex: guestPrivateKeyHex,
        jsrUrl: "http://jsr:5556",
        sshAuthorizedKey: ssh.publicKey,
      };
      cloudInit = buildTunnelUserData(txCtx);
    } else {
      const didPlcKey = pds.did.startsWith("did:plc:")
        ? pds.did.slice("did:plc:".length)
        : pds.did;
      const ctx: CloudInitContext = {
        vmName,
        didPlc: pds.did,
        didPlcKey,
        relayHost: ingressProxyHost,
        xrpcRelaySubdomain: relaySubdomain,
        sshAuthorizedKey: ssh.publicKey,
      };
      cloudInit = opts.baseUserData
        ? patchDefaultUserData(opts.baseUserData, ctx)
        : buildDefaultUserData(ctx);
    }
    if (opts.userDataFactory) {
      cloudInit = opts.userDataFactory(ssh.publicKey);
    }
  } else {
    cloudInit = `#cloud-config
packages:
  - curl
runcmd:
  - echo "test VM (no sshd) ready" | tee /tmp/ready
`;
  }

  // 1. Create payload record (compute.vm by default, or via payloadFactory).
  let vmUri: string;
  let vmCid: string;
  if (opts.payloadFactory) {
    const ref = await opts.payloadFactory();
    vmUri = ref.uri;
    vmCid = ref.cid;
  } else {
    const ref = await pds.createRepoRecord(COMPUTE_VM_NSID, {
      $type: COMPUTE_VM_NSID,
      role: vmName.trim() || "compute",
      user_data: cloudInit,
      createdAt: new Date().toISOString(),
    });
    vmUri = ref.uri;
    vmCid = ref.cid;
  }
  log("vm_record_created", { uri: vmUri, cid: vmCid });

  // 2. Create signed market.rfp.
  const rfpRecord: Record<string, unknown> = {
    $type: RFP_NSID,
    domain: "compute",
    payload: { $type: "com.atproto.repo.strongRef", uri: vmUri, cid: vmCid },
    submitBid: `${pds.did}#pdr_temp_market`,
    createdAt: new Date().toISOString(),
  };

  // Attach fulfillment policy if a policyMode is set.
  let policyRef: { uri: string; cid: string } | undefined;
  if (policyMode) {
    try {
      const { createPolicy } = await import("@publicdomainrelay/market-policy");
      const policy = createPolicy(policyMode, { signer: pds.signer ?? signer });
      if (policy) {
        const policyRecord = policy.buildPolicyRecord(pds.did, policyEngineEndpoint as string | undefined);
        policyRef = await pds.createRepoRecord(policy.policyNsid, policyRecord);
        rfpRecord.policy = { $type: "com.atproto.repo.strongRef", uri: policyRef.uri, cid: policyRef.cid };
        log("policy_attached", { policyMode, policyUri: policyRef.uri });
      }
    } catch (err) {
      log("policy_create_error", { error: String(err) });
    }
  }

  const rfpResult = await pds.createSignedRepoRecord(RFP_NSID, rfpRecord, pds.attestationKp, pds.did);
  const rfpUri = rfpResult.uri;
  const rfpCid = rfpResult.cid;
  _rfpUri = rfpUri; // so firehose watcher (started above) can match incoming bids
  log("rfp_created", { uri: rfpUri, cid: rfpCid, hasPolicy: !!policyRef });

  // 3. Discover bidder DIDs.
  const idResolver = new IdResolver({ plcUrl: opts.plcUrl });

  // 3a. Vouch-based discovery via DelegatedTrustResolver.
  // Reads requester's own badgeBlueKeys for requester_associate records,
  // resolves each associated DID's vouch records via VouchResolver.
  let vouchedDids: string[] = [];
  try {
    const publicVouchResolver = createTangledGraphVouchResolver({
      listRecords: (repo, coll) => listRecordsPublic(idResolver, repo, coll),
    });
    const localVouchResolver = createTangledGraphVouchResolver({
      listRecords: async (repo, coll) => {
        const result = await (pds as RequesterPDSImpl).api.listRecords(repo, coll);
        return (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
      },
    });
    const delegatedTrust = createBadgeBlueKeysDelegatedTrustResolver({
      vouchResolver: publicVouchResolver,
      listOwnRecords: async (collection, _opts) => {
        const result = await (pds as RequesterPDSImpl).api.listRecords(pds.did, collection);
        const records = (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
        for (const r of records) {
          log("delegated_trust_record", { challenge: r.value.challenge, service: r.value.service, keyId: r.value.keyId, selfDid: pds.did });
        }
        log("delegated_trust_badgeBlueKeys", { did: pds.did, collection, count: records.length });
        return records;
      },
    });
    const vouchedSet = await delegatedTrust.getDelegatedTrustedDids(pds.did);
    vouchedDids = [...vouchedSet];
    log("vouch_discovery", { count: vouchedDids.length });
  } catch (err) {
    log("vouch_discovery_error", { error: String(err) });
  }

  // 3b. Relay-based discovery (PRIMARY — relay IS the registry).
  // Merge configured relayUrls + auto-discovered from $ATPROTO_DID.
  const autoRelayUrls = await autoDiscoverRelayUrls({ log: logger });
  const allRelayUrls = [...new Set([...relayUrls, ...autoRelayUrls])];
  let relayDids: string[] = [];
  if (allRelayUrls.length > 0) {
    relayDids = await discoverBiddersFromRelays({ relayUrls: allRelayUrls, collection: OFFERING_NSID, log: logger, timeoutMs: 15_000 });
    if (relayDids.length > 0) log("relay_discovery", { count: relayDids.length, relays: allRelayUrls.length, configured: relayUrls.length, autodiscovered: autoRelayUrls.length });
    else log("relay_discovery_empty", { relays: allRelayUrls.length, configured: relayUrls.length, autodiscovered: autoRelayUrls.length });
  }

  // 3c. Live firehose offering watch (complements the relay index; catches
  // offerings the relay lagged or missed during this run).
  const watcherDids = opts.offeringWatcherDids?.() ?? [];
  if (watcherDids.length > 0) log("offering_watch_discovery", { count: watcherDids.length });

  const bidderDids = Array.from(new Set([...relayDids, ...watcherDids, ...vouchedDids, ...extraBidderDids]));
  const deniedSet = new Set(denyBidderDids);
  const filteredBidderDids = bidderDids.filter((d) => !deniedSet.has(d));
  log("bidder_discovery", { total: filteredBidderDids.length, relay: relayDids.length, watch: watcherDids.length, vouched: vouchedDids.length, extra: extraBidderDids.length, denied: bidderDids.length - filteredBidderDids.length });

  // 4. Submit RFP to each bidder (parallel across bidders, deduped by endpoint).
  const seen = new Set<string>();
  await Promise.allSettled(filteredBidderDids.map(async (bidderDid) => {
    try {
      const doc = await idResolver.did.resolve(bidderDid);
      if (!doc) return;
      const pdsUrl = getPdsEndpoint(doc);
      if (!pdsUrl) return;

      // Fetch offering records from bidder's PDS.
      const offerings = await listRecordsAll(pdsUrl, bidderDid, OFFERING_NSID);
      for (const offering of offerings) {
        const appliesTo = offering.value.appliesTo as string[] | undefined;
        const endpointUrl = offering.value.endpointUrl as string | undefined;
        if (!endpointUrl || !Array.isArray(appliesTo) || !appliesTo.includes(opts.appliesToNsid ?? COMPUTE_VM_NSID)) continue;

        const target = await pds.resolveBidderEndpoint(endpointUrl);
        if (!target) {
          log("bidder_unknown_endpoint", { endpointUrl });
          continue;
        }

        // Deduplicate: one RFP per (bidderDid, targetUrl) pair.
        const dedupKey = `${bidderDid}::${target.targetUrl}`;
        if (seen.has(dedupKey)) continue;
        seen.add(dedupKey);

        log("submitting_rfp", { bidderDid, endpointUrl });
        const r = await pds.callBidder(target.targetUrl, SUBMIT_RFP_NSID, SUBMIT_RFP_LXM, target.audDid, {
          rfpUri, rfpCid,
        });
        log("submitRfp_result", { bidderDid, status: r.status, ok: r.ok });
      }
    } catch (err) {
      log("bidder_error", { bidderDid, error: String(err) });
    }
  }));

  // 5. Wait for bids.
  log("waiting_for_bids", { bidWindowSec });
  await new Promise<void>((resolve) => setTimeout(resolve, bidWindowSec * 1000));

  const bids = pds.pendingBids.get(rfpUri) ?? [];
  pds.pendingBids.delete(rfpUri);

  // Merge firehose-discovered bids (deduped by URI).
  const fhBids = firehoseDiscoveredBids.get(rfpUri) ?? [];
  if (fhBids.length > 0) {
    const xrpcCount = bids.length;
    const existingUris = new Set(bids.map((b: CollectedBid) => b.uri));
    for (const fb of fhBids) {
      if (!existingUris.has(fb.uri)) {
        bids.push(fb);
        existingUris.add(fb.uri);
      }
    }
    log("firehose_bids_merged", { xrpcCount, firehoseNew: bids.length - xrpcCount, totalBids: bids.length });
  }

  // Clean up bid watcher — bids collected.
  bidWatcher?.close();
  log("bids_collected", { count: bids.length });

  if (bids.length === 0) {
    const result: ContractFlowResult = { event: "no_bids", error: `no bids received within ${bidWindowSec}s` };
    log("no_bids", result as unknown as Record<string, unknown>);
    return result;
  }

  // 6. Pick lowest-cost winner.
  const winner = bids.reduce((best, b) => {
    const cost = (n: CollectedBid) => Number((n.record.payload as Record<string, unknown> | undefined)?.cost ?? Infinity);
    return cost(b) < cost(best) ? b : best;
  }, bids[0]);
  log("winner", { uri: winner.uri, did: winner.did });

  // 6b. Authorize the VM to register its SSH host key. Resolve the winner's
  // bidConfig (wif.simple), then write a com.fedproxy.rbac record into our own
  // repo granting the VM (by wif subject) createRecord on com.fedproxy.sshPublicKey
  // for this VM's service name. The local PDS is served over the xrpc relay, so
  // the booting VM reaches it through the relay and publishes its host key —
  // exactly the reference compute-spa flow. Off unless opts.rbac (CLI default-on).
  if (opts.rbac && !skipSsh) {
    const bidConfigRef = (winner.record.config ?? winner.record.bidConfig) as { uri?: string; cid?: string } | undefined;
    if (bidConfigRef?.uri && bidConfigRef?.cid) {
      try {
        const resolver = createRecordResolver(idResolver);
        const cfg = await resolver.resolve({ uri: bidConfigRef.uri, cid: bidConfigRef.cid }) as Record<string, unknown>;
        const issuerUri = cfg.issuer_uri as string | undefined;
        const actx = cfg.actx as string | undefined;
        const subjectTemplate = cfg.subject as string | undefined;
        if (issuerUri && actx) {
          const serviceName = vmName.trim() || "compute";
          const rbacRecord = buildSshKeyRbacRecord({
            serviceName,
            issuerUri,
            actx,
            requesterDid: pds.did,
            subjectTemplate,
          });
          const { uri: rbacUri } = await pds.createRepoRecord(FEDPROXY_RBAC_NSID, rbacRecord);
          log("rbac_created", { uri: rbacUri, serviceName, issuerUri });
        } else {
          log("rbac_skipped", { reason: "bidConfig missing issuer_uri/actx", bidConfigUri: bidConfigRef.uri });
        }
      } catch (err) {
        log("rbac_skipped", { reason: String(err), bidConfigUri: bidConfigRef.uri });
      }
    } else {
      log("rbac_skipped", { reason: "winner bid has no bidConfig ref" });
    }
  }

  // 7. Evaluate policy against winner before accepting (policy_based only).
  if (policyRef && policyMode === DYNAMIC) {
    const { evaluateRfpPolicy } = await import("@publicdomainrelay/market-policy");
    const evalResult = await evaluateRfpPolicy({
      policyRef,
      subjectDid: winner.did,
      rootRequesterDid: pds.did,
      counterpartyDid: winner.did,
      resolve: async (ref) => {
        const resolver = createRecordResolver(idResolver);
        return await resolver.resolve(ref);
      },
      signer: pds.signer ?? signer,
      log: (level, msg, meta) => log(`policy_eval_${level}`, { msg, ...(meta ?? {}) }),
    });
    if (!evalResult.allow) {
      log("policy_rejected", { violations: evalResult.violations, winnerDid: winner.did });
      const result: ContractFlowResult = {
        event: "policy_rejected",
        error: `winner rejected by policy: ${evalResult.violations.map(v => v.msg).join("; ")}`,
        bids: bids.length,
      };
      return result;
    }
  }

  // 8. Create signed market.accept.
  const { uri: acceptUri, cid: acceptCid } = await pds.createSignedRepoRecord(ACCEPT_NSID, {
    $type: ACCEPT_NSID,
    rfp: { $type: "com.atproto.repo.strongRef", uri: rfpUri, cid: rfpCid },
    bid: { $type: "com.atproto.repo.strongRef", uri: winner.uri, cid: winner.cid },
    submitEvent: `${pds.did}#pdr_temp_compute_event`,
    createdAt: new Date().toISOString(),
  }, pds.attestationKp, pds.did);
  log("accept_created", { uri: acceptUri, cid: acceptCid });

  // 8. Submit accept to winning bidder.
  const submitAcceptTarget = winner.record.submitAccept as string | undefined;
  let receiptUri: string | undefined;
  let receiptCid: string | undefined;
  let submitEventRef: string | undefined;

  if (submitAcceptTarget) {
    const target = await pds.resolveBidderEndpoint(submitAcceptTarget);
    if (target) {
      log("submitting_accept", { target: submitAcceptTarget });
      const r = await pds.callBidder(target.targetUrl, SUBMIT_ACCEPT_NSID, SUBMIT_ACCEPT_LXM, target.audDid, {
        acceptUri, acceptCid,
      });
      const body = r.body as { id?: string; uri?: string; cid?: string; submitEvent?: string };
      receiptUri = body.uri;
      receiptCid = body.cid;
      submitEventRef = body.submitEvent;
      log("submitAccept_result", { status: r.status, receiptUri, receiptCid, submitEventRef });
    } else {
      log("accept_target_unresolvable", { submitAcceptTarget });
    }
  }

  // 8b. If no receipt from submitAccept XRPC, try firehose-based discovery.
  // Watch RECEIPT_NSID for a receipt whose accept.uri matches ours.
  if (!receiptUri && eventStreams) {
    log("receipt_firehose_fallback", { acceptUri });
    const receiptFromFirehose = await new Promise<{ receiptUri: string; receiptCid: string; submitEventRef?: string } | null>(
      (resolve) => {
        const timeoutMs = 30_000;
        let resolved = false;
        const timer = setTimeout(() => {
          if (!resolved) { resolved = true; watcher?.close(); resolve(null); }
        }, timeoutMs);

        let watcher: { close(): void } | undefined;
        watcher = eventStreams!.watch({
          wantedCollections: [RECEIPT_NSID],
          onEvent: async (e) => {
            if (resolved) return;
            if (e.operation !== "create") return;
            try {
              const doc = await idResolver.did.resolve(e.did);
              if (!doc) return;
              const pdsUrl = getPdsEndpoint(doc);
              if (!pdsUrl) return;
              const records = await listRecordsAll(pdsUrl, e.did, RECEIPT_NSID, { timeoutMs: 10_000 });
              for (const rec of records) {
                if (rec.uri !== e.uri) continue;
                const val = rec.value as Record<string, unknown>;
                const acceptRef = val.accept as { uri?: string } | undefined;
                if (acceptRef?.uri !== acceptUri) continue;
                resolved = true;
                clearTimeout(timer);
                watcher?.close();
                const se = val.submitEvent as string | undefined;
                resolve({ receiptUri: rec.uri, receiptCid: rec.cid, submitEventRef: se });
                return;
              }
            } catch { /* best-effort */ }
          },
          log: logger,
        });
      },
    );
    if (receiptFromFirehose) {
      receiptUri = receiptFromFirehose.receiptUri;
      receiptCid = receiptFromFirehose.receiptCid;
      submitEventRef = receiptFromFirehose.submitEventRef;
      log("receipt_from_firehose", { receiptUri, receiptCid, submitEventRef });
    } else {
      log("receipt_firehose_timeout", { acceptUri });
    }
  }

  // 9. Verify receipt.
  let receiptOk = false;
  if (receiptUri && receiptCid) {
    try {
      const resolver = createRecordResolver(new IdResolver());
      const receipt = await resolver.resolve({ uri: receiptUri, cid: receiptCid });
      const accept = await resolver.resolve({ uri: acceptUri, cid: acceptCid });
      const receiptBare = stripResolved(receipt) as Record<string, unknown>;
      const sigOk = await verifyRecordSignatures({
        record: receiptBare,
        repositoryDid: atUriAuthority(receiptUri),
      });
      const bindOk = verifyRemoteProof({
        subjectRecord: stripResolved(accept) as Record<string, unknown>,
        subjectRepositoryDid: atUriAuthority(acceptUri),
        proofRecord: receiptBare,
      });
      receiptOk = sigOk && bindOk;
      log("receipt_verified", { receiptUri, sigOk, bindOk, ok: receiptOk });
    } catch (err) {
      log("receipt_verify_error", { receiptUri, error: String(err) });
    }
  } else {
    log("receipt_missing", { receiptUri, receiptCid });
  }

  const result: ContractFlowResult = {
    event: "compute_request_complete",
    vmUri, vmCid,
    rfpUri, rfpCid,
    acceptUri, acceptCid,
    bidUri: winner.uri, bidCid: winner.cid, winnerDid: winner.did,
    receiptUri, receiptCid, submitEventRef,
    receiptOk,
    bids: bids.length,
  };
  log("compute_request_complete", result as unknown as Record<string, unknown>);

  // 10. SSH (gated on valid receipt).
  if (skipSsh) {
    // tests / headless: skip SSH.
  } else if (!receiptOk) {
    log("vm_poll_bailed", { reason: "no valid receipt", receiptUri, receiptCid });
  } else {
    // Check for shared IP file written by bidder (--no-ingress-proxy direct SSH).
    if (!directVmHost && receiptUri) {
      const receiptRkey = receiptUri.split("/").pop()!;
      const ipFile = `/tmp/pdr-vm-ip-${receiptRkey}.txt`;
      try {
        const ipContent = await Deno.readTextFile(ipFile).then(s => s.trim());
        if (ipContent && /^\d+\.\d+\.\d+\.\d+$/.test(ipContent)) {
          directVmHost = ipContent;
          log("vm_ip_from_file", { ipFile, ip: directVmHost });
        }
      } catch { /* file not found — not yet provisioned */ }
    }
    // If no direct IP yet, poll for the IP file to appear.
    if (!directVmHost && receiptUri) {
      const receiptRkey = receiptUri.split("/").pop()!;
      const ipFile = `/tmp/pdr-vm-ip-${receiptRkey}.txt`;
      const deadline = Date.now() + 120_000; // 2 min for provisioning
      while (Date.now() < deadline && !directVmHost) {
        try {
          const ipContent = await Deno.readTextFile(ipFile).then(s => s.trim());
          if (ipContent && /^\d+\.\d+\.\d+\.\d+$/.test(ipContent)) {
            directVmHost = ipContent;
            log("vm_ip_from_file_poll", { ipFile, ip: directVmHost });
            break;
          }
        } catch { /* not yet */ }
        await new Promise(r => setTimeout(r, 2000));
      }
    }
    // Choose SSH transport: direct IP if available, otherwise fedproxy relay.
    if (directVmHost) {
      // Direct SSH to container IP (discovered from vm.onNetwork firehose or shared file).
      log("vm_ssh_waiting_direct", { host: directVmHost, timeoutSec: vmReadyTimeoutSec });
      const ready = await pollDirectSsh(directVmHost, 22, vmReadyTimeoutSec * 1000);
      result.sshReady = ready;
      if (!ready) {
        log("vm_ssh_unavailable_direct", { host: directVmHost });
      } else {
        opts.onSshStart?.();
        const code = await runDirectSsh(privateKeyPath, directVmHost, execProgram);
        await opts.onSshEnd?.();
        result.sshExitCode = code;
        log("vm_ssh_session_exit_direct", { host: directVmHost, code });
      }
    } else {
      // Standard path: SSH through fedproxy relay tunnel (websocat ProxyCommand).
      log("vm_ssh_waiting", { vmFqdn, timeoutSec: vmReadyTimeoutSec });
    const ready = await sshProvider.pollReady(privateKeyPath, vmFqdn, vmReadyTimeoutSec * 1000);
    result.sshReady = ready;
    if (!ready) {
      log("vm_ssh_unavailable", { vmFqdn });
    } else {
      opts.onSshStart?.();
      const code = await sshProvider.runSession(privateKeyPath, vmFqdn, execProgram);
      await opts.onSshEnd?.();
      result.sshExitCode = code;
      log("vm_ssh_session_exit", { vmFqdn, code });
    }
  }
  }

  // 11. Tear down VM via compute.events.vm.delete (unless --keep-vm).
  if (keepVm) {
    log("vm_delete_skipped", { reason: "--keep-vm" });
  } else if (!receiptUri || !receiptCid || !submitEventRef) {
    log("vm_delete_skipped", { reason: "missing receipt refs", receiptUri, receiptCid, submitEventRef });
  } else {
    try {
      const nowIso = new Date().toISOString();
      const { uri: delUri, cid: delCid } = await pds.createSignedRepoRecord(
        COMPUTE_EVENTS_VM_DELETE_NSID,
        { $type: COMPUTE_EVENTS_VM_DELETE_NSID, reason: "session_ended", createdAt: nowIso },
        pds.attestationKp, pds.did,
      );
      const eventRecord = {
        $type: EVENT_NSID,
        receipt: { $type: "com.atproto.repo.strongRef", uri: receiptUri, cid: receiptCid },
        payload: { $type: "com.atproto.repo.strongRef", uri: delUri, cid: delCid },
        createdAt: nowIso,
      };
      const { uri: eventUri, cid: eventCid } = await pds.createSignedRepoRecord(
        EVENT_NSID, eventRecord, pds.attestationKp, pds.did,
      );
      const target = await pds.resolveBidderEndpoint(submitEventRef);
      if (!target) {
        log("vm_delete_target_unresolvable", { submitEventRef });
      } else {
        log("submitting_vm_delete", { submitEventRef, eventUri });
        const r = await pds.callBidder(target.targetUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_LXM, target.audDid, {
          uri: eventUri,
          cid: eventCid,
          record: eventRecord,
        });
        log("vm_delete_result", { status: r.status, ok: r.ok });
      }
    } catch (err) {
      log("vm_delete_error", { error: String(err) });
    }
  }

  return result;
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// OAuth requester — lightweight RequesterPDS backed by OAuth agent
// ---------------------------------------------------------------------------

export interface OAuthRequesterHandle {
  pds: RequesterPDS;
  startFlow(): Promise<string>;
  completeFlow(params: Record<string, string>): Promise<void>;
  restore(): Promise<boolean>;
}

export interface CreateOAuthRequesterOpts {
  handle: string;
  sessionPath: string;
  clientId?: string;
  redirectUri?: string;
  scope?: string;
  pdsUrl?: string;
  plcDirectoryUrl?: string;
  logger?: StructuredLoggerInterface;
  attestationKp: AttestationKeypair;
  privateKeyHex: string;
}

export async function createOAuthRequester(opts: CreateOAuthRequesterOpts): Promise<OAuthRequesterHandle> {
  const clientId = opts.clientId ?? "http://localhost";
  const redirectUri = opts.redirectUri ?? "http://127.0.0.1:0/callback";
  const scope = opts.scope ?? "atproto";
  const log = opts.logger;

  const client = new OAuthClient({
    responseMode: "query",
    clientMetadata: {
      client_id: clientId,
      application_type: "web",
      dpop_bound_access_tokens: true,
      redirect_uris: [redirectUri],
      grant_types: ["authorization_code", "refresh_token"],
      response_types: ["code"],
      scope,
      token_endpoint_auth_method: "none",
    },
    stateStore: memoryStateStore(),
    sessionStore: jsonSessionStore(opts.sessionPath),
    runtimeImplementation: webCryptoRuntime(),
    identityResolver: {
      resolve: async (identifier: string) => {
        const resolver = new IdResolver({ plcUrl: opts.plcDirectoryUrl ?? "https://plc.directory" });
        const did = identifier.startsWith("did:") ? identifier : (await resolver.handle.resolve(identifier)) ?? identifier;
        const didDoc = await resolver.did.resolve(did) as Record<string, unknown>;
        const handle = ((didDoc?.alsoKnownAs as string[] | undefined)?.[0] ?? "").replace("at://", "");
        return { did, didDoc, handle: handle || "handle.invalid" };
      },
    } as never,
    allowHttp: clientId === "http://localhost",
  });

  let _session: Awaited<ReturnType<typeof client.restore>> | null = null;
  function getSession() { if (!_session) throw new Error("OAuth session not initialized"); return _session; }

  let _did = "";
  const idResolver = new IdResolver();

  const pds: RequesterPDS = {
    get did() { return _did; },
    serve: { tcpPort: 0, onConnected() {}, beginServe: async () => {}, shutdown() {}, app: { route() {}, fetch: async () => new Response() } } as unknown as ServeHandle,
    relay: { ingressRef: "", ingressUrl: "", ingressHost: "", close() {}, onServe: async () => {} } as IngressRef,
    ingressRef: "",
    relaySubdomain: "",
    get ingressUrl() { return ""; },
    get ingressHost() { return ""; },
    async beginServe() {},
    pendingBids: new Map(),
    attestationKp: opts.attestationKp,
    signer: {
      did: () => _did,
      sign: async () => { throw new Error("use getServiceAuth for OAuth requester"); },
    },
    privateKeyHex: opts.privateKeyHex,
    associateCalled: Promise.resolve(""),
    approveAssociation() {},
    rejectAssociation(_err: Error) {},
    async dispose() {},

    async createRepoRecord(collection: string, record: Record<string, unknown>) {
      const s = getSession();
      const rkey = TID.next().toString();
      const res = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.applyWrites`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ repo: _did, writes: [{ action: "create", collection, rkey, record }] }),
      });
      if (!res.ok) throw new Error(`applyWrites failed: ${res.status}`);
      const rec = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(_did)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`);
      if (!rec.ok) throw new Error(`getRecord failed: ${rec.status}`);
      const data = await rec.json() as { uri: string; cid?: string };
      return { uri: data.uri, cid: data.cid ?? "" };
    },

    async createSignedRepoRecord(collection: string, record: Record<string, unknown>, aKp: AttestationKeypair, issuer?: string) {
      const s = getSession();
      const rkey = TID.next().toString();
      const att = attestationFor(aKp, issuer);
      const entry = await att.sign({ record: record as Record<string, unknown>, repository: _did }) as InlineAttestation;
      const signed = { ...record, signatures: [toStorableEntry(entry)] };
      const res = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.applyWrites`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ repo: _did, writes: [{ action: "create", collection, rkey, record: signed }] }),
      });
      if (!res.ok) throw new Error(`applyWrites failed: ${res.status}`);
      const rec = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(_did)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`);
      if (!rec.ok) throw new Error(`getRecord failed: ${rec.status}`);
      const data = await rec.json() as { uri: string; cid?: string };
      return { uri: data.uri, cid: data.cid ?? "" };
    },

    async resolveBidderEndpoint(endpointUrl: string) {
      if (endpointUrl.startsWith("http://") || endpointUrl.startsWith("https://")) {
        return { targetUrl: `${endpointUrl.replace(/\/+$/, "")}/xrpc`, audDid: `did:web:${new URL(endpointUrl).host}` };
      }
      if (endpointUrl.startsWith("did:")) {
        const didPart = endpointUrl.split("#")[0];
        const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : "pdr_temp_market";
        const doc = await idResolver.did.resolve(didPart);
        const svc = doc?.service?.find?.((s: { id: string }) => s.id === `#${svcId}`);
        if (!svc) return null;
        const ep = (svc as { serviceEndpoint: string }).serviceEndpoint.replace(/\/+$/, "");
        return { targetUrl: `${ep}/xrpc`, audDid: `did:web:${new URL(ep).host}` };
      }
      return null;
    },

    async callBidder(targetBase: string, nsid: string, lxm: string, audDid: string, body: Record<string, unknown>) {
      const s = getSession();
      const saRes = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.server.getServiceAuth`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ aud: audDid, lxm }),
      });
      if (!saRes.ok) throw new Error(`getServiceAuth failed: ${saRes.status}`);
      const saData = await saRes.json() as { token: string };
      const res = await fetch(`${targetBase}/${nsid}`, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: `Bearer ${saData.token}` },
        body: JSON.stringify(body),
      });
      let resBody: unknown;
      try { resBody = await res.json(); } catch { resBody = await res.text(); }
      return { status: res.status, ok: res.ok, body: resBody };
    },

  };

  return {
    pds,
    async startFlow(): Promise<string> {
      const result = await client.authorize(opts.handle, { scope });
      return String(result);
    },
    async completeFlow(params: Record<string, string>): Promise<void> {
      const result = await client.callback(new URLSearchParams(params));
      _session = result.session;
      _did = result.session.did;
      log?.info("oauth_session_complete", { did: _did });
    },
    async restore(): Promise<boolean> {
      try { _session = await client.restore(opts.handle); _did = _session.did; log?.info("oauth_session_restored", { did: _did }); return true; }
      catch { return false; }
    },
  };
}

function randomHex8(): string {
  const b = new Uint8Array(4);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

async function listRecordsAll(
  pdsUrl: string,
  repo: string,
  collection: string,
  opts?: { limit?: number; timeoutMs?: number },
): Promise<Array<{ uri: string; cid: string; value: Record<string, unknown> }>> {
  // Use the market-atproto listRecordsAll helper — import it above
  const { listRecordsAll: lra } = await import("@publicdomainrelay/market-atproto");
  return lra(pdsUrl, repo, collection, opts);
}
