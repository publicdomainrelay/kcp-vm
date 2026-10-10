export const CONTRACT_STATE_VERSION = 1;

export const IROH_SCHEME = "iroh://";

export interface RecordRef {
  uri: string;
  cid: string;
}

export type ContractPhase =
  | "requested"
  | "rfp_submitted"
  | "winner_selected"
  | "accepted"
  | "receipt"
  | "network"
  | "released";

export const CONTRACT_PHASES: readonly ContractPhase[] = [
  "requested",
  "rfp_submitted",
  "winner_selected",
  "accepted",
  "receipt",
  "network",
  "released",
];

export type ContractOutcome =
  | "no_bids"
  | "bids_denied"
  | "policy_rejected"
  | "returned"
  | "receipt_invalid"
  | "network_timeout"
  | "complete";

export const NO_CONTRACT_OUTCOMES: readonly ContractOutcome[] = [
  "no_bids",
  "bids_denied",
  "policy_rejected",
];

export interface ContractWinner {
  did: string;
  submitAccept?: string;
}

export interface ContractRelease {
  event: RecordRef;
  payload: RecordRef;
  ok: boolean;
  status?: number;
}

export interface ContractState {
  version: typeof CONTRACT_STATE_VERSION;
  phase: ContractPhase;
  vmName: string;
  requesterDid: string;
  marketDid: string;
  payload?: RecordRef;
  rfp?: RecordRef;
  policy?: RecordRef;
  bid?: RecordRef;
  winner?: ContractWinner;
  accept?: RecordRef;
  receipt?: RecordRef;
  receiptOk?: boolean;
  submitEventRef?: string;
  vmAddress?: string;
  bids?: number;
  release?: ContractRelease;
  outcome?: ContractOutcome;
  error?: string;
  timestamps: Partial<Record<ContractPhase, string>>;
}

export type ContractStatePatch = Partial<
  Omit<ContractState, "version" | "phase" | "timestamps">
>;

export interface ContractStateInit {
  vmName: string;
  requesterDid: string;
  marketDid: string;
  at: string;
}

export function createContractState(init: ContractStateInit): ContractState {
  return {
    version: CONTRACT_STATE_VERSION,
    phase: "requested",
    vmName: init.vmName,
    requesterDid: init.requesterDid,
    marketDid: init.marketDid,
    timestamps: { requested: init.at },
  };
}

export function patchContract(state: ContractState, patch: ContractStatePatch): ContractState {
  return { ...state, ...patch, timestamps: { ...state.timestamps } };
}

export function advanceContract(
  state: ContractState,
  phase: ContractPhase,
  patch: ContractStatePatch,
  at: string,
): ContractState {
  return {
    ...state,
    ...patch,
    phase,
    timestamps: { ...state.timestamps, [phase]: at },
  };
}

export function hasContract(state: ContractState): boolean {
  return state.accept !== undefined;
}

export interface ReleaseTarget {
  receipt: RecordRef;
  submitEventRef: string;
}

export function releaseTargetOf(state: ContractState): ReleaseTarget | null {
  if (!state.receipt?.uri || !state.receipt?.cid || !state.submitEventRef) return null;
  return { receipt: state.receipt, submitEventRef: state.submitEventRef };
}

export function receiptKeyOf(receipt: RecordRef): string {
  return `${receipt.uri}#${receipt.cid}`;
}

export function serializeContractState(state: ContractState): string {
  return JSON.stringify(state);
}

export class InvalidContractStateError extends Error {
  constructor(reason: string) {
    super(`invalid contract state: ${reason}`);
    this.name = "InvalidContractStateError";
  }
}

function isRecordRef(v: unknown): v is RecordRef {
  if (!v || typeof v !== "object") return false;
  const o = v as Record<string, unknown>;
  return typeof o.uri === "string" && typeof o.cid === "string";
}

const REF_FIELDS = ["payload", "rfp", "policy", "bid", "accept", "receipt"] as const;

