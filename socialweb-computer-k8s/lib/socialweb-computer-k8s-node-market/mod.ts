import { createSshSessionProvider } from "@publicdomainrelay/requester-xrpc";
import type { RequesterPDS, SshSessionProvider } from "@publicdomainrelay/requester-abc";
import { type ComputeRequestHandle, releaseCompute, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import {
  type ComputeRequestHandlers,
  CONTRACT_STATE_VERSION,
  type ContractState,
  type FlowDecision,
  NO_CONTRACT_OUTCOMES,
  parseContractState,
  releaseTargetOf,
} from "@publicdomainrelay/compute-request-abc";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { type NodeClaim, nodeClaimKey, type NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { createK3sJoinModule } from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import { encodeNodeJoinDocument, K3S_JOIN_PATH, pinnedJoinToken } from "@publicdomainrelay/socialweb-computer-k8s-common";
import {
  FINALIZER_NODE_CLAIM,
  type NodeClaimContract,
  type NodeClaimPhase,
  type NodeClaimStatus,
} from "@publicdomainrelay/socialweb-computer-k8s-api";
import {
  dumbpipeConnector,
  type GuestConnector,
  type Log,
  type NodeSessionOutcome,
  type NodeSessionRunner,
  pollGuestReady,
  runNodeSession,
} from "./guest.ts";

export { BoundedTail, redactTransportLine, runNodeSession } from "./guest.ts";
export type { GuestConnector, NodeSessionOutcome, NodeSessionPlan, NodeSessionRunner } from "./guest.ts";

type ComputeRequester = typeof requestCompute;
type ComputeReleaser = typeof releaseCompute;

export interface MarketNodeFulfillerOptions {
  store: NodeClaimStore;
  requesterFor(did: string): Promise<RequesterPDS>;
  apiserverHost?: string;
  plcUrl?: string;
  ingressProxyHost?: string;
  relayUrls?: string[];
  vmReadyTimeoutSec?: number;
  policy?: { name: string; args: Record<string, unknown> };
  extraBidderDids?: string[];
  pollIntervalMs?: number;
  requestCompute?: ComputeRequester;
  releaseCompute?: ComputeReleaser;
  nodeSession?: NodeSessionRunner;
  keypairs?: Pick<SshSessionProvider, "generateKeypair">;
  pollReady?: (privateKeyPath: string, ticket: string, timeoutMs: number, signal: AbortSignal) => Promise<boolean>;
  dumbpipePath?: string;
  connect?: GuestConnector;
  eventStreams?: () => { close(): void };
  readText?: (path: string) => Promise<string>;
  log?: Log;
}

export interface MarketNodeFulfiller {
  pass(): Promise<void>;
  start(): void;
  stop(): Promise<void>;
  active(): string[];
}

const LIVE_PHASES: ReadonlySet<NodeClaimPhase> = new Set(["Bidding", "Provisioning", "Joining", "Running"]);

const RELEASE_REASON_SESSION_ENDED = "session_ended";
const RELEASE_REASON_DOOR_RESTARTED = "requester_restarted";

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
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

export function explainEnd(
  state: ContractState | null,
  session: NodeSessionOutcome | null,
  sshReady?: boolean,
): string {
  if (session) return `${session.reason}${session.tail ? `: ${session.tail.trim().split("\n").slice(-5).join(" | ")}` : ""}`;
  if (!state) return "the contract did not run";
  if (state.outcome === "no_bids") return "no bids arrived for this node";
  if (state.outcome === "policy_rejected") return `the winning bid was rejected by policy${state.error ? `: ${state.error}` : ""}`;
  if (sshReady === false || (state.receiptOk && !state.vmAddress)) return "a VM was provisioned but its SSH never came up";
  return `provisioning did not complete (${state.outcome ?? state.phase})${state.error ? `: ${state.error}` : ""}`;
}

interface ReleaseOutcome {
  state: ContractState | null;
  attempted: boolean;
}

export function createMarketNodeFulfiller(opts: MarketNodeFulfillerOptions): MarketNodeFulfiller {
  const log = opts.log ?? (() => {});
  const request = opts.requestCompute ?? requestCompute;
  const release = opts.releaseCompute ?? releaseCompute;
  const nodeSession = opts.nodeSession ?? runNodeSession;
  const keypairs = opts.keypairs ?? createSshSessionProvider(undefined);
  const readText = opts.readText ?? ((p: string) => Deno.readTextFile(p));
  const apiserverHost = opts.apiserverHost ?? "127.0.0.1";
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 600;
  const eventStreams = opts.eventStreams ?? (() =>
    createDefaultATProtoEventStreamsClient({ additionalRelays: opts.relayUrls ?? [], log: quietLogger(log) }) as unknown as {
      close(): void;
    });
  const connect: GuestConnector = opts.connect ?? dumbpipeConnector(opts.dumbpipePath ?? "dumbpipe");
  const ready = opts.pollReady ??
    ((key: string, ticket: string, timeoutMs: number, signal: AbortSignal) =>
      pollGuestReady(key, ticket, timeoutMs, signal, log, connect));

  const running = new Map<string, { abort: AbortController; done: Promise<void> }>();
  let timer: ReturnType<typeof setInterval> | undefined;
  let passing: Promise<void> | null = null;

  function claimLogger(key: string) {
    return {
      debug: () => {},
      info: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
      warn: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
      error: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
    };
  }

  async function releaseFromClaim(claim: NodeClaim, reason: string): Promise<ReleaseOutcome> {
    const state = contractStateOfClaim(claim);
    if (!needsRelease(state)) return { state, attempted: false };
    const pds = await opts.requesterFor(claim.spec.ownerDid);
    const next = await release(pds, state, { logger: claimLogger(nodeClaimKey(claim)), reason });
    log("node_claim_contract_released", {
      claim: nodeClaimKey(claim),
      reason,
      receiptUri: state.receipt?.uri,
      ok: next.release?.ok ?? false,
      error: next.error ?? null,
    });
    return { state: next, attempted: true };
  }

  function fulfil(claim: NodeClaim): void {
    const key = nodeClaimKey(claim);
    const abort = new AbortController();
    let status: NodeClaimStatus = { phase: "Bidding", contract: {} };
    let writes = Promise.resolve();
    const write = (patch: Partial<NodeClaimStatus>) => {
      status = { ...status, ...patch, contract: { ...status.contract, ...patch.contract } };
      const snapshot = structuredClone(status);
      writes = writes.then(() => opts.store.replaceStatus(claim, snapshot)).catch((err) =>
        log("node_claim_status_write_failed", { claim: key, error: String(err) })
      );
      return writes;
    };
    const record = (state: ContractState, patch: Partial<NodeClaimStatus> = {}) =>
      write({ ...patch, contract: claimContractOf(state) });
    const stopWhenReleased = (): FlowDecision => abort.signal.aborted ? "return" : "continue";

    const done = (async () => {
      let streams: { close(): void } | null = null;
      let session: NodeSessionOutcome | null = null;
      let state: ContractState | null = null;
      let handle: ComputeRequestHandle | null = null;
      let sshReady: boolean | undefined;
      try {
        await write({ phase: "Bidding", message: "requesting a node from the compute market" });
        const spec = claim.spec;
        const [token, nodePassword, serverCA] = await Promise.all([
          readText(spec.tokenFile),
          readText(spec.nodePasswordFile),
          readText(spec.serverCAFile),
        ]);
        const joinDocument = encodeNodeJoinDocument({
          nodeName: spec.nodeName,
          serverUrl: `https://${spec.advertiseAddress}:${spec.apiserverPort}`,
          advertiseAddress: spec.advertiseAddress,
          token: pinnedJoinToken(await sha256Hex(serverCA), token.trim()),
          nodePassword: nodePassword.trim(),
        });
        const pds = await opts.requesterFor(spec.ownerDid);
        const keypair = await keypairs.generateKeypair(spec.nodeName);
        streams = eventStreams();

        const handlers: ComputeRequestHandlers = {
          onRfpSubmitted: async (_event, s) => {
            await record(s);
            return stopWhenReleased();
          },
          onAccepted: async (_event, s): Promise<FlowDecision> => {
            await record(s, { phase: "Provisioning", message: "a bid was accepted; the VM is booting" });
            return "continue";
          },
          onReceipt: async (_event, s) => {
            await record(s);
            return stopWhenReleased();
          },
          onNetwork: async (_event, s) => {
            await record(s);
            return stopWhenReleased();
          },
        };

        handle = await request(pds, {
          vmName: spec.nodeName,
          sshPublicKey: keypair.publicKey,
          userData: { modules: ["k3s", createK3sJoinModule()] },
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
        const settled = handle.state;
        state = settled;
        await record(settled);

        const ticket = settled.vmAddress;
        if (settled.receiptOk && ticket && !abort.signal.aborted) {
          sshReady = await ready(keypair.privateKeyPath, ticket, vmReadyTimeoutSec * 1000, abort.signal);
          if (sshReady && !abort.signal.aborted) {
            session = await nodeSession({
              privateKeyPath: keypair.privateKeyPath,
              ticket,
              connect,
              program: K3S_JOIN_PATH,
              joinDocument,
              forwardAddress: spec.advertiseAddress,
              apiserverHost,
              apiserverPort: spec.apiserverPort,
              signal: abort.signal,
              onJoining: () => void write({ phase: "Joining", fqdn: ticket, message: "the node is joining its cluster" }),
              onForwarded: () => void write({ phase: "Running", message: "the node's agent reached its apiserver" }),
              log,
            });
          }
        }
      } catch (err) {
        log("node_claim_contract_failed", { claim: key, error: String(err) });
        session ??= { code: 1, reason: `the contract failed: ${String(err)}`, tail: "" };
      } finally {
        streams?.close();
      }

      const released = abort.signal.aborted;
      await writes;
      let recorded: NodeClaim | null = null;
      let outcome: ReleaseOutcome | null = null;
      try {
        recorded = await opts.store.get(claim);
        if (recorded) outcome = await releaseFromClaim(recorded, RELEASE_REASON_SESSION_ENDED);
      } catch (err) {
        log("node_claim_release_deferred", { claim: key, error: String(err) });
      }
      await handle?.dispose().catch(() => undefined);
      running.delete(key);
      if (!outcome) {
        log("node_claim_ended", { claim: key, released, outcome: state?.outcome ?? null, deferred: true });
        return;
      }

      const finalState = outcome.state ?? state;
      const contract = finalState ? claimContractOf(finalState) : {};
      const gone = !outcome.attempted || confirmedReleased(outcome.state);
      await write(
        released
          ? {
            phase: gone ? "Released" : "Failed",
            message: gone
              ? "the node was released and its VM deleted"
              : "the node was released but its VM could not be confirmed deleted",
            contract,
          }
          : { phase: "Failed", message: explainEnd(state, session, sshReady), contract },
      );
      if (released) {
        await opts.store.removeFinalizer(claim).catch((err) =>
          log("node_claim_finalizer_release_failed", { claim: key, error: String(err) })
        );
      }
      log("node_claim_ended", { claim: key, released, outcome: state?.outcome ?? null, vmReleased: outcome.attempted });
    })();

    running.set(key, { abort, done });
  }

  async function settleDeleted(claim: NodeClaim): Promise<void> {
    const key = nodeClaimKey(claim);
    const live = running.get(key);
    if (live) {
      live.abort.abort();
      return;
    }
    if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) return;
    const outcome = await releaseFromClaim(claim, RELEASE_REASON_DOOR_RESTARTED);
    if (outcome.attempted) log("node_claim_reaped", { claim: key, ok: confirmedReleased(outcome.state) });
    await opts.store.removeFinalizer(claim);
  }

  async function adoptNever(claim: NodeClaim): Promise<void> {
    const key = nodeClaimKey(claim);
    const outcome = await releaseFromClaim(claim, RELEASE_REASON_DOOR_RESTARTED);
    const reaped = outcome.attempted && confirmedReleased(outcome.state);
    await opts.store.replaceStatus(claim, {
      ...claim.status,
      phase: reaped ? "Released" : "Failed",
      contract: outcome.state ? { ...claim.status.contract, ...claimContractOf(outcome.state) } : claim.status.contract,
      message: reaped
        ? "the door restarted and released this node's VM; a new node will be requested"
        : "the door restarted while this node was live; its VM could not be confirmed released",
    });
    log("node_claim_orphaned", { claim: key, reaped });
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
        if (running.has(key)) continue;
        const phase = claim.status.phase;
        if (!phase || phase === "Pending") {
          if (!claim.finalizers.includes(FINALIZER_NODE_CLAIM)) await opts.store.addFinalizer(claim);
          fulfil(claim);
        } else if (LIVE_PHASES.has(phase)) {
          await adoptNever(claim);
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
      await passing;
      for (const { abort } of running.values()) abort.abort();
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
