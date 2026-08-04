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
  PolicyEvalCtx,
  PolicyEvalRequest,
  PolicyRecord,
  PolicyResult,
  RecordRef,
  RefreshHandle,
  ScopeRequest,
} from "@publicdomainrelay/policy-common";

export type { PolicyRecord, PolicyResult, PolicyEvalCtx };

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
