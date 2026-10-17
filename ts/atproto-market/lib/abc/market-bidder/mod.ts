import type { IdResolver } from "@atproto/identity";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import type {
  AttestationKeypair,
  EventCallbacks,
  RecordResolver,
  RfpCallbacks,
  SubmitAcceptCallback,
} from "@publicdomainrelay/market-abc";
import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import type { PolicyEvalCtx, PolicyResult } from "@publicdomainrelay/policy-engine-abc";

export interface GuestContractEntry {
  receiptKey: string;
  receiptUri: string;
  receiptCid: string;
  submitEventUrl?: string;
}

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
  receiptUri?: string;
  receiptCid?: string;
  acceptUri?: string;
  acceptCid?: string;
  acceptedAt?: string;
  submitEventUrl?: string;
  provider?: string;
  restored?: boolean;
}

export interface TerminatedReceipt {
  acceptAuthor: string;
  reason: string;
  at: string;
}

export const DEFAULT_TERMINATED_RECEIPTS_CAPACITY = 1024;

export class TerminatedReceipts {
  readonly #entries = new Map<string, TerminatedReceipt>();

  constructor(readonly capacity: number = DEFAULT_TERMINATED_RECEIPTS_CAPACITY) {}

  get size(): number {
    return this.#entries.size;
  }

  has(receiptUri: string): boolean {
    return this.#entries.has(receiptUri);
  }

  get(receiptUri: string): TerminatedReceipt | undefined {
    return this.#entries.get(receiptUri);
  }

  add(receiptUri: string, entry: TerminatedReceipt): void {
    this.#entries.delete(receiptUri);
    this.#entries.set(receiptUri, entry);
    while (this.#entries.size > this.capacity) {
      const oldest = this.#entries.keys().next().value;
      if (oldest === undefined) break;
      this.#entries.delete(oldest);
    }
  }
}

export interface ContractEvent {
  type: "accepted" | "provisioned" | "provisioning-failed" | "terminated" | "termination-failed";
  key: string;
  receiptUri: string;
  receiptCid: string;
  acceptUri?: string;
  acceptCid?: string;
  acceptAuthor: string;
  acceptedAt: string;
  terminatedAt?: string;
  providerId?: string | number;
  provider?: string;
  submitEventUrl?: string;
}

export interface StoredContract {
  key: string;
  receiptUri: string;
  receiptCid: string;
  acceptUri?: string;
  acceptCid?: string;
  acceptAuthor: string;
  acceptedAt: string;
  providerId?: string | number;
  provider?: string;
  submitEventUrl?: string;
}

export interface Tombstone extends TerminatedReceipt {
  receiptUri: string;
  receiptCid?: string;
  acceptUri?: string;
  acceptCid?: string;
}

export interface StoredContracts {
  contracts: StoredContract[];
  tombstones: Tombstone[];
}

export interface ContractStore {
  load(): Promise<StoredContracts>;
  save(event: ContractEvent): Promise<void>;
  saveTombstone(tombstone: Tombstone): Promise<void>;
}

export function storedContractOf(event: ContractEvent): StoredContract {
  const stored: StoredContract = {
    key: event.key,
    receiptUri: event.receiptUri,
    receiptCid: event.receiptCid,
    acceptAuthor: event.acceptAuthor,
    acceptedAt: event.acceptedAt,
  };
  if (event.acceptUri !== undefined) stored.acceptUri = event.acceptUri;
  if (event.acceptCid !== undefined) stored.acceptCid = event.acceptCid;
  if (event.providerId !== undefined) stored.providerId = event.providerId;
  if (event.provider !== undefined) stored.provider = event.provider;
  if (event.submitEventUrl !== undefined) stored.submitEventUrl = event.submitEventUrl;
  return stored;
}

export function activeContractOf(stored: StoredContract): ActiveContract {
  return {
    providerIdPromise: Promise.resolve(stored.providerId),
    acceptAuthor: stored.acceptAuthor,
    receiptUri: stored.receiptUri,
    receiptCid: stored.receiptCid,
    acceptUri: stored.acceptUri,
    acceptCid: stored.acceptCid,
    acceptedAt: stored.acceptedAt,
    submitEventUrl: stored.submitEventUrl,
    provider: stored.provider,
    restored: true,
  };
}

export interface BidRef {
  uri: string;
  cid?: string;
}

export type BidPhaseName = "Deciding" | "Published" | "Won" | "Lost" | "Expired" | "Failed";

export interface BidDecision {
  rfp: BidRef;
  requester: string;
  payloadNsid?: string;
  offer: { kind: string; cost?: number };
  provider: string;
  policy: { scope?: string; attached?: BidRef[]; verdict: string };
  rfpSeenAt: string;
  decidedAt: string;
}

export interface BidEntry {
  name: string;
  createdAt?: string;
  heartbeatAt?: string;
  rfp: BidRef;
  requester: string;
  expiresAt: string;
  phase: BidPhaseName;
  bid?: BidRef;
  accept?: BidRef;
  claimedBy?: string;
  acceptedAt?: string;
  computeVM?: string;
  publishedAt?: string;
  terminated?: Tombstone;
  settledAt?: string;
}

