/**
 * policy-common — wire types + constants for the policy engine, shared by the
 * ABC, executors, seeder, server and CLI. External deps only; no project-local
 * imports (layer-0 rule).
 */

// ── NSIDs ────────────────────────────────────────────────────────────────────

/** Pre-defined policy workflow records referenced by RFP.policies[]. */
export const POLICY_GHA_LITE_NSID = "computer.socialweb.temp.policy.gha-lite";
export const POLICY_TYPESCRIPT_NSID = "computer.socialweb.temp.policy.typescript";

/** Market records a policy operates on / alongside. */
export const RFP_NSID = "com.publicdomainrelay.temp.market.rfp";
export const VOUCH_NSID = "sh.tangled.graph.vouch";
export const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";

/** Policy-engine XRPC (server surface). */
export const MARKET_EVALUATE_POLICY_NSID = "com.publicdomainrelay.temp.market.evaluatePolicy";

/** Split an at:// URI into repo / collection / rkey. */
export function splitAtUri(uri: string): { repo: string; collection: string; rkey: string } {
  const parts = uri.replace(/^at:\/\//, "").split("/");
  return { repo: parts[0] ?? "", collection: parts[1] ?? "", rkey: parts[2] ?? "" };
}

// ── Wire types ───────────────────────────────────────────────────────────────

export interface StrongRef {
  uri: string;
  cid: string;
}

export interface PolicyArgs {
  bidWindowSec?: number;
  firstFree?: boolean;
  cacheTtlSec?: number;
  [key: string]: unknown;
}

export interface PolicySpec {
  name: string;
  description?: string;
  args: PolicyArgs;
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

/**
 * A market record the policy is operating on, in the firehose/event shape:
 * uri + cid are the strongRef; value is the record body. rfp/bid/accept are
 * optional — the caller passes whichever records exist at their stage.
 */
export interface PolicyRecord {
  uri: string;
  cid: string;
  value?: Record<string, unknown>;
  /** Inline resolved payload body so work policies can evaluate offline. */
  payload?: Record<string, unknown>;
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

/** Host-brokered context handed to a policy. Same shape the sandbox sees. */
export interface PolicyEvalCtx {
  policyName: string;
  args: PolicyArgs;
  perspective: PolicyPerspective;
  selfDid: string;
  counterpartyDid: string;
  subjectDid: string;
  rootRequesterDid: string;
  resolve: (ref: StrongRef) => Promise<Record<string, unknown>>;
  resolveOperatorDid: (did: string) => Promise<string | null>;
  getVouchedDids: (did: string) => Promise<Set<string>>;
  log: (level: string, msg: string, meta?: Record<string, unknown>) => void;
  policyRef?: StrongRef;
  demand?: DemandSide;
  offer?: OfferSide;
}

/** Request the server-side evaluate() surface accepts. */
export interface PolicyEvalRequest {
  /** RFP policies[] strongRef the caller is evaluating. */
  policyRef?: StrongRef;
  /** Already-resolved policy record (uri/cid/value). */
  policyRecord?: PolicyRecord;
  name?: string;
  args?: PolicyArgs;
  perspective?: PolicyPerspective;
  selfDid?: string;
  subjectDid: string;
  rootRequesterDid: string;
  counterpartyDid?: string;
  demand?: DemandSide;
  offer?: OfferSide;
}

export interface ScopeRequest {
  name: string;
  args?: PolicyArgs;
  perspective?: PolicyPerspective;
  selfDid?: string;
  subjectDid: string;
  rootRequesterDid: string;
  counterpartyDid?: string;
}

export interface DescribedPolicy {
  name: string;
  kind: "trust" | "work";
  description: string;
  perspectives?: PolicyPerspective[];
}

/** A strongRef to a record the seeder created/upserted. */
export interface RecordRef {
  uri: string;
  cid: string;
}

/** Handle returned by PolicySeeder.startRefresh; stop() cancels the periodic refresh. */
export interface RefreshHandle {
  stop(): void;
}
