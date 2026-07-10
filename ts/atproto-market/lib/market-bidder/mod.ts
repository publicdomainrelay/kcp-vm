// Market bidder factory — lifecycle orchestration, callback merging, route wiring.
// Does NOT own I/O (no Deno.serve, signals, WS connect). Takes atproto + serve +
// providers; wires everything in beginServe().

import { TID } from "@atproto/common";
import { getPdsEndpoint } from "@atproto/common-web";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { createRecordResolver, createRfpDispatcher, startOfferingRefresh, listRecordsPublic, listRecordsAll } from "@publicdomainrelay/market-atproto";
import type { MarketServerDeps, OfferingRefreshHandle } from "@publicdomainrelay/market-atproto";
import { createMarketFactory } from "@publicdomainrelay/hono-factory-market-atproto";
import { RFP_NSID, VOUCH_NSID } from "@publicdomainrelay/market-common";
import type {
  ATProtoEventStreamsClient,
} from "@publicdomainrelay/atproto-event-streams-client";
import type { Logger } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
} from "@publicdomainrelay/market-common";
import {
  OFFERING_NSID,
  ALLOWLIST_RBAC_DID_NSID,
  BADGE_BLUE_KEYS_NSID,
  ACCEPT_NSID,
  EVENT_NSID,
} from "@publicdomainrelay/market-lexicons";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";
import type { VouchResolver, OperatorDiscovery } from "@publicdomainrelay/trust-graph-abc";
import { createBadgeBlueKeysOperatorDiscovery } from "@publicdomainrelay/operator-discovery-badge-blue-keys";
import { createTangledGraphVouchResolver } from "@publicdomainrelay/trust-graph-tangled-graph";
import { PolicyModeFilter, type PolicyMode, TANGLED_VOUCH, MUTUALS } from "@publicdomainrelay/market-policy-abc";
import { parseAtUri, type ATProto } from "@publicdomainrelay/atproto-helpers";
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
  relay?: IngressRef;
  providers?: MarketBidderProviderRef[];
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  callbackFactory?: (deps: CallbackFactoryDeps) => CallbackSet | Promise<CallbackSet>;
  /** Fires on contract lifecycle changes (accepted, provisioned, terminated). */
  onContractChange?: (event: ContractEvent) => void;
  /** Accept jobs from scope. Controls which RFPs the bidder responds to. */
  policyMode?: PolicyMode | null;
  /**
   * ATProto event streams client for firehose-based record discovery.
   * When set, the bidder self-discovers RFP/ACCEPT/EVENT records via firehose
   * (pull mode), no inbound submit* XRPC required. The client handles cross-source
   * deduplication by AT-URI + CID.
   */
  eventStreams?: ATProtoEventStreamsClient;
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
  /** Re-commits the offering record so relays re-index it immediately. */
  refreshOffering(): Promise<void>;
}

function logAdapter(logger: StructuredLoggerInterface): Logger {
  return (level: string, message: string, meta?: Record<string, unknown>) => {
    logger[level as "info" | "warn" | "error" | "debug"]?.(message, meta);
  };
}


