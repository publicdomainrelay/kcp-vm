// Full-lifecycle market bidder factory.
// Follows createRequesterPDS pattern: keypair, PLC, repo, relay, route wiring,
// server start, signal handling. Accepts callbackFactory for NSID-keyed
// extensibility — swap callbacks to change what the bidder bids on.

import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { PlcClient, createGenesisOp } from "@publicdomainrelay/did-plc";
import { loadOrGenerateKeypair } from "@publicdomainrelay/market-atproto";
import type { AttestationKeypair } from "@publicdomainrelay/market-atproto";
import {
  attestationFor,
  toStorableEntry,
  createRecordResolver,
  type InlineAttestation,
} from "@publicdomainrelay/market-atproto";
import { createSubscriber } from "@publicdomainrelay/did-key-relay-subscriber-xrpc";
import { createSubscriberFactory } from "@publicdomainrelay/hono-factory-did-key-relay-subscriber-xrpc";
import { createMarketFactory } from "@publicdomainrelay/hono-factory-market-atproto";
import type {
  RfpCallbacks,
  SubmitAcceptCallback,
  EventCallbacks,
} from "@publicdomainrelay/market-atproto";
import type { RecordMap } from "@atiproto/atproto-attestation";
import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import { noopLogger } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
  DEFAULT_COMPUTE_EVENT_SERVICE_ID,
} from "@publicdomainrelay/market-common";

// ---------------------------------------------------------------------------
// Config types
// ---------------------------------------------------------------------------

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
}

export interface ServeOpts {
  addr: string;
  port: number;
}

export interface XrpcRelayConfig {
  host: string;
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
  relay: { proxyRef: string; subdomain: string };
  dispatcherHost: string;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  createRecord: (collection: string, record: Record<string, unknown>) => Promise<StrongRef>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string }>;
  deleteRecord: (collection: string, rkey: string) => Promise<void>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: ReturnType<typeof createRecordResolver>;
}

export interface MarketBidderConfig {
  serveOpts: ServeOpts;
  privateKeyHex?: string;
  plcDirectoryUrl: string;
  label: string;
  relay: XrpcRelayConfig;
  inProcessPds?: { storage?: MemoryStorage };
  callbackFactory: (deps: CallbackFactoryDeps) => CallbackSet | Promise<CallbackSet>;
  log?: Logger;
}

