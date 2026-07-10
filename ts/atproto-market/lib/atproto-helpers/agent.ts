import { TID } from "@atproto/common";
import { IdResolver } from "@atproto/identity";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Agent, CredentialSession } from "@atproto/api";
import { OAuthClient } from "@atproto/oauth-client";
import type { Key } from "@atproto/oauth-client";
import { webCryptoRuntime, memoryStateStore, jsonSessionStore } from "@publicdomainrelay/atproto-oauth-helpers";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RepoApi, WriteOp } from "@publicdomainrelay/atproto-repo-abc";
import type { CommitEvent } from "@publicdomainrelay/atproto-repo-abc";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, DenoKvStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { PlcClient, PlcNotFoundError, createGenesisOp } from "@publicdomainrelay/did-plc";
import type { AttestationKeypair, InlineAttestation } from "@publicdomainrelay/market-atproto";
import { attestationFor, toStorableEntry, createRecordResolver } from "@publicdomainrelay/market-atproto";
import type { RecordResolver } from "@publicdomainrelay/market-abc";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";
import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
  DEFAULT_COMPUTE_EVENT_SERVICE_ID,
} from "@publicdomainrelay/market-common";
import type { RecordMap } from "@atiproto/atproto-attestation";
import { ASSOCIATE_CONFIRM_NSID } from "@publicdomainrelay/market-lexicons";
import { verifyServiceAuth } from "@publicdomainrelay/market-atproto";