export async function createMarketBidder(config: MarketBidderConfig): Promise<MarketBidder> {
  const { logger, serve, atproto, relay, providers, setup, teardown, callbackFactory, onContractChange, eventStreams, offeringRefreshMs, skipServeBegin, policyMode } = config;
  const log = logAdapter(logger);
  const activeContracts = new Map<string, ActiveContract>();
  const idResolver = atproto.idResolver;
  let offeringRefresher: OfferingRefreshHandle | null = null;

  const operatorDiscovery: OperatorDiscovery = createBadgeBlueKeysOperatorDiscovery({
    listRecordsOwn: async (collection, opts) => {
      const result = await atproto.listRecords(atproto.did, collection, opts);
      return (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
    },
    listRecordsPublic: (repo, collection) =>
      listRecordsPublic(idResolver, repo, collection),
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });

  const selfVouchResolver: VouchResolver = createTangledGraphVouchResolver({
    listRecords: async (_repo, coll) => {
      const result = await atproto.listRecords(atproto.did, coll, { limit: 200 });
      return (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
    },
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });

  const publicVouchResolver: VouchResolver = createTangledGraphVouchResolver({
    listRecords: (repo, coll) => listRecordsPublic(idResolver, repo, coll),
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });

  // Pre-load vouched DIDs for direct_network scope filter.
  // Reads vouches from operator repos (discovered via bidder_associate records)
  // and from the bidder's own repo (for desktop-bidder where worker == operator).
  // Pre-load vouched DIDs for direct_network scope filter.
  let vouchedDids: Set<string> | null = null;

  // Rebuild transitive vouch set from own badgeBlueKeys records
  // where keyId is in the global vouchedDids set.
  async function rebuildTransitiveVouchedDids(): Promise<void> {
    if (!vouchedDids) return;
    try {
      const ownBadge = await atproto.listRecords(atproto.did, BADGE_BLUE_KEYS_NSID, { limit: 200 });
      for (const rec of ownBadge?.records ?? []) {
        const v = rec.value as Record<string, unknown>;
        if (v.service === "requester_associate" && typeof v.challenge === "string" && v.challenge.startsWith("did:") && typeof v.keyId === "string" && vouchedDids.has(v.keyId)) {
          vouchedDids.add(v.challenge);
        }
      }
    } catch {}
  }

  // Reloads vouchedDids from operator repos using shared VouchResolver.
  // Called at boot and again lazily when operators become available post-association.
  async function reloadVouchedDidsFromOperators(opDids: string[]): Promise<void> {
    if (!vouchedDids) return;
    const results = await Promise.all(
      opDids.map(opDid => publicVouchResolver.getVouchedDids(opDid).catch(() => new Set<string>())),
    );
    for (const s of results) s.forEach(d => vouchedDids!.add(d));
    await rebuildTransitiveVouchedDids();
  }

  if (policyMode === TANGLED_VOUCH || policyMode === MUTUALS) {
    vouchedDids = new Set();
    // Load vouches from the bidder's own repo via shared VouchResolver.
    try {
      const selfVouched = await selfVouchResolver.getVouchedDids(atproto.did);
      selfVouched.forEach(d => vouchedDids!.add(d));
    } catch (err) {
      logger.warn("bidder vouch set load from own repo failed", { error: String(err) });
    }
    // Load vouches from operator repos (hono-bidder worker path).
    try {
      const opDids = await discoverOperatorDids();
      await reloadVouchedDidsFromOperators(opDids);
    } catch {}
    logger.info("bidder vouch set loaded", { count: vouchedDids.size });
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

  async function discoverOperatorDids(): Promise<string[]> {
    return operatorDiscovery.discoverOperatorDids(atproto.did);
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
    // Lazy-reload vouched DIDs when operators were discovered after boot.
    // discoverOperatorDids returns empty at cold boot (badgeBlueKeys isn't
    // written until did-key-associate completes). When operators appear
    // later, load their vouch records so scope checks see the full chain.
    if (operatorDids.length > 0 && vouchedDids && vouchedDids.size === 0) {
      await reloadVouchedDidsFromOperators(operatorDids);
      logger.info("bidder vouch set lazy reloaded", { count: vouchedDids.size });
    }
    if (operatorDids.length > 0) {
      const operatorSet = new Set(operatorDids);
      try {
        const reqRecords = await listRecordsPublic(idResolver, requesterDid, BADGE_BLUE_KEYS_NSID);
        for (const r of reqRecords) {
          const v = r.value as Record<string, unknown>;
          if (v.service === "requester_associate" && (operatorSet.has(v.keyId as string) || (vouchedDids?.has(v.keyId as string) ?? false))) {
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
    const result = await atproto.listRecords(atproto.did, ALLOWLIST_RBAC_DID_NSID, { limit: 100 });
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
    const allowlistRef = await atproto.createRecord(ALLOWLIST_RBAC_DID_NSID, {
      $type: ALLOWLIST_RBAC_DID_NSID,
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
      endpointUrl: relay?.ingressUrl || `${atproto.did}#pdr_temp_market`,
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
      const wantedEp = relay?.ingressUrl || `${atproto.did}#pdr_temp_market`;
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
      relay: relay ?? { ingressRef: "", ingressUrl: "", ingressHost: "" },
      ingressProxyHost: "",
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
      hostname: () => relay?.ingressHost || "",
      idResolver,
      resolve: recordResolver,
      log,
    };

    if (merged.rfpCallbacks || merged.onAccept || merged.eventCallbacks) {
      const factory = createMarketFactory(marketDeps, {
        rfp: merged.rfpCallbacks,
        rfpScopeFilter: new PolicyModeFilter(policyMode, atproto.did, vouchedDids ?? undefined, { isRequesterAssociated }).toAcceptScopeFilter(),
        accept: merged.onAccept
          ? { serviceIds: [DEFAULT_MARKET_SERVICE_ID], onAccept: merged.onAccept }
          : undefined,
        event: merged.eventCallbacks
          ? { callbacks: merged.eventCallbacks, background: merged.eventBackground ?? true }
          : undefined,
      });
      serve.app.route("/", factory.createApp() as never);
    }

    if (merged.rfpCallbacks && eventStreams) {
      const dispatch = createRfpDispatcher({ deps: marketDeps, callbacks: merged.rfpCallbacks });
      // Dedup handled by ATProtoEventStreamsClient — no per-group seen Set needed.
      eventStreams.watch({
        wantedCollections: [RFP_NSID],
        onEvent: (e) => {
          if (e.operation !== "create" && e.operation !== "update") return;
          const preFilter = new PolicyModeFilter(policyMode, atproto.did, vouchedDids ?? undefined);
          if (!preFilter.preFilter(e.did)) return;
          log("info", "rfp watch discovered", { rfpUri: e.uri });
          dispatch({ rfpUri: e.uri, rfpCid: e.cid, issuerDid: e.did })
            .catch((err) => log("error", "rfp watch dispatch failed", { rfpUri: e.uri, err: String(err) }));
        },
      });
      logger.info("bidder rfp firehose watches started via eventStreams client");
    }

    // Firehose watcher for ACCEPT_NSID — fallback when bid.submitAccept is absent.
    // Discovers accept records referencing our bids, dispatches to merged.onAccept.
    if (merged.onAccept && eventStreams) {
      const preFilter = new PolicyModeFilter(policyMode, atproto.did, vouchedDids ?? undefined);
      eventStreams.watch({
        wantedCollections: [ACCEPT_NSID],
        onEvent: async (e) => {
          if (e.operation !== "create") return;
          if (!preFilter.preFilter(e.did)) return;
          log("info", "accept watch discovered", { acceptUri: e.uri });
          try {
            const doc = await idResolver.did.resolve(e.did);
            if (!doc) return;
            const pdsUrl = getPdsEndpoint(doc);
            if (!pdsUrl) return;
            const records = await listRecordsAll(pdsUrl, e.did, ACCEPT_NSID);
            const acceptRec = records.find((r) => r.uri === e.uri);
            if (!acceptRec) return;
            const value = acceptRec.value as Record<string, unknown>;
            const bidRef = value.bid as { uri?: string } | undefined;
            if (!bidRef?.uri) return;
            try {
              const { repo, collection, rkey } = parseAtUri(bidRef.uri);
              const existing = await atproto.getRecord(repo, collection, rkey);
              if (!existing) return;
            } catch { return; }
            log("info", "accept watch matched own bid", { acceptUri: e.uri, bidUri: bidRef.uri });
            await merged.onAccept!({
              acceptUri: e.uri,
              acceptCid: e.cid,
              accept: value as Parameters<typeof merged.onAccept>[0]["accept"],
              issuerDid: e.did,
              resolve: recordResolver,
              log,
              req: new Request("https://localhost"),
            });
          } catch (err) {
            log("error", "accept watch dispatch failed", { acceptUri: e.uri, err: String(err) });
          }
        },
      });
      logger.info("bidder accept firehose watch started via eventStreams client");
    }

    // Firehose watcher for EVENT_NSID — fallback when accept.submitEvent is absent.
    // Routes lifecycle events to merged.eventCallbacks by payload NSID.
    if (merged.eventCallbacks && eventStreams) {
      eventStreams.watch({
        wantedCollections: [EVENT_NSID],
        onEvent: async (e) => {
          if (e.operation !== "create") return;
          log("info", "event watch discovered", { eventUri: e.uri });
          try {
            const doc = await idResolver.did.resolve(e.did);
            if (!doc) return;
            const pdsUrl = getPdsEndpoint(doc);
            if (!pdsUrl) return;
            const records = await listRecordsAll(pdsUrl, e.did, EVENT_NSID);
            const eventRec = records.find((r) => r.uri === e.uri);
            if (!eventRec) return;
            const value = eventRec.value as Record<string, unknown>;
            const payload = value.payload as { $type?: string; uri?: string; cid?: string } | undefined;
            if (!payload?.$type) return;
            const payloadNsid = payload.$type;
            const handlers = merged.eventCallbacks!["pdr_temp_compute_event"];
            if (!handlers) return;
            const handler = handlers[payloadNsid];
            if (!handler) return;
            log("info", "event watch dispatching", { eventUri: e.uri, payloadNsid });
            const ctx = {
              uri: eventRec.uri,
              cid: eventRec.cid,
              event: value as Parameters<typeof handler>[0]["event"],
              payloadNsid,
              issuerDid: e.did,
              serviceId: "pdr_temp_compute_event",
              resolve: recordResolver,
              log,
              req: new Request("https://localhost"),
            };
            await handler(ctx);
          } catch (err) {
            log("error", "event watch dispatch failed", { eventUri: e.uri, err: String(err) });
          }
        },
      });
      logger.info("bidder event firehose watch started via eventStreams client");
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
    eventStreams?.close();
    offeringRefresher?.stop();
    activeContracts.clear();
    for (const p of providers ?? []) {
      p.teardown?.().catch(() => {});
    }
    teardown?.().catch(() => {});
    if (!skipServeBegin) serve.shutdown();
  }

  const refreshOffering = () => ensureOffering().then(() => {});
  return { beginServe, shutdown, refreshOffering };
}
