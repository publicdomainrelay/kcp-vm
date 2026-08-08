// Market bidder factory -- lifecycle orchestration, callback merging, route wiring.
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
import { OAuthSessionExpiredError } from "@publicdomainrelay/atproto-helpers";
import {
  OFFERING_NSID,
  ALLOWLIST_RBAC_DID_NSID,
  BADGE_BLUE_KEYS_NSID,
  ACCEPT_NSID,
  EVENT_NSID,
  COMPUTE_EVENTS_VM_ONNETWORK_NSID,
  BIDDER_ASSOCIATION_NSID,
} from "@publicdomainrelay/market-lexicons";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";
import type { VouchResolver } from "@publicdomainrelay/trust-graph-abc";
import { createTangledGraphVouchResolver } from "@publicdomainrelay/trust-graph-tangled-graph";
import { createBadgeBlueKeysDelegatedTrustResolver } from "@publicdomainrelay/delegated-trust-badge-blue-keys";
import { createPolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import type { PolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import { createScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import { createPolicyRegistry } from "@publicdomainrelay/policy-deno-typescript";
import { resolvePolicyName } from "@publicdomainrelay/policy-deno-typescript-shared";
import type { PolicyArgs } from "@publicdomainrelay/policy-deno-typescript-shared";
import { WORKFLOWS } from "@publicdomainrelay/policies-gha-lite";
import { GhaLiteExecutor } from "@publicdomainrelay/policy-engine-executor-gha-lite";
import { TypescriptExecutor } from "@publicdomainrelay/policy-engine-executor-typescript";
import { POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-engine-abc";
import { parseAtUri, type ATProto } from "@publicdomainrelay/atproto-helpers";
import type {
  ActiveContract,
  CallbackFactoryDeps,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
  PolicyExecOptions,
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
  /** Registry name of the policy gating which RFPs the bidder responds to. */
  policy?: string | null;
  /** Arguments handed to that policy. */
  policyArgs?: PolicyArgs;
  /** How this bidder is willing to execute an RFP's attached policy. */
  policyExec?: PolicyExecOptions;
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
   * run -- only the serve.beginServe() call is skipped.
   */
  skipServeBegin?: boolean;
  /**
   * Called when the OAuth session expires (refresh token consumed/revoked).
   * The session is dead -- delete the file and re-authenticate.
   */
  onSessionExpired?: (err: OAuthSessionExpiredError) => void;
  /** Pre-created acceptToContract map -- shared with providers for guest event routes. */
  acceptToContract?: Map<string, import("@publicdomainrelay/market-bidder-abc").GuestContractEntry>;
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
  const { logger, serve, atproto, relay, providers, setup, teardown, callbackFactory, onContractChange, eventStreams, offeringRefreshMs, skipServeBegin, onSessionExpired } = config;
  const policyRegistry = createPolicyRegistry();
  const policyArgs = config.policyArgs ?? {};
  // Legacy --policy name canonicalized to the bidder-side variant
  // (only-me -> bidder-only-me). Unknown/wrong-side names warn and fall back to
  // open (no scope gate), matching the old registry-miss behavior.
  let canonical: string | undefined;
  if (config.policy) {
    try {
      canonical = resolvePolicyName(policyRegistry, config.policy, "bidder");
    } catch (err) {
      logger.warn("bidder scope policy not found in registry", {
        policy: config.policy,
        known: policyRegistry.names(),
        error: String(err),
      });
    }
  }
  // The bidder's own in-memory gha-lite policy record backing the scope gate.
  // Stable synthetic uri/cid so the scope cache keys predictably per canonical
  // name + counterparty + args.
  const scopeRecord = canonical && WORKFLOWS[canonical]
    ? {
        uri: `at://${atproto.did}/policy-gha-lite/${canonical}`,
        cid: canonical,
        value: {
          $type: POLICY_GHA_LITE_NSID,
          name: canonical,
          workflow: WORKFLOWS[canonical],
          createdAt: new Date().toISOString(),
        },
      }
    : undefined;
  const log = logAdapter(logger);
  const activeContracts = new Map<string, ActiveContract>();
  const acceptToContract = config.acceptToContract ?? new Map<string, import("@publicdomainrelay/market-bidder-abc").GuestContractEntry>();
  const idResolver = atproto.idResolver;
  let offeringRefresher: OfferingRefreshHandle | null = null;

  const selfVouchResolver: VouchResolver = createTangledGraphVouchResolver({
    listRecords: async (_repo, coll) => {
      const result = await atproto.listRecords(atproto.did, coll, { limit: 100 });
      return (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
    },
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });

  const publicVouchResolver: VouchResolver = createTangledGraphVouchResolver({
    listRecords: (repo, coll) => listRecordsPublic(idResolver, repo, coll),
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });

  // -- Policy engine scope cache ----------------------------------------------
  // Hot-path verdict cache keyed by (policy identity, counterparty DID, args).
  // Firehose trust events (badgeBlueKeys / vouch / association) invalidate the
  // affected DIDs' cached verdicts so a changed operator or vouch cannot leave a
  // stale "no" (or "yes") behind. Negatives carry a short TTL, positives longer.
  const scopeCache = createScopeCache();

  // Network-based operator discovery: read the subject's badgeBlueKeys records
  // and return the CHALLENGE whose keyId is the subject DID and whose service is
  // bidder_associate / requester_associate (that challenge is the operator DID).
  // Canonical shape is {challenge: operator, keyId: associated} -- the operator
  // acknowledges the associated bidder/requester. The old inverted shape
  // {challenge: subject, keyId: operator} is a DIFFERENT subject's
  // acknowledgment and must not be read as this subject's operator.
  // No local trust snapshot to warm -- the scope cache absorbs per-counterparty
  // verdicts. Self-owned operators fall through to the evaluator's
  // `?? selfDid` handling when this returns null.
  const resolveOperatorDid = async (bidderDid: string): Promise<string | null> => {
    try {
      const records = await listRecordsPublic(idResolver, bidderDid, BADGE_BLUE_KEYS_NSID, { limit: 200 });
      for (const rec of records) {
        const v = rec.value as Record<string, unknown>;
        if (v.service !== "bidder_associate" && v.service !== "requester_associate") continue;
        // Canonical: {challenge: operator, keyId: associated} -- the subject is
        // the associated party; the challenge is its operator.
        if (v.keyId === bidderDid && typeof v.challenge === "string" && v.challenge.startsWith("did:")) {
          return v.challenge;
        }
      }
      // Legacy inverted shape: {challenge: subject, keyId: operator}. An
      // ephemeral requester/bidder declares its operator with challenge=self.
      // (An OPERATOR's own repo has acknowledgments of the same shape -- those
      // keyIds are its associated DIDs, not its operator -- so this fallback is
      // only a tiebreak for subjects whose canonical lookup found nothing.)
      for (const rec of records) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === bidderDid && (v.service === "bidder_associate" || v.service === "requester_associate")) {
          const keyId = v.keyId;
          if (typeof keyId === "string" && keyId.startsWith("did:")) return keyId;
        }
      }
    } catch {
      // non-critical -- no operator association readable for this did
    }
    return null;
  };

  // Policy vouch lookups are operator-delegated (same rationale as the
  // requester side): the ephemeral bidder DID has no vouch records of its own,
  // so a raw lookup would leave tangled-vouch with an empty set. Resolve self
  // -> operator via badgeBlueKeys (bidder_associate / requester_associate),
  // then merge the operator's vouches.
  const delegatedVouchResolver = createBadgeBlueKeysDelegatedTrustResolver({
    vouchResolver: publicVouchResolver,
    listOwnRecords: async (collection, opts) => {
      const result = await atproto.listRecords(atproto.did, collection, { limit: opts?.limit ?? 100 });
      return (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
    },
    log: (level, msg, meta) => logger[level as "info" | "warn"]?.(msg, meta),
  });
  const getVouchedDids = (did: string): Promise<Set<string>> => delegatedVouchResolver.getDelegatedTrustedDids(did);

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
  // canonical rkey for this bidder's lifetime -- every subsequent write
  // (correction or refresh) updates that same record in place via
  // atproto.updateRecord rather than creating a new one, so the collection
  // never grows past one record.
  async function ensureOffering(): Promise<{ rkey: string; createdAt: string } | null> {
    try {
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
    } catch (err) {
      if (err instanceof OAuthSessionExpiredError) {
        logger.error("bidder oauth session expired", {
          sessionPath: err.sessionPath,
          error: err.message,
        });
        onSessionExpired?.(err);
        return null;
      }
      throw err;
    }
  }

  async function beginServe(): Promise<void> {
    const recordResolver = createRecordResolver(idResolver);

    // Policy engine: registry (record $type -> executor), evaluator, and the
    // bidder's own scope gate. The scope gate is the hot-path engagement check:
    // on a scope-cache miss the evaluator runs the scope-mode workflow against
    // the counterparty DID, otherwise it serves the cached verdict.
    const ghaLiteExecutor = new GhaLiteExecutor();
    const typescriptExecutor = new TypescriptExecutor();
    const engineRegistry = {
      get: ($type: string) =>
        $type === POLICY_GHA_LITE_NSID ? ghaLiteExecutor
          : $type === POLICY_TYPESCRIPT_NSID ? typescriptExecutor
          : undefined,
      kinds: () => [POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID],
    };
    const evaluator: PolicyEvaluator = createPolicyEvaluator({
      registry: engineRegistry,
      resolve: (ref) => recordResolver.resolve(ref),
      resolveOperatorDid,
      getVouchedDids,
      scopeCache,
      log: (level, msg, meta) => logger[level as "info" | "warn" | "error" | "debug"]?.(msg, meta),
    });
    const scopeGate = async (counterpartyDid: string): Promise<boolean> => {
      if (!scopeRecord) return true;
      const r = await evaluator.scope({
        policyRecord: scopeRecord,
        perspective: "bidder",
        selfDid: atproto.did,
        counterpartyDid,
        args: policyArgs,
      });
      return r.allow;
    };

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
      acceptToContract,
      createRecord: atproto.createRecord,
      createRepoRecord: atproto.createRepoRecord,
      createSignedRepoRecord: atproto.createSignedRepoRecord,
      deleteRecord: atproto.deleteRecord,
      callService: atproto.callService,
      resolve: recordResolver,
      onContractChange,
      policyExec: config.policyExec,
      getVouchedDids,
      resolveOperatorDid,
      evaluator,
    };

    for (const p of providers ?? []) {
      await p.setup?.();
    }
    await setup?.();

    const merged: CallbackSet = {};
    // Dedup accept dispatch: push XRPC and firehose watcher both deliver the
    // same accept record. Once either path processes it, the other must skip.
    const acceptedUris = new Map<string, number>();
    let acceptSweepCounter = 0;
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

    // Firehose-only mode: initialize empty callbacks so RFP watcher can run
    if (eventStreams && !merged.rfpCallbacks) merged.rfpCallbacks = {};

    if (callbackFactory) {
      const cb = await callbackFactory(deps);
      if (cb.rfpCallbacks) merged.rfpCallbacks = deepMergeCallbacks(merged.rfpCallbacks ?? {}, cb.rfpCallbacks);
      if (cb.onAccept) merged.onAccept = merged.onAccept ?? cb.onAccept;
      if (cb.eventCallbacks) merged.eventCallbacks = deepMergeCallbacks(merged.eventCallbacks ?? {}, cb.eventCallbacks);
    }

    // Wrap merged.onAccept so the same acceptUri is never processed twice --
    // push XRPC and firehose watcher converge on this single callback.
    if (merged.onAccept) {
      const _raw = merged.onAccept;
      merged.onAccept = async (opts) => {
        if (acceptedUris.has(opts.acceptUri)) {
          log("info", "accept already processed, skipping duplicate dispatch", { acceptUri: opts.acceptUri });
          return { status: 200 };
        }
        acceptedUris.set(opts.acceptUri, Date.now());
        acceptSweepCounter++;
        if (acceptSweepCounter > 100) {
          const cutoff = Date.now() - 3_600_000; // 1 hour TTL
          for (const [k, ts] of acceptedUris) if (ts < cutoff) acceptedUris.delete(k);
          acceptSweepCounter = 0;
        }
        return _raw(opts);
      };
    }

    const marketDeps: MarketServerDeps = {
      hostname: () => relay?.ingressHost || "",
      idResolver,
      resolve: recordResolver,
      log,
    };

    if (merged.rfpCallbacks || merged.onAccept || merged.eventCallbacks || eventStreams) {
      const factory = createMarketFactory(marketDeps, {
        rfp: merged.rfpCallbacks,
        rfpScopeFilter: async ({ issuerDid }) => scopeGate(issuerDid),
        accept: merged.onAccept
          ? { serviceIds: [DEFAULT_MARKET_SERVICE_ID], onAccept: merged.onAccept }
          : undefined,
        event: merged.eventCallbacks
          ? { callbacks: merged.eventCallbacks, background: merged.eventBackground ?? true }
          : undefined,
      });
      serve.app.route("/", factory.createApp() as never);

      // Guest event endpoint -- VM calls back at boot with accept ref from accept.json.
      serve.app.post("/v1/on-network", async (c) => {
        let body: { acceptUri?: string; acceptCid?: string; address?: string; createdAt?: string };
        try { body = await c.req.json(); } catch {
          return c.json({ error: "InvalidRequest" }, 400);
        }
        if (!body.acceptUri || !body.acceptCid) {
          return c.json({ error: "InvalidRequest", message: "missing acceptUri or acceptCid" }, 400);
        }
        const acceptKey = `${body.acceptUri}#${body.acceptCid}`;
        const guestEntry = acceptToContract.get(acceptKey);
        if (!guestEntry) {
          log("warn", "guest.onNetwork: unknown accept ref", { acceptKey });
          return c.json({ error: "UnknownAccept", message: "accept ref not found -- VM may have been provisioned before receipt was created" }, 404);
        }
        const nowIso = body.createdAt ?? new Date().toISOString();
        const { uri, cid } = await atproto.createRepoRecord(
          COMPUTE_EVENTS_VM_ONNETWORK_NSID,
          { $type: COMPUTE_EVENTS_VM_ONNETWORK_NSID, address: body.address, createdAt: nowIso },
        );
        // Wrap in market.event with proper receipt strongRef
        const { uri: eventUri, cid: eventCid, record: eventRecord } = await atproto.createSignedRepoRecord(
          EVENT_NSID, {
            $type: EVENT_NSID,
            receipt: { $type: "com.atproto.repo.strongRef", uri: guestEntry.receiptUri, cid: guestEntry.receiptCid },
            payload: { $type: "com.atproto.repo.strongRef", uri, cid },
          }, atproto.did);
        log("info", "guest.onNetwork recorded", { receiptKey: guestEntry.receiptKey, uri, address: body.address });
        // Submit to requester if submitEventUrl exists
        if (guestEntry.submitEventUrl) {
          atproto.callService(guestEntry.submitEventUrl, "com.publicdomainrelay.temp.market.submitEvent", "com.publicdomainrelay.temp.market.submitEvent", {
            uri: eventUri, cid: eventCid, record: eventRecord,
          }).catch((err: unknown) => log("error", "guest.onNetwork submitEvent failed", { error: String(err) }));
        }
        return c.json({ ok: true, uri, cid, eventUri, eventCid });
      });
    }

    if (eventStreams) {
      // Trust invalidation: association/vouch/badgeBlueKeys commits drop the
      // scope cache's verdicts for the affected DID, so a changed operator or
      // vouch cannot leave a stale verdict behind. Only Jetstream delivers custom
      // collections; other transports get the scope cache's TTL instead.
      eventStreams.watch({
        wantedCollections: [BADGE_BLUE_KEYS_NSID, VOUCH_NSID, BIDDER_ASSOCIATION_NSID],
        onEvent: (e) => {
          scopeCache.applyEvent({ did: e.did, rkey: e.rkey });
        },
      });

      const dispatchCallbacks = merged.rfpCallbacks ?? {};
      const dispatch = createRfpDispatcher({ deps: marketDeps, callbacks: dispatchCallbacks });
      // Dedup handled by ATProtoEventStreamsClient -- no per-group seen Set needed.
      eventStreams.watch({
        wantedCollections: [RFP_NSID],
        onEvent: async (e) => {
          if (e.operation !== "create" && e.operation !== "update") return;
          if (!await scopeGate(e.did)) return;
          log("info", "rfp watch discovered", { rfpUri: e.uri });
          dispatch({ rfpUri: e.uri, rfpCid: e.cid, issuerDid: e.did })
            .catch((err) => log("error", "rfp watch dispatch failed", { rfpUri: e.uri, err: String(err) }));
        },
      });
      logger.info("bidder rfp firehose watches started via eventStreams client");
    }

    // Firehose watcher for ACCEPT_NSID -- fallback when bid.submitAccept is absent.
    // Discovers accept records referencing our bids, dispatches to merged.onAccept.
    if (merged.onAccept && eventStreams) {
      eventStreams.watch({
        wantedCollections: [ACCEPT_NSID],
        onEvent: async (e) => {
          if (e.operation !== "create") return;
          if (!await scopeGate(e.did)) return;
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

    // Firehose watcher for EVENT_NSID -- fallback when accept.submitEvent is absent.
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
            if (!payload) return;
            // market.event.payload is a strongRef, so its $type is the ref type
            // ("com.atproto.repo.strongRef"), not the payload's NSID -- keying the
            // handlers off it silently matched nothing and dropped every event
            // (vm.delete included). The NSID is the referenced URI's collection.
            const payloadNsid = payload.$type && payload.$type !== "com.atproto.repo.strongRef"
              ? payload.$type
              : payload.uri?.split("/")[3];
            if (!payloadNsid) return;
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
      const offeringResult = await ensureOffering();
      if (!offeringResult) {
        logger.warn("bidder offering skipped -- session expired");
        return;
      }
      const { rkey: offeringRkey, createdAt: offeringCreatedAt } = offeringResult;
      if (offeringRefreshMs && offeringRkey) {
        offeringRefresher = startOfferingRefresh({
          intervalMs: offeringRefreshMs,
          log,
          refresh: async () => {
            try {
              await atproto.updateRecord(OFFERING_NSID, offeringRkey, buildOffering(offeringCreatedAt));
            } catch (err) {
              if (err instanceof OAuthSessionExpiredError) {
                logger.error("bidder offering refresh failed -- session expired", {
                  sessionPath: err.sessionPath,
                });
                onSessionExpired?.(err);
                return;
              }
              /* best-effort for other errors */
            }
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

  const refreshOffering = () => ensureOffering().then((r) => { if (!r) logger.warn("bidder refreshOffering skipped -- session expired"); });
  return { beginServe, shutdown, refreshOffering };
}
