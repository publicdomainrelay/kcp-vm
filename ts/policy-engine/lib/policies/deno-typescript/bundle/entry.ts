/**
 * The builtin workerManifest bundle for typescript policy records.
 *
 * The workerManifest `bundle` contract (see TypescriptExecutor): the bundle
 * MUST assign `globalThis.__evaluatePolicy` to an async function taking the
 * plain-data policy context and returning `{ allow, violations }`. This entry
 * wraps the builtin deno-typescript policy registry: it looks up
 * `input.policyName` and dispatches to that policy's `evaluate(ctx)`.
 *
 * The ctx is built from the input. `getVouchedDids` is reconstituted as a Set
 * because the host serializes it to a string[] across the RPC boundary;
 * `resolve` / `resolveOperatorDid` / `log` pass straight through. An unknown
 * policy name denies with `unknown policy: <name>` under `policyId: <name>`.
 */

import { createPolicyRegistry } from "../registry.ts";
import type {
  PolicyArgs,
  PolicyEvalCtx,
  PolicyPerspective,
  PolicyResult,
  StrongRef,
} from "@publicdomainrelay/policy-deno-typescript-shared";

/** The plain-data input the executor's worker shim passes to __evaluatePolicy. */
interface EvaluatePolicyInput {
  policyName: string;
  args: PolicyArgs;
  perspective: PolicyPerspective;
  selfDid: string;
  subjectDid: string;
  rootRequesterDid: string;
  counterpartyDid: string;
  resolve: (ref: StrongRef) => Promise<Record<string, unknown>>;
  resolveOperatorDid: (did: string) => Promise<string | null>;
  /** Host serializes the vouch Set to a string[]; reconstitute below. */
  getVouchedDids: (did: string) => Promise<string[]>;
  log: (level: string, msg: string, meta?: Record<string, unknown>) => void;
  policyRef?: StrongRef;
  demand?: PolicyEvalCtx["demand"];
  offer?: PolicyEvalCtx["offer"];
}

declare global {
  var __evaluatePolicy: (input: EvaluatePolicyInput) => Promise<PolicyResult>;
}

const registry = createPolicyRegistry();

function deny(msg: string, policyId: string): PolicyResult {
  return { allow: false, violations: [{ msg, policyId }] };
}

globalThis.__evaluatePolicy = async (input: EvaluatePolicyInput): Promise<PolicyResult> => {
  const policy = registry.get(input.policyName);
  if (!policy) {
    return deny(`unknown policy: ${input.policyName}`, input.policyName);
  }

  const ctx: PolicyEvalCtx = {
    policyName: input.policyName,
    args: input.args ?? {},
    perspective: input.perspective,
    selfDid: input.selfDid,
    subjectDid: input.subjectDid,
    rootRequesterDid: input.rootRequesterDid,
    counterpartyDid: input.counterpartyDid,
    resolve: input.resolve,
    resolveOperatorDid: input.resolveOperatorDid,
    getVouchedDids: async (did) => new Set(await input.getVouchedDids(did)),
    log: input.log,
    policyRef: input.policyRef,
    demand: input.demand,
    offer: input.offer,
  };

  return policy.evaluate(ctx);
};