export interface AtprotoAgentLike {
  did: string;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  applyWrites(did: string, writes: WriteOp[]): Promise<CommitEvent>;
  getRecord(did: string, collection: string, rkey: string): Promise<{ uri: string; cid: string; value: Record<string, unknown> } | null>;
  listRecords(did: string, collection: string, opts?: { limit?: number }): Promise<{ records: Array<{ uri: string; cid: string; value: Record<string, unknown> }> }>;
  getServiceAuth?(aud: string, lxm?: string): Promise<string>;
  /** Bypass applyWrites for Lexicons a PDS rejects unless created via the direct endpoint. */
  createRecord?(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  putRecord?(did: string, collection: string, rkey: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
}

export interface CreateATProtoOpts {
  logger: StructuredLoggerInterface;
  badgeBlueSigner: AttestationKeypair;
  plcDirectory: PlcClient;
  agent: AtprotoAgentLike;
}

export interface ATProto {
  did: string;
  getAgentDid(): string;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  attestationKp: AttestationKeypair;
  idResolver: IdResolver;
  plcClient: PlcClient;
  applyWrites(did: string, writes: WriteOp[]): Promise<CommitEvent>;
  getRecord(did: string, collection: string, rkey: string): Promise<{ uri: string; cid: string; value: Record<string, unknown> } | null>;
  listRecords(did: string, collection: string, opts?: { limit?: number }): Promise<{ records: Array<{ uri: string; cid: string; value: Record<string, unknown> }> }>;
  createRecord(collection: string, record: Record<string, unknown>): Promise<StrongRef>;
  updateRecord(collection: string, rkey: string, record: Record<string, unknown>): Promise<StrongRef>;
  createRepoRecord(collection: string, record: Record<string, unknown>): Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord(collection: string, record: Record<string, unknown>, issuer?: string): Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  deleteRecord(collection: string, rkey: string): Promise<void>;
  callService(endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>): Promise<{ status: number; ok: boolean; body: unknown }>;
  /** Register PDS hostname with a relay via requestCrawl. */
  requestCrawl(relayUrl: string, hostname: string): Promise<void>;
  /** Query a relay for DIDs that have records in the given collection. */
  listReposByCollection(relayUrl: string, collection: string): Promise<string[]>;
  resolve: ReturnType<typeof createRecordResolver>;
}

function makeLogger(logger: StructuredLoggerInterface): Logger {
  return (level: string, message: string, meta?: Record<string, unknown>) => {
    const l = level as "info" | "warn" | "error" | "debug";
    logger[l]?.(message, meta);
  };
}

export async function createATProto(opts: CreateATProtoOpts): Promise<ATProto> {
  const { logger, badgeBlueSigner, plcDirectory, agent } = opts;
  const l = makeLogger(logger);
  const did = agent.did;
  const signer = agent.signer;
  const idResolver = new IdResolver();

  async function createRecord(
    collection: string,
    record: Record<string, unknown>,
  ): Promise<StrongRef> {
    const rkey = TID.next().toString();
    await agent.applyWrites(did, [{ action: "create", collection, rkey, record }]);
    const rec = await agent.getRecord(did, collection, rkey);
    return {
      $type: "com.atproto.repo.strongRef",
      uri: `at://${did}/${collection}/${rkey}`,
      cid: rec?.cid ?? "",
    } as StrongRef;
  }

  async function updateRecord(
    collection: string,
    rkey: string,
    record: Record<string, unknown>,
  ): Promise<StrongRef> {
    await agent.applyWrites(did, [{ action: "update", collection, rkey, record }]);
    const rec = await agent.getRecord(did, collection, rkey);
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
    await agent.applyWrites(did, [{ action: "create", collection, rkey, record }]);
    const rec = await agent.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function createSignedRepoRecord(
    collection: string,
    record: Record<string, unknown>,
    issuer?: string,
  ): Promise<{ uri: string; cid: string; record: Record<string, unknown> }> {
    const rkey = TID.next().toString();
    const att = attestationFor(badgeBlueSigner, issuer);
    const entry = await att.sign({ record: record as RecordMap, repository: did }) as InlineAttestation;
    const signed = { ...record, signatures: [toStorableEntry(entry)] };
    await agent.applyWrites(did, [{ action: "create", collection, rkey, record: signed }]);
    const rec = await agent.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "", record: signed };
  }

  async function deleteRecord(collection: string, rkey: string): Promise<void> {
    await agent.applyWrites(did, [{ action: "delete", collection, rkey }]);
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

    const token = agent.getServiceAuth
      ? await agent.getServiceAuth(audDid, lxm)
      : await signServiceAuth(signer, { aud: audDid, lxm });
    const res = await fetch(`${targetBase}/${nsid}`, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    });
    let resBody: unknown;
    try { resBody = await res.json(); } catch { resBody = await res.text(); }
    return { status: res.status, ok: res.ok, body: resBody };
  }

  async function requestCrawl(relayUrl: string, hostname: string): Promise<void> {
    const base = relayUrl.replace(/\/+$/, "");
    l("info", "relay_registering_pds", { hostname, relay: relayUrl });
    try {
      const res = await fetch(`${base}/xrpc/com.atproto.sync.requestCrawl`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ hostname }),
      });
      if (res.ok) {
        l("info", "relay_registered_pds", { hostname, relay: relayUrl });
      } else {
        const text = await res.text().catch(() => "");
        l("warn", "relay_register_pds_failed", { hostname, relay: relayUrl, status: res.status, body: text.slice(0, 200) });
      }
    } catch (err) {
      l("warn", "relay_register_pds_error", { hostname, relay: relayUrl, error: String(err) });
    }
  }

  async function listReposByCollection(relayUrl: string, collection: string): Promise<string[]> {
    try {
      const url = `${relayUrl.replace(/\/+$/, "")}/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}`;
      const res = await fetch(url);
      if (!res.ok) return [];
      const data = await res.json() as { repos?: Array<{ did: string }> };
      return [...new Set((data.repos ?? []).map((r) => r.did).filter(Boolean))];
    } catch {
      return [];
    }
  }

  const recordResolver = createRecordResolver(idResolver);

  return {
    did,
    getAgentDid: () => did,
    signer,
    attestationKp: badgeBlueSigner,
    idResolver,
    plcClient: plcDirectory,
    applyWrites: (d: string, writes: WriteOp[]) => agent.applyWrites(d, writes),
    getRecord: (d: string, collection: string, rkey: string) => agent.getRecord(d, collection, rkey),
    listRecords: (d: string, collection: string, opts?: { limit?: number }) => agent.listRecords(d, collection, opts),
    createRecord,
    updateRecord,
    createRepoRecord,
    createSignedRepoRecord,
    deleteRecord,
    callService,
    requestCrawl,
    listReposByCollection,
    resolve: recordResolver,
  };
}

export interface CreateLocalPDSAgentOpts {
  logger: StructuredLoggerInterface;
  keypair: Secp256k1Keypair;
  serve: ServeHandle;
  plcDirectoryUrl: string;
  ingressProxyHost: string;
  storagePath?: string;
  /** Service ID for associateConfirm route + DID doc. Default: "requester_associate". */
  associateServiceId?: string;
}