export interface BidAcceptClaim {
  accept: BidRef;
  claimant: string;
  at: string;
  staleBefore: string;
}

export type BidClaim = { claimed: true; entry: BidEntry } | { claimed: false; entry: BidEntry };

export interface BidLedger {
  find(rfpUri: string): Promise<BidEntry | null>;
  claim(decision: BidDecision): Promise<BidClaim>;
  published(rfpUri: string, bid: BidRef, at: string): Promise<void>;
  failed(rfpUri: string, reason: string, at: string): Promise<void>;
  won(rfpUri: string, accept: BidRef, at: string): Promise<void>;
  accepting(rfpUri: string, claim: BidAcceptClaim): Promise<BidClaim>;
  boundTo(rfpUri: string, computeVM: string): Promise<void>;
  terminated(rfpUri: string, tombstone: Tombstone): Promise<void>;
  lost(rfpUri: string, reason: string, at: string): Promise<void>;
  expired(rfpUri: string, at: string): Promise<void>;
  release(rfpUri: string, liveBefore?: string): Promise<boolean>;
  touch(rfpUri: string, at: string): Promise<void>;
  list(): Promise<BidEntry[]>;
  capacity?(): Promise<string | undefined>;
}

export const DEFAULT_BID_WINDOW_MS = 300_000;

export const DEFAULT_BID_CLAIM_STALE_MS = 10_000;

export const DECLINE_CAPACITY = "capacity";

export const DECLINE_WRITE_BUDGET = "writeBudget";

export class BidCapacityError extends Error {
  constructor(readonly resource: string, cause?: unknown) {
    super(`no capacity: the quota of ${resource} is used up`, { cause });
    this.name = "BidCapacityError";
  }
}

export function bidExpiresAt(decidedAt: string, windowMs: number = DEFAULT_BID_WINDOW_MS): string {
  return new Date(Date.parse(decidedAt) + windowMs).toISOString();
}

export function bidOpen(entry: BidEntry, now: Date): boolean {
  return (entry.phase === "Published" || entry.phase === "Deciding") && Date.parse(entry.expiresAt) > now.getTime();
}

export function acceptClaimable(entry: BidEntry, claim: BidAcceptClaim): boolean {
  if (entry.phase === "Deciding" || entry.phase === "Published") return Date.parse(entry.expiresAt) > Date.parse(claim.at);
  if (entry.phase !== "Won" || entry.accept?.uri !== claim.accept.uri) return false;
  if (!entry.claimedBy || entry.claimedBy === claim.claimant) return true;
  return (entry.acceptedAt ?? "") < claim.staleBefore;
}

export interface CallbackSet {
  rfpCallbacks?: RfpCallbacks;
  onAccept?: SubmitAcceptCallback;
  eventCallbacks?: EventCallbacks;
  eventBackground?: boolean;
  restore?(): Promise<void>;
}

export interface CallbackFactoryDeps {
  did: string;
  repoApi: RepoApi;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  attestationKp: AttestationKeypair;
  idResolver: IdResolver;
  relay: { ingressRef: string; ingressUrl: string; ingressHost: string };
  ingressProxyHost: string;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  onContractChange?: (event: ContractEvent) => void | Promise<void>;
  terminatedReceipts?: TerminatedReceipts;
  saveTombstone?: (tombstone: Tombstone) => Promise<void>;
  createRecord: (collection: string, record: Record<string, unknown>) => Promise<StrongRef>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSharedRecord?: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  deleteRecord: (collection: string, rkey: string) => Promise<void>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: RecordResolver;
  /** Maps acceptUri#acceptCid -> receipt info. Populated at receipt creation. */
  acceptToContract?: Map<string, GuestContractEntry>;
  /** How this bidder is willing to execute an RFP's attached policy. */
  policyExec?: PolicyExecOptions;
  /** Vouch/follow set lookup handed to locally executed policies. */
  getVouchedDids?: (did: string) => Promise<Set<string>>;
  /** Bidder DID -> operator DID, via bidder_associate records. */
  resolveOperatorDid?: (bidderDid: string) => Promise<string | null>;
  bids?: BidLedger;
  scopePolicy?: string;
  /** Policy engine evaluator used to evaluate RFP.policies[] refs in onRfp. */
  evaluator: {
    evaluatePolicies(input: {
      refs: Array<{ uri: string; cid: string }>;
      ctx: PolicyEvalCtx;
    }): Promise<PolicyResult>;
  };
}

export interface PolicyExecOptions {
  /** Refuse to evaluate any non-service policy record locally. */
  onlyRemote?: boolean;
  /** Permit policies.denoWorker records to run caller-supplied bundles. */
  allowUntrusted?: boolean;
}

export interface MarketBidderProviderRef {
  serviceId: string;
  appliesTo: string[];
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  buildCallbacks(deps: CallbackFactoryDeps): CallbackSet;
}

