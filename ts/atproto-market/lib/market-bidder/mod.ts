// Market bidder factory — lifecycle orchestration, callback merging, route wiring.
// Does NOT own I/O (no Deno.serve, signals, WS connect). Takes atproto + serve +
// providers; wires everything in beginServe().

import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import type { AttestationKeypair } from "@publicdomainrelay/market-atproto";
import { createRecordResolver } from "@publicdomainrelay/market-atproto";
import { createMarketFactory } from "@publicdomainrelay/hono-factory-market-atproto";
import type {
  RfpCallbacks,
  SubmitAcceptCallback,
  EventCallbacks,
} from "@publicdomainrelay/market-atproto";
import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
} from "@publicdomainrelay/market-common";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RelayRef, ServeHandle } from "@publicdomainrelay/serve";
import type { ATProto } from "@publicdomainrelay/atproto-helpers";
import type { ComputeProvider } from "@publicdomainrelay/compute-provider-abc";
import { createVmBidderCallbacks } from "@publicdomainrelay/market-bidder-compute";
import { createWorkerBidderCallbacks, type WorkerProvider } from "@publicdomainrelay/market-bidder-worker";

// ---------------------------------------------------------------------------
// Config types
// ---------------------------------------------------------------------------

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
}

export interface CallbackSet {
  rfpCallbacks?: RfpCallbacks;
  onAccept?: SubmitAcceptCallback;
  eventCallbacks?: EventCallbacks;
  eventBackground?: boolean;
}

export interface CallbackFactoryDeps {
  did: string;
  repoApi: RepoApi;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  attestationKp: AttestationKeypair;
  idResolver: IdResolver;
  relay: { proxyRef: string };
  dispatcherHost: string;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  createRecord: (collection: string, record: Record<string, unknown>) => Promise<StrongRef>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  deleteRecord: (collection: string, rkey: string) => Promise<void>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: ReturnType<typeof createRecordResolver>;
}

export interface MarketBidderConfig {
  logger: StructuredLoggerInterface;
  serve: ServeHandle;
  atproto: ATProto;
  relay?: RelayRef;
  providers?: MarketBidderProviderRef[];
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  callbackFactory?: (deps: CallbackFactoryDeps) => CallbackSet | Promise<CallbackSet>;
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
  const { logger, serve, atproto, relay, providers, setup, teardown, callbackFactory } = config;
  const log = logAdapter(logger);
  const activeContracts = new Map<string, ActiveContract>();
  const idResolver = atproto.idResolver;

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
    const rkey = TID.next().toString();
    await atproto.applyWrites(atproto.did, [{
      action: "create", collection: ALLOWLIST_NSID, rkey,
      record: {
        $type: ALLOWLIST_NSID,
        protects: { allowSelf: { service, scope: "account.auth" } },
        allowed: { allowSelf: [atproto.did] },
        createdAt: new Date().toISOString(),
      },
    }]);
    log("info", "bidder allowlist created", { uri: `at://${atproto.did}/${ALLOWLIST_NSID}/${rkey}`, service });
  }

  async function ensureOffering(): Promise<void> {
    const existing = await atproto.listRecords(atproto.did, OFFERING_NSID, { limit: 1 });
    if (existing?.records?.length) {
      log("info", "bidder offering exists", { uri: existing.records[0].uri });
      return;
    }
    const rkey = TID.next().toString();
    await atproto.applyWrites(atproto.did, [{
      action: "create", collection: OFFERING_NSID, rkey,
      record: {
        $type: OFFERING_NSID,
        endpointUrl: relay?.proxyRef ? didWebToHttps(relay.proxyRef) : `${atproto.did}#pdr_temp_market`,
        appliesTo: ["com.publicdomainrelay.temp.compute.vm", "com.publicdomainrelay.temp.compute.deno.workerManifest"],
        createdAt: new Date().toISOString(),
      },
    }]);
    log("info", "bidder offering created", { uri: `at://${atproto.did}/${OFFERING_NSID}/${rkey}` });
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

    if (merged.rfpCallbacks || merged.onAccept || merged.eventCallbacks) {
      const factory = createMarketFactory(
        {
          hostname: () => relay?.proxyRef ? didWebHost(relay.proxyRef) : "",
          idResolver,
          resolve: recordResolver,
          log,
        },
        {
          rfp: merged.rfpCallbacks,
          accept: merged.onAccept
            ? { serviceIds: [DEFAULT_MARKET_SERVICE_ID], onAccept: merged.onAccept }
            : undefined,
          event: merged.eventCallbacks
            ? { callbacks: merged.eventCallbacks, background: merged.eventBackground ?? true }
            : undefined,
        },
      );
      serve.app.route("/", factory.createApp() as never);
    }

    logger.info("bidder starting serve");
    await serve.beginServe();

    await ensureOperatorAllowlist("");
    await ensureOffering();
    logger.info("bidder ready", { did: atproto.did });
  }

  function shutdown(): void {
    for (const p of providers ?? []) {
      p.teardown?.().catch(() => {});
    }
    teardown?.().catch(() => {});
    serve.shutdown();
  }

  return { beginServe, shutdown };
}

export interface MarketBidderProviderRef {
  serviceId: string;
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  buildCallbacks(deps: CallbackFactoryDeps): CallbackSet;
}

export function isWorkerProvider(p: ComputeProvider | WorkerProvider): p is WorkerProvider {
  return "kind" in p && p.kind === "worker";
}

export function createComputeProviderMarketBidderHooks(opts: {
  provider: ComputeProvider | WorkerProvider;
}): MarketBidderProviderRef {
  const { provider } = opts;

  if (isWorkerProvider(provider)) {
    return {
      serviceId: "pdr_temp_market",
      setup: provider.setup,
      teardown: provider.teardown,
      buildCallbacks(deps: CallbackFactoryDeps): CallbackSet {
        const w = createWorkerBidderCallbacks({
          did: deps.did,
          attestationKp: deps.attestationKp,
          signer: deps.signer,
          idResolver: deps.idResolver,
          relay: deps.relay,
          workerManifestStore: provider.workerManifestStore,
          workerRunner: provider.workerRunner,
          log: deps.log,
          activeContracts: deps.activeContracts,
          createRepoRecord: deps.createRepoRecord,
          createSignedRepoRecord: deps.createSignedRepoRecord,
          callService: deps.callService,
          resolve: deps.resolve,
        });
        return { rfpCallbacks: w.rfp, onAccept: w.accept };
      },
    };
  }

  return {
    serviceId: "pdr_temp_market",
    setup: provider.setup,
    teardown: provider.teardown,
    buildCallbacks(deps: CallbackFactoryDeps): CallbackSet {
      const vm = createVmBidderCallbacks({
        did: deps.did,
        attestationKp: deps.attestationKp,
        signer: deps.signer,
        idResolver: deps.idResolver,
        relay: deps.relay,
        computeProvider: provider,
        log: deps.log,
        activeContracts: deps.activeContracts,
        createRepoRecord: deps.createRepoRecord,
        createSignedRepoRecord: deps.createSignedRepoRecord,
        callService: deps.callService,
        resolve: deps.resolve,
      });
      return { rfpCallbacks: vm.rfp, onAccept: vm.accept, eventCallbacks: vm.event };
    },
  };
}
