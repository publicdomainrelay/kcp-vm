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

export interface ActiveContract {
  providerIdPromise?: Promise<string | number | undefined>;
  acceptAuthor: string;
  receiptUri: string;
  receiptCid: string;
  acceptedAt: string;
}

export interface ContractEvent {
  type: "accepted" | "provisioned" | "provisioning-failed" | "terminated";
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
  relay: { proxyRef: string };
  dispatcherHost: string;
  log: Logger;
  activeContracts: Map<string, ActiveContract>;
  onContractChange?: (event: ContractEvent) => void;
  createRecord: (collection: string, record: Record<string, unknown>) => Promise<StrongRef>;
  createRepoRecord: (collection: string, record: Record<string, unknown>) => Promise<{ uri: string; cid: string }>;
  createSignedRepoRecord: (collection: string, record: Record<string, unknown>, issuer?: string) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  deleteRecord: (collection: string, rkey: string) => Promise<void>;
  callService: (endpointUrl: string, nsid: string, lxm: string, body: Record<string, unknown>) => Promise<{ status: number; ok: boolean; body: unknown }>;
  resolve: RecordResolver;
}

export interface MarketBidderProviderRef {
  serviceId: string;
  appliesTo: string[];
  setup?(): Promise<void>;
  teardown?(): Promise<void>;
  buildCallbacks(deps: CallbackFactoryDeps): CallbackSet;
}
