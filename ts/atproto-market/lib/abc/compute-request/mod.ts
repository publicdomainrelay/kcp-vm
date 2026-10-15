import {
  COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  EVENT_NSID,
  MAX_VM_HOST_KEYS,
} from "@publicdomainrelay/market-common";

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

export interface ContractTermination {
  reason: string;
  exitCode?: number;
  observedAt: string;
  event: RecordRef;
  payload: RecordRef;
  request?: RecordRef;
}

export interface ContractHostKey {
  type: string;
  publicKey: string;
  fingerprint: string;
}

export interface ContractHostKeys {
  keys: ContractHostKey[];
  observedAt: string;
  event: RecordRef;
  payload: RecordRef;
}

export interface ContractAcceptPending {
  bidUri: string;
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
  acceptPending?: ContractAcceptPending;
  accept?: RecordRef;
  receipt?: RecordRef;
  receiptOk?: boolean;
  submitEventRef?: string;
  vmAddress?: string;
  bids?: number;
  release?: ContractRelease;
  termination?: ContractTermination;
  hostKeys?: ContractHostKeys;
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

export function isTerminated(state: ContractState): boolean {
  return state.termination !== undefined;
}

export interface ComputeEventEnvelope {
  uri: string;
  cid: string;
  record: Record<string, unknown>;
  payload: Record<string, unknown>;
}

export type TerminationBinding =
  | { ok: true; termination: ContractTermination }
  | { ok: false; reason: string };

function authorityOf(uri: string): string {
  return uri.replace("at://", "").split("/")[0] ?? "";
}

function collectionOf(uri: string): string {
  return uri.replace("at://", "").split("/")[1] ?? "";
}

type BoundPayload = { ok: true; payloadRef: RecordRef } | { ok: false; reason: string };

function boundEventPayload(envelope: ComputeEventEnvelope, receipt: RecordRef, nsid: string, name: string): BoundPayload {
  const { record, payload } = envelope;
  if (record.$type !== undefined && record.$type !== EVENT_NSID) {
    return { ok: false, reason: "envelope is not a market.event" };
  }
  const receiptRef = record.receipt;
  if (!isRecordRef(receiptRef)) return { ok: false, reason: "missing receipt ref" };
  if (receiptRef.uri !== receipt.uri) return { ok: false, reason: "event names another receipt" };
  const bidder = authorityOf(receipt.uri);
  if (authorityOf(envelope.uri) !== bidder) {
    return { ok: false, reason: "event not authored by the receipt's bidder" };
  }
  const payloadRef = record.payload;
  if (!isRecordRef(payloadRef)) return { ok: false, reason: "missing payload ref" };
  if (authorityOf(payloadRef.uri) !== bidder) {
    return { ok: false, reason: "payload not in the bidder's repo" };
  }
  if (collectionOf(payloadRef.uri) !== nsid) {
    return { ok: false, reason: `payload is not ${name}` };
  }
  if (payload.$type !== undefined && payload.$type !== nsid) {
    return { ok: false, reason: `payload record is not ${name}` };
  }
  return { ok: true, payloadRef: { uri: payloadRef.uri, cid: payloadRef.cid } };
}

export function terminationOf(envelope: ComputeEventEnvelope, receipt: RecordRef): TerminationBinding {
  const bound = boundEventPayload(envelope, receipt, COMPUTE_EVENTS_VM_TERMINATED_NSID, "vm.terminated");
  if (!bound.ok) return bound;
  const { payload } = envelope;
  const payloadRef = bound.payloadRef;
  if (typeof payload.reason !== "string" || !payload.reason) return { ok: false, reason: "missing reason" };
  if (typeof payload.observedAt !== "string" || !payload.observedAt) {
    return { ok: false, reason: "missing observedAt" };
  }
  const exitCode = Number.isInteger(payload.exitCode) ? payload.exitCode as number : undefined;
  const request = isRecordRef(payload.request) ? { uri: payload.request.uri, cid: payload.request.cid } : undefined;
  return {
    ok: true,
    termination: {
      reason: payload.reason,
      ...(exitCode !== undefined ? { exitCode } : {}),
      observedAt: payload.observedAt,
      event: { uri: envelope.uri, cid: envelope.cid },
      payload: { uri: payloadRef.uri, cid: payloadRef.cid },
      ...(request ? { request } : {}),
    },
  };
}

export type HostKeysBinding =
  | { ok: true; hostKeys: ContractHostKeys }
  | { ok: false; reason: string };

const HOST_KEY_TYPE = /^[a-z0-9][a-z0-9.@-]{0,63}$/;
const HOST_KEY_BLOB = /^[A-Za-z0-9+/]{16,4096}={0,2}$/;
const HOST_KEY_FINGERPRINT = /^SHA256:[A-Za-z0-9+/]{43}$/;

export function hostKeysOf(envelope: ComputeEventEnvelope, receipt: RecordRef): HostKeysBinding {
  const bound = boundEventPayload(envelope, receipt, COMPUTE_EVENTS_VM_HOST_KEYS_NSID, "vm.hostKeys");
  if (!bound.ok) return bound;
  const { payload } = envelope;
  if (typeof payload.observedAt !== "string" || !payload.observedAt) {
    return { ok: false, reason: "missing observedAt" };
  }
  const raw = payload.keys;
  if (!Array.isArray(raw) || raw.length === 0 || raw.length > MAX_VM_HOST_KEYS) {
    return { ok: false, reason: "keys is not a non-empty array" };
  }
  const keys: ContractHostKey[] = [];
  for (const k of raw) {
    const { type, publicKey, fingerprint } = (k ?? {}) as Record<string, unknown>;
    if (typeof type !== "string" || !HOST_KEY_TYPE.test(type)) return { ok: false, reason: "malformed key type" };
    if (typeof publicKey !== "string" || !HOST_KEY_BLOB.test(publicKey)) {
      return { ok: false, reason: "malformed public key" };
    }
    if (typeof fingerprint !== "string" || !HOST_KEY_FINGERPRINT.test(fingerprint)) {
      return { ok: false, reason: "malformed fingerprint" };
    }
    keys.push({ type, publicKey, fingerprint });
  }
  return {
    ok: true,
    hostKeys: {
      keys,
      observedAt: payload.observedAt,
      event: { uri: envelope.uri, cid: envelope.cid },
      payload: bound.payloadRef,
    },
  };
}

export function hostKeyAliasOf(receipt: RecordRef): string {
  const clean = (v: string) => v.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  const rkey = receipt.uri.split("/").pop() ?? "";
  return `swc-guest-${clean(authorityOf(receipt.uri))}-${clean(rkey)}`;
}

export function knownHostsOf(alias: string, keys: Array<{ type: string; publicKey: string }>): string {
  return keys.map((k) => `${alias} ${k.type} ${k.publicKey}\n`).join("");
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

export type GuestAddressKind = "iroh-ticket" | "refused" | "ip" | "hostname";

export function guestAddressKind(address: string): GuestAddressKind {
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?$/.test(address)) return "ip";
  if (address.startsWith(IROH_SCHEME)) return "iroh-ticket";
  return address.includes(".") ? "refused" : "hostname";
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
  arrivedAt: string;
}

export type WinnerReason = "firstFree" | "bestWithinWindow" | "policy" | "resumed";

export interface WinnerEvent {
  bid: RecordRef;
  did: string;
  reason: WinnerReason;
  considered: number;
}

export interface AcceptingEvent {
  rfp: RecordRef;
  bid: RecordRef;
  winnerDid: string;
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

export type TerminatedEvent = ContractTermination;

export type HostKeysEvent = ContractHostKeys;

export interface ComputeRequestHandlers {
  onRfpSubmitted?: (event: RfpSubmittedEvent, state: ContractState) => Awaitable<FlowDecision>;
  onBid?: (event: BidEvent, state: ContractState) => Awaitable<BidDecision>;
  onWinner?: (event: WinnerEvent, state: ContractState) => Awaitable<void>;
  onAccepting?: (event: AcceptingEvent, state: ContractState) => Awaitable<FlowDecision>;
  onAccepted?: (event: AcceptedEvent, state: ContractState) => Awaitable<FlowDecision>;
  onReceipt?: (event: ReceiptEvent, state: ContractState) => Awaitable<FlowDecision>;
  onNetwork?: (event: NetworkEvent, state: ContractState) => Awaitable<FlowDecision>;
  onSecretsFetched?: (
    event: SecretsFetchedEvent,
    state: ContractState,
  ) => Awaitable<FlowDecision>;
  onTerminated?: (event: TerminatedEvent, state: ContractState) => Awaitable<void>;
  onHostKeys?: (event: HostKeysEvent, state: ContractState) => Awaitable<void>;
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
