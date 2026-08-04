/**
 * Evaluator unit tests — scope cache hit/miss, evaluatePolicies short-circuit.
 * Run: deno test test/evaluator_test.ts
 */

import { assertEquals } from "@std/assert";
import { createPolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import { createScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import { POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-common";
import type { PolicyEvalCtx, PolicyResult } from "@publicdomainrelay/policy-common";
import type { PolicyEngineExecutor } from "@publicdomainrelay/policy-engine-abc";

class CountingExecutor implements PolicyEngineExecutor {
  kind = POLICY_TYPESCRIPT_NSID;
  scopeCalls = 0;
  executeCalls = 0;
  deny = false;

  async execute(): Promise<PolicyResult> {
    this.executeCalls++;
    return this.deny ? { allow: false, violations: [{ msg: "denied", policyId: "t" }] } : { allow: true, violations: [] };
  }

  async scope(): Promise<PolicyResult | undefined> {
    this.scopeCalls++;
    return this.deny ? { allow: false, violations: [{ msg: "denied", policyId: "t" }] } : { allow: true, violations: [] };
  }
}

function baseCtx(): PolicyEvalCtx {
  return {
    policyName: "t",
    args: {},
    perspective: "bidder",
    selfDid: "did:plc:bidder",
    subjectDid: "did:plc:req",
    rootRequesterDid: "did:plc:req",
    counterpartyDid: "did:plc:req",
    resolve: async () => ({}),
    resolveOperatorDid: async () => null,
    getVouchedDids: async () => new Set(),
    log: () => {},
  };
}

const REF = { uri: "at://did:plc:req/com.publicdomainrelay.temp.policy.typescript/rec", cid: "cid" };

Deno.test("evaluator.scope runs the executor once, then serves the scope cache", async () => {
  const ex = new CountingExecutor();
  const cache = createScopeCache();
  const evaluator = createPolicyEvaluator({
    registry: { get: ($t) => ($t === POLICY_TYPESCRIPT_NSID ? ex : undefined), kinds: () => [POLICY_TYPESCRIPT_NSID] },
    resolve: async () => ({ $type: POLICY_TYPESCRIPT_NSID, name: "t", policies: [{ name: "only-me" }] }),
    scopeCache: cache,
  });

  const r1 = await evaluator.scope({ ref: REF, perspective: "bidder", selfDid: "did:plc:bidder", counterpartyDid: "did:plc:req", args: {} });
  const r2 = await evaluator.scope({ ref: REF, perspective: "bidder", selfDid: "did:plc:bidder", counterpartyDid: "did:plc:req", args: {} });
  assertEquals(r1.allow, true);
  assertEquals(r2.allow, true);
  assertEquals(ex.scopeCalls, 1, "second call must be a cache hit");
});

Deno.test("evaluator.scope without a cache runs every time", async () => {
  const ex = new CountingExecutor();
  const evaluator = createPolicyEvaluator({
    registry: { get: ($t) => ($t === POLICY_TYPESCRIPT_NSID ? ex : undefined), kinds: () => [POLICY_TYPESCRIPT_NSID] },
    resolve: async () => ({ $type: POLICY_TYPESCRIPT_NSID, name: "t" }),
  });
  await evaluator.scope({ ref: REF, perspective: "bidder", selfDid: "did:plc:bidder", counterpartyDid: "did:plc:req", args: {} });
  await evaluator.scope({ ref: REF, perspective: "bidder", selfDid: "did:plc:bidder", counterpartyDid: "did:plc:req", args: {} });
  assertEquals(ex.scopeCalls, 2);
});

Deno.test("evaluator.scope escalates to execute when the executor has no scope lane", async () => {
  const ex = new CountingExecutor();
  ex.scope = undefined as never; // remove the scope lane
  const evaluator = createPolicyEvaluator({
    registry: { get: ($t) => ($t === POLICY_TYPESCRIPT_NSID ? ex : undefined), kinds: () => [POLICY_TYPESCRIPT_NSID] },
    resolve: async () => ({ $type: POLICY_TYPESCRIPT_NSID, name: "t" }),
  });
  const r = await evaluator.scope({ ref: REF, perspective: "bidder", selfDid: "did:plc:bidder", counterpartyDid: "did:plc:req", args: {} });
  assertEquals(r.allow, true);
  assertEquals(ex.executeCalls, 1);
});

Deno.test("evaluatePolicies short-circuits on the first deny", async () => {
  const allowEx = new CountingExecutor();
  const denyEx = new CountingExecutor();
  denyEx.deny = true;
  const evaluator = createPolicyEvaluator({
    registry: {
      get: ($t) => ($t === "t1" ? allowEx : $t === "t2" ? denyEx : undefined),
      kinds: () => ["t1", "t2"],
    },
    resolve: async (ref) => ({ $type: ref.uri.includes("t1") ? "t1" : "t2" }),
  });

  const result = await evaluator.evaluatePolicies({
    refs: [
      { uri: "at://did:plc:req/t1/1", cid: "c1" },
      { uri: "at://did:plc:req/t2/2", cid: "c2" },
    ],
    ctx: baseCtx(),
  });
  assertEquals(result.allow, false);
  assertEquals(allowEx.executeCalls, 1);
  assertEquals(denyEx.executeCalls, 1);
});

Deno.test("evaluatePolicies denies an unknown record type", async () => {
  const evaluator = createPolicyEvaluator({
    registry: { get: () => undefined, kinds: () => [] },
    resolve: async () => ({ $type: "com.example.unknown" }),
  });
  const result = await evaluator.evaluatePolicies({ refs: [REF], ctx: baseCtx() });
  assertEquals(result.allow, false);
});