export interface LocalPDSAgent extends AtprotoAgentLike {
  repoApi: RepoApi;
  serve: ServeHandle;
  relay: IngressRef;
  beginServe(): Promise<void>;
  stop(): void;
  /** Resolves with the caller's DID when the webapp calls associateConfirm. */
  readonly associateCalled: Promise<string>;
  /** CLI calls this to approve the association and respond to the webapp. */
  approveAssociation(): void;
  /** CLI calls this to reject the association. */
  rejectAssociation(err: Error): void;
}

export async function createLocalPDSAgent(opts: CreateLocalPDSAgentOpts): Promise<LocalPDSAgent> {
  const { logger, keypair, serve, plcDirectoryUrl, ingressProxyHost, storagePath } = opts;
  const associateServiceId = opts.associateServiceId ?? "requester_associate";
  const signingKeyDid = keypair.did();
  // DID-doc service endpoints advertise the canonical host without any internal
  // port (production has none); the relay subdomain alone routes via dispatcher.
  const epHost = ingressProxyHost.replace(/:\d+$/, "");

  const plc = new PlcClient({ baseUrl: plcDirectoryUrl });

  const attestationKp = await (async () => {
    const exportBytes = await keypair.export();
    const hex = Array.from(exportBytes).map((b) => b.toString(16).padStart(2, "0")).join("");
    const { loadOrGenerateKeypair } = await import("@publicdomainrelay/market-atproto");
    return loadOrGenerateKeypair(hex);
  })();

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
      [associateServiceId]: {
        type: "PDRRequesterAssociate",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  logger.info("localPDS plc registering", { did });
  try {
    await plc.resolve(did);
    logger.info("localPDS plc already registered", { did });
  } catch (err) {
    if (err instanceof PlcNotFoundError) {
      await plc.submitOp(did, op);
      logger.info("localPDS plc registered", { did });
    } else {
      throw err;
    }
  }

  const signer = {
    did: () => did,
    sign: (bytes: Uint8Array) => keypair.sign(bytes),
  };

  const { app, api, subscribe } = createRepoFactory({
    storage: storagePath ? await DenoKvStorage.create(storagePath) : new MemoryStorage(),
    signer,
    baseOrigin: `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${ingressProxyHost}`,
    didWebServices: [
      { id: DEFAULT_MARKET_SERVICE_ID, type: "PDRTempMarket" },
      { id: DEFAULT_COMPUTE_EVENT_SERVICE_ID, type: "PDRTempComputeEvent" },
      { id: associateServiceId, type: "PDRRequesterAssociate" },
    ],
  });

  serve.app.route("/", app as never);

  const xrpcRelay = createIngress({
    logger,
    ingressProxyHost,
    signer,
    keypair,
    directSubscriptionHandler: (subscriptionId, nsid, params, onEvent, _onData) => {
      const unsub = subscribe({ nsid, params }, (frame) => {
        try {
          onEvent(frame);
        } catch { /* relay closed */ }
      });
      return () => unsub?.();
    },
  });
  serve.addRelay(xrpcRelay);

  // ── association confirmation (webapp calls this when user scans QR) ──
  const idResolver = new IdResolver({ plcUrl: plcDirectoryUrl });
  let resolveAssociateCalled!: (callerDid: string) => void;
  const associateCalled = new Promise<string>((r) => { resolveAssociateCalled = r; });
  let resolveAssociationApproved!: () => void;
  let rejectAssociationApproved!: (err: Error) => void;
  const associationApproved = new Promise<void>((resolve, reject) => {
    resolveAssociationApproved = resolve;
    rejectAssociationApproved = reject;
  });

  serve.app.post(`/xrpc/${ASSOCIATE_CONFIRM_NSID}`, async (c: { req: { header(name: string): string | undefined }; json(obj: unknown, status?: number): Response }) => {
    const authHeader = c.req.header("Authorization");
    if (!authHeader) return c.json({ error: "Unauthorized" }, 401);
    try {
      const relayHost = xrpcRelay.ingressHost;
      const auth = await verifyServiceAuth({
        authHeader,
        hostname: relayHost || ingressProxyHost,
        lxm: ASSOCIATE_CONFIRM_NSID,
        serviceIds: [associateServiceId, "pdr_temp_market"],
        extraAudienceDids: [did],
        idResolver,
      });
      resolveAssociateCalled(auth.issuerDid);
      await associationApproved;
      const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
      await api.applyWrites(did, [{
        action: "create",
        collection: BADGE_BLUE_KEYS_NSID,
        rkey: TID.next().toString(),
        record: {
          $type: BADGE_BLUE_KEYS_NSID,
          keyId: auth.issuerDid,
          challenge: did,
          service: associateServiceId,
          createdAt: new Date().toISOString(),
        },
      }]);
      return c.json({ ok: true, requesterDid: did });
    } catch (err) {
      return c.json({ error: String(err) }, 401);
    }
  });

  return {
    did,
    signer,
    repoApi: api,
    serve,
    relay: xrpcRelay,
    applyWrites: (d: string, writes: WriteOp[]) => api.applyWrites(d, writes),
    getRecord: (d: string, collection: string, rkey: string) => api.getRecord(d, collection, rkey) as Promise<{ uri: string; cid: string; value: Record<string, unknown> } | null>,
    listRecords: (d: string, collection: string, opts?: { limit?: number }) => api.listRecords(d, collection, opts) as Promise<{ records: Array<{ uri: string; cid: string; value: Record<string, unknown> }> }>,
    beginServe: () => serve.beginServe(),
    stop: () => serve.shutdown(),
    associateCalled,
    approveAssociation: () => { resolveAssociationApproved(); },
    rejectAssociation: (err: Error) => { rejectAssociationApproved(err); },
  };
}

export interface CreateRemoteAgentOpts {
  handle: string;
  password: string;
  pdsUrl: string;
}

export async function createRemoteAgent(opts: CreateRemoteAgentOpts): Promise<AtprotoAgentLike> {
  const session = new CredentialSession(new URL(opts.pdsUrl));
  await session.login({ identifier: opts.handle, password: opts.password });
  const agent = new Agent(session);
  const did = session.did ?? "";

  const signer = {
    did: () => did,
    sign: async (_bytes: Uint8Array): Promise<Uint8Array> => {
      throw new Error("remote agent sign not supported");
    },
  };

  return {
    did,
    signer,
    async applyWrites(repo: string, writes: WriteOp[]): Promise<CommitEvent> {
      const res = await agent.com.atproto.repo.applyWrites({
        repo,
        writes: writes.map((w) => {
          const base: Record<string, unknown> = {
            action: w.action,
            collection: w.collection,
            rkey: w.rkey,
          };
          if (w.action !== "delete") (base as Record<string, unknown>).value = (w as { record: unknown }).record;
          return base as never;
        }),
      });
      const rev = res.data.commit?.rev as string ?? "";
      return { repo: repo as never, commit: rev as never, rev: rev as never, since: null as never, blocks: new Uint8Array() as never, ops: [] };
    },
    async getRecord(repo: string, collection: string, rkey: string) {
      try {
        const res = await agent.com.atproto.repo.getRecord({ repo, collection, rkey });
        return { uri: res.data.uri, cid: res.data.cid ?? "", value: res.data.value as Record<string, unknown> };
      } catch {
        return null;
      }
    },
    async listRecords(repo: string, collection: string, opts?: { limit?: number }) {
      const all: Array<{ uri: string; cid: string; value: Record<string, unknown> }> = [];
      let cursor: string | undefined;
      const limit = opts?.limit ?? 100;
      do {
        const res = await agent.com.atproto.repo.listRecords({ repo, collection, limit, cursor });
        for (const r of res.data.records) {
          all.push({ uri: r.uri, cid: r.cid ?? "", value: r.value as Record<string, unknown> });
        }
        cursor = res.data.cursor;
        if (all.length >= limit) break;
      } while (cursor);
      return { records: all.slice(0, limit) };
    },
  };
}

// ---------------------------------------------------------------------------
// OAuth agent — ATProto OAuth with @atproto/oauth-client
// ---------------------------------------------------------------------------

export interface CreateOAuthAgentOpts {
  handle: string;
  sessionPath: string;
  clientId?: string;
  redirectUri?: string;
  scope?: string;
  logger?: StructuredLoggerInterface;
}

export interface OAuthAgent extends AtprotoAgentLike {
  oauthClient: OAuthClient;
  startFlow(): Promise<string>; // returns auth URL
  completeFlow(params: Record<string, string>): Promise<void>;
  restore(): Promise<boolean>;
}

export async function createOAuthAgent(opts: CreateOAuthAgentOpts): Promise<OAuthAgent> {
  const clientId = opts.clientId ?? "http://localhost";
  const redirectUri = opts.redirectUri ?? "http://127.0.0.1:0/callback";
  const scope = opts.scope ?? "atproto";
  const log = opts.logger;

  const client = new OAuthClient({
    responseMode: "query",
    clientMetadata: {
      client_id: clientId,
      application_type: "native",
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
    allowHttp: clientId === "http://localhost" || redirectUri.startsWith("http://127.0.0.1") || redirectUri.startsWith("http://localhost"),
  });

  let _session: Awaited<ReturnType<typeof client.restore>> | null = null;

  function getSession() {
    if (!_session) throw new Error("OAuth session not initialized");
    return _session;
  }

  let _did = "";
  const _signer = {
    did: () => _did,
    sign: async () => { throw new Error("OAuth agent uses getServiceAuth, not local signing"); },
  };

  const agent: OAuthAgent = {
    oauthClient: client,
    get did() { return _did; },
    signer: _signer,

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
      try {
        _session = await client.restore(opts.handle);
        _did = _session.did;
        log?.info("oauth_session_restored", { did: _did });
        return true;
      } catch {
        return false;
      }
    },

    async getServiceAuth(aud: string, lxm?: string): Promise<string> {
      const s = await getSession();
      const res = await s.fetchHandler(
        `${s.server.issuer}/xrpc/com.atproto.server.getServiceAuth`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ aud, lxm: lxm ?? aud }),
        },
      );
      if (!res.ok) {
        throw new Error(`getServiceAuth failed: ${res.status}`);
      }
      const data = await res.json() as { token: string };
      return data.token;
    },

    async applyWrites(repo: string, writes: Parameters<AtprotoAgentLike["applyWrites"]>[1]) {
      const s = await getSession();
      const res = await s.fetchHandler(
        `${s.server.issuer}/xrpc/com.atproto.repo.applyWrites`,
        {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ repo, writes: writes.map((w) => {
            const base: Record<string, unknown> = {
              action: w.action,
              collection: w.collection,
              rkey: w.rkey,
            };
            if (w.action !== "delete") (base as Record<string, unknown>).value = (w as { record: unknown }).record;
            return base;
          }) }),
        },
      );
      if (!res.ok) {
        throw new Error(`applyWrites failed: ${res.status} ${await res.text()}`);
      }
      const data = await res.json() as { commit?: { rev?: string } };
      const rev = data.commit?.rev ?? "";
      return { repo, commit: rev, rev, since: null, blocks: new Uint8Array(), ops: [] } as unknown as CommitEvent;
    },

    async getRecord(repo: string, collection: string, rkey: string) {
      const s = await getSession();
      const res = await s.fetchHandler(
        `${s.server.issuer}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(repo)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`,
      );
      if (!res.ok) return null;
      const data = await res.json() as { uri: string; cid?: string; value: Record<string, unknown> };
      return { uri: data.uri, cid: data.cid ?? "", value: data.value };
    },

    async listRecords(repo: string, collection: string, opts?: { limit?: number }) {
      const s = await getSession();
      const all: Array<{ uri: string; cid: string; value: Record<string, unknown> }> = [];
      let cursor: string | undefined;
      const limit = opts?.limit ?? 100;
      do {
        const params = new URLSearchParams({ repo, collection, limit: String(limit) });
        if (cursor) params.set("cursor", cursor);
        const res = await s.fetchHandler(
          `${s.server.issuer}/xrpc/com.atproto.repo.listRecords?${params.toString()}`,
        );
        if (!res.ok) break;
        const data = await res.json() as { records: Array<{ uri: string; cid?: string; value: unknown }>; cursor?: string };
        for (const r of data.records) {
          all.push({ uri: r.uri, cid: r.cid ?? "", value: r.value as Record<string, unknown> });
        }
        cursor = data.cursor;
      } while (cursor);
      return { records: all.slice(0, limit) };
    },
  };

  return agent;
}
