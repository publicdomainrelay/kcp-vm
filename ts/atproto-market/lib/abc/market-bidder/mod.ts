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
  acceptedAt?: string;
}

export interface ContractEvent {
  type: "accepted" | "provisioned" | "provisioning-failed" | "terminated" | "termination-failed";
  key: string;
  receiptUri: string;
  receiptCid: string;
  acceptAuthor: string;
  acceptedAt: string;
  terminatedAt?: string;
  providerId?: string | number;
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
  relay: { ingressRef: string; ingressUrl: string; ingressHost: string };
  ingressProxyHost: string;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  onContractChange?: (event: ContractEvent) => void;
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

