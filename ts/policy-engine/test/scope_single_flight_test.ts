import { assert, assertEquals } from "@std/assert";
import { createPolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import { createScopeCache, type PolicyIdentity } from "@publicdomainrelay/policy-engine-scope-cache";
import { POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-common";
import type { PolicyResult } from "@publicdomainrelay/policy-common";
import type { PolicyEngineExecutor } from "@publicdomainrelay/policy-engine-abc";

const REF = { uri: "at://did:plc:req/com.publicdomainrelay.temp.policy.typescript/rec", cid: "cid" };
const IDENTITY: PolicyIdentity = { kind: "ref", uri: REF.uri, cid: REF.cid };
const SELF = "did:plc:req";
const BIDDER = "did:plc:bidder";
const ALLOW: PolicyResult = { allow: true, violations: [] };

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

class SlowExecutor implements PolicyEngineExecutor {
  kind = POLICY_TYPESCRIPT_NSID;
  scopeCalls = 0;
  constructor(readonly ms: number, readonly verdict: () => PolicyResult = () => ALLOW) {}

  execute(): Promise<PolicyResult> {
    return Promise.resolve(this.verdict());
  }

  async scope(): Promise<PolicyResult | undefined> {
    this.scopeCalls++;
    await delay(this.ms);
    return this.verdict();
  }
}

function evaluatorOver(ex: PolicyEngineExecutor, cache: ReturnType<typeof createScopeCache>) {
  return createPolicyEvaluator({
    registry: { get: ($t) => ($t === POLICY_TYPESCRIPT_NSID ? ex : undefined), kinds: () => [POLICY_TYPESCRIPT_NSID] },
    resolve: () => Promise.resolve({ $type: POLICY_TYPESCRIPT_NSID, name: "t" }),
    scopeCache: cache,
  });
}

function ask(evaluator: ReturnType<typeof createPolicyEvaluator>, extra: { refresh?: boolean } = {}) {
  return evaluator.scope({
    ref: REF,
    perspective: "requester",
    selfDid: SELF,
    counterpartyDid: BIDDER,
    args: { firstFree: true },
    ...extra,
  });
}

Deno.test("concurrent scope asks for one counterparty share one run across evaluators", async () => {
  const ex = new SlowExecutor(300);
  const cache = createScopeCache();
  const evaluators = [evaluatorOver(ex, cache), evaluatorOver(ex, cache), evaluatorOver(ex, cache)];
  const verdicts = await Promise.all([...evaluators.map((e) => ask(e)), ...evaluators.map((e) => ask(e))]);
  for (const v of verdicts) assertEquals(v.allow, true);
  assertEquals(ex.scopeCalls, 1, "six concurrent asks ran the scope lane once");
  assertEquals(cache.stats().entries, 1);
  assertEquals(cache.stats().inflight, 0, "the settled run left no in-flight entry");
});

Deno.test("an ask that arrives mid-run waits only for the rest of that run", async () => {
  const ex = new SlowExecutor(600);
  const cache = createScopeCache();
  const prewarm = ask(evaluatorOver(ex, cache));
  await delay(400);
  const startedAt = Date.now();
  const verdict = await ask(evaluatorOver(ex, cache));
  const waitedMs = Date.now() - startedAt;
  await prewarm;
  assertEquals(verdict.allow, true);
  assertEquals(ex.scopeCalls, 1);
  assert(waitedMs < 450, `the late ask joined the running lane and waited ${waitedMs}ms`);
});

Deno.test("a trust event during a run keeps that run's verdict out of the cache", async () => {
  const ex = new SlowExecutor(200);
  const cache = createScopeCache();
  const evaluator = evaluatorOver(ex, cache);
  const before = ask(evaluator);
  await delay(50);
  cache.applyEvent({ did: BIDDER, rkey: "" });
  assertEquals(cache.stats().inflight, 0, "the trust event dropped the in-flight run");
  const after = ask(evaluator);
  await Promise.all([before, after]);
  assertEquals(ex.scopeCalls, 2, "the ask after the event did not join the stale run");
  assertEquals(cache.get(IDENTITY, BIDDER, { firstFree: true })?.allow, true);
});

Deno.test("a stale run that settles after the event never overwrites the fresh verdict", async () => {
  let calls = 0;
  const ex: PolicyEngineExecutor = {
    kind: POLICY_TYPESCRIPT_NSID,
    execute: () => Promise.resolve(ALLOW),
    scope: async () => {
      calls++;
      const mine = calls;
      await delay(mine === 1 ? 300 : 50);
      return mine === 1 ? { allow: false, violations: [{ msg: "stale", policyId: "t" }] } : ALLOW;
    },
  };
  const cache = createScopeCache();
  const evaluator = evaluatorOver(ex, cache);
  const stale = ask(evaluator);
  await delay(20);
  cache.applyEvent({ did: BIDDER, rkey: "" });
  assertEquals((await ask(evaluator)).allow, true);
  assertEquals((await stale).allow, false);
  assertEquals(cache.get(IDENTITY, BIDDER, { firstFree: true })?.allow, true);
});

Deno.test("refresh re-runs the lane while every other ask keeps the cached verdict", async () => {
  const ex = new SlowExecutor(300);
  const cache = createScopeCache();
  const evaluator = evaluatorOver(ex, cache);
  await ask(evaluator);
  assertEquals(ex.scopeCalls, 1);

  const refreshing = ask(evaluator, { refresh: true });
  await delay(20);
  const startedAt = Date.now();
  const served = await ask(evaluator);
  assert(Date.now() - startedAt < 50, "a plain ask during a refresh was served from the cache");
  assertEquals(served.allow, true);
  await refreshing;
  assertEquals(ex.scopeCalls, 2, "refresh ran the lane again");
});

Deno.test("a policy that fails to resolve is not cached and does not stick in flight", async () => {
  let resolves = 0;
  const ex = new SlowExecutor(10);
  const cache = createScopeCache();
  const evaluator = createPolicyEvaluator({
    registry: { get: () => ex, kinds: () => [POLICY_TYPESCRIPT_NSID] },
    resolve: () => {
      resolves++;
      return Promise.reject(new Error("offline"));
    },
    scopeCache: cache,
  });
  assertEquals((await ask(evaluator)).allow, false);
  assertEquals((await ask(evaluator)).allow, false);
  assertEquals(resolves, 2, "the resolve failure was retried, not served from the cache");
  assertEquals(cache.stats().entries, 0);
  assertEquals(cache.stats().inflight, 0);
});

Deno.test("an executor that throws releases the flight so the next ask runs again", async () => {
  let calls = 0;
  const ex: PolicyEngineExecutor = {
    kind: POLICY_TYPESCRIPT_NSID,
    execute: () => Promise.resolve(ALLOW),
    scope: () => {
      calls++;
      return calls === 1 ? Promise.reject(new Error("boom")) : Promise.resolve(ALLOW);
    },
  };
  const cache = createScopeCache();
  const evaluator = evaluatorOver(ex, cache);
  let threw = false;
  try {
    await ask(evaluator);
  } catch {
    threw = true;
  }
  assert(threw, "the executor error reached the caller");
  assertEquals(cache.stats().inflight, 0);
  assertEquals((await ask(evaluator)).allow, true);
  assertEquals(calls, 2);
});
