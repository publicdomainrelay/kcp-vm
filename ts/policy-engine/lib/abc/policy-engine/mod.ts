/**
 * policy-engine-abc — the pluggability contracts for the policy engine.
 * Pure interfaces + tiny state, zero I/O. Depends on policy-common (types only).
 *
 *   PolicyEngineExecutor — the unit of execution; one per record $type.
 *   EngineRegistry       — $type → executor dispatch.
 *   PolicyEngineServer   — the HTTP/XHRPC surface (evaluate/describe/checkScope).
 *   PolicySeeder         — pre-creates known policy records (offering-style).
 */

import type {
  DescribedPolicy,
  PolicyArgs,
  PolicyEvalCtx,
  PolicyEvalRequest,
  PolicyPerspective,
  PolicyRecord,
  PolicyResult,
  RecordRef,
  RefreshHandle,
  ScopeRequest,
} from "@publicdomainrelay/policy-common";

export type { PolicyRecord, PolicyResult, PolicyEvalCtx };
export { POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-common";

/**
 * Trust-only scope request for the hot path / checkScope.
 *
 * The scope lane is the policy's cheap gate: a trust-policy `decide` over a
 * trust snapshot. It never touches workload records (no demand/offer, no
 * work-policy evaluation). An executor that cannot answer from the scope lane
 * returns `undefined` and the caller escalates to `execute()`.
 */
export interface ScopeInput {
  perspective: PolicyPerspective;
  selfDid: string;
  counterpartyDid: string;
  args: PolicyArgs;
  /** Host resolvers — used by in-process scope (builtin/typescript) to build a
   *  trust snapshot for decide(). gha-lite scope resolves its own trust data
   *  inside the workflow action (network + its own cache), so it ignores these. */
  resolveOperatorDid?: (did: string) => Promise<string | null>;
  getVouchedDids?: (did: string) => Promise<Set<string>>;
}

/**
 * The single pluggability point. An executor is bound to a record $type; the
 * EngineRegistry maps record $type → executor.
 */
export interface PolicyEngineExecutor {
  /** Record $type this executor handles (dispatch key). */
  readonly kind: string;
  /** Execute one already-resolved policy record against a ctx. */
  execute(input: {
    policyRecord: PolicyRecord;
    ctx: PolicyEvalCtx;
    permissions?: Record<string, unknown>;
  }): Promise<PolicyResult>;
  /** Trust-only scope verdict for the hot path / checkScope. `undefined` =
   *  cannot decide from the scope lane alone; the caller escalates to
   *  execute(). Optional — an executor without a scope lane is always
   *  escalated. */
  scope?(input: {
    policyRecord: PolicyRecord;
    scope: ScopeInput;
  }): Promise<PolicyResult | undefined>;
}

/** $type → executor dispatch. A new executor = a sibling package + one entry. */
export interface EngineRegistry {
  get($type: string): PolicyEngineExecutor | undefined;
  kinds(): string[];
}

/** Server surface. Dispatches evaluate() through an EngineRegistry. */
export interface PolicyEngineServer {
  evaluate(input: PolicyEvalRequest): Promise<PolicyResult>;
  describe(): Promise<DescribedPolicy[]>;
  checkScope(input: ScopeRequest): Promise<PolicyResult>;
  registry: EngineRegistry;
}

/**
 * Idempotent upsert of every known pre-defined policy record, mirroring how the
 * bidder seeds market.offering records (ensureOffering + startOfferingRefresh).
 */
export interface PolicySeeder {
  /** Upsert every known policy record; returns the strongRefs to reference. */
  ensure(): Promise<RecordRef[]>;
  /** Re-commit records periodically so relays re-index them; stop() cancels. */
  startRefresh(intervalMs: number): RefreshHandle;
}
