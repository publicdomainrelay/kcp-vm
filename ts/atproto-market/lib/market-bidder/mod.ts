// Market bidder factory — lifecycle orchestration, callback merging, route wiring.
// Does NOT own I/O (no Deno.serve, signals, WS connect). Takes atproto + serve +
// providers; wires everything in beginServe().

import { TID } from "@atproto/common";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { createRecordResolver, createRfpDispatcher, startOfferingRefresh, listRecordsPublic } from "@publicdomainrelay/market-atproto";
import type { MarketServerDeps, OfferingRefreshHandle } from "@publicdomainrelay/market-atproto";
import { createMarketFactory } from "@publicdomainrelay/hono-factory-market-atproto";
import { RFP_NSID } from "@publicdomainrelay/market-common";
import type {
  FirehoseRecordEvent,
  FirehoseWatcher,
} from "@publicdomainrelay/firehose-watcher-abc";
import type { Logger } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
} from "@publicdomainrelay/market-common";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RelayRef, ServeHandle } from "@publicdomainrelay/serve";
import type { ATProto } from "@publicdomainrelay/atproto-helpers";
import type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
} from "@publicdomainrelay/market-bidder-abc";
export type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function deepMergeCallbacks<T>(
  a: Record<string, Record<string, T>>,
  b: Record<string, Record<string, T>>,
): Record<string, Record<string, T>> {
  const result: Record<string, Record<string, T>> = {};
  for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
    result[key] = { ...(a[key] ?? {}), ...(b[key] ?? {}) };
  }
  return result;
}

// ---------------------------------------------------------------------------
// Config types
// ---------------------------------------------------------------------------

