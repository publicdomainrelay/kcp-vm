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

import {
  createCacheStore,
  createPolicyCtx,
  runPolicy,
  type Policy,
  type PolicyArgs,
  type PolicyPerspective,
  type PolicyRecord,
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

export async function evaluatePolicyAction(policy: Policy): Promise<void> {
  const selfDid = input("self-did");

  // The engine does not enforce action.yml `required:`, so validate here.
  if (!selfDid) {
    console.error("::error::policy action: self-did is required (the evaluator's own DID)");
    Deno.exit(1);
  }

  const perspective = policyPerspective(policy);
  const store = createCacheStore();
  const ctx = await createPolicyCtx({
    policyName: policy.name,
    args: inputJson<PolicyArgs>("policy-args") ?? {},
    perspective,
    selfDid,
    rfp: inputJson<PolicyRecord>("rfp"),
    bid: inputJson<PolicyRecord>("bid"),
    accept: inputJson<PolicyRecord>("accept"),
    store,
  });

  const { result, fromCache, cacheKey } = await runPolicy(store, policy, ctx, {
    cacheKey: input("cache-key") || undefined,
  });

  const outputPath = Deno.env.get("GITHUB_OUTPUT");
  if (outputPath) {
    const lines = [
      `allow=${result.allow}`,
      `violations=${JSON.stringify(result.violations)}`,
      `from-cache=${fromCache}`,
      `cache-key=${cacheKey}`,
    ];
    for (const line of lines) {
      await Deno.writeTextFile(outputPath, `${line}\n`, { append: true });
    }
  }

  console.log(
    JSON.stringify({ policy: policy.name, allow: result.allow, violations: result.violations, fromCache, cacheKey }),
  );
}
