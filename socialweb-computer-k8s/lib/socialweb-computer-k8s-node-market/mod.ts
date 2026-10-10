import { createSshSessionProvider, runComputeContract } from "@publicdomainrelay/requester-xrpc";
import type { RequesterPDS, SshSessionProvider } from "@publicdomainrelay/requester-abc";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { COMPUTE_EVENTS_VM_DELETE_NSID, EVENT_NSID, SUBMIT_EVENT_LXM, SUBMIT_EVENT_NSID } from "@publicdomainrelay/market-lexicons";
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

type ContractRunner = typeof runComputeContract;
type ContractResult = Awaited<ReturnType<ContractRunner>>;

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
  runContract?: ContractRunner;
  nodeSession?: NodeSessionRunner;
  keypairs?: Pick<SshSessionProvider, "generateKeypair">;
  pollReady?: (privateKeyPath: string, ticket: string, timeoutMs: number, signal: AbortSignal) => Promise<boolean>;
  /** The dumbpipe binary the iroh transport runs, from an operator's PATH or the one ensureDumbpipe fetched. */
  dumbpipePath?: string;
  /** How a guest's transport address is opened; defaults to one dumbpipe per session. */
  connect?: GuestConnector;
  eventStreams?: () => { close(): void };
  readText?: (path: string) => Promise<string>;
  reap?: (pds: RequesterPDS, contract: NodeClaimContract) => Promise<boolean>;
  log?: Log;
}

export interface MarketNodeFulfiller {
  pass(): Promise<void>;
  start(): void;
  stop(): Promise<void>;
  active(): string[];
}

const LIVE_PHASES: ReadonlySet<NodeClaimPhase> = new Set(["Bidding", "Provisioning", "Joining", "Running"]);

async function sha256Hex(text: string): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function hasReceipt(contract: NodeClaimContract | undefined): contract is Required<Pick<NodeClaimContract, "receiptUri" | "receiptCid" | "submitEventRef">> & NodeClaimContract {
  return !!contract?.receiptUri && !!contract.receiptCid && !!contract.submitEventRef;
}

export async function reapContract(pds: RequesterPDS, contract: NodeClaimContract): Promise<boolean> {
  if (!hasReceipt(contract)) return false;
  const nowIso = new Date().toISOString();
  const del = await pds.createSignedRepoRecord(
    COMPUTE_EVENTS_VM_DELETE_NSID,
    { $type: COMPUTE_EVENTS_VM_DELETE_NSID, reason: "requester_restarted", createdAt: nowIso },
    pds.attestationKp,
    pds.did,
  );
  const eventRecord = {
    $type: EVENT_NSID,
    receipt: { $type: "com.atproto.repo.strongRef", uri: contract.receiptUri, cid: contract.receiptCid },
    payload: { $type: "com.atproto.repo.strongRef", uri: del.uri, cid: del.cid },
    createdAt: nowIso,
  };
  const event = await pds.createSignedRepoRecord(EVENT_NSID, eventRecord, pds.attestationKp, pds.did);
  const target = await pds.resolveBidderEndpoint(contract.submitEventRef);
  if (!target) return false;
  const res = await pds.callBidder(target.targetUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_LXM, target.audDid, {
    uri: event.uri,
    cid: event.cid,
    record: eventRecord,
  });
  return res.ok;
}

const CONTRACT_EVENTS: Record<string, "vmUri" | "rfpUri" | "acceptUri"> = {
  vm_record_created: "vmUri",
  rfp_created: "rfpUri",
  accept_created: "acceptUri",
};

export function contractFromEvent(event: string, meta: Record<string, unknown>): Partial<NodeClaimContract> {
  const out: Partial<NodeClaimContract> = {};
  const field = CONTRACT_EVENTS[event];
  if (field && typeof meta.uri === "string") out[field] = meta.uri;
  for (const key of ["receiptUri", "receiptCid", "submitEventRef"] as const) {
    if (typeof meta[key] === "string" && meta[key]) out[key] = meta[key] as string;
  }
  return out;
}

export function explainEnd(result: Pick<ContractResult, "event" | "error" | "bids" | "sshReady"> | null, session: NodeSessionOutcome | null): string {
  if (session) return `${session.reason}${session.tail ? `: ${session.tail.trim().split("\n").slice(-5).join(" | ")}` : ""}`;
  if (!result) return "the contract did not run";
  if (result.event === "no_bids") return "no bids arrived for this node";
  if (result.event === "policy_rejected") return `the winning bid was rejected by policy${result.error ? `: ${result.error}` : ""}`;
  if (result.sshReady === false) return "a VM was provisioned but its SSH never came up";
  return `provisioning did not complete (${result.event ?? "unknown"})${result.error ? `: ${result.error}` : ""}`;
}

