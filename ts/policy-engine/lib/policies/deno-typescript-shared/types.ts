/**
 * Inline structural types for policy evaluation.
 *
 * Mirrors the ABC package shapes (`lib/abc/market-policy/mod.ts` and the
 * `TrustQuery` half of `lib/abc/market-policy-trust/mod.ts`) so this package —
 * and the gha-lite bundles built from it — are fully self-contained and need no
 * cross-repo / registry resolution. The shapes are structural, so a policy
 * typed here stays assignable to the atproto-market ABC interfaces.
 */

export interface StrongRef {
  uri: string;
  cid: string;
}

export interface PolicyArgs {
  bidWindowSec?: number;
  firstFree?: boolean;
  [key: string]: unknown;
}

export type PolicyPerspective = "bidder" | "requester";

export interface PolicyViolation {
  msg: string;
  policyId: string | StrongRef;
}

export interface PolicyResult {
  allow: boolean;
  violations: PolicyViolation[];
}

export interface DemandSide {
  rfpRef: StrongRef;
  payloadRef: StrongRef;
  payloadNsid: string;
  payload?: Record<string, unknown>;
}

export interface OfferSide {
  bidRef: StrongRef;
  payloadRef: StrongRef;
  payloadNsid: string;
  payload?: Record<string, unknown>;
}

/**
 * A market record the policy is operating on, in the firehose/event shape:
 * `uri` + `cid` are the strongRef; `value` is the record body (hydrated over
 * the network when absent). `rfp`, `bid`, and `accept` are all optional — the
 * caller passes whichever records exist at their stage, and the policy
 * evaluates with what it has.
 */
export interface PolicyRecord {
  uri: string;
  cid: string;
  value?: Record<string, unknown>;
  /** Inline resolved payload body (the rfp's compute.vm / bid's bids.free), so
   * work policies can evaluate offline without resolving payloadRef. */
  payload?: Record<string, unknown>;
}

export interface PolicyEvalCtx {
  policyName: string;
  args: PolicyArgs;
  /** Who is evaluating: the bidder deciding whether to bid, or the requester. */
  perspective: PolicyPerspective;
  /** The DID running this evaluation. */
  selfDid: string;
  /** The other side of the transaction. */
  counterpartyDid: string;
  subjectDid: string;
  rootRequesterDid: string;
  resolve: (ref: StrongRef) => Promise<Record<string, unknown>>;
  resolveOperatorDid: (did: string) => Promise<string | null>;
  /** Vouch/follow set for a DID. Host-brokered so policies stay pure. */
  getVouchedDids: (did: string) => Promise<Set<string>>;
  log: (level: string, msg: string, meta?: Record<string, unknown>) => void;
  policyRef?: StrongRef;
  /** The workload a bidder is being asked to run. */
  demand?: DemandSide;
  /** The bid offered in response (requester side). */
  offer?: OfferSide;
  /** The records the evaluation operates on, whichever exist at this stage. */
  rfp?: PolicyRecord;
  bid?: PolicyRecord;
  accept?: PolicyRecord;
}

/**
 * Sync trust state for the hot-path `decide` gate. Zero I/O; undefined means
 * "unknown, needs a refresh", never a hard decision. Mirrors market-policy-trust-abc.
 */
export interface TrustQuery {
  operatorOf(did: string): string | undefined;
  sameOperator(a: string, b: string): boolean | undefined;
  isVouched(voucher: string, vouchee: string): boolean;
  vouchedBy(voucher: string): ReadonlySet<string>;
  trustedOperators(): ReadonlySet<string>;
  associatedWith(operatorDid: string): ReadonlySet<string>;
}

export interface PreFilterInput {
  did: string;
  selfDid: string;
  vouchedDids?: Set<string>;
  args: PolicyArgs;
  /** Sync trust cache for the hot path. */
  query?: TrustQuery;
}

/**
 * Trust policies gate engagement: "may I transact with this counterparty".
 * `decide` is the sync hot-path gate over the trust cache; undefined means
 * "cannot decide from cache alone". `evaluate` is the full async decision.
 */
export interface TrustPolicy {
  readonly kind: "trust";
  readonly name: string;
  readonly description: string;
  readonly needsVouchSet?: boolean;
  decide(input: PreFilterInput): boolean | undefined;
  evaluate(ctx: PolicyEvalCtx): Promise<PolicyResult>;
}

/** Work policies gate the workload itself: "do I want to run this". */
export interface WorkPolicy {
  readonly kind: "work";
  readonly name: string;
  readonly description: string;
  readonly perspectives: PolicyPerspective[];
  evaluate(ctx: PolicyEvalCtx): Promise<PolicyResult>;
}

export type Policy = TrustPolicy | WorkPolicy;

export interface PolicyRegistry {
  get(name: string): Policy | undefined;
  names(): string[];
}

/**
 * Fail loud when a policy is used from the wrong side. Work policies declare
 * `perspectives[]`; trust policies carry the side in the name
 * (bidder-* / requester-*; `open` is symmetric). Never silently no-op — a
 * bidder must not run a requester-only policy.
 */
export function assertPolicyPerspective(policy: Policy, perspective: PolicyPerspective): void {
  if (policy.kind === "work" && !policy.perspectives.includes(perspective)) {
    throw new Error(
      `policy "${policy.name}" is not usable from the ${perspective} side (perspectives: ${policy.perspectives.join(", ")})`,
    );
  }
  if (policy.kind === "trust") {
    if (policy.name.startsWith("bidder-") && perspective !== "bidder") {
      throw new Error(`policy "${policy.name}" is not usable from the ${perspective} side`);
    }
    if (policy.name.startsWith("requester-") && perspective !== "requester") {
      throw new Error(`policy "${policy.name}" is not usable from the ${perspective} side`);
    }
  }
}
