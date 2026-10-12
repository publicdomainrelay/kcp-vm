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

