// Common logic for the bundled policy actions (lib/policies/gha-lite).
//
// Reads the INPUT_* inputs — `self-did` (the evaluator) plus the market
// records the caller has at its stage (`rfp`/`bid`/`accept`, each optional,
// firehose shape {uri,cid,value?}) — builds the shared record-centric ctx via
// createPolicyCtx, evaluates the policy via runPolicy (memoizing the verdict
// into the GITHUB_CACHE-backed JSON-object cache, keyed on the record
// strongRefs), and writes allow/violations/cache-key to GITHUB_OUTPUT. This
// module is inlined into each action's self-contained dist/index.js at bundle
// time (deno task build).
//
// Policies are perspective-named, so no `perspective` input exists: the side is
// derived from the policy (work policies declare it, trust policies carry it in
// the name).
//
// Two lanes, selected by the `mode` workflow_dispatch input:
//
//   mode: full (default)  — full evaluate: records + workload + work policies.
//   mode: scope           — trust-only gate for the hot path / checkScope.
//                           Runs the policy's decide() over a trust snapshot
//                           via scopeDecide; on abstain it falls back to a full
//                           evaluate against the same counterparty.

import {
  createCacheStore,
  createPolicyCtx,
  runPolicy,
  PolicyEvalCtxImpl,
  scopeDecide,
  type Policy,
  type PolicyArgs,
  type PolicyPerspective,
  type PolicyRecord,
  type PolicyRegistry,
} from "@publicdomainrelay/policy-deno-typescript-shared";

function input(name: string): string {
  return Deno.env.get("INPUT_" + name.toUpperCase()) ?? "";
}

function inputJson<T>(name: string): T | undefined {
  const raw = input(name);
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as T;
  } catch {
    console.error(`::error::policy action: input ${name} is not valid JSON: ${raw}`);
    Deno.exit(1);
  }
}

/** The side a policy acts on: work policies declare it, trust policies carry it
 * in the name (bidder-* / requester-*; open is symmetric → requester). */
function policyPerspective(policy: Policy): PolicyPerspective {
  if (policy.kind === "work") return policy.perspectives[0] ?? "requester";
  return policy.name.startsWith("bidder-") ? "bidder" : "requester";
}

interface ActionOutputs {
  allow: boolean;
  violations: unknown[];
  fromCache: boolean;
  cacheKey: string;
}

async function writeOutputs(o: ActionOutputs): Promise<void> {
  const outputPath = Deno.env.get("GITHUB_OUTPUT");
  if (outputPath) {
    const lines = [
      `allow=${o.allow}`,
      `violations=${JSON.stringify(o.violations)}`,
      `from-cache=${o.fromCache}`,
      `cache-key=${o.cacheKey}`,
    ];
    for (const line of lines) {
      await Deno.writeTextFile(outputPath, `${line}\n`, { append: true });
    }
  }
}

/** A registry holding exactly one policy — what scopeDecide needs for the
 * bundled action's single policy. */
function onePolicyRegistry(policy: Policy): PolicyRegistry {
  return {
    get: (name) => (name === policy.name ? policy : undefined),
    names: () => [policy.name],
  };
}

/**
 * Host-provided trust snapshot. When present, the action answers trust
 * queries from it instead of re-reading the network — the engine resolves the
 * two sides' operators + vouch sets through the host's own resolvers, so local
 * test envs (fake DIDs, local PDS) and prod both work.
 */
interface TrustInput {
  operatorOf: Record<string, string>;
  vouchedBy: Record<string, string[]>;
  trustedOperators: string[];
}

/** Snapshot-backed trust resolvers, or undefined when no `trust` input. */
function trustResolvers(): {
  resolveOperatorDid: (did: string) => Promise<string | null>;
  getVouchedDids: (did: string) => Promise<Set<string>>;
} | undefined {
  const trust = inputJson<TrustInput>("trust");
  if (!trust) return undefined;
  return {
    resolveOperatorDid: async (did) => trust.operatorOf[did] ?? null,
    getVouchedDids: async (did) => new Set(trust.vouchedBy[did] ?? []),
  };
}

/**
 * Trust-only scope lane. Builds a ctx with the counterparty as subject, runs
 * the policy's decide() via scopeDecide; on abstain falls back to a full
 * evaluate so scope never hard-denies a party the graph could still admit.
 */
