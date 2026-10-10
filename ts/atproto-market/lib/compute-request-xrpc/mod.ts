import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { getPdsEndpoint } from "@atproto/common-web";
import {
  createRecordResolver,
  listRecordsAll,
  listRecordsPublic,
  verifyRecordSignatures,
  verifyRemoteProof,
} from "@publicdomainrelay/market-atproto";
import { atUriAuthority, stripResolved } from "@publicdomainrelay/market-abc";
import {
  ACCEPT_NSID,
  BID_NSID,
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_ONNETWORK_NSID,
  COMPUTE_EVENTS_VM_REGISTER_IDENTITY_NSID,
  COMPUTE_VM_NSID,
  EVENT_NSID,
  OFFERING_NSID,
  RECEIPT_NSID,
  RELAYS_NSID,
  RFP_NSID,
  SUBMIT_ACCEPT_LXM,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_EVENT_LXM,
  SUBMIT_EVENT_NSID,
  SUBMIT_RFP_LXM,
  SUBMIT_RFP_NSID,
} from "@publicdomainrelay/market-common";
import { bidWindowSecOf, firstFreeOf } from "@publicdomainrelay/policy-engine-cli-options";
import {
  createPolicyEvaluator,
  resolvePolicyName,
} from "@publicdomainrelay/policy-engine-evaluator";
import { WORKFLOWS } from "@publicdomainrelay/policies-gha-lite";
import { createPolicyRegistry } from "@publicdomainrelay/policy-deno-typescript";
import { GhaLiteExecutor } from "@publicdomainrelay/policy-engine-executor-gha-lite";
import { TypescriptExecutor } from "@publicdomainrelay/policy-engine-executor-typescript";
import {
  POLICY_GHA_LITE_NSID,
  POLICY_TYPESCRIPT_NSID,
  type PolicyResult,
} from "@publicdomainrelay/policy-engine-abc";
import { buildUserData } from "@publicdomainrelay/cloud-init-common";
import type { CloudInitContext } from "@publicdomainrelay/cloud-init-common";
import { buildSshKeyRbacRecord, FEDPROXY_RBAC_NSID } from "@publicdomainrelay/fedproxy-rbac-common";
import { deriveGrantVars, isWifSimpleConfig } from "@publicdomainrelay/guest-capability-abc";
import type {
  GuestCapability,
  GuestFetchedEvent,
  PrepareContext,
  WifSimpleConfig,
} from "@publicdomainrelay/guest-capability-abc";
import type {
  CollectedBid,
  ContractFlowOptions,
  RequesterPDS,
} from "@publicdomainrelay/requester-abc";
import { BidCollector, bidPayloadNsid, selectWinner } from "@publicdomainrelay/requester-abc";
import {
  advanceContract,
  createContractState,
  guestAddressKind,
  hasContract,
  patchContract,
  receiptKeyOf,
  releaseTargetOf,
  ReturnLatch,
} from "@publicdomainrelay/compute-request-abc";
import type {
  BidDecision,
  ComputeRequestHandlers,
  ContractOutcome,
  ContractReleaser,
  ContractState,
  ContractStatePatch,
  FlowDecision,
  NetworkEvent,
  RecordRef,
} from "@publicdomainrelay/compute-request-abc";
import type { LoggerInterface, StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { BIDS_FREE_NSID } from "@publicdomainrelay/market-lexicons";
import { createTangledGraphVouchResolver } from "@publicdomainrelay/trust-graph-tangled-graph";
import { createBadgeBlueKeysDelegatedTrustResolver } from "@publicdomainrelay/delegated-trust-badge-blue-keys";
import { createBadgeBlueKeysOperatorDiscovery } from "@publicdomainrelay/operator-discovery-badge-blue-keys";

type EventStreamsClient =
  import("@publicdomainrelay/atproto-event-streams-client").ATProtoEventStreamsClient;

export const DEFAULT_TRANSPORT_MODULE = "iroh";
export const DEFAULT_VM_DISK = "50G";
export const DEFAULT_VM_READY_TIMEOUT_SEC = 300;
export const RECEIPT_FIREHOSE_TIMEOUT_MS = 30_000;

export function defaultVmName(): string {
  const b = new Uint8Array(4);
  crypto.getRandomValues(b);
  return `compute-${Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("")}`;
}

export async function discoverBiddersFromRelay(opts: {
  relayUrl: string;
  collection: string;
  log?: LoggerInterface;
  timeoutMs?: number;
}): Promise<string[]> {
  const { relayUrl, collection, log, timeoutMs } = opts;
  try {
    const url = `${
      relayUrl.replace(/\/+$/, "")
    }/xrpc/com.atproto.sync.listReposByCollection?collection=${
      encodeURIComponent(collection)
    }&limit=1000`;
    log?.info("relay_discovery_query", { url, collection });
    const res = await fetch(url, {
      signal: timeoutMs ? AbortSignal.timeout(timeoutMs) : undefined,
    });
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
  return [...new Set(results.flat())];
}

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
          if (
            typeof item === "string" && item.trim() &&
            (item.startsWith("https://") || item.startsWith("http://"))
          ) urls.push(item.trim());
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

export async function withDeadline<T>(
  what: string,
  ms: number,
  work: Promise<T>,
  onExpiry: (what: string, ms: number) => void,
): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      work,
      new Promise<undefined>((resolve) => {
        timer = setTimeout(() => {
          onExpiry(what, ms);
          resolve(undefined);
        }, ms);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

type FlowLog = (event: string, extra?: Record<string, unknown>) => void;

function flowLog(logger?: StructuredLoggerInterface): FlowLog {
  return (event, extra = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
}

function bestEffort(work: () => Promise<void>): void {
  work().catch(() => undefined);
}

function nowIso(): string {
  return new Date().toISOString();
}

interface FetchedRecord {
  uri: string;
  cid: string;
  value: Record<string, unknown>;
}

async function pdsUrlOf(idResolver: IdResolver, did: string): Promise<string | undefined> {
  const doc = await idResolver.did.resolve(did);
  if (!doc) return undefined;
  return getPdsEndpoint(doc) ?? undefined;
}

async function getRecordFrom(
  pdsUrl: string,
  repo: string,
  collection: string,
  rkey: string,
): Promise<FetchedRecord | null> {
  const recordUrl = `${pdsUrl}/xrpc/com.atproto.repo.getRecord?repo=${
    encodeURIComponent(repo)
  }&collection=${encodeURIComponent(collection)}&rkey=${rkey}`;
  const res = await fetch(recordUrl);
  const data = await res.json();
  const value = data.value as Record<string, unknown> | undefined;
  if (!value) return null;
  return { uri: data.uri as string, cid: data.cid as string, value };
}

interface FirehoseReceipt {
  receiptUri: string;
  receiptCid: string;
  submitEventRef?: string;
}

type OnNetworkPds = Pick<
  RequesterPDS,
  "setOnNetworkResolved" | "clearOnNetworkResolved" | "resolveIrohNodeId"
>;

interface ContractWatch {
  readonly firehose: boolean;
  readonly address: Promise<NetworkEvent>;
  setRfp(uri: string): void;
  setReceipt(receipt: RecordRef): void;
  bidsFor(rfpUri: string): CollectedBid[];
  receiptFor(acceptUri: string): FirehoseReceipt | undefined;
  backfill(winnerDid: string): Promise<void>;
  close(): void;
}

function openContractWatch(opts: {
  pds: OnNetworkPds;
  eventStreams?: EventStreamsClient;
  idResolver: IdResolver;
  logger?: StructuredLoggerInterface;
  log: FlowLog;
}): ContractWatch {
  const { pds, eventStreams, idResolver, logger, log } = opts;
  const discoveredBids = new Map<string, CollectedBid[]>();
  const discoveredReceipts = new Map<string, FirehoseReceipt>();
  const contractOnNetworkUris = new Set<string>();
  const addressReady = Promise.withResolvers<NetworkEvent>();
  let rfpUri = "";
  let receiptUri = "";
  let receiptCid = "";
  let vmAddress = "";
  let registeredKey = "";
  let watcher: { close(): void } | undefined;

  const resolveGuestAddress = (address: string, source: string) => {
    if (!address || vmAddress) return;
    const kind = guestAddressKind(address);
    if (kind === "ip") {
      log("vm_onnetwork_ip_skipped", { address, source, hint: "waiting for guest ticket or FQDN" });
      return;
    }
    vmAddress = address;
    addressReady.resolve({ address, kind, source });
    log("vm_fqdn_discovered", { fqdn: address, kind, source });
  };

  const onNetworkPayload = async (pdsUrl: string, payloadUri: string, eventUri: string) => {
    const [, , payloadRepo, payloadColl] = payloadUri.split("/");
    const rkey = payloadUri.split("/").pop()!;
    const payload = await getRecordFrom(pdsUrl, payloadRepo, payloadColl, rkey);
    const address = payload?.value.address as string | undefined;
    if (address) resolveGuestAddress(address, `firehose:${eventUri}`);
  };

  const matchesReceipt = (receiptRef: { uri?: string; cid?: string } | undefined): boolean => {
    if (
      receiptRef?.uri && receiptRef.cid && receiptRef.uri === receiptUri &&
      receiptRef.cid === receiptCid
    ) {
      return true;
    }
    if (receiptRef?.uri && receiptRef.uri === receiptUri && receiptRef.cid !== receiptCid) {
      log("receipt_cid_mismatch", {
        receiptUri,
        eventCid: receiptRef.cid,
        contractCid: receiptCid,
      });
    }
    return false;
  };

  const onWrappedEvent = async (pdsUrl: string, event: FetchedRecord) => {
    if (!matchesReceipt(event.value.receipt as { uri?: string; cid?: string } | undefined)) return;
    const payloadRef = event.value.payload as { uri?: string } | undefined;
    if (!payloadRef?.uri) return;
    if (payloadRef.uri.split("/")[3] !== COMPUTE_EVENTS_VM_ONNETWORK_NSID) return;
    contractOnNetworkUris.add(payloadRef.uri);
    log("firehose_onnetwork_wrapped", { eventUri: event.uri, payloadUri: payloadRef.uri });
    await onNetworkPayload(pdsUrl, payloadRef.uri, event.uri);
  };

  if (eventStreams) {
    const watched = [BID_NSID, EVENT_NSID, COMPUTE_EVENTS_VM_ONNETWORK_NSID, RECEIPT_NSID];
    watcher = eventStreams.watch({
      wantedCollections: watched,
      onEvent: (e) => {
        if (e.operation !== "create") return;
        if (!rfpUri && !receiptUri) return;
        if (e.collection === BID_NSID && rfpUri) {
          bestEffort(async () => {
            const pdsUrl = await pdsUrlOf(idResolver, e.did);
            if (!pdsUrl) return;
            const data = await getRecordFrom(pdsUrl, e.did, e.collection, e.rkey);
            if (!data) return;
            if ((data.value.rfp as { uri?: string } | undefined)?.uri !== rfpUri) return;
            const queue = discoveredBids.get(rfpUri) ?? [];
            queue.push({ did: e.did, uri: data.uri, cid: data.cid, record: data.value });
            discoveredBids.set(rfpUri, queue);
            log("firehose_bid_discovered", { bidUri: data.uri, rfpUri, bidderDid: e.did });
          });
        }
        if (e.collection === EVENT_NSID) {
          bestEffort(async () => {
            const pdsUrl = await pdsUrlOf(idResolver, e.did);
            if (!pdsUrl) return;
            const data = await getRecordFrom(pdsUrl, e.did, e.collection, e.rkey);
            if (!data) return;
            await onWrappedEvent(pdsUrl, data);
          });
        }
        if (e.collection === COMPUTE_EVENTS_VM_ONNETWORK_NSID) {
          log("firehose_onnetwork_raw", { uri: e.uri, did: e.did, rkey: e.rkey });
          if (!receiptUri || !contractOnNetworkUris.has(e.uri)) return;
          bestEffort(async () => {
            const pdsUrl = await pdsUrlOf(idResolver, e.did);
            if (!pdsUrl) return;
            const data = await getRecordFrom(pdsUrl, e.did, e.collection, e.rkey);
            const address = data?.value.address;
            if (typeof address === "string" && address) {
              resolveGuestAddress(address, `firehose:${data!.uri}`);
            }
          });
        }
        if (e.collection === COMPUTE_EVENTS_VM_REGISTER_IDENTITY_NSID) {
          bestEffort(async () => {
            const pdsUrl = await pdsUrlOf(idResolver, e.did);
            if (!pdsUrl) return;
            const data = await getRecordFrom(pdsUrl, e.did, e.collection, e.rkey);
            const identity = data?.value.computeIdentity as Record<string, unknown> | undefined;
            if (identity?.nodeId) {
              log("iroh_node_id_firehose", { nodeId: identity.nodeId });
              pds.resolveIrohNodeId?.(String(identity.nodeId));
            }
          });
        }
        if (e.collection === RECEIPT_NSID) {
          bestEffort(async () => {
            const pdsUrl = await pdsUrlOf(idResolver, e.did);
            if (!pdsUrl) return;
            const records = await listRecordsAll(pdsUrl, e.did, RECEIPT_NSID, {
              timeoutMs: 10_000,
            });
            for (const rec of records) {
              if (rec.uri !== e.uri) continue;
              const val = rec.value as Record<string, unknown>;
              const acceptRef = val.accept as { uri?: string } | undefined;
              if (!acceptRef?.uri) continue;
              discoveredReceipts.set(acceptRef.uri, {
                receiptUri: rec.uri,
                receiptCid: rec.cid,
                submitEventRef: val.submitEvent as string | undefined,
              });
            }
          });
        }
      },
      log: logger,
    });
    log("firehose_market_watch_started", {
      relayCount: eventStreams.relays.length,
      jetstreamCount: eventStreams.jetstreams.length,
      collections: watched,
    });
  }

  const clearRegistration = () => {
    if (registeredKey) pds.clearOnNetworkResolved?.(registeredKey);
    registeredKey = "";
  };

  return {
    firehose: watcher !== undefined,
    address: addressReady.promise,
    setRfp(uri) {
      rfpUri = uri;
    },
    setReceipt(receipt) {
      receiptUri = receipt.uri;
      receiptCid = receipt.cid;
      if (!receiptUri || !receiptCid) return;
      clearRegistration();
      registeredKey = receiptKeyOf(receipt);
      pds.setOnNetworkResolved?.(
        registeredKey,
        (address) => resolveGuestAddress(address, "submitEvent"),
      );
    },
    bidsFor(uri) {
      return discoveredBids.get(uri) ?? [];
    },
    receiptFor(acceptUri) {
      return discoveredReceipts.get(acceptUri);
    },
    async backfill(winnerDid) {
      try {
        const pdsUrl = await pdsUrlOf(idResolver, winnerDid);
        if (!pdsUrl) return;
        const events = await listRecordsAll(pdsUrl, winnerDid, EVENT_NSID, { timeoutMs: 10_000 });
        for (const event of events) {
          if (vmAddress) return;
          await onWrappedEvent(pdsUrl, event as FetchedRecord);
        }
      } catch (err) {
        log("onnetwork_backfill_failed", { winnerDid, error: String(err) });
      }
    },
    close() {
      clearRegistration();
      watcher?.close();
      watcher = undefined;
    },
  };
}

async function waitForAddress(
  watch: ContractWatch,
  timeoutMs: number,
  latch?: ReturnLatch,
): Promise<NetworkEvent | null> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      watch.address,
      new Promise<null>((resolve) => {
        timer = setTimeout(() => resolve(null), timeoutMs);
      }),
      ...(latch ? [latch.returned.then(() => null)] : []),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

export interface ReceiptVerification {
  ok: boolean;
  sigOk?: boolean;
  bindOk?: boolean;
}

export type ReceiptVerifier = (
  refs: { receipt: RecordRef; accept: RecordRef },
) => Promise<ReceiptVerification>;

export type RecordResolver = (ref: RecordRef) => Promise<Record<string, unknown>>;

export function createIdRecordResolver(idResolver: IdResolver): RecordResolver {
  return async (ref) =>
    await createRecordResolver(idResolver).resolve({ uri: ref.uri, cid: ref.cid } as never);
}

export function createReceiptVerifier(resolveRecord: RecordResolver): ReceiptVerifier {
  return async ({ receipt: receiptRef, accept: acceptRef }) => {
    const receipt = await resolveRecord({ uri: receiptRef.uri, cid: receiptRef.cid });
    const accept = await resolveRecord({ uri: acceptRef.uri, cid: acceptRef.cid });
    const receiptBare = stripResolved(receipt as never) as Record<string, unknown>;
    const sigOk = await verifyRecordSignatures({
      record: receiptBare,
      repositoryDid: atUriAuthority(receiptRef.uri),
    });
    const bindOk = verifyRemoteProof({
      subjectRecord: stripResolved(accept as never) as Record<string, unknown>,
      subjectRepositoryDid: atUriAuthority(acceptRef.uri),
      proofRecord: receiptBare,
    });
    return { ok: sigOk && bindOk, sigOk, bindOk };
  };
}

export type ComputeRequestOptions =
  & Omit<
    ContractFlowOptions,
    | "execProgram"
    | "keepVm"
    | "hold"
    | "holdAbort"
    | "onSshStart"
    | "onSshEnd"
    | "sshProxyCommandFn"
  >
  & {
    relayUrl?: string;
    offeringWatcherDids?: () => string[];
    logger?: StructuredLoggerInterface;
    payloadFactory?: () => Promise<{ uri: string; cid: string }>;
    vmDisk?: string;
    eventStreams?: EventStreamsClient;
    sshPublicKey?: string;
    receiptVerifier?: ReceiptVerifier;
    recordResolver?: RecordResolver;
  };

export interface ComputeRequestHandle {
  state: ContractState;
  capabilities: GuestCapability[];
  dispose(): Promise<void>;
}

type HandlerName = keyof ComputeRequestHandlers;

async function disposeAll(capabilities: GuestCapability[], log: FlowLog): Promise<void> {
  for (const cap of capabilities) {
    try {
      await cap.dispose?.();
    } catch (err) {
      log("capability_dispose_failed", { capability: cap.id, error: String(err) });
    }
  }
}

function capabilityHandle(
  state: ContractState,
  capabilities: GuestCapability[],
  log: FlowLog,
): ComputeRequestHandle {
  let disposed: Promise<void> | undefined;
  return {
    state,
    capabilities,
    dispose: () => disposed ??= disposeAll(capabilities, log),
  };
}

export function flowViewOf(state: ContractState): Record<string, unknown> {
  return {
    vmUri: state.payload?.uri,
    vmCid: state.payload?.cid,
    rfpUri: state.rfp?.uri,
    rfpCid: state.rfp?.cid,
    acceptUri: state.accept?.uri,
    acceptCid: state.accept?.cid,
    bidUri: state.bid?.uri,
    bidCid: state.bid?.cid,
    winnerDid: state.winner?.did,
    receiptUri: state.receipt?.uri,
    receiptCid: state.receipt?.cid,
    submitEventRef: state.submitEventRef,
    receiptOk: state.receiptOk ?? false,
    bids: state.bids,
  };
}

export async function requestCompute(
  pds: RequesterPDS,
  opts: ComputeRequestOptions = {},
  handlers: ComputeRequestHandlers = {},
): Promise<ComputeRequestHandle> {
  const vmName = opts.vmName ?? defaultVmName();
  const policySpec = opts.policy;
  const policyArgs = policySpec?.args ?? {};
  const bidWindowSec = bidWindowSecOf(policyArgs);
  const firstFree = firstFreeOf(policyArgs);
  const skipSsh = opts.skipSsh ?? false;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? DEFAULT_VM_READY_TIMEOUT_SEC;
  const extraBidderDids = opts.extraBidderDids ?? [];
  const denyBidderDids = opts.denyBidderDids ?? [];
  const capabilities = opts.capabilities ?? [];
  const relayUrls = opts.relayUrls ?? (opts.relayUrl ? [opts.relayUrl] : []);
  const transport = opts.userData?.transport ?? DEFAULT_TRANSPORT_MODULE;
  const logger = opts.logger;
  const log = flowLog(logger);
  const idResolver = new IdResolver({ plcUrl: opts.plcUrl });
  const resolveRecord = opts.recordResolver ?? createIdRecordResolver(idResolver);
  const verifyReceipt = opts.receiptVerifier ?? createReceiptVerifier(resolveRecord);

  let capKeypair: Promise<Secp256k1Keypair> | null = null;
  const capabilitySigner: PrepareContext["signer"] = {
    did: () => pds.did,
    sign: async (bytes: Uint8Array) => {
      capKeypair ??= Secp256k1Keypair.import(pds.privateKeyHex);
      return await (await capKeypair).sign(bytes);
    },
  };

  const marketDid =
    (pds as unknown as { oauthSession?: { userDid?: string } }).oauthSession?.userDid ??
      pds.did;
  const ownApi = (pds as unknown as {
    api: { listRecords(repo: string, coll: string): Promise<{ records?: unknown } | undefined> };
  }).api;
  const oauthAgent = (pds as unknown as Record<string, unknown>).oauthAgent as
    | {
      listRecords(
        did: string,
        collection: string,
        opts?: { limit?: number },
      ): Promise<{ records: Array<{ uri: string; cid?: string; value: Record<string, unknown> }> }>;
    }
    | undefined;

  let state = createContractState({ vmName, requesterDid: pds.did, marketDid, at: nowIso() });
  const latch = new ReturnLatch();
  let ended = false;

  const advance = (phase: ContractState["phase"], patch: ContractStatePatch) => {
    state = advanceContract(state, phase, patch, nowIso());
  };

  const runHandler = async <E, D extends FlowDecision | BidDecision>(
    name: HandlerName,
    fn: ((event: E, state: ContractState) => D | Promise<D>) | undefined,
    event: E,
  ): Promise<D | undefined> => {
    if (!fn) return undefined;
    try {
      return await fn(event, structuredClone(state));
    } catch (err) {
      log("compute_handler_failed", { handler: name, error: String(err) });
      state = patchContract(state, { error: `${name} failed: ${String(err)}` });
      latch.request(`${name}_failed`);
      return undefined;
    }
  };

  const decideFlow = async <E>(
    name: HandlerName,
    fn: ((event: E, state: ContractState) => FlowDecision | Promise<FlowDecision>) | undefined,
    event: E,
  ): Promise<boolean> => {
    const decision = await runHandler(name, fn, event);
    return latch.apply(decision, name);
  };

  const onGuestFetched = (event: GuestFetchedEvent) => {
    void (async () => {
      const decision = await runHandler("onSecretsFetched", handlers.onSecretsFetched, event);
      if (!ended) latch.apply(decision, "onSecretsFetched");
    })();
  };

  const watch = openContractWatch({
    pds,
    eventStreams: opts.eventStreams,
    idResolver,
    logger,
    log,
  });

  const finish = async (
    outcome: ContractOutcome,
    patch: ContractStatePatch = {},
  ): Promise<ComputeRequestHandle> => {
    ended = true;
    watch.close();
    state = patchContract(state, { ...patch, outcome });
    if (outcome === "returned") {
      log("compute_request_returned", { phase: state.phase, by: latch.reason });
    }
    if (!hasContract(state)) {
      await disposeAll(capabilities, log);
      return capabilityHandle(structuredClone(state), [], log);
    }
    return capabilityHandle(structuredClone(state), capabilities, log);
  };

  try {
    const ingressRef = pds.relay.ingressRef;
    const ingressProxyHost = opts.ingressProxyHost ??
      (pds.relaySubdomain.includes(".")
        ? pds.relaySubdomain.substring(pds.relaySubdomain.indexOf(".") + 1)
        : "xrpc.fedproxy.com");
    log("relay_ready_for_rfp", { ingressRef });

    let cloudInit: string;
    if (!skipSsh) {
      const capCtx: Partial<CloudInitContext> = {};
      for (const cap of capabilities) {
        const prepared = await cap.prepare?.({
          vmName,
          requesterDid: marketDid,
          ingressProxyHost,
          signer: capabilitySigner,
          log: (event, extra) => log(event, extra ?? {}),
          onGuestFetched,
        });
        Object.assign(capCtx, prepared?.ctx ?? {});
      }
      const ud = opts.userData;
      cloudInit = buildUserData({
        ctx: {
          vmName,
          ingressProxyHost,
          audHost: (opts.fedingressHost ? opts.fedingressHost.replace(/:\d+$/, "") : undefined) ||
            ingressProxyHost.replace(/:\d+$/, ""),
          hostAliases: opts.guestHostAliases,
          sshAuthorizedKey: [opts.sshPublicKey, ...(opts.sshAuthorizedKeys ?? [])]
            .filter((k): k is string => !!k)
            .join("\n"),
          ...capCtx,
        },
        base: ud?.base ?? opts.baseUserData,
        modules: [
          transport,
          ...(ud?.modules ?? []),
          ...capabilities.flatMap((c) => c.userDataModule ? [c.userDataModule] : []),
        ],
        overrides: ud?.overrides,
      });
      if (opts.userDataFactory) {
        cloudInit = opts.userDataFactory(opts.sshPublicKey ?? "");
      }
    } else {
      cloudInit = `#cloud-config
runcmd:
  - echo "test VM (no sshd) ready" | tee /tmp/ready
`;
    }

    const payload: RecordRef = opts.payloadFactory
      ? await opts.payloadFactory()
      : await pds.createRepoRecord(COMPUTE_VM_NSID, {
        $type: COMPUTE_VM_NSID,
        role: vmName.trim() || "compute",
        disk: opts.vmDisk ?? DEFAULT_VM_DISK,
        user_data: cloudInit,
        createdAt: nowIso(),
      });
    state = patchContract(state, { payload: { uri: payload.uri, cid: payload.cid } });
    log("vm_record_created", { uri: payload.uri, cid: payload.cid });

    const rfpRecord: Record<string, unknown> = {
      $type: RFP_NSID,
      domain: "compute",
      payload: { $type: "com.atproto.repo.strongRef", uri: payload.uri, cid: payload.cid },
      submitBid: `${pds.did}#pdr_temp_market`,
      createdAt: nowIso(),
    };

    const resolveRecordForPolicy = (
      ref: { uri: string; cid: string },
    ): Promise<Record<string, unknown>> => resolveRecord({ uri: ref.uri, cid: ref.cid });

    const listRecordsForPolicy = async (repo: string, coll: string) => {
      const merged = new Map<string, { uri: string; value: Record<string, unknown> }>();
      if (repo === marketDid) {
        for (
          const read of [
            async () => (await oauthAgent?.listRecords(repo, coll, { limit: 100 }))?.records,
            async () =>
              (await ownApi.listRecords(repo, coll))?.records as
                | Array<{ uri: string; value: Record<string, unknown> }>
                | undefined,
          ]
        ) {
          try {
            for (const r of (await read()) ?? []) merged.set(r.uri, r);
          } catch {
            continue;
          }
        }
      }
      try {
        for (const r of await listRecordsPublic(idResolver, repo, coll)) merged.set(r.uri, r);
      } catch (err) {
        log("policy_public_records_failed", { repo, collection: coll, error: String(err) });
      }
      return [...merged.values()];
    };

    const operatorDiscovery = createBadgeBlueKeysOperatorDiscovery({
      listRecordsOwn: (collection) => listRecordsForPolicy(marketDid, collection),
      listRecordsPublic: (repo, collection) => listRecordsPublic(idResolver, repo, collection),
      log: (level, msg, meta) => log(`operator_discovery_${level}`, { msg, ...(meta ?? {}) }),
    });

    const resolveOperatorDid = async (bidderDid: string): Promise<string | null> => {
      try {
        const opDids = await operatorDiscovery.discoverOperatorDids(bidderDid);
        if (opDids.length > 0) return opDids[0];
      } catch (err) {
        log("operator_did_resolution_failed", { bidderDid, error: String(err) });
      }
      return bidderDid;
    };

    const policyVouchResolver = createBadgeBlueKeysDelegatedTrustResolver({
      vouchResolver: createTangledGraphVouchResolver({
        listRecords: listRecordsForPolicy,
        log: (level, msg, meta) => log(`policy_vouch_${level}`, { msg, ...(meta ?? {}) }),
      }),
      listOwnRecords: (collection, _opts) => listRecordsForPolicy(marketDid, collection),
      log: (level, msg, meta) => log(`policy_vouch_${level}`, { msg, ...(meta ?? {}) }),
    });

    const policyRegistry = createPolicyRegistry();
    const ghaLiteExecutor = new GhaLiteExecutor();
    const typescriptExecutor = new TypescriptExecutor();
    const evaluator = createPolicyEvaluator({
      registry: {
        get: ($t: string) =>
          $t === POLICY_GHA_LITE_NSID
            ? ghaLiteExecutor
            : $t === POLICY_TYPESCRIPT_NSID
            ? typescriptExecutor
            : undefined,
        kinds: () => [POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID],
      },
      resolve: (ref) => resolveRecordForPolicy(ref),
      resolveOperatorDid: (did) => resolveOperatorDid(did),
      getVouchedDids: (did) => policyVouchResolver.getDelegatedTrustedDids(did),
      policies: policyRegistry,
      log: (level, msg, meta) => log(`policy_eval_${level}`, { msg, ...(meta ?? {}) }),
    });

    let policyRef: RecordRef | undefined;
    if (policySpec) {
      try {
        const canonical = resolvePolicyName(policyRegistry, policySpec.name, "requester");
        const workflow = WORKFLOWS[canonical];
        const { nsid, record } = evaluator.buildPolicyRecord({
          name: policySpec.name,
          description: policySpec.description,
          args: policyArgs,
          requesterDid: marketDid,
          perspective: "requester",
          kind: workflow ? "gha-lite" : "typescript",
          workflow,
          policies: [{ name: canonical, args: policyArgs }],
        });
        policyRef = await pds.createRepoRecord(nsid, record);
        rfpRecord.policies = [{
          $type: "com.atproto.repo.strongRef",
          uri: policyRef.uri,
          cid: policyRef.cid,
        }];
        log("policy_attached", {
          policy: policySpec.name,
          policyNsid: nsid,
          policyUris: [policyRef.uri],
        });
      } catch (err) {
        log("policy_create_error", { error: String(err) });
      }
    }

    const rfp = await pds.createSignedRepoRecord(RFP_NSID, rfpRecord, pds.attestationKp, pds.did);
    const rfpUri = rfp.uri;
    const rfpCid = rfp.cid;
    watch.setRfp(rfpUri);
    log("rfp_created", { uri: rfpUri, cid: rfpCid, hasPolicy: !!policyRef });

    let vouchedDids: string[] = [];
    try {
      const publicVouchResolver = createTangledGraphVouchResolver({
        listRecords: (repo, coll) => listRecordsPublic(idResolver, repo, coll),
      });
      const delegatedTrust = createBadgeBlueKeysDelegatedTrustResolver({
        vouchResolver: publicVouchResolver,
        listOwnRecords: async (collection, _opts) => {
          const result = oauthAgent && marketDid !== pds.did
            ? await oauthAgent.listRecords(marketDid, collection, { limit: 100 })
            : await ownApi.listRecords(marketDid, collection);
          const records =
            (result?.records as Array<{ uri: string; value: Record<string, unknown> }>) ?? [];
          for (const r of records) {
            log("delegated_trust_record", {
              challenge: r.value.challenge,
              service: r.value.service,
              keyId: r.value.keyId,
              selfDid: marketDid,
            });
          }
          log("delegated_trust_badgeBlueKeys", {
            did: marketDid,
            collection,
            count: records.length,
          });
          return records;
        },
      });
      vouchedDids = [...await delegatedTrust.getDelegatedTrustedDids(marketDid)];
      log("vouch_discovery", { count: vouchedDids.length });
    } catch (err) {
      log("vouch_discovery_error", { error: String(err) });
    }

    const autoRelayUrls = await autoDiscoverRelayUrls({ log: logger });
    const allRelayUrls = [...new Set([...relayUrls, ...autoRelayUrls])];
    let relayDids: string[] = [];
    if (allRelayUrls.length > 0) {
      relayDids = await discoverBiddersFromRelays({
        relayUrls: allRelayUrls,
        collection: OFFERING_NSID,
        log: logger,
        timeoutMs: 15_000,
      });
      const counts = {
        relays: allRelayUrls.length,
        configured: relayUrls.length,
        autodiscovered: autoRelayUrls.length,
      };
      if (relayDids.length > 0) log("relay_discovery", { count: relayDids.length, ...counts });
      else log("relay_discovery_empty", counts);
    }

    const watcherDids = opts.offeringWatcherDids?.() ?? [];
    if (watcherDids.length > 0) log("offering_watch_discovery", { count: watcherDids.length });

    const bidderDids = Array.from(
      new Set([...relayDids, ...watcherDids, ...vouchedDids, ...extraBidderDids]),
    );
    const deniedSet = new Set(denyBidderDids);
    const filteredBidderDids = bidderDids.filter((d) => !deniedSet.has(d));
    log("bidder_discovery", {
      total: filteredBidderDids.length,
      relay: relayDids.length,
      watch: watcherDids.length,
      vouched: vouchedDids.length,
      extra: extraBidderDids.length,
      denied: bidderDids.length - filteredBidderDids.length,
    });

    const seen = new Set<string>();
    const rfpDeadlineMs = Math.max(5_000, bidWindowSec * 1_000);
    await Promise.allSettled(filteredBidderDids.map((bidderDid) =>
      withDeadline(
        bidderDid,
        rfpDeadlineMs,
        (async () => {
          try {
            const pdsUrl = await pdsUrlOf(idResolver, bidderDid);
            if (!pdsUrl) return;
            const offerings = await listRecordsAll(pdsUrl, bidderDid, OFFERING_NSID);
            for (const offering of offerings) {
              const appliesTo = offering.value.appliesTo as string[] | undefined;
              const endpointUrl = offering.value.endpointUrl as string | undefined;
              if (
                !endpointUrl || !Array.isArray(appliesTo) ||
                !appliesTo.includes(opts.appliesToNsid ?? COMPUTE_VM_NSID)
              ) continue;
              const target = await pds.resolveBidderEndpoint(endpointUrl);
              if (!target) {
                log("bidder_unknown_endpoint", { endpointUrl });
                continue;
              }
              const dedupKey = `${bidderDid}::${target.targetUrl}`;
              if (seen.has(dedupKey)) continue;
              seen.add(dedupKey);
              log("submitting_rfp", { bidderDid, endpointUrl });
              const r = await pds.callBidder(
                target.targetUrl,
                SUBMIT_RFP_NSID,
                SUBMIT_RFP_LXM,
                target.audDid,
                {
                  rfpUri,
                  rfpCid,
                },
              );
              log("submitRfp_result", { bidderDid, status: r.status, ok: r.ok });
            }
          } catch (err) {
            log("bidder_error", { bidderDid, error: String(err) });
          }
        })(),
        (what, ms) => log("bidder_deadline_exceeded", { what, ms }),
      )
    ));

    advance("rfp_submitted", {
      rfp: { uri: rfpUri, cid: rfpCid },
      ...(policyRef ? { policy: policyRef } : {}),
    });
    if (
      await decideFlow("onRfpSubmitted", handlers.onRfpSubmitted, {
        rfp: { uri: rfpUri, cid: rfpCid },
        payload,
        policy: policyRef,
        bidderDids: filteredBidderDids,
      })
    ) {
      pds.pendingBids.delete(rfpUri);
      return await finish("returned");
    }

    const evaluateCandidate = async (candidate: CollectedBid): Promise<PolicyResult> => {
      if (!policyRef) return { allow: true, violations: [] };
      const candidateDid = candidate.did;
      const payloadRef = candidate.record.payload as { uri: string; cid: string } | undefined;
      return await evaluator.evaluatePolicies({
        refs: [policyRef],
        ctx: {
          policyName: "requester-recheck",
          args: policyArgs,
          perspective: "requester",
          selfDid: marketDid,
          subjectDid: candidateDid,
          rootRequesterDid: marketDid,
          counterpartyDid: candidateDid,
          resolve: (ref) => resolveRecordForPolicy(ref),
          resolveOperatorDid,
          getVouchedDids: (did) => policyVouchResolver.getDelegatedTrustedDids(did),
          log: (level, msg, meta) => log(`policy_eval_${level}`, { msg, ...(meta ?? {}) }),
          ...(payloadRef
            ? {
              offer: {
                bidRef: { uri: candidate.uri, cid: candidate.cid },
                payloadRef,
                payloadNsid: bidPayloadNsid(candidate),
              },
            }
            : {}),
        },
      });
    };

    const bidDecisions = new Map<string, Promise<BidDecision>>();
    const decideBid = (bid: CollectedBid): Promise<BidDecision> => {
      let decision = bidDecisions.get(bid.uri);
      if (!decision) {
        decision = (async () => {
          const d = await runHandler("onBid", handlers.onBid, { bid });
          const result: BidDecision = d === "deny" ? "deny" : "accept";
          if (result === "deny") log("bid_denied", { uri: bid.uri, did: bid.did });
          return result;
        })();
        bidDecisions.set(bid.uri, decision);
      }
      return decision;
    };

    log("waiting_for_bids", { bidWindowSec, firstFree });

    let earlyWinner: CollectedBid | undefined;
    const collector = new BidCollector({
      freeBidNsid: BIDS_FREE_NSID,
      firstFree,
      allow: async (bid) =>
        (await decideBid(bid)) === "accept" && (await evaluateCandidate(bid)).allow,
      onEarlyWinner: (bid) => {
        earlyWinner = bid;
        log("first_free_winner", { uri: bid.uri, did: bid.did });
      },
    });

    const notDenied = (bid: CollectedBid) => !deniedSet.has(bid.did);
    const collect = () => {
      collector.addAll((pds.pendingBids.get(rfpUri) ?? []).filter(notDenied));
      collector.addAll(watch.bidsFor(rfpUri).filter(notDenied));
      for (const bid of collector.all()) void decideBid(bid);
    };

    let windowTimer: ReturnType<typeof setTimeout> | undefined;
    const windowElapsed = new Promise<void>((resolve) => {
      windowTimer = setTimeout(resolve, bidWindowSec * 1000);
    });
    const poller = setInterval(collect, 250);
    try {
      await Promise.race([
        windowElapsed,
        ...(firstFree ? [collector.earlyWinner.then(() => {})] : []),
      ]);
    } finally {
      clearInterval(poller);
      if (windowTimer !== undefined) clearTimeout(windowTimer);
    }

    collect();
    pds.pendingBids.delete(rfpUri);
    const bids = collector.all();
    log("bids_collected", { count: bids.length, earlyExit: !!earlyWinner });

    if (bids.length === 0) {
      const error = `no bids received within ${bidWindowSec}s`;
      log("no_bids", { event: "no_bids", error });
      return await finish("no_bids", { error, bids: 0 });
    }

    const candidates: CollectedBid[] = [];
    for (const bid of bids) {
      if ((await decideBid(bid)) === "accept") candidates.push(bid);
    }
    if (candidates.length === 0) {
      const error = `all ${bids.length} bids denied by onBid`;
      log("bids_denied", { event: "bids_denied", error });
      return await finish("bids_denied", { error, bids: bids.length });
    }

    const winner = earlyWinner ?? selectWinner(candidates)!;
    log("winner", { uri: winner.uri, did: winner.did, viaFirstFree: !!earlyWinner });
    const submitAcceptTarget = winner.record.submitAccept as string | undefined;
    advance("winner_selected", {
      bid: { uri: winner.uri, cid: winner.cid },
      winner: {
        did: winner.did,
        ...(submitAcceptTarget ? { submitAccept: submitAcceptTarget } : {}),
      },
      bids: bids.length,
    });

    const serviceName = vmName.trim() || "compute";
    let wifConfig: WifSimpleConfig | undefined;
    if ((opts.rbac && !skipSsh) || capabilities.length > 0) {
      const bidConfigRef = (winner.record.config ?? winner.record.bidConfig) as
        | { uri?: string; cid?: string }
        | undefined;
      if (bidConfigRef?.uri && bidConfigRef?.cid) {
        try {
          const cfg = await resolveRecord({ uri: bidConfigRef.uri, cid: bidConfigRef.cid });
          if (isWifSimpleConfig(cfg)) wifConfig = cfg;
          else {log("bid_config_incomplete", {
              reason: "missing issuer_uri/actx",
              bidConfigUri: bidConfigRef.uri,
            });}
        } catch (err) {
          log("bid_config_resolve_failed", { reason: String(err), bidConfigUri: bidConfigRef.uri });
        }
      } else {
        log("bid_config_missing", { reason: "winner bid has no bidConfig ref" });
      }
    }

    if (opts.rbac && !skipSsh) {
      if (wifConfig) {
        try {
          const rbacRecord = buildSshKeyRbacRecord({
            serviceName,
            issuerUri: wifConfig.issuer_uri,
            actx: wifConfig.actx,
            requesterDid: marketDid,
            subjectTemplate: wifConfig.subject,
          });
          const { uri: rbacUri } = await pds.createRepoRecord(FEDPROXY_RBAC_NSID, rbacRecord);
          log("rbac_created", { uri: rbacUri, serviceName, issuerUri: wifConfig.issuer_uri });
        } catch (err) {
          log("rbac_skipped", { reason: String(err) });
        }
      } else {
        log("rbac_skipped", { reason: "no usable bidConfig" });
      }
    }

    if (capabilities.length > 0) {
      if (wifConfig) {
        const grantVars = deriveGrantVars({
          cfg: wifConfig,
          subjectDid: atUriAuthority(rfpUri),
          audienceDid: marketDid,
          role: serviceName,
        });
        log("capability_grants", {
          subject: grantVars.subject,
          issuerUri: grantVars.issuerUri,
          aud: grantVars.expectedAud,
          capabilities: capabilities.map((c) => c.id),
        });
        for (const cap of capabilities) {
          try {
            await cap.onContract?.(grantVars);
          } catch (err) {
            log("capability_grant_failed", { capability: cap.id, error: String(err) });
          }
        }
      } else {
        log("capability_grants_skipped", {
          reason: "no usable bidConfig",
          capabilities: capabilities.map((c) => c.id),
        });
      }
    }

    if (policyRef && !earlyWinner) {
      const evalResult = await evaluateCandidate(winner);
      if (!evalResult.allow) {
        log("policy_rejected", { violations: evalResult.violations, winnerDid: winner.did });
        return await finish("policy_rejected", {
          error: `winner rejected by policy: ${evalResult.violations.map((v) => v.msg).join("; ")}`,
          bids: bids.length,
        });
      }
    }

    const accept = await pds.createSignedRepoRecord(
      ACCEPT_NSID,
      {
        $type: ACCEPT_NSID,
        rfp: { $type: "com.atproto.repo.strongRef", uri: rfpUri, cid: rfpCid },
        bid: { $type: "com.atproto.repo.strongRef", uri: winner.uri, cid: winner.cid },
        submitEvent: `${pds.did}#pdr_temp_compute_event`,
        createdAt: nowIso(),
      },
      pds.attestationKp,
      pds.did,
    );
    const acceptUri = accept.uri;
    const acceptCid = accept.cid;
    log("accept_created", { uri: acceptUri, cid: acceptCid });

    let receiptUri: string | undefined;
    let receiptCid: string | undefined;
    let submitEventRef: string | undefined;
    const takeReceipt = (found: { uri?: string; cid?: string; submitEvent?: string }) => {
      receiptUri = found.uri;
      receiptCid = found.cid;
      submitEventRef = found.submitEvent;
      watch.setReceipt({ uri: receiptUri ?? "", cid: receiptCid ?? "" });
    };

    if (submitAcceptTarget) {
      const target = await pds.resolveBidderEndpoint(submitAcceptTarget);
      if (target) {
        log("submitting_accept", { target: submitAcceptTarget });
        try {
          const r = await pds.callBidder(
            target.targetUrl,
            SUBMIT_ACCEPT_NSID,
            SUBMIT_ACCEPT_LXM,
            target.audDid,
            {
              acceptUri,
              acceptCid,
            },
          );
          takeReceipt(r.body as { uri?: string; cid?: string; submitEvent?: string });
          log("submitAccept_result", { status: r.status, receiptUri, receiptCid, submitEventRef });
        } catch (err) {
          log("submitAccept_error", { target: submitAcceptTarget, error: String(err) });
        }
      } else {
        log("accept_target_unresolvable", { submitAcceptTarget });
      }
    }

    const receiptPatch = (): ContractStatePatch => ({
      ...(receiptUri && receiptCid ? { receipt: { uri: receiptUri, cid: receiptCid } } : {}),
      ...(submitEventRef ? { submitEventRef } : {}),
    });

    advance("accepted", { accept: { uri: acceptUri, cid: acceptCid }, ...receiptPatch() });
    if (
      await decideFlow("onAccepted", handlers.onAccepted, {
        accept: { uri: acceptUri, cid: acceptCid },
        bid: { uri: winner.uri, cid: winner.cid },
        winnerDid: winner.did,
      })
    ) {
      return await finish("returned");
    }

    if (!receiptUri && watch.firehose) {
      log("receipt_firehose_fallback", { acceptUri });
      const deadline = Date.now() + RECEIPT_FIREHOSE_TIMEOUT_MS;
      let fromFirehose: FirehoseReceipt | undefined;
      while (Date.now() < deadline && !latch.requested) {
        fromFirehose = watch.receiptFor(acceptUri);
        if (fromFirehose) break;
        await new Promise((r) => setTimeout(r, 500));
      }
      if (fromFirehose) {
        takeReceipt({
          uri: fromFirehose.receiptUri,
          cid: fromFirehose.receiptCid,
          submitEvent: fromFirehose.submitEventRef,
        });
        log("receipt_from_firehose", { receiptUri, receiptCid, submitEventRef });
      } else if (!latch.requested) {
        log("receipt_firehose_timeout", { acceptUri });
      }
    }
    if (latch.requested) return await finish("returned", receiptPatch());

    let receiptOk = false;
    if (receiptUri && receiptCid) {
      try {
        const v = await verifyReceipt({
          receipt: { uri: receiptUri, cid: receiptCid },
          accept: { uri: acceptUri, cid: acceptCid },
        });
        receiptOk = v.ok;
        log("receipt_verified", { receiptUri, sigOk: v.sigOk, bindOk: v.bindOk, ok: receiptOk });
      } catch (err) {
        log("receipt_verify_error", { receiptUri, error: String(err) });
      }
      advance("receipt", { ...receiptPatch(), receiptOk });
    } else {
      log("receipt_missing", { receiptUri, receiptCid });
      state = patchContract(state, { ...receiptPatch(), receiptOk });
    }
    log("compute_request_complete", { event: "compute_request_complete", ...flowViewOf(state) });

    if (
      await decideFlow("onReceipt", handlers.onReceipt, {
        receipt: state.receipt,
        receiptOk,
        submitEventRef,
      })
    ) {
      return await finish("returned");
    }

    if (skipSsh) return await finish("complete");
    if (!receiptOk) {
      log("vm_poll_bailed", { reason: "no valid receipt", receiptUri, receiptCid });
      return await finish("receipt_invalid");
    }

    const network = await waitForAddress(watch, vmReadyTimeoutSec * 1000, latch);
    if (!network) {
      if (latch.requested) return await finish("returned");
      log("vm_fqdn_timeout", { timeoutSec: vmReadyTimeoutSec });
      return await finish("network_timeout");
    }
    advance("network", { vmAddress: network.address });
    if (await decideFlow("onNetwork", handlers.onNetwork, network)) return await finish("returned");
    return await finish(latch.requested ? "returned" : "complete");
  } catch (err) {
    ended = true;
    watch.close();
    await disposeAll(capabilities, log);
    throw err;
  }
}

export interface AwaitComputeNetworkOptions {
  logger?: StructuredLoggerInterface;
  eventStreams?: EventStreamsClient;
  plcUrl?: string;
  timeoutMs?: number;
  backfill?: boolean;
  onNetwork?: ComputeRequestHandlers["onNetwork"];
}

export async function awaitComputeNetwork(
  pds: OnNetworkPds,
  state: ContractState,
  opts: AwaitComputeNetworkOptions = {},
): Promise<ContractState> {
  const log = flowLog(opts.logger);
  if (state.vmAddress) return state;
  if (!state.receipt) {
    log("onnetwork_resume_skipped", { reason: "no receipt", phase: state.phase });
    return patchContract(state, { error: "no receipt to resume from" });
  }
  const watch = openContractWatch({
    pds,
    eventStreams: opts.eventStreams,
    idResolver: new IdResolver({ plcUrl: opts.plcUrl }),
    logger: opts.logger,
    log,
  });
  try {
    watch.setReceipt(state.receipt);
    log("onnetwork_resume", { receiptUri: state.receipt.uri, winnerDid: state.winner?.did });
    if (opts.backfill !== false && state.winner?.did) void watch.backfill(state.winner.did);
    const timeoutMs = opts.timeoutMs ?? DEFAULT_VM_READY_TIMEOUT_SEC * 1000;
    const network = await waitForAddress(watch, timeoutMs);
    if (!network) {
      log("vm_fqdn_timeout", { timeoutSec: timeoutMs / 1000 });
      return patchContract(state, { outcome: "network_timeout" });
    }
    const next = advanceContract(state, "network", {
      vmAddress: network.address,
      outcome: "complete",
    }, nowIso());
    try {
      await opts.onNetwork?.(network, structuredClone(next));
    } catch (err) {
      log("compute_handler_failed", { handler: "onNetwork", error: String(err) });
    }
    return next;
  } finally {
    watch.close();
  }
}

export interface ReleaseComputeOptions {
  logger?: StructuredLoggerInterface;
  capabilities?: GuestCapability[];
  reason?: string;
}

export async function releaseCompute(
  pds: ContractReleaser,
  state: ContractState,
  opts: ReleaseComputeOptions = {},
): Promise<ContractState> {
  const log = flowLog(opts.logger);
  const target = releaseTargetOf(state);
  if (!target) {
    log("vm_delete_skipped", {
      reason: "missing receipt refs",
      receiptUri: state.receipt?.uri,
      receiptCid: state.receipt?.cid,
      submitEventRef: state.submitEventRef,
    });
    return state;
  }
  let next = state;
  try {
    const at = nowIso();
    const deletion = await pds.createSignedRepoRecord(
      COMPUTE_EVENTS_VM_DELETE_NSID,
      {
        $type: COMPUTE_EVENTS_VM_DELETE_NSID,
        reason: opts.reason ?? "session_ended",
        createdAt: at,
      },
      pds.attestationKp,
      pds.did,
    );
    const eventRecord = {
      $type: EVENT_NSID,
      receipt: {
        $type: "com.atproto.repo.strongRef",
        uri: target.receipt.uri,
        cid: target.receipt.cid,
      },
      payload: { $type: "com.atproto.repo.strongRef", uri: deletion.uri, cid: deletion.cid },
      createdAt: at,
    };
    const event = await pds.createSignedRepoRecord(
      EVENT_NSID,
      eventRecord,
      pds.attestationKp,
      pds.did,
    );
    const bidder = await pds.resolveBidderEndpoint(target.submitEventRef);
    if (!bidder) {
      log("vm_delete_target_unresolvable", { submitEventRef: target.submitEventRef });
      next = patchContract(state, {
        error: `vm.delete target unresolvable: ${target.submitEventRef}`,
      });
    } else {
      log("submitting_vm_delete", { submitEventRef: target.submitEventRef, eventUri: event.uri });
      const r = await pds.callBidder(
        bidder.targetUrl,
        SUBMIT_EVENT_NSID,
        SUBMIT_EVENT_LXM,
        bidder.audDid,
        {
          uri: event.uri,
          cid: event.cid,
          record: eventRecord,
        },
      );
      log("vm_delete_result", { status: r.status, ok: r.ok });
      next = advanceContract(state, "released", {
        release: {
          event: { uri: event.uri, cid: event.cid },
          payload: { uri: deletion.uri, cid: deletion.cid },
          ok: r.ok,
          status: r.status,
        },
      }, nowIso());
    }
  } catch (err) {
    log("vm_delete_error", { error: String(err) });
    next = patchContract(state, { error: `vm.delete failed: ${String(err)}` });
  }
  for (const cap of opts.capabilities ?? []) {
    try {
      await cap.onRevoke?.();
    } catch (err) {
      log("capability_revoke_failed", { capability: cap.id, error: String(err) });
    }
  }
  return next;
}