export interface MarketBidderConfig {
  logger: StructuredLoggerInterface;
  serve: ServeHandle;
  atproto: ATProto;
  relay?: RelayRef;
  providers?: MarketBidderProviderRef[];
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  callbackFactory?: (deps: CallbackFactoryDeps) => CallbackSet | Promise<CallbackSet>;
  /** Fires on contract lifecycle changes (accepted, provisioned, terminated). */
  onContractChange?: (event: ContractEvent) => void;
  /** Accept jobs from scope. Controls which RFPs the bidder responds to. */
  acceptScope?: "only_me" | "direct_network" | "policy_based" | null;
  /**
   * Builds a firehose watcher bound to `onRecord`. The CLI selects the transport
   * (subscribeRepos or jetstream) and owns the WebSocket; the bidder supplies
   * `onRecord`. When set, new RFP records are self-discovered and bid on (pull
   * mode), no inbound submitRfp required.
   */
  rfpWatcherFactory?: (onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher;
  /**
   * Multiple firehose watcher factories (e.g. bsky + own relay). Each gets its
   * own WebSocket and the same onRecord dispatch. Prefer this over the singular
   * rfpWatcherFactory when watching more than one relay.
   */
  rfpWatcherFactories?: Array<(onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher>;
  /** Period for re-committing the offering record to stay discoverable. */
  offeringRefreshMs?: number;
  /**
   * RFP NSIDs to advertise in the offering record. Overrides the union of
   * provider `appliesTo`. Set for callbackFactory-only bidders with no providers.
   */
  appliesTo?: string[];
  /**
   * When true, the caller already started the serve (e.g. a desktop app with its
   * own HTTP server). Route mounting, offering setup, and firehose watchers still
   * run — only the serve.beginServe() call is skipped.
   */
  skipServeBegin?: boolean;
}

export interface MarketBidder {
  beginServe(): Promise<void>;
  shutdown(): void;
}

function logAdapter(logger: StructuredLoggerInterface): Logger {
  return (level: string, message: string, meta?: Record<string, unknown>) => {
    logger[level as "info" | "warn" | "error" | "debug"]?.(message, meta);
  };
}


export async function createMarketBidder(config: MarketBidderConfig): Promise<MarketBidder> {
  const { logger, serve, atproto, relay, providers, setup, teardown, callbackFactory, onContractChange, rfpWatcherFactory, rfpWatcherFactories, offeringRefreshMs, skipServeBegin, acceptScope } = config;
  const log = logAdapter(logger);
  const activeContracts = new Map<string, ActiveContract>();
  const idResolver = atproto.idResolver;
  const rfpWatchers: FirehoseWatcher[] = [];
  let offeringRefresher: OfferingRefreshHandle | null = null;

  const ALLOWLIST_NSID = "com.publicdomainrelay.temp.auth.allowlist.rbacDid";
  const OFFERING_NSID = "com.publicdomainrelay.temp.market.offering";

  // Pre-load vouched DIDs for direct_network scope filter (firehose hot path).
  let vouchedDids: Set<string> | null = null;
  if (acceptScope === "direct_network") {
    vouchedDids = new Set();
    try {
      const result = await atproto.listRecords(atproto.did, "sh.tangled.graph.vouch", { limit: 200 });
      for (const rec of result?.records ?? []) {
        const v = rec.value as Record<string, unknown>;
        if (v.kind === "denounce") continue;
        const rkey = rec.uri.split("/").pop() ?? "";
        if (rkey.startsWith("did:")) vouchedDids.add(rkey);
      }
      logger.info("bidder vouch set loaded", { count: vouchedDids.size });
    } catch (err) {
      logger.warn("bidder vouch set load failed, declining all direct_network RFPs", { error: String(err) });
    }
  }

  // On-demand check: requester must be associated with the operator (parent)
  // DID that owns this bidder. Two paths:
  //   1. Bidder DID == operator DID (desktop bidder): check requester_associate
  //      records directly on the bidder's repo.
  //   2. Bidder DID != operator DID (hono-bidder): discover operator DID from
  //      bidder_associate records on this repo, then check requester_associate
  //      records on the operator's repo.
  // Results cached in-memory — badgeBlueKeys records are write-once so TTL is unnecessary.
  const associationCache = new Map<string, boolean>();
  const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
  const operatorDidCache = new Map<string, string[]>(); // bidderDid → [operatorDids]

  async function discoverOperatorDids(): Promise<string[]> {
    const cached = operatorDidCache.get(atproto.did);
    if (cached) return cached;
    const dids: string[] = [];
    try {
      const ownRecords = await atproto.listRecords(atproto.did, BADGE_BLUE_KEYS_NSID, { limit: 200 });
      for (const rec of ownRecords?.records ?? []) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === atproto.did && v.service === "bidder_associate") {
          const keyId = v.keyId as string | undefined;
          if (keyId && keyId.startsWith("did:")) dids.push(keyId);
        }
      }
    } catch {
      // fall through to public read below
    }
    if (dids.length === 0) {
      try {
        const publicRecords = await listRecordsPublic(idResolver, atproto.did, BADGE_BLUE_KEYS_NSID);
        for (const r of publicRecords) {
          const v = r.value as Record<string, unknown>;
          if (v.challenge === atproto.did && v.service === "bidder_associate") {
            const keyId = v.keyId as string | undefined;
            if (keyId && keyId.startsWith("did:")) dids.push(keyId);
          }
        }
      } catch {
        // non-critical
      }
    }
    operatorDidCache.set(atproto.did, dids);
    if (dids.length > 0) logger.info("bidder scope check: discovered operator DIDs", { bidderDid: atproto.did, operatorDids: dids });
    return dids;
  }