async function evaluateScope(
  policy: Policy,
  opts: {
    selfDid: string;
    perspective: PolicyPerspective;
    args: PolicyArgs;
    store: ReturnType<typeof createCacheStore>;
  },
): Promise<void> {
  const { selfDid, perspective, args, store } = opts;
  const counterpartyDid = input("counterparty-did");
  if (!counterpartyDid) {
    console.error("::error::policy action: scope mode requires counterparty-did");
    Deno.exit(1);
  }

  const subjectDid = counterpartyDid;
  const rootRequesterDid = perspective === "requester" ? selfDid : counterpartyDid;

  const tr = trustResolvers();
  const ctx = new PolicyEvalCtxImpl({
    policyName: policy.name,
    args,
    perspective,
    selfDid,
    subjectDid,
    rootRequesterDid,
    resolveOperatorDid: tr?.resolveOperatorDid,
    getVouchedDids: tr?.getVouchedDids,
    store,
  });

  const verdict = await scopeDecide({
    registry: onePolicyRegistry(policy),
    named: [{ name: policy.name, args }],
    perspective,
    selfDid,
    counterpartyDid,
    resolveOperatorDid: tr?.resolveOperatorDid ?? ((did) => ctx.resolveOperatorDid(did)),
    getVouchedDids: tr?.getVouchedDids ?? ((did) => ctx.getVouchedDids(did)),
  });

  if (verdict !== undefined) {
    await writeOutputs({ allow: verdict.allow, violations: verdict.violations, fromCache: false, cacheKey: "" });
    console.log(JSON.stringify({ policy: policy.name, allow: verdict.allow, violations: verdict.violations, mode: "scope" }));
    return;
  }

  // decide() abstained — the graph could not answer from the snapshot alone.
  // Fall back to a full evaluate against the same counterparty.
  const { result, fromCache, cacheKey } = await runPolicy(store, policy, ctx, {
    cacheKey: input("cache-key") || undefined,
  });
  await writeOutputs({ allow: result.allow, violations: result.violations, fromCache, cacheKey });
  console.log(
    JSON.stringify({ policy: policy.name, allow: result.allow, violations: result.violations, fromCache, cacheKey, mode: "scope-fallback" }),
  );
}

export async function evaluatePolicyAction(policy: Policy): Promise<void> {
  const selfDid = input("self-did");

  // The engine does not enforce action.yml `required:`, so validate here.
  if (!selfDid) {
    console.error("::error::policy action: self-did is required (the evaluator's own DID)");
    Deno.exit(1);
  }

  const perspective = policyPerspective(policy);
  const store = createCacheStore();
  const args = inputJson<PolicyArgs>("policy-args") ?? {};

  if (input("mode") === "scope") {
    await evaluateScope(policy, { selfDid, perspective, args, store });
    return;
  }

  const tr = trustResolvers();
  const ctx = await createPolicyCtx({
    policyName: policy.name,
    args,
    perspective,
    selfDid,
    // The engine passes the caller's explicit DID context when the workflow is
    // evaluated against known parties (e.g. the bidder checking an RFP's policy
    // before any bid exists); these win over record-derived fields.
    subjectDid: input("subject-did") || undefined,
    rootRequesterDid: input("root-requester-did") || undefined,
    counterpartyDid: input("counterparty-did") || undefined,
    // The host's trust snapshot (operators + vouch sets) when provided — the
    // action answers trust queries from it instead of re-reading the network.
    resolveOperatorDid: tr?.resolveOperatorDid,
    getVouchedDids: tr?.getVouchedDids,
    rfp: inputJson<PolicyRecord>("rfp"),
    bid: inputJson<PolicyRecord>("bid"),
    accept: inputJson<PolicyRecord>("accept"),
    store,
  });

  const { result, fromCache, cacheKey } = await runPolicy(store, policy, ctx, {
    cacheKey: input("cache-key") || undefined,
  });

  await writeOutputs({ allow: result.allow, violations: result.violations, fromCache, cacheKey });
  console.log(
    JSON.stringify({ policy: policy.name, allow: result.allow, violations: result.violations, fromCache, cacheKey }),
  );
}