export function parseContractState(input: string | unknown): ContractState {
  let value: unknown = input;
  if (typeof input === "string") {
    try {
      value = JSON.parse(input);
    } catch (err) {
      throw new InvalidContractStateError(`not JSON: ${String(err)}`);
    }
  }
  if (!value || typeof value !== "object") throw new InvalidContractStateError("not an object");
  const o = value as Record<string, unknown>;
  if (o.version !== CONTRACT_STATE_VERSION) {
    throw new InvalidContractStateError(`unsupported version ${String(o.version)}`);
  }
  if (!CONTRACT_PHASES.includes(o.phase as ContractPhase)) {
    throw new InvalidContractStateError(`unknown phase ${String(o.phase)}`);
  }
  for (const key of ["vmName", "requesterDid", "marketDid"] as const) {
    if (typeof o[key] !== "string") throw new InvalidContractStateError(`${key} is not a string`);
  }
  for (const key of REF_FIELDS) {
    if (o[key] !== undefined && !isRecordRef(o[key])) {
      throw new InvalidContractStateError(`${key} is not a strongRef`);
    }
  }
  if (!o.timestamps || typeof o.timestamps !== "object") {
    throw new InvalidContractStateError("timestamps is not an object");
  }
  return o as unknown as ContractState;
}

export type GuestAddressKind = "iroh-ticket" | "relay-fqdn" | "ip";

export function guestAddressKind(address: string): GuestAddressKind {
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(address)) return "ip";
  return address.startsWith(IROH_SCHEME) ? "iroh-ticket" : "relay-fqdn";
}

export type FlowDecision = "continue" | "return";
export type BidDecision = "accept" | "deny";
export type Awaitable<T> = T | Promise<T>;

export interface BidView {
  did: string;
  uri: string;
  cid: string;
  record: Record<string, unknown>;
}

export interface RfpSubmittedEvent {
  rfp: RecordRef;
  payload: RecordRef;
  policy?: RecordRef;
  bidderDids: string[];
}

export interface BidEvent {
  bid: BidView;
}

export interface AcceptedEvent {
  accept: RecordRef;
  bid: RecordRef;
  winnerDid: string;
}

export interface ReceiptEvent {
  receipt?: RecordRef;
  receiptOk: boolean;
  submitEventRef?: string;
}

export interface NetworkEvent {
  address: string;
  kind: GuestAddressKind;
  source: string;
}

export interface SecretsFetchedEvent {
  capability: string;
  subject: string;
  count: number;
}

export interface ComputeRequestHandlers {
  onRfpSubmitted?: (event: RfpSubmittedEvent, state: ContractState) => Awaitable<FlowDecision>;
  onBid?: (event: BidEvent, state: ContractState) => Awaitable<BidDecision>;
  onAccepted?: (event: AcceptedEvent, state: ContractState) => Awaitable<FlowDecision>;
  onReceipt?: (event: ReceiptEvent, state: ContractState) => Awaitable<FlowDecision>;
  onNetwork?: (event: NetworkEvent, state: ContractState) => Awaitable<FlowDecision>;
  onSecretsFetched?: (
    event: SecretsFetchedEvent,
    state: ContractState,
  ) => Awaitable<FlowDecision>;
}

export class ReturnLatch {
  readonly returned: Promise<void>;
  #resolve!: () => void;
  #requested = false;
  #reason = "";

  constructor() {
    const { promise, resolve } = Promise.withResolvers<void>();
    this.returned = promise;
    this.#resolve = resolve;
  }

  get requested(): boolean {
    return this.#requested;
  }

  get reason(): string {
    return this.#reason;
  }

  request(reason: string): void {
    if (this.#requested) return;
    this.#requested = true;
    this.#reason = reason;
    this.#resolve();
  }

  apply(decision: FlowDecision | undefined, reason: string): boolean {
    if (decision === "return") this.request(reason);
    return this.#requested;
  }
}

export interface AttestationKeyLike {
  did(): string;
  privateKey: { bytes: Uint8Array; toBytes?(): Uint8Array };
}

export interface ContractReleaser {
  did: string;
  attestationKp: AttestationKeyLike;
  createSignedRepoRecord(
    collection: string,
    record: Record<string, unknown>,
    aKp: AttestationKeyLike,
    issuer?: string,
  ): Promise<RecordRef>;
  resolveBidderEndpoint(endpointUrl: string): Promise<{ targetUrl: string; audDid: string } | null>;
  callBidder(
    targetBase: string,
    nsid: string,
    lxm: string,
    audDid: string,
    body: Record<string, unknown>,
  ): Promise<{ status: number; ok: boolean; body: unknown }>;
}
