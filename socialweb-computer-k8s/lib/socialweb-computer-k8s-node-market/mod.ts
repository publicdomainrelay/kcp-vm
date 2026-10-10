import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import { type ComputeRequestHandle, releaseCompute, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import {
  type ComputeRequestHandlers,
  CONTRACT_STATE_VERSION,
  type ContractState,
  type FlowDecision,
  parseContractState,
  releaseTargetOf,
} from "@publicdomainrelay/compute-request-abc";
import type { GrantVars, GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import { createSecretsCapability } from "@publicdomainrelay/guest-capability-secrets";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import {
  type ClusterLinkReader,
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

export type SecretsCapabilityFactory = (secrets: SecretEntry[], logger: StructuredLoggerInterface) => GuestCapability;

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
  pollIntervalMs?: number;
  requestCompute?: ComputeRequester;
  releaseCompute?: ComputeReleaser;
  secretsCapability?: SecretsCapabilityFactory;
  openBaoCAFileOf?: (spec: NodeClaimSpec) => string;
  eventStreams?: () => { close(): void };
  readText?: (path: string) => Promise<string>;
  meshd?: MeshdPin;
  debugSsh?: DebugNodeSsh;
  scopeCache?: ComputeRequestOptions["scopeCache"];
  log?: Log;
}

export interface DebugNodeSsh {
  authorizedKey: string;
  onNetwork(claim: string, address: string): void;
}

export interface MarketNodeFulfiller {
  pass(): Promise<void>;
  start(): void;
  stop(): Promise<void>;
  active(): string[];
}

const ORPHANED_PHASES: ReadonlySet<NodeClaimPhase> = new Set(["Bidding", "Provisioning"]);

const RELEASE_REASON_CLAIM_DELETED = "claim_deleted";

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
  return state !== null && state.phase !== "released" && releaseTargetOf(state) !== null;
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
): GuestCapability {
  return {
    id: inner.id,
    userDataModule: inner.userDataModule,
    prepare: inner.prepare ? (ctx) => inner.prepare!(ctx) : undefined,
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
  const secretsCapability = opts.secretsCapability ??
    ((secrets: SecretEntry[], logger: StructuredLoggerInterface) => createSecretsCapability({ secrets, logger }));
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 600;
  const joinModule = createK3sJoinModule(opts.meshd ? { meshd: opts.meshd } : {});
  const eventStreams = opts.eventStreams ?? (() =>
    createDefaultATProtoEventStreamsClient({ additionalRelays: opts.relayUrls ?? [], log: quietLogger(log) }) as unknown as {
      close(): void;
    });

  const running = new Map<string, { deleted: AbortController; done: Promise<void> }>();
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
    await opts.store.removeFinalizer(recorded).catch((err) =>
      log("node_claim_finalizer_release_failed", { claim: key, error: String(err) })
    );
    log("node_claim_released", { claim: key, vmReleased: outcome.attempted, confirmed: gone });
  }

  function fulfil(claim: NodeClaim): void {
    const key = nodeClaimKey(claim);
    const deleted = new AbortController();
    const ended = () => deleted.signal.aborted || stopping.signal.aborted;
    let status: NodeClaimStatus = { phase: "Bidding", contract: {} };
    let writes: Promise<unknown> = Promise.resolve();
    const write = (patch: Partial<NodeClaimStatus>): Promise<boolean> => {
      status = { ...status, ...patch, contract: { ...status.contract, ...patch.contract } };
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
        await write({ phase: "Bidding", message: "requesting a node from the compute market" });
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
        const apiserverLink = await opts.links.get(claim.workspace, apiserverName);
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
            next: "the guest reads its apiserver ticket out of the cluster OpenBao instead, once its own link " +
              "to that OpenBao is up, so the join is serial rather than parallel",
          });
        }
        const capability = grantingCapability(secretsCapability(bundle, claimLogger(key)), async (vars) => {
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
        });
        const pds = await opts.requesterFor(spec.ownerDid);
        streams = eventStreams();

        const handlers: ComputeRequestHandlers = {
          onRfpSubmitted: async (_event, s) => {
            await record(s);
            return stopWhenEnded();
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
            opts.debugSsh?.onNetwork(spec.nodeName, event.address);
            await record(s, { fqdn: event.address });
            return stopWhenEnded();
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
          userData: { modules: ["k3s", joinModule] },
          capabilities: [capability],
          ...(opts.debugSsh ? { sshAuthorizedKeys: [opts.debugSsh.authorizedKey] } : {}),
          ...(opts.scopeCache ? { scopeCache: opts.scopeCache } : {}),
          denyBidderDids: spec.denyBidderDids ?? [],
          extraBidderDids: opts.extraBidderDids ?? [],
          vmReadyTimeoutSec,
          plcUrl: opts.plcUrl,
          ingressProxyHost: opts.ingressProxyHost,
          relayUrls: opts.relayUrls,
          eventStreams: streams as never,
          ...(opts.policy ? { policy: opts.policy } : {}),
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
      } else if (fetched) {
        log("node_claim_joining", { claim: key, outcome: state?.outcome ?? null });
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
        if (!phase || phase === "Pending") {
          if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) await opts.store.addFinalizer(claim);
          fulfil(claim);
        } else if (ORPHANED_PHASES.has(phase)) {
          await markOrphaned(claim);
        }
      } catch (err) {
        log("node_claim_pass_failed", { claim: key, error: String(err) });
      }
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
    },
    async stop() {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
      stopping.abort();
      await passing;
      await Promise.allSettled([...running.values()].map((r) => r.done));
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