export interface MarketBidder {
  did: string;
  app: ReturnType<typeof createRepoFactory>["app"];
  api: RepoApi;
  relay: { subdomain: string; proxyRef: string };
  stop(): void;
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

export async function createMarketBidder(config: MarketBidderConfig): Promise<MarketBidder> {
  const log = config.log ?? noopLogger;
  const { serveOpts, privateKeyHex, plcDirectoryUrl, label, relay: relayConfig } = config;
  const dispatcherHost = relayConfig.host;

  log("info", "bidder starting", { label, dispatcherHost });

  // ── keypair ──────────────────────────────────────────────────────────

  const keypair = privateKeyHex
    ? await Secp256k1Keypair.import(privateKeyHex)
    : await Secp256k1Keypair.create({ exportable: true });

  const privateKeyHexFinal = privateKeyHex ||
    Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

  const attestationKp = await loadOrGenerateKeypair(privateKeyHexFinal);

  // ── PLC registration ─────────────────────────────────────────────────

  const plc = new PlcClient({ baseUrl: plcDirectoryUrl });
  const signingKeyDid = keypair.did();

  const { did, op } = await createGenesisOp({
    rotationKeys: [signingKeyDid],
    verificationMethods: {
      atproto: signingKeyDid,
      attestation: attestationKp.did(),
    },
    alsoKnownAs: [
      `at://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    ],
    services: {
      atproto_pds: {
        type: "AtprotoPersonalDataServer",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
      },
      pdr_temp_market: {
        type: "PDRTempMarket",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
      },
      pdr_temp_compute_event: {
        type: "PDRTempComputeEvent",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
      },
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  log("info", "bidder did plc registering", { did });
  await plc.submitOp(did, op);
  log("info", "bidder did plc registered", { did });

  // ── signer ───────────────────────────────────────────────────────────

  const signer = {
    did: () => did,
    sign: (bytes: Uint8Array) => keypair.sign(bytes),
  };

  // ── repo factory ─────────────────────────────────────────────────────

  const { app, api } = createRepoFactory({
    storage: config.inProcessPds?.storage ?? new MemoryStorage(),
    signer,
    baseOrigin: `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    didWebServices: [
      { id: DEFAULT_MARKET_SERVICE_ID, type: "PDRTempMarket" },
      { id: DEFAULT_COMPUTE_EVENT_SERVICE_ID, type: "PDRTempComputeEvent" },
    ],
  });

  // ── record helpers ───────────────────────────────────────────────────

  async function createRecord(
    collection: string,
    record: Record<string, unknown>,
  ): Promise<StrongRef> {
    const rkey = TID.next().toString();
    await api.applyWrites(did, [{ action: "create", collection, rkey, record }]);
    const rec = await api.getRecord(did, collection, rkey);
    return {
      $type: "com.atproto.repo.strongRef",
      uri: `at://${did}/${collection}/${rkey}`,
      cid: rec?.cid ?? "",
    } as StrongRef;
  }

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
    issuer?: string,
  ): Promise<{ uri: string; cid: string }> {
    const rkey = TID.next().toString();
    const att = attestationFor(attestationKp, issuer);
    const entry = await att.sign({ record: record as RecordMap, repository: did }) as InlineAttestation;
    const signed = { ...record, signatures: [toStorableEntry(entry)] };
    await api.applyWrites(did, [{ action: "create", collection, rkey, record: signed }]);
    const rec = await api.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function deleteRecord(collection: string, rkey: string): Promise<void> {
    await api.applyWrites(did, [{ action: "delete", collection, rkey }]);
  }

  async function callService(
    endpointUrl: string,
    nsid: string,
    lxm: string,
    body: Record<string, unknown>,
  ): Promise<{ status: number; ok: boolean; body: unknown }> {
    let targetBase: string;
    let audDid: string;

    if (endpointUrl.startsWith("http://") || endpointUrl.startsWith("https://")) {
      targetBase = `${endpointUrl.replace(/\/+$/, "")}/xrpc`;
      audDid = `did:web:${new URL(endpointUrl).host}`;
    } else if (endpointUrl.startsWith("did:")) {
      const didPart = endpointUrl.split("#")[0];
      const svcDoc = await idResolver.did.resolve(didPart);
      const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : DEFAULT_MARKET_SERVICE_ID;
      const svc = svcDoc?.service?.find?.((s: { id: string }) => s.id === `#${svcId}`);
      if (!svc) throw new Error(`service ${svcId} not found in DID doc for ${didPart}`);
      const ep = (svc as { serviceEndpoint: string }).serviceEndpoint.replace(/\/+$/, "");
      targetBase = `${ep}/xrpc`;
      audDid = `did:web:${new URL(ep).host}`;
    } else {
      throw new Error(`unresolvable endpoint: ${endpointUrl}`);
    }

    const token = await signServiceAuth(signer, { aud: audDid, lxm });
    const res = await fetch(`${targetBase}/${nsid}`, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    });
    let resBody: unknown;
    try { resBody = await res.json(); } catch { resBody = await res.text(); }
    return { status: res.status, ok: res.ok, body: resBody };
  }

  // ── relay subscription ───────────────────────────────────────────────

  const idResolver = new IdResolver();
  const dispatcherDid = `did:web:${dispatcherHost}`;
  const { handleRequest } = createSubscriberFactory({ app });

  async function getServiceAuthToken(lxm: string): Promise<string> {
    return await signServiceAuth(signer, { aud: dispatcherDid, lxm });
  }

  const relayBox = { subdomain: "", proxyRef: "" };

  log("info", "bidder relay connecting", { dispatcherHost });
  const handle = await createSubscriber({
    label,
    keypair,
    getServiceAuthToken,
    dispatcherHost,
    handleRequest,
  });
  relayBox.subdomain = handle.subdomain;
  relayBox.proxyRef = handle.proxyRef;
  log("info", "bidder relay registered", { subdomain: handle.subdomain, proxyRef: handle.proxyRef });

  // ── active contracts ─────────────────────────────────────────────────

  const activeContracts = new Map<string, ActiveContract>();

  // ── record resolver ──────────────────────────────────────────────────

  const recordResolver = createRecordResolver(idResolver);

  // ── callbacks ────────────────────────────────────────────────────────

  const callbackDeps: CallbackFactoryDeps = {
    did,
    repoApi: api,
    signer,
    attestationKp,
    idResolver,
    relay: relayBox,
    dispatcherHost,
    log,
    activeContracts,
    createRecord,
    createRepoRecord,
    createSignedRepoRecord,
    deleteRecord,
    callService,
    resolve: recordResolver,
  };

  const callbacks = await config.callbackFactory(callbackDeps);

  // ── route wiring ─────────────────────────────────────────────────────

  if (callbacks.rfpCallbacks || callbacks.onAccept || callbacks.eventCallbacks) {
    const factory = createMarketFactory(
      {
        hostname: () => relayBox.subdomain ? `${relayBox.subdomain}.${dispatcherHost}` : dispatcherHost,
        idResolver,
        resolve: recordResolver,
        log,
      },
      {
        rfp: callbacks.rfpCallbacks,
        accept: callbacks.onAccept
          ? { serviceIds: [DEFAULT_MARKET_SERVICE_ID], onAccept: callbacks.onAccept }
          : undefined,
        event: callbacks.eventCallbacks
          ? { callbacks: callbacks.eventCallbacks, background: callbacks.eventBackground ?? true }
          : undefined,
      },
    );
    // deno-lint-ignore no-explicit-any
    app.route("/", factory.createApp() as any);
  }

  // ── allowlist + offering ─────────────────────────────────────────────

  const ALLOWLIST_NSID = "com.publicdomainrelay.temp.auth.allowlist.rbacDid";
  const OFFERING_NSID = "com.publicdomainrelay.temp.market.offering";

  async function ensureOperatorAllowlist(service: string): Promise<void> {
    const existing = await api.listRecords(did, ALLOWLIST_NSID, { limit: 100 });
    for (const rec of existing?.records ?? []) {
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
    await api.applyWrites(did, [{
      action: "create", collection: ALLOWLIST_NSID, rkey,
      record: {
        $type: ALLOWLIST_NSID,
        protects: { allowSelf: { service, scope: "account.auth" } },
        allowed: { allowSelf: [did] },
        createdAt: new Date().toISOString(),
      },
    }]);
    log("info", "bidder allowlist created", { uri: `at://${did}/${ALLOWLIST_NSID}/${rkey}`, service });
  }

  async function ensureOffering(): Promise<void> {
    const existing = await api.listRecords(did, OFFERING_NSID, { limit: 1 });
    if (existing?.records?.length) {
      log("info", "bidder offering exists", { uri: existing.records[0].uri });
      return;
    }
    const rkey = TID.next().toString();
    await api.applyWrites(did, [{
      action: "create", collection: OFFERING_NSID, rkey,
      record: {
        $type: OFFERING_NSID,
        endpointUrl: `${did}#pdr_temp_market`,
        appliesTo: ["com.publicdomainrelay.temp.compute.vm", "com.publicdomainrelay.temp.compute.deno.workerManifest"],
        createdAt: new Date().toISOString(),
      },
    }]);
    log("info", "bidder offering created", { uri: `at://${did}/${OFFERING_NSID}/${rkey}` });
  }

  await ensureOperatorAllowlist("");
  await ensureOffering();

  // ── serve ────────────────────────────────────────────────────────────

  const url = `https://${relayBox.subdomain}.${dispatcherHost}`;
  log("info", "bidder ready", { did, subdomain: relayBox.subdomain, proxyRef: relayBox.proxyRef, url });

  const ac = new AbortController();
  Deno.serve(
    { hostname: serveOpts.addr, port: serveOpts.port, signal: ac.signal, onListen: ({ port: p }) => log("info", "listening", { port: p, did }) },
    app.fetch,
  );

  // ── stop ─────────────────────────────────────────────────────────────

  function stop(): void {
    handle.ws.close();
    activeContracts.clear();
    ac.abort();
  }

  Deno.addSignalListener("SIGINT", () => {
    log("info", "shutting down", { signal: "SIGINT" });
    stop();
    Deno.exit();
  });
  Deno.addSignalListener("SIGTERM", () => {
    log("info", "shutting down", { signal: "SIGTERM" });
    stop();
    Deno.exit();
  });

  return { did, app, api, relay: relayBox, stop };
}
