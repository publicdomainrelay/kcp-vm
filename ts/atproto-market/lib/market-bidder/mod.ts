// Market bidder factory — lifecycle orchestration, callback merging, route wiring.
// Does NOT own I/O (no Deno.serve, signals, WS connect). Takes atproto + serve +
// providers; wires everything in beginServe().

import { TID } from "@atproto/common";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { createRecordResolver, createRfpDispatcher, startOfferingRefresh } from "@publicdomainrelay/market-atproto";
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
  MarketBidderProviderRef,
} from "@publicdomainrelay/market-bidder-abc";
export type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  MarketBidderProviderRef,
};

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

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

function didWebHost(s: string): string {
  return s.startsWith("did:web:") ? s.slice("did:web:".length) : s;
}

export async function createMarketBidder(config: MarketBidderConfig): Promise<MarketBidder> {
  const { logger, serve, atproto, relay, providers, setup, teardown, callbackFactory, rfpWatcherFactory, rfpWatcherFactories, offeringRefreshMs, skipServeBegin } = config;
  const log = logAdapter(logger);
  const activeContracts = new Map<string, ActiveContract>();
  const idResolver = atproto.idResolver;
  const rfpWatchers: FirehoseWatcher[] = [];
  let offeringRefresher: OfferingRefreshHandle | null = null;

  const ALLOWLIST_NSID = "com.publicdomainrelay.temp.auth.allowlist.rbacDid";
  const OFFERING_NSID = "com.publicdomainrelay.temp.market.offering";

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
      endpointUrl: relay?.proxyRef ? didWebToHttps(relay.proxyRef) : `${atproto.did}#pdr_temp_market`,
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
      if (haveStr === wantedStr && hasGoodEp) {
        log("info", "bidder offering exists (matched)", { uri: rec.uri, appliesTo: haveStr });
      } else {
        await atproto.updateRecord(OFFERING_NSID, rkey, buildOffering(createdAt));
        log("info", "bidder offering corrected", { uri: rec.uri, appliesTo: wantedStr });
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
      relay: relay ?? { proxyRef: "" },
      dispatcherHost: "",
      log,
      activeContracts,
      createRecord: atproto.createRecord,
      createRepoRecord: atproto.createRepoRecord,
      createSignedRepoRecord: atproto.createSignedRepoRecord,
      deleteRecord: atproto.deleteRecord,
      callService: atproto.callService,
      resolve: recordResolver,
    };

    for (const p of providers ?? []) {
      await p.setup?.();
    }
    await setup?.();

    const merged: CallbackSet = {};
    for (const p of providers ?? []) {
      const cb = p.buildCallbacks(deps);
      if (cb.rfpCallbacks) {
        merged.rfpCallbacks = { ...(merged.rfpCallbacks ?? {}), ...cb.rfpCallbacks };
      }
      if (cb.onAccept) {
        merged.onAccept = merged.onAccept ?? cb.onAccept;
      }
      if (cb.eventCallbacks) {
        merged.eventCallbacks = { ...(merged.eventCallbacks ?? {}), ...cb.eventCallbacks };
      }
    }

    if (callbackFactory) {
      const cb = await callbackFactory(deps);
      if (cb.rfpCallbacks) merged.rfpCallbacks = { ...(merged.rfpCallbacks ?? {}), ...cb.rfpCallbacks };
      if (cb.onAccept) merged.onAccept = merged.onAccept ?? cb.onAccept;
      if (cb.eventCallbacks) merged.eventCallbacks = { ...(merged.eventCallbacks ?? {}), ...cb.eventCallbacks };
    }

    const marketDeps: MarketServerDeps = {
      hostname: () => relay?.proxyRef ? didWebHost(relay.proxyRef) : "",
      idResolver,
      resolve: recordResolver,
      log,
    };

    if (merged.rfpCallbacks || merged.onAccept || merged.eventCallbacks) {
      const factory = createMarketFactory(marketDeps, {
        rfp: merged.rfpCallbacks,
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
          seen.add(e.uri);
          log("info", "rfp watch discovered", { rfpUri: e.uri });
          dispatch({ rfpUri: e.uri, rfpCid: e.cid, issuerDid: e.did })
            .catch((err) => log("error", "rfp watch dispatch failed", { rfpUri: e.uri, err: String(err) }));
        });
        rfpWatchers.push(w);
      }
      logger.info("bidder rfp firehose watches started", { count: rfpWatchers.length });
    }

    if (!skipServeBegin) {
      logger.info("bidder starting serve");
      await serve.beginServe();
    }

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
