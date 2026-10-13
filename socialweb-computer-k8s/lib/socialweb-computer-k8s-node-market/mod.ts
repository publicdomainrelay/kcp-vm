import { Secp256k1Keypair } from "@atproto/crypto";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import {
  awaitComputeTermination,
  type ComputeRequestHandle,
  findVmContract,
  listVmRecords,
  releaseCompute,
  requestCompute,
} from "@publicdomainrelay/compute-request-xrpc";
import {
  type ComputeRequestHandlers,
  CONTRACT_STATE_VERSION,
  type ContractState,
  type FlowDecision,
  isTerminated,
  parseContractState,
  releaseTargetOf,
} from "@publicdomainrelay/compute-request-abc";
import type { CapabilityPrepared, GrantVars, GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import { createSecretsCapability } from "@publicdomainrelay/guest-capability-secrets";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import {
  type ClaimKeyStore,
  type ClusterLinkReader,
  createMemoryClaimKeyStore,
  type NodeClaim,
  nodeClaimKey,
  type NodeClaimStore,
  type PinnedDownload,
} from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { createK3sJoinModule, MESHD_ARCHES, type MeshdPin } from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import {
  clusterLinkName,
  NODE_CLAIM_DOC_PATH,
  NODE_LINK_ENV_PATH,
  nodeAudience,
  nodeClaimDocument,
  nodeLinkEnv,
  OPENBAO_CA_PATH,
  OPENBAO_TICKET_PATH,
} from "@publicdomainrelay/socialweb-computer-k8s-common";
import {
  FINALIZER_NODE_CLAIM,
  type NodeClaimContract,
  type NodeClaimGrant,
  type NodeClaimPhase,
  type NodeClaimSpec,
  type NodeClaimStatus,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

type ComputeRequester = typeof requestCompute;

type ComputeRequestOptions = NonNullable<Parameters<ComputeRequester>[1]>;
type ComputeReleaser = typeof releaseCompute;

export type Log = (event: string, data?: Record<string, unknown>) => void;

export type SecretsCapabilityFactory = (
  secrets: SecretEntry[],
  logger: StructuredLoggerInterface,
  opts: { privateKeyHex: string },
) => GuestCapability;

export interface MarketNodeFulfillerOptions {
  store: NodeClaimStore;
  links: ClusterLinkReader;
  requesterFor(did: string): Promise<RequesterPDS>;
  plcUrl?: string;
  ingressProxyHost?: string;
  relayUrls?: string[];
  vmReadyTimeoutSec?: number;
  policy?: { name: string; args: Record<string, unknown> };
  extraBidderDids?: string[];
  guestHostAliases?: string[];
  userDataBase?: string;
  guestCaPem?: string;
  guestCurlHome?: string;
  pollIntervalMs?: number;
  requestCompute?: ComputeRequester;
  releaseCompute?: ComputeReleaser;
  secretsCapability?: SecretsCapabilityFactory;
  openBaoCAFileOf?: (spec: NodeClaimSpec) => string;
  eventStreams?: () => { close(): void };
  readText?: (path: string) => Promise<string>;
  meshd?: MeshdPin;
  scopeCache?: ComputeRequestOptions["scopeCache"];
  scopeRefresher?: ComputeRequestOptions["scopeRefresher"];
  claimKeys?: ClaimKeyStore;
  awaitTermination?: typeof awaitComputeTermination;
  findContract?: typeof findVmContract;
  owners?: () => Promise<string[]>;
  listVms?: typeof listVmRecords;
  ownershipScanIntervalMs?: number;
  unclaimedGraceMs?: number;
  now?: () => number;
  log?: Log;
}

export interface MarketNodeFulfiller {
  pass(): Promise<void>;
  start(): void;
  stop(): Promise<void>;
  active(): string[];
}

const IN_FLIGHT_PHASES: ReadonlySet<NodeClaimPhase> = new Set(["Bidding", "Provisioning"]);

const JOINED_PHASES: ReadonlySet<NodeClaimPhase> = new Set(["Joining", "Running"]);

const RELEASE_REASON_CLAIM_DELETED = "claim_deleted";

const RELEASE_REASON_UNCLAIMED = "unclaimed_after_join_grace";

export const DEFAULT_OWNERSHIP_SCAN_INTERVAL_MS = 5 * 60_000;

export const DEFAULT_UNCLAIMED_GRACE_MS = 10 * 60_000;

export const UNCLAIMED_SCAN_LOOKUPS_PER_PASS = 20;

export const OPENBAO_CA_RELATIVE_TO_CLUSTER_DIR = "openbao/tls/ca.crt";

export const MESHD_DOWNLOAD_PREFIX = "socialweb-meshd-";

export function meshdPinOf(downloads: PinnedDownload[], downloadBase: string | undefined): MeshdPin | null {
  if (!downloadBase) return null;
  const sha256: Partial<MeshdPin["sha256"]> = {};
  for (const arch of MESHD_ARCHES) {
    const found = downloads.find((d) => d.name === `${MESHD_DOWNLOAD_PREFIX}${arch}`);
    if (!found) return null;
    sha256[arch] = found.sha256;
  }
  return { downloadBase, sha256: sha256 as MeshdPin["sha256"] };
}

export function defaultOpenBaoCAFile(spec: NodeClaimSpec): string {
  const dir = spec.tokenFile.replace(/\/+[^/]*$/, "");
  return `${dir}/${OPENBAO_CA_RELATIVE_TO_CLUSTER_DIR}`;
}

export function publishedOpenBaoCAFile(caDir: string): (spec: NodeClaimSpec) => string {
  return (spec) => {
    const dir = spec.tokenFile.replace(/\/+[^/]*$/, "");
    const name = dir.replace(/^.*\//, "");
    return `${caDir.replace(/\/+$/, "")}/${name}.crt`;
  };
}

async function sha256(text: string): Promise<Uint8Array> {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
}

async function sha256Hex(text: string): Promise<string> {
  return Array.from(await sha256(text), (b) => b.toString(16).padStart(2, "0")).join("");
}

export function clusterKeyOfClaim(spec: Pick<NodeClaimSpec, "ownerDid" | "cluster">): Promise<string> {
  return Promise.resolve(spec.cluster);
}

export async function nodeGrantOf(spec: NodeClaimSpec, vars: Pick<GrantVars, "issuerUri" | "subject">): Promise<NodeClaimGrant> {
  return {
    issuer: vars.issuerUri,
    subject: vars.subject,
    audience: nodeAudience(await clusterKeyOfClaim(spec), spec.nodeName),
    role: spec.nodeName,
  };
}

function definedOnly<T extends object>(value: T): T {
  return Object.fromEntries(Object.entries(value).filter(([, v]) => v !== undefined)) as T;
}

export function claimContractOf(state: ContractState): NodeClaimContract {
  return definedOnly({
    ...state,
    vmUri: state.payload?.uri,
    rfpUri: state.rfp?.uri,
    acceptUri: state.accept?.uri,
    receiptUri: state.receipt?.uri,
    receiptCid: state.receipt?.cid,
  });
}

const LEGACY_FIELDS = ["vmUri", "rfpUri", "acceptUri", "receiptUri", "receiptCid"] as const;

function legacyContractState(claim: NodeClaim, contract: NodeClaimContract): ContractState {
  return {
    version: CONTRACT_STATE_VERSION,
    phase: contract.receiptUri && contract.receiptCid ? "receipt" : "requested",
    vmName: claim.spec.nodeName,
    requesterDid: claim.spec.ownerDid,
    marketDid: claim.spec.ownerDid,
    ...(contract.receiptUri && contract.receiptCid ? { receipt: { uri: contract.receiptUri, cid: contract.receiptCid } } : {}),
    ...(contract.submitEventRef ? { submitEventRef: contract.submitEventRef } : {}),
    timestamps: {},
  };
}

export function contractStateOfClaim(claim: NodeClaim): ContractState | null {
  const contract = claim.status.contract;
  if (!contract || Object.keys(contract).length === 0) return null;
  if (contract.version !== undefined) {
    const saved: Record<string, unknown> = { ...contract };
    for (const key of LEGACY_FIELDS) delete saved[key];
    try {
      return parseContractState(saved);
    } catch {
      return legacyContractState(claim, contract);
    }
  }
  return legacyContractState(claim, contract);
}

export function needsRelease(state: ContractState | null): state is ContractState {
  return state !== null && state.phase !== "released" && !isTerminated(state) && releaseTargetOf(state) !== null;
}

export function lostMessage(state: ContractState): string {
  const t = state.termination;
  if (!t) return "the node's guest is gone";
  const exit = t.exitCode !== undefined ? `, exit code ${t.exitCode}` : "";
  return `the bidder reported the guest terminated (${t.reason}${exit}) at ${t.observedAt}; the node is ` +
    "released without a vm.delete and replaced through a new claim and RFP";
}

async function freshKeyHex(): Promise<string> {
  const keypair = await Secp256k1Keypair.create({ exportable: true });
  return Array.from(await keypair.export(), (b) => b.toString(16).padStart(2, "0")).join("");
}

function confirmedReleased(state: ContractState | null): boolean {
  return state?.phase === "released" && state.release?.ok === true;
}

export interface EndFacts {
  error?: string;
  grantFailed?: boolean;
}

export function explainEnd(state: ContractState | null, facts: EndFacts = {}): string {
  if (facts.error) return `the contract failed: ${facts.error}`;
  if (!state) return "the contract did not run";
  if (state.outcome === "no_bids") return "no bids arrived for this node";
  if (state.outcome === "policy_rejected") return `the winning bid was rejected by policy${state.error ? `: ${state.error}` : ""}`;
  if (facts.grantFailed) {
    return "the node's workload grant could not be written to its claim, so the guest could never log in to the cluster OpenBao";
  }
  if (state.receiptOk) return "a VM was provisioned but it never fetched its join secrets";
  return `provisioning did not complete (${state.outcome ?? state.phase})${state.error ? `: ${state.error}` : ""}`;
}

export function grantingCapability(
  inner: GuestCapability,
  beforeGrant: (vars: GrantVars) => Promise<void>,
  afterPrepare?: (prepared: CapabilityPrepared | void) => Promise<void>,
): GuestCapability {
  return {
    id: inner.id,
    userDataModule: inner.userDataModule,
    prepare: inner.prepare
      ? async (ctx) => {
        const prepared = await inner.prepare!(ctx);
        await afterPrepare?.(prepared);
        return prepared;
      }
      : undefined,
    async onContract(vars) {
      await beforeGrant(vars);
      await inner.onContract?.(vars);
    },
    onRevoke: inner.onRevoke ? () => inner.onRevoke!() : undefined,
    dispose: inner.dispose ? () => inner.dispose!() : undefined,
  };
}

function untilFetched(fetched: Promise<void>, signals: AbortSignal[], timeoutMs: number): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const bounded = new Promise<void>((resolve) => {
    timer = setTimeout(resolve, timeoutMs);
    for (const signal of signals) signal.addEventListener("abort", () => resolve(), { once: true });
  });
  return Promise.race([fetched, bounded]).finally(() => clearTimeout(timer));
}

interface ReleaseOutcome {
  state: ContractState | null;
  attempted: boolean;
}

export function createMarketNodeFulfiller(opts: MarketNodeFulfillerOptions): MarketNodeFulfiller {
  const log = opts.log ?? (() => {});
  const request = opts.requestCompute ?? requestCompute;
  const release = opts.releaseCompute ?? releaseCompute;
  const readText = opts.readText ?? ((p: string) => Deno.readTextFile(p));
  const openBaoCAFileOf = opts.openBaoCAFileOf ?? defaultOpenBaoCAFile;
  const secretsCapability: SecretsCapabilityFactory = opts.secretsCapability ??
    ((secrets, logger, o) => createSecretsCapability({ secrets, logger, privateKeyHex: o.privateKeyHex }));
  const claimKeys = opts.claimKeys ?? createMemoryClaimKeyStore();
  const awaitTermination = opts.awaitTermination ?? awaitComputeTermination;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 600;
  const joinModule = createK3sJoinModule(opts.meshd ? { meshd: opts.meshd } : {});
  const guestNetwork = opts.guestCaPem || opts.guestCurlHome
    ? () => ({
      ...(opts.guestCaPem ? { ca_certs: { trusted: [opts.guestCaPem] } } : {}),
      ...(opts.guestCurlHome ? { bootcmd: [["systemctl", "set-environment", `CURL_HOME=${opts.guestCurlHome}`]] } : {}),
    })
    : null;
  const eventStreams = opts.eventStreams ?? (() =>
    createDefaultATProtoEventStreamsClient({ onlyRelays: opts.relayUrls?.length ? opts.relayUrls : undefined, log: quietLogger(log) }) as unknown as {
      close(): void;
    });

  const findContract = opts.findContract ?? findVmContract;
  const listVms = opts.listVms ?? listVmRecords;
  const now = opts.now ?? Date.now;
  const startedAt = now();
  const scanEveryMs = opts.ownershipScanIntervalMs ?? DEFAULT_OWNERSHIP_SCAN_INTERVAL_MS;
  const unclaimedGraceMs = opts.unclaimedGraceMs ?? DEFAULT_UNCLAIMED_GRACE_MS;
  const settledVms = new Set<string>();
  const knownOwners = new Set<string>();
  const unclaimedSince = new Map<string, number>();
  let lastScan = Number.NEGATIVE_INFINITY;
  let scanning: Promise<void> | null = null;

  const running = new Map<string, { deleted: AbortController; done: Promise<void> }>();
  const watching = new Map<string, { stop: AbortController; done: Promise<void> }>();
  const stopping = new AbortController();
  let timer: ReturnType<typeof setInterval> | undefined;
  let passing: Promise<void> | null = null;

  function claimLogger(key: string): StructuredLoggerInterface {
    return {
      debug: () => {},
      info: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
      warn: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
      error: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
    };
  }

  async function releaseFromClaim(claim: NodeClaim): Promise<ReleaseOutcome> {
    const state = contractStateOfClaim(claim);
    if (!needsRelease(state)) return { state, attempted: false };
    const pds = await opts.requesterFor(claim.spec.ownerDid);
    const next = await release(pds, state, { logger: claimLogger(nodeClaimKey(claim)), reason: RELEASE_REASON_CLAIM_DELETED });
    log("node_claim_contract_released", {
      claim: nodeClaimKey(claim),
      reason: RELEASE_REASON_CLAIM_DELETED,
      receiptUri: state.receipt?.uri,
      ok: next.release?.ok ?? false,
      error: next.error ?? null,
    });
    return { state: next, attempted: true };
  }

  async function markLost(claim: NodeClaim, ended: ContractState): Promise<void> {
    const key = nodeClaimKey(claim);
    let recorded: NodeClaim | null;
    try {
      recorded = await opts.store.get(claim);
    } catch (err) {
      log("node_claim_lost_deferred", { claim: key, error: String(err) });
      return;
    }
    if (!recorded || recorded.deletionTimestamp) return;
    if (recorded.status.phase === "Lost" || recorded.status.phase === "Released") return;
    const base = contractStateOfClaim(recorded) ?? ended;
    await opts.store.replaceStatus(recorded, {
      ...recorded.status,
      phase: "Lost",
      message: lostMessage(ended),
      contract: claimContractOf({ ...base, termination: ended.termination }),
    });
    log("node_claim_lost", {
      claim: key,
      reason: ended.termination?.reason ?? null,
      observedAt: ended.termination?.observedAt ?? null,
      receiptUri: base.receipt?.uri ?? null,
    });
  }

  function watchTermination(claim: NodeClaim, state: ContractState): void {
    const key = nodeClaimKey(claim);
    if (watching.has(key) || stopping.signal.aborted || !state.receipt || isTerminated(state)) return;
    const stop = new AbortController();
    const done = (async () => {
      let streams: { close(): void } | null = null;
      try {
        const pds = await opts.requesterFor(claim.spec.ownerDid);
        streams = eventStreams();
        log("node_claim_termination_watch", { claim: key, receiptUri: state.receipt?.uri ?? null });
        const ended = await awaitTermination(pds, state, {
          signal: stop.signal,
          eventStreams: streams as never,
          plcUrl: opts.plcUrl,
          logger: claimLogger(key),
        });
        if (ended && isTerminated(ended) && !stop.signal.aborted) await markLost(claim, ended);
      } catch (err) {
        log("node_claim_termination_watch_failed", { claim: key, error: String(err) });
      } finally {
        streams?.close();
        watching.delete(key);
      }
    })();
    watching.set(key, { stop, done });
  }

  function stopWatching(key: string): void {
    watching.get(key)?.stop.abort();
  }

  async function releaseDeleted(claim: NodeClaim): Promise<void> {
    const key = nodeClaimKey(claim);
    let recorded: NodeClaim | null;
    try {
      recorded = await opts.store.get(claim);
    } catch (err) {
      log("node_claim_release_deferred", { claim: key, error: String(err) });
      return;
    }
    if (!recorded) return;
    const outcome = await releaseFromClaim(recorded);
    const gone = !outcome.attempted || confirmedReleased(outcome.state);
    await opts.store.replaceStatus(recorded, {
      ...recorded.status,
      phase: gone ? "Released" : "Failed",
      message: gone
        ? "the claim was deleted and the node's VM released"
        : "the claim was deleted but the node's VM could not be confirmed released",
      contract: outcome.state ? { ...recorded.status.contract, ...claimContractOf(outcome.state) } : recorded.status.contract,
    }).catch((err) => log("node_claim_status_write_failed", { claim: key, error: String(err) }));
    if (gone) {
      await claimKeys.remove(recorded.spec.nodeName).catch((err) =>
        log("node_claim_key_remove_failed", { claim: key, error: String(err) })
      );
    }
    await opts.store.removeFinalizer(recorded).catch((err) =>
      log("node_claim_finalizer_release_failed", { claim: key, error: String(err) })
    );
    log("node_claim_released", { claim: key, vmReleased: outcome.attempted, confirmed: gone });
  }

  function fulfil(claim: NodeClaim, resume: ContractState | null = null): void {
    const key = nodeClaimKey(claim);
    const deleted = new AbortController();
    const ended = () => deleted.signal.aborted || stopping.signal.aborted;
    let status: NodeClaimStatus = resume ? structuredClone(claim.status) : { phase: "Bidding", contract: {} };
    let writes: Promise<unknown> = Promise.resolve();
    const write = (patch: Partial<NodeClaimStatus>): Promise<boolean> => {
      status = { ...status, ...patch, contract: patch.contract ?? status.contract };
      const snapshot = structuredClone(status);
      const result = writes.then(() => opts.store.replaceStatus(claim, snapshot)).then(
        () => true,
        (err) => {
          log("node_claim_status_write_failed", { claim: key, error: String(err) });
          return false;
        },
      );
      writes = result;
      return result;
    };
    const record = (state: ContractState, patch: Partial<NodeClaimStatus> = {}) =>
      write({ ...patch, contract: claimContractOf(state) });
    const stopWhenEnded = (): FlowDecision => ended() ? "return" : "continue";

    const done = (async () => {
      let streams: { close(): void } | null = null;
      let state: ContractState | null = null;
      let handle: ComputeRequestHandle | null = null;
      let error: string | undefined;
      let grantFailed = false;
      let fetched = false;
      const secretsFetched = Promise.withResolvers<void>();
      try {
        if (resume) {
          log("node_claim_resumed", { claim: key, phase: claim.status.phase ?? null, contractPhase: resume.phase });
        } else {
          await write({ phase: "Bidding", message: "requesting a node from the compute market" });
        }
        const spec = claim.spec;
        const clusterKey = await clusterKeyOfClaim(spec);
        const linkName = clusterLinkName(clusterKey, "openbao");
        const link = await opts.links.get(claim.workspace, linkName);
        const ticket = link?.status.ticket;
        if (!ticket) {
          throw new Error(
            `the cluster's OpenBao link ${linkName} carries no ticket yet${link?.status.message ? ` (${link.status.message})` : ""}`,
          );
        }
        const openBaoCA = await readText(openBaoCAFileOf(spec));
        const bundle: SecretEntry[] = [
          { path: OPENBAO_TICKET_PATH, value: ticket },
          { path: OPENBAO_CA_PATH, value: openBaoCA },
        ];
        const apiserverName = clusterLinkName(clusterKey, "apiserver");
        let apiserverLink: Awaited<ReturnType<ClusterLinkReader["get"]>> = null;
        let apiserverWhy: string | null = null;
        try {
          apiserverLink = await opts.links.get(claim.workspace, apiserverName);
        } catch (err) {
          apiserverWhy = String(err);
        }
        const linkEnv = apiserverLink?.status.phase === "Listening" && apiserverLink.status.ticket
          ? nodeLinkEnv(
            { node: spec.nodeName, advertiseAddress: spec.advertiseAddress, apiserverPort: spec.apiserverPort },
            apiserverLink.status.ticket,
          )
          : null;
        if (linkEnv) {
          bundle.push({ path: NODE_LINK_ENV_PATH, value: linkEnv });
          log("node_claim_apiserver_link_delivered", { claim: key, link: apiserverName });
        } else {
          log("node_claim_apiserver_link_withheld", {
            claim: key,
            link: apiserverName,
            phase: apiserverLink?.status.phase ?? null,
            ticketBytes: apiserverLink?.status.ticket?.length ?? 0,
            error: apiserverWhy,
            next: "the guest reads its apiserver ticket out of the cluster OpenBao instead, once its own link " +
              "to that OpenBao is up, so this node's join is serial rather than parallel; nothing else about " +
              "the claim changes, because this ticket is the only thing the door was adding",
          });
        }
        const stored = await claimKeys.ensure(spec.nodeName, await freshKeyHex());
        const ingressDid = (await Secp256k1Keypair.import(stored.privateKeyHex)).did();
        const served = async (prepared: CapabilityPrepared | void) => {
          const url = String(prepared?.ctx?.secretsUrl ?? "");
          const secrets = { ingressDid, keyRef: stored.keyRef, url, servedAt: new Date().toISOString() };
          if (!(await write({ secrets }))) {
            throw new Error("the secrets server's served state could not be written to its claim");
          }
          log("node_claim_secrets_served", { claim: key, url, ingressDid, keyRef: stored.keyRef, resumed: resume !== null });
        };
        const inner = secretsCapability(bundle, claimLogger(key), { privateKeyHex: stored.privateKeyHex });
        const capability = grantingCapability(inner, async (vars) => {
          const grant = await nodeGrantOf(spec, vars);
          if (!(await write({ grant }))) {
            const { grant: _unwritten, ...withoutGrant } = status;
            status = withoutGrant;
            grantFailed = true;
            throw new Error("the node's workload grant could not be written to its claim");
          }
          const doc = nodeClaimDocument(clusterKey, spec.nodeName, await sha256Hex(vars.issuerUri));
          bundle.push({ path: NODE_CLAIM_DOC_PATH, value: `${JSON.stringify(doc)}\n` });
          log("node_claim_grant_written", { claim: key, issuer: grant.issuer, audience: grant.audience, jwtMount: doc.jwtMount });
        }, served);
        const pds = await opts.requesterFor(spec.ownerDid);
        streams = eventStreams();

        const handlers: ComputeRequestHandlers = {
          onRfpSubmitted: async (_event, s) => {
            await record(s);
            return stopWhenEnded();
          },
          onAccepting: async (_event, s): Promise<FlowDecision> => {
            if (!(await record(s))) return "return";
            return stopWhenEnded();
          },
          onTerminated: async (_event, s) => {
            await record(s);
          },
          onAccepted: async (_event, s): Promise<FlowDecision> => {
            await record(s, { phase: "Provisioning", message: "a bid was accepted; the VM is booting" });
            return "continue";
          },
          onReceipt: async (_event, s): Promise<FlowDecision> => {
            await record(s);
            return grantFailed ? "return" : stopWhenEnded();
          },
          onNetwork: async (event, s) => {
            await record(s, { fqdn: event.address });
            return stopWhenEnded();
          },
          onHostKeys: async (event, s) => {
            log("node_claim_host_keys", { claim: key, fingerprints: event.keys.map((k) => k.fingerprint) });
            await record(s);
          },
          onSecretsFetched: async (_event, s): Promise<FlowDecision> => {
            if (fetched || ended()) return "return";
            fetched = true;
            await record(s, { phase: "Joining", message: "the node fetched its join secrets and is joining its cluster" });
            secretsFetched.resolve();
            return "return";
          },
        };

        handle = await request(pds, {
          vmName: spec.nodeName,
          userData: {
            ...(opts.userDataBase ? { base: opts.userDataBase } : {}),
            modules: ["k3s", joinModule, ...(guestNetwork ? [guestNetwork] : [])],
          },
          ...(opts.guestHostAliases?.length ? { guestHostAliases: opts.guestHostAliases } : {}),
          capabilities: [capability],
          ...(opts.scopeCache ? { scopeCache: opts.scopeCache } : {}),
          ...(opts.scopeRefresher ? { scopeRefresher: opts.scopeRefresher } : {}),
          denyBidderDids: spec.denyBidderDids ?? [],
          extraBidderDids: opts.extraBidderDids ?? [],
          vmReadyTimeoutSec,
          plcUrl: opts.plcUrl,
          ingressProxyHost: opts.ingressProxyHost,
          relayUrls: opts.relayUrls,
          eventStreams: streams as never,
          ...(opts.policy ? { policy: opts.policy } : {}),
          ...(resume ? { resume } : {}),
          logger: claimLogger(key),
        } as Parameters<ComputeRequester>[1], handlers);
        state = handle.state;
        await record(state);

        if (!fetched && state.accept && state.receiptOk && !grantFailed && !ended()) {
          await untilFetched(secretsFetched.promise, [deleted.signal, stopping.signal], vmReadyTimeoutSec * 1000);
        }
      } catch (err) {
        log("node_claim_contract_failed", { claim: key, error: String(err) });
        error = String(err);
      } finally {
        streams?.close();
        await handle?.dispose().catch(() => undefined);
      }

      await writes;
      if (deleted.signal.aborted) {
        await releaseDeleted(claim).catch((err) => log("node_claim_release_deferred", { claim: key, error: String(err) }));
      } else if (state && isTerminated(state)) {
        await write({ phase: "Lost", message: lostMessage(state) });
        await writes;
        log("node_claim_lost", { claim: key, reason: state.termination?.reason ?? null, during: "fulfil" });
      } else if (fetched) {
        log("node_claim_joining", { claim: key, outcome: state?.outcome ?? null });
        if (state) watchTermination(claim, state);
      } else if (stopping.signal.aborted) {
        log("node_claim_left_for_restart", { claim: key, phase: status.phase ?? null });
      } else {
        await write({ phase: "Failed", message: explainEnd(state, { error, grantFailed }) });
        log("node_claim_ended", { claim: key, outcome: state?.outcome ?? null, error: error ?? null });
      }
      running.delete(key);
    })();

    running.set(key, { deleted, done });
  }

  async function settleDeleted(claim: NodeClaim): Promise<void> {
    stopWatching(nodeClaimKey(claim));
    const live = running.get(nodeClaimKey(claim));
    if (live) {
      live.deleted.abort();
      return;
    }
    if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) return;
    await releaseDeleted(claim);
  }

  async function markOrphaned(claim: NodeClaim): Promise<void> {
    await opts.store.replaceStatus(claim, {
      ...claim.status,
      phase: "Failed",
      message: "the door restarted before this node fetched its join secrets, and the secrets server its guest " +
        "was told to use went with the old door; the node is released when its claim is deleted",
    });
    log("node_claim_orphaned", { claim: nodeClaimKey(claim), phase: claim.status.phase ?? null });
  }

  function adoptJoined(claim: NodeClaim): void {
    const key = nodeClaimKey(claim);
    const deleted = new AbortController();
    const done = (async () => {
      try {
        if (claim.status.phase !== "Adopting") {
          await opts.store.replaceStatus(claim, {
            ...claim.status,
            phase: "Adopting",
            message: "a joined Node with no claim: matching it to its receipt and vm.hostKeys record",
          });
        }
        log("node_claim_adopting", { claim: key, node: claim.spec.nodeName });
        const pds = await opts.requesterFor(claim.spec.ownerDid);
        const found = await findContract(pds as never, claim.spec.nodeName, { plcUrl: opts.plcUrl, logger: claimLogger(key) });
        const fresh = await opts.store.get(claim);
        if (!fresh || fresh.deletionTimestamp || deleted.signal.aborted || stopping.signal.aborted) return;
        const why = !found
          ? "no receipt in the owner's records names this node"
          : found.deleteSubmitted
          ? "its receipt was already released with a vm.delete"
          : isTerminated(found.state)
          ? `its bidder reported the guest terminated (${found.state.termination?.reason})`
          : !found.state.hostKeys
          ? "its receipt has no bidder-signed vm.hostKeys record, the guest's signed identity"
          : null;
        if (found?.state.payload) settledVms.add(found.state.payload.uri);
        if (why) {
          const contract = found
            ? claimContractOf(found.deleteSubmitted ? { ...found.state, phase: "released" } : found.state)
            : fresh.status.contract;
          await opts.store.replaceStatus(fresh, {
            ...fresh.status,
            phase: "Failed",
            message: `the joined Node could not be adopted: ${why}`,
            ...(contract ? { contract } : {}),
          });
          log("node_claim_adoption_refused", { claim: key, reason: why, receiptUri: found?.state.receipt?.uri ?? null });
          return;
        }
        await opts.store.replaceStatus(fresh, {
          ...fresh.status,
          phase: "Running",
          message: "adopted: the joined Node, its vault role and its receipt's vm.hostKeys record agree; no RFP and no vm.delete",
          contract: claimContractOf(found!.state),
        });
        log("node_claim_adopted", {
          claim: key,
          receiptUri: found!.state.receipt?.uri ?? null,
          winnerDid: found!.state.winner?.did ?? null,
          hostKeys: found!.state.hostKeys?.keys.map((k) => k.fingerprint) ?? [],
        });
        watchTermination(fresh, found!.state);
      } catch (err) {
        log("node_claim_adoption_failed", { claim: key, error: String(err), next: "the next pass tries again" });
      } finally {
        running.delete(key);
      }
    })();
    running.set(key, { deleted, done });
  }

  function claimsBlockingName(claims: NodeClaim[], owner: string, vmName: string): boolean {
    return claims.some((c) => {
      if (c.spec.ownerDid !== owner || c.spec.nodeName !== vmName) return false;
      const phase = c.status.phase;
      const settled = phase === "Running" || phase === "Joining" || phase === "Released" || phase === "Failed" ||
        phase === "Lost";
      return !settled || !contractStateOfClaim(c)?.payload;
    });
  }

  async function scanUnclaimed(claims: NodeClaim[]): Promise<void> {
    const claimedPayloads = new Set<string>();
    for (const owner of await opts.owners?.().catch(() => []) ?? []) knownOwners.add(owner);
    const owners = knownOwners;
    for (const c of claims) {
      owners.add(c.spec.ownerDid);
      const payload = contractStateOfClaim(c)?.payload?.uri;
      if (payload) claimedPayloads.add(payload);
    }
    let lookups = 0;
    let waiting = 0;
    for (const owner of owners) {
      if (stopping.signal.aborted) return;
      const pds = await opts.requesterFor(owner);
      const vms = await listVms(pds as never, { plcUrl: opts.plcUrl });
      for (const vm of vms) {
        if (stopping.signal.aborted) return;
        if (settledVms.has(vm.uri) || claimedPayloads.has(vm.uri) || claimsBlockingName(claims, owner, vm.vmName)) continue;
        if (lookups >= UNCLAIMED_SCAN_LOOKUPS_PER_PASS) {
          log("node_ownership_scan_bounded", { lookups, next: "the next scan continues" });
          return;
        }
        lookups++;
        const found = await findContract(pds as never, vm.vmName, { plcUrl: opts.plcUrl, payloadUri: vm.uri });
        if (!found || found.deleteSubmitted || isTerminated(found.state) || found.state.release) {
          settledVms.add(vm.uri);
          unclaimedSince.delete(vm.uri);
          continue;
        }
        const first = unclaimedSince.get(vm.uri) ?? now();
        unclaimedSince.set(vm.uri, first);
        const since = Math.max(first, startedAt);
        if (now() - since < unclaimedGraceMs) {
          waiting++;
          log("node_receipt_unclaimed_waiting", {
            owner,
            vmName: vm.vmName,
            receiptUri: found.state.receipt?.uri ?? null,
            waitedMs: now() - since,
            graceMs: unclaimedGraceMs,
            next: "a joined Node of this name is claimed again by the cluster provider and adopted; otherwise " +
              "the receipt is released once when the grace ends",
          });
          continue;
        }
        const next = await release(pds, found.state, { logger: claimLogger(`${owner}/${vm.vmName}`), reason: RELEASE_REASON_UNCLAIMED });
        settledVms.add(vm.uri);
        unclaimedSince.delete(vm.uri);
        log("node_receipt_unclaimed_released", {
          owner,
          vmName: vm.vmName,
          receiptUri: found.state.receipt?.uri ?? null,
          ok: next.release?.ok ?? false,
          error: next.error ?? null,
        });
      }
    }
    log("node_ownership_scan", { owners: owners.size, claims: claims.length, lookups, waiting, settled: settledVms.size });
  }

  function scanDue(): boolean {
    const elapsed = now() - lastScan;
    return elapsed >= scanEveryMs || (unclaimedSince.size > 0 && elapsed >= 30_000);
  }

  async function pass(): Promise<void> {
    const claims = await opts.store.list();
    for (const claim of claims) {
      const key = nodeClaimKey(claim);
      try {
        if (claim.deletionTimestamp) {
          await settleDeleted(claim);
          continue;
        }
        if (running.has(key) || stopping.signal.aborted) continue;
        const phase = claim.status.phase;
        if (phase === "Adopting" || ((!phase || phase === "Pending") && claim.adoptJoinedNode)) {
          if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) await opts.store.addFinalizer(claim);
          adoptJoined(claim);
        } else if (!phase || phase === "Pending") {
          if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) await opts.store.addFinalizer(claim);
          fulfil(claim);
        } else if (JOINED_PHASES.has(phase)) {
          if (!watching.has(key)) {
            const state = contractStateOfClaim(claim);
            if (state) watchTermination(claim, state);
          }
        } else if (IN_FLIGHT_PHASES.has(phase)) {
          const stored = claim.status.secrets?.keyRef ? await claimKeys.read(claim.spec.nodeName) : null;
          if (stored) fulfil(claim, contractStateOfClaim(claim));
          else await markOrphaned(claim);
        }
      } catch (err) {
        log("node_claim_pass_failed", { claim: key, error: String(err) });
      }
    }
    if (!scanning && !stopping.signal.aborted && scanDue()) {
      lastScan = now();
      scanning = scanUnclaimed(claims).catch((err) => log("node_ownership_scan_failed", { error: String(err) })).finally(() => {
        scanning = null;
      });
    }
  }

  return {
    pass,
    start() {
      if (timer !== undefined) return;
      const tick = () => {
        if (passing) return;
        passing = pass().catch((err) => log("node_claim_list_failed", { error: String(err) })).finally(() => {
          passing = null;
        });
      };
      tick();
      timer = setInterval(tick, opts.pollIntervalMs ?? 5_000);
      opts.store.watch?.(stopping.signal, tick);
    },
    async stop() {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
      stopping.abort();
      for (const w of watching.values()) w.stop.abort();
      await passing;
      await scanning;
      await Promise.allSettled([...running.values()].map((r) => r.done));
      await Promise.allSettled([...watching.values()].map((w) => w.done));
    },
    active() {
      return [...running.keys()];
    },
  };
}

function quietLogger(log: Log) {
  return {
    debug: () => {},
    info: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    warn: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    error: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
  };
}