  const isRequesterAssociated = async (requesterDid: string): Promise<boolean> => {
    logger.info("scope_check_start", { requesterDid });
    const cached = associationCache.get(requesterDid);
    if (cached !== undefined) {
      logger.info("scope_check_cache_hit", { requesterDid, cached });
      if (cached) logger.info("bidder scope check: matched requester association (cached)", { requesterDid });
      return cached;
    }

    // Path 1: bidder's own repo (works when bidder DID == operator DID).
    try {
      const ownRecords = await atproto.listRecords(atproto.did, BADGE_BLUE_KEYS_NSID, { limit: 200 });
      for (const rec of ownRecords?.records ?? []) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === requesterDid && v.service === "requester_associate") {
          associationCache.set(requesterDid, true);
          logger.info("bidder scope check: matched requester association on own repo", { requesterDid });
          return true;
        }
      }
    } catch (err) {
      logger.warn("bidder scope check: own repo listRecords failed", { requesterDid, error: String(err) });
    }

    // Path 2: check requester's repo for requester_associate records
    // where keyId matches a discovered operator DID. The requester
    // creates badgeBlueKeys on its own LocalPDS repo during QR flow:
    //   keyId = operator DID (who scanned the QR)
    //   challenge = requester DID (the PDS's own DID)
    //   service = "requester_associate"
    const operatorDids = await discoverOperatorDids();
    if (operatorDids.length > 0) {
      const operatorSet = new Set(operatorDids);
      try {
        const reqRecords = await listRecordsPublic(idResolver, requesterDid, BADGE_BLUE_KEYS_NSID);
        for (const r of reqRecords) {
          const v = r.value as Record<string, unknown>;
          if (v.service === "requester_associate" && operatorSet.has(v.keyId as string)) {
            associationCache.set(requesterDid, true);
            logger.info("bidder scope check: matched requester association via operator", { requesterDid, operatorDid: v.keyId });
            return true;
          }
        }
      } catch (err) {
        logger.warn("bidder scope check: requester repo listRecords failed", { requesterDid, error: String(err) });
      }
    }

    // Path 3: legacy check — public read on own repo (desktop bidder fallback).
    try {
      const listed = await listRecordsPublic(idResolver, atproto.did, BADGE_BLUE_KEYS_NSID);
      for (const r of listed) {
        const v = r.value;
        if (v.challenge === atproto.did && v.service === "requester_associate" && v.keyId === requesterDid) {
          associationCache.set(requesterDid, true);
          logger.info("bidder scope check: matched requester association (legacy public read)", { requesterDid, keyId: v.keyId });
          return true;
        }
      }
    } catch (err) {
      logger.warn("bidder scope check: public listRecords failed", { requesterDid, error: String(err) });
    }

    // Negative results are never cached: the requester's association record
    // may not exist yet at first-RFP time and appear moments later (e.g. the
    // self-attested requester_associate write racing the RFP submit).
    logger.warn("bidder scope check: no matching requester association", {
      requesterDid, bidderDid: atproto.did, operatorDids,
    });
    return false;
  };

  async function ensureOperatorAllowlist(service: string): Promise<void> {
    const result = await atproto.listRecords(atproto.did, ALLOWLIST_NSID, { limit: 100 });
    for (const rec of result?.records ?? []) {
      const v = rec.value as Record<string, unknown>;
      const protects = v.protects as Record<string, { service: string; scope?: string }> | undefined;
      for (const p of Object.values(protects ?? {})) {
        if (
          (p.service === service || p.service === "*") &&
          (p.scope === "account.auth" || p.scope === "*" || !p.scope)
        ) {
          log("info", "bidder allowlist exists", { uri: rec.uri });
          return;
        }
      }
    }
    const allowlistRef = await atproto.createRecord(ALLOWLIST_NSID, {
      $type: ALLOWLIST_NSID,
      protects: { allowSelf: { service, scope: "account.auth" } },
      allowed: { allowSelf: [atproto.did] },
      createdAt: new Date().toISOString(),
    });
    log("info", "bidder allowlist created", { uri: allowlistRef.uri, service });
  }

  function buildOffering(createdAt: string): Record<string, unknown> {
    const appliesTo = config.appliesTo ??
      [...new Set((providers ?? []).flatMap((p) => p.appliesTo))];
    return {
      $type: OFFERING_NSID,
      endpointUrl: relay?.proxyUrl || `${atproto.did}#pdr_temp_market`,
      appliesTo,
      createdAt,
      refreshedAt: new Date().toISOString(),
    };
  }

  // Ensures exactly one offering record per bidder DID (one active service =
  // one endpoint). The first (oldest) existing record's rkey becomes the
  // canonical rkey for this bidder's lifetime — every subsequent write
  // (correction or refresh) updates that same record in place via
  // atproto.updateRecord rather than creating a new one, so the collection
  // never grows past one record.
  async function ensureOffering(): Promise<{ rkey: string; createdAt: string }> {
    const wanted = config.appliesTo ??
      [...new Set((providers ?? []).flatMap((p) => p.appliesTo))];
    const existing = await atproto.listRecords(atproto.did, OFFERING_NSID, { limit: 20 });
    const records = existing?.records ?? [];
    if (records.length) {
      const rec = records[0];
      const rkey = rec.uri.split("/").pop() ?? "";
      const createdAt = (rec.value.createdAt as string) ?? new Date().toISOString();
      const val = rec.value as Record<string, unknown>;
      const appliesTo = (val.appliesTo as string[] | undefined) ?? [];
      const endpointUrl = val.endpointUrl as string | undefined;
      const wantedStr = [...wanted].sort().join(",");
      const haveStr = [...appliesTo].sort().join(",");
      const hasGoodEp = endpointUrl?.startsWith("https://") ?? false;
      const wantedEp = relay?.proxyUrl || `${atproto.did}#pdr_temp_market`;
      const epSame = endpointUrl === wantedEp;
      if (haveStr === wantedStr && hasGoodEp && epSame) {
        log("info", "bidder offering exists (matched)", { uri: rec.uri, appliesTo: haveStr, endpointUrl });
      } else {
        await atproto.updateRecord(OFFERING_NSID, rkey, buildOffering(createdAt));
        log("info", "bidder offering corrected", { uri: rec.uri, appliesTo: wantedStr, endpointUrl: wantedEp, reason: haveStr === wantedStr ? (hasGoodEp ? "endpoint_changed" : "bad_endpoint") : "appliesTo_mismatch" });
      }
      return { rkey, createdAt };
    }
    const createdAt = new Date().toISOString();
    const offeringRef = await atproto.createRecord(OFFERING_NSID, buildOffering(createdAt));
    const rkey = offeringRef.uri.split("/").pop() ?? "";
    log("info", "bidder offering created", { uri: offeringRef.uri });
    return { rkey, createdAt };
  }

  async function beginServe(): Promise<void> {
    const recordResolver = createRecordResolver(idResolver);

    const deps: CallbackFactoryDeps = {
      did: atproto.did,
      repoApi: {} as RepoApi,
      signer: atproto.signer,
      attestationKp: atproto.attestationKp,
      idResolver,
      relay: relay ?? { proxyRef: "", proxyUrl: "", proxyHost: "" },
      dispatcherHost: "",
      log,
      activeContracts,
      createRecord: atproto.createRecord,
      createRepoRecord: atproto.createRepoRecord,
      createSignedRepoRecord: atproto.createSignedRepoRecord,
      deleteRecord: atproto.deleteRecord,
      callService: atproto.callService,
      resolve: recordResolver,
      onContractChange,
    };

    for (const p of providers ?? []) {
      await p.setup?.();
    }
    await setup?.();

    const merged: CallbackSet = {};
    for (const p of providers ?? []) {
      const cb = p.buildCallbacks(deps);
      if (cb.rfpCallbacks) {
        merged.rfpCallbacks = deepMergeCallbacks(merged.rfpCallbacks ?? {}, cb.rfpCallbacks);
      }
      if (cb.onAccept) {
        merged.onAccept = merged.onAccept ?? cb.onAccept;
      }
      if (cb.eventCallbacks) {
        merged.eventCallbacks = deepMergeCallbacks(merged.eventCallbacks ?? {}, cb.eventCallbacks);
      }
    }

    if (callbackFactory) {
      const cb = await callbackFactory(deps);
      if (cb.rfpCallbacks) merged.rfpCallbacks = deepMergeCallbacks(merged.rfpCallbacks ?? {}, cb.rfpCallbacks);
      if (cb.onAccept) merged.onAccept = merged.onAccept ?? cb.onAccept;
      if (cb.eventCallbacks) merged.eventCallbacks = deepMergeCallbacks(merged.eventCallbacks ?? {}, cb.eventCallbacks);
    }

    const marketDeps: MarketServerDeps = {
      hostname: () => relay?.proxyHost || "",
      idResolver,
      resolve: recordResolver,
      log,
    };

    if (merged.rfpCallbacks || merged.onAccept || merged.eventCallbacks) {
      const factory = createMarketFactory(marketDeps, {
        rfp: merged.rfpCallbacks,
        rfpScopeFilter: acceptScope
          ? async ({ issuerDid }) => {
              if (acceptScope === "only_me") return isRequesterAssociated(issuerDid);
              if (acceptScope === "direct_network") return issuerDid === atproto.did || (vouchedDids?.has(issuerDid) ?? false) || isRequesterAssociated(issuerDid);
              if (acceptScope === "policy_based") return true;
              return true;
            }
          : undefined,
        accept: merged.onAccept
          ? { serviceIds: [DEFAULT_MARKET_SERVICE_ID], onAccept: merged.onAccept }
          : undefined,
        event: merged.eventCallbacks
          ? { callbacks: merged.eventCallbacks, background: merged.eventBackground ?? true }
          : undefined,
      });
      serve.app.route("/", factory.createApp() as never);
    }

    if (merged.rfpCallbacks && (rfpWatcherFactory || rfpWatcherFactories?.length)) {
      const dispatch = createRfpDispatcher({ deps: marketDeps, callbacks: merged.rfpCallbacks });
      const seen = new Set<string>();
      const factories = rfpWatcherFactories ?? (rfpWatcherFactory ? [rfpWatcherFactory] : []);
      for (const factory of factories) {
        const w = factory((e) => {
          if (e.collection !== RFP_NSID) return;
          if (e.operation !== "create" && e.operation !== "update") return;
          if (seen.has(e.uri)) return;

          // Scope filter — fast path, no record resolution.
          // e.did is the DID whose repo the RFP was created in (requester DID).
          // only_me: no pre-filter, dispatcher does async isRequesterAssociated check.
          if (acceptScope === "only_me") { /* pass through, dispatch checks */ }
          else if (acceptScope === "direct_network" && e.did !== atproto.did) {
            if (!vouchedDids?.has(e.did)) return;
          }
          seen.add(e.uri);
          log("info", "rfp watch discovered", { rfpUri: e.uri });
          dispatch({ rfpUri: e.uri, rfpCid: e.cid, issuerDid: e.did })
            .catch((err) => log("error", "rfp watch dispatch failed", { rfpUri: e.uri, err: String(err) }));
        });
        rfpWatchers.push(w);
      }
      logger.info("bidder rfp firehose watches started", { count: rfpWatchers.length });
    }

    serve.onConnected(async () => {
      await ensureOperatorAllowlist("");
      const { rkey: offeringRkey, createdAt: offeringCreatedAt } = await ensureOffering();
      if (offeringRefreshMs && offeringRkey) {
        offeringRefresher = startOfferingRefresh({
          intervalMs: offeringRefreshMs,
          log,
          refresh: async () => {
            try {
              await atproto.updateRecord(OFFERING_NSID, offeringRkey, buildOffering(offeringCreatedAt));
            } catch { /* best-effort */ }
            log("info", "bidder offering refreshed", { rkey: offeringRkey });
          },
        });
        logger.info("bidder offering refresh started", { intervalMs: offeringRefreshMs });
      }
    });

    if (!skipServeBegin) {
      logger.info("bidder starting serve");
      await serve.beginServe();
      // offering already created by onConnected callback during serve.beginServe()
    }

    logger.info("bidder ready", { did: atproto.did });
  }

  function shutdown(): void {
    for (const w of rfpWatchers) w.close();
    offeringRefresher?.stop();
    activeContracts.clear();
    for (const p of providers ?? []) {
      p.teardown?.().catch(() => {});
    }
    teardown?.().catch(() => {});
    if (!skipServeBegin) serve.shutdown();
  }

  return { beginServe, shutdown };
}
