import { TID } from "@atproto/common";
import { IdResolver } from "@atproto/identity";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Agent, CredentialSession } from "@atproto/api";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { RepoApi, WriteOp } from "@publicdomainrelay/atproto-repo-abc";
import type { CommitEvent } from "@publicdomainrelay/atproto-repo-abc";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { PlcClient, createGenesisOp } from "@publicdomainrelay/did-plc";
import type { AttestationKeypair, InlineAttestation } from "@publicdomainrelay/market-atproto";
import { attestationFor, toStorableEntry, createRecordResolver } from "@publicdomainrelay/market-atproto";
import type { RecordResolver } from "@publicdomainrelay/market-abc";
import { createXrpcRelay } from "@publicdomainrelay/xrpc-relay";
import type { RelayRef, ServeHandle } from "@publicdomainrelay/serve";
import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import {
  DEFAULT_MARKET_SERVICE_ID,
  DEFAULT_COMPUTE_EVENT_SERVICE_ID,
} from "@publicdomainrelay/market-common";
import type { RecordMap } from "@atiproto/atproto-attestation";

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
    resolve: recordResolver,
  };
}

export interface CreateLocalPDSAgentOpts {
  logger: StructuredLoggerInterface;
  keypair: Secp256k1Keypair;
  serve: ServeHandle;
  plcDirectoryUrl: string;
  dispatcherHost: string;
}

export interface LocalPDSAgent extends AtprotoAgentLike {
  repoApi: RepoApi;
  serve: ServeHandle;
  relay: RelayRef;
  beginServe(): Promise<void>;
  stop(): void;
}

export async function createLocalPDSAgent(opts: CreateLocalPDSAgentOpts): Promise<LocalPDSAgent> {
  const { logger, keypair, serve, plcDirectoryUrl, dispatcherHost } = opts;
  const signingKeyDid = keypair.did();
  // DID-doc service endpoints advertise the canonical host without any internal
  // port (production has none); the relay subdomain alone routes via dispatcher.
  const epHost = dispatcherHost.replace(/:\d+$/, "");

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
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  logger.info("localPDS plc registering", { did });
  await plc.submitOp(did, op);
  logger.info("localPDS plc registered", { did });

  const signer = {
    did: () => did,
    sign: (bytes: Uint8Array) => keypair.sign(bytes),
  };

  const { app, api } = createRepoFactory({
    storage: new MemoryStorage(),
    signer,
    baseOrigin: `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    didWebServices: [
      { id: DEFAULT_MARKET_SERVICE_ID, type: "PDRTempMarket" },
      { id: DEFAULT_COMPUTE_EVENT_SERVICE_ID, type: "PDRTempComputeEvent" },
    ],
  });

  serve.app.route("/", app as never);

  const xrpcRelay = createXrpcRelay({
    logger,
    dispatcherHost,
    signer,
    keypair,
    localWsTarget: () => ({ hostname: "127.0.0.1", port: serve.tcpPort }),
  });
  serve.addRelay(xrpcRelay);

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