export function createMarketNodeFulfiller(opts: MarketNodeFulfillerOptions): MarketNodeFulfiller {
  const log = opts.log ?? (() => {});
  const runContract = opts.runContract ?? runComputeContract;
  const nodeSession = opts.nodeSession ?? runNodeSession;
  const keypairs = opts.keypairs ?? createSshSessionProvider(undefined);
  const readText = opts.readText ?? ((p: string) => Deno.readTextFile(p));
  const reap = opts.reap ?? reapContract;
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

    const done = (async () => {
      let streams: { close(): void } | null = null;
      let session: NodeSessionOutcome | null = null;
      let result: ContractResult | null = null;
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
        streams = eventStreams();

        const logger = {
          debug: () => {},
          info: (event: string, meta?: Record<string, unknown>) => {
            const contract = contractFromEvent(event, meta ?? {});
            if (Object.keys(contract).length > 0) {
              void write({ contract, ...(event === "accept_created" ? { phase: "Provisioning" as const, message: "a bid was accepted; the VM is booting" } : {}) });
            }
            log(event, { claim: key, ...meta });
          },
          warn: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
          error: (event: string, meta?: Record<string, unknown>) => log(event, { claim: key, ...meta }),
        };

        const provider: SshSessionProvider = {
          generateKeypair: (vmName) => keypairs.generateKeypair(vmName),
          pollReady: (privateKeyPath, ticket, timeoutMs) => ready(privateKeyPath, ticket, timeoutMs, abort.signal),
          runSession: async (privateKeyPath, ticket, program) => {
            session = await nodeSession({
              privateKeyPath,
              ticket,
              connect,
              program,
              joinDocument,
              forwardAddress: spec.advertiseAddress,
              apiserverHost,
              apiserverPort: spec.apiserverPort,
              signal: abort.signal,
              onJoining: () => void write({ phase: "Joining", fqdn: ticket, message: "the node is joining its cluster" }),
              onForwarded: () => void write({ phase: "Running", message: "the node's agent reached its apiserver" }),
              log,
            });
            return session.code;
          },
        };

        result = await runContract(pds, {
          vmName: spec.nodeName,
          execProgram: K3S_JOIN_PATH,
          sshProvider: provider,
          userData: { modules: ["k3s", createK3sJoinModule()] },
          denyBidderDids: spec.denyBidderDids ?? [],
          extraBidderDids: opts.extraBidderDids ?? [],
          vmReadyTimeoutSec,
          plcUrl: opts.plcUrl,
          ingressProxyHost: opts.ingressProxyHost,
          relayUrls: opts.relayUrls,
          eventStreams: streams as never,
          ...(opts.policy ? { policy: opts.policy } : {}),
          logger,
        } as Parameters<ContractRunner>[1]);
      } catch (err) {
        log("node_claim_contract_failed", { claim: key, error: String(err) });
        session ??= { code: 1, reason: `the contract failed: ${String(err)}`, tail: "" };
      } finally {
        streams?.close();
      }

      const released = abort.signal.aborted;
      const cleared: NodeClaimContract = { receiptUri: undefined, receiptCid: undefined, submitEventRef: undefined };
      await write(
        released
          ? { phase: "Released", message: "the node was released and its VM deleted", contract: result ? cleared : {} }
          : { phase: "Failed", message: explainEnd(result, session), contract: result ? cleared : {} },
      );
      running.delete(key);
      if (released) {
        await opts.store.removeFinalizer(claim).catch((err) =>
          log("node_claim_finalizer_release_failed", { claim: key, error: String(err) })
        );
      }
      log("node_claim_ended", { claim: key, released, event: result?.event ?? null });
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
    if (hasReceipt(claim.status.contract)) {
      const pds = await opts.requesterFor(claim.spec.ownerDid);
      const ok = await reap(pds, claim.status.contract!);
      log("node_claim_reaped", { claim: key, ok });
    }
    await opts.store.removeFinalizer(claim);
  }

  async function adoptNever(claim: NodeClaim): Promise<void> {
    const key = nodeClaimKey(claim);
    let reaped = false;
    if (hasReceipt(claim.status.contract)) {
      const pds = await opts.requesterFor(claim.spec.ownerDid);
      reaped = await reap(pds, claim.status.contract!);
    }
    await opts.store.replaceStatus(claim, {
      ...claim.status,
      phase: reaped ? "Released" : "Failed",
      contract: { ...claim.status.contract, receiptUri: undefined, receiptCid: undefined, submitEventRef: undefined },
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
