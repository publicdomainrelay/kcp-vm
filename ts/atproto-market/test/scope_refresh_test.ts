import { assertEquals } from "@std/assert";
import { createScopeCache, scopeCacheKey } from "@publicdomainrelay/policy-engine-scope-cache";
import { createPolicyEvaluator } from "@publicdomainrelay/policy-engine-evaluator";
import {
  POLICY_GHA_LITE_NSID,
  type PolicyEngineExecutor,
} from "@publicdomainrelay/policy-engine-abc";
import type { PolicyResult } from "@publicdomainrelay/policy-common";
import { createCandidateScopeGate } from "@publicdomainrelay/compute-request-xrpc";
import { createScopeRefresher } from "@publicdomainrelay/scope-refresh-timers";
import type { ScopeRefresher, ScopeRefreshInput } from "@publicdomainrelay/scope-refresh-timers";

const REQUESTER_DID = "did:plc:requesterfake";
const BIDDER_DID = "did:plc:bidderone";
const POLICY_NAME = "open";
const SCOPE_URI = `at://${REQUESTER_DID}/policy-gha-lite/${POLICY_NAME}`;
const POLICY_ARGS = { bidWindowSec: 5, firstFree: true };
const IDENTITY = { kind: "ref" as const, uri: SCOPE_URI, cid: POLICY_NAME };
const KEY = scopeCacheKey(IDENTITY, BIDDER_DID, POLICY_ARGS);

const POLICY_RECORD = {
  uri: SCOPE_URI,
  cid: POLICY_NAME,
  value: { $type: POLICY_GHA_LITE_NSID, name: POLICY_NAME, workflow: "name: open policy" },
};

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitFor(claim: string, predicate: () => boolean, timeoutMs = 2000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await delay(5);
  }
  throw new Error(`timed out waiting for ${claim}`);
}

interface ArmedTimer {
  handle: number;
  fn: () => void;
  at: number;
}

function fakeTimers(startMs = 1_000_000) {
  let nowMs = startMs;
  const armed: ArmedTimer[] = [];
  let nextHandle = 1;
  return {
    now: () => nowMs,
    setTimer: (fn: () => void, ms: number) => {
      const handle = nextHandle++;
      armed.push({ handle, fn, at: nowMs + ms });
      return handle;
    },
    clearTimer: (handle: number) => {
      const index = armed.findIndex((t) => t.handle === handle);
      if (index >= 0) armed.splice(index, 1);
    },
    unrefTimer: (_handle: number) => {},
    setNow: (ms: number) => {
      nowMs = ms;
    },
    pending: () => armed.length,
    armedTimes: () => armed.map((t) => t.at),
    take: () => armed.slice(),
    fire(fn: () => void) {
      fn();
    },
    fireAllDue() {
      for (const timer of armed.slice()) {
        if (timer.at > nowMs) continue;
        const index = armed.indexOf(timer);
        if (index >= 0) armed.splice(index, 1);
        timer.fn();
      }
    },
  };
}

function countingExecutor(work: () => Promise<PolicyResult>) {
  const state = { executions: 0 };
  const executor: PolicyEngineExecutor = {
    kind: POLICY_GHA_LITE_NSID,
    execute: () => Promise.resolve({ allow: true, violations: [] }),
    scope: () => {
      state.executions++;
      return work();
    },
  };
  return { executor, state };
}

function harness(opts: {
  executor: PolicyEngineExecutor;
  cache: ReturnType<typeof createScopeCache>;
}) {
  const evaluator = createPolicyEvaluator({
    registry: { get: () => opts.executor, kinds: () => [POLICY_GHA_LITE_NSID] },
    resolve: () => Promise.resolve({ $type: POLICY_GHA_LITE_NSID, workflow: "name: open policy" }),
    scopeCache: opts.cache,
  });
  return evaluator;
}

function gateOf(evaluator: ReturnType<typeof harness>, refresher?: ScopeRefresher) {
  return createCandidateScopeGate({
    evaluator,
    selfDid: REQUESTER_DID,
    args: POLICY_ARGS,
    policyRecord: POLICY_RECORD,
    log: () => {},
    ...(refresher ? { refresher } : {}),
  });
}

function refresherOf(opts: {
  cache: ReturnType<typeof createScopeCache>;
  evaluator: ReturnType<typeof harness>;
  timers: ReturnType<typeof fakeTimers>;
  positiveTtlMs: number;
  refreshAtFraction?: number;
  idleTtlMs?: number;
}): ScopeRefresher {
  return createScopeRefresher({
    scopeCache: opts.cache,
    evaluator: opts.evaluator,
    positiveTtlMs: opts.positiveTtlMs,
    ...(opts.refreshAtFraction !== undefined ? { refreshAtFraction: opts.refreshAtFraction } : {}),
    ...(opts.idleTtlMs !== undefined ? { idleTtlMs: opts.idleTtlMs } : {}),
    now: opts.timers.now,
    setTimer: opts.timers.setTimer,
    clearTimer: opts.timers.clearTimer,
    unrefTimer: opts.timers.unrefTimer,
    log: () => {},
  });
}

Deno.test("a verdict a contract used is re-evaluated before its positive TTL ends", async () => {
  const { executor, state } = countingExecutor(() => Promise.resolve({ allow: true, violations: [] }));
  const cache = createScopeCache({ positiveTtlMs: 1200 });
  const evaluator = harness({ executor, cache });
  const refresher = createScopeRefresher({
    scopeCache: cache,
    evaluator,
    positiveTtlMs: 1200,
    refreshAtFraction: 0.8,
    log: () => {},
  });
  const gate = gateOf(evaluator, refresher);
  try {
    const first = await gate(BIDDER_DID);
    assertEquals(first.allow, true);
    assertEquals(state.executions, 1, "the contract ran the scope lane once");

    await delay(1000);
    assertEquals(state.executions, 2, "the refresher re-ran the scope lane before the positive TTL ended");

    await delay(450);
    const after = await gate(BIDDER_DID);
    assertEquals(after.allow, true);
    assertEquals(state.executions, 2, "the contract after the TTL found the verdict warm: 0 executions on its path");
  } finally {
    refresher.close();
  }
});

Deno.test("refresh stops for a counterparty idle past the idle TTL", async () => {
  const { executor, state } = countingExecutor(() => Promise.resolve({ allow: true, violations: [] }));
  const cache = createScopeCache({ positiveTtlMs: 10_000 });
  const evaluator = harness({ executor, cache });
  const timers = fakeTimers();
  const refresher = refresherOf({
    cache,
    evaluator,
    timers,
    positiveTtlMs: 10_000,
    refreshAtFraction: 0.8,
    idleTtlMs: 1_800_000,
  });
  const gate = gateOf(evaluator, refresher);

  const startedAt = timers.now();
  await gate(BIDDER_DID);
  assertEquals(state.executions, 1);
  assertEquals(refresher.stats().pending, 1, "the used verdict armed one refresh");
  assertEquals(timers.armedTimes(), [startedAt + 8000]);

  timers.setNow(startedAt + 8000);
  timers.fireAllDue();
  await waitFor("the first refresh", () => state.executions === 2);
  await delay(20);
  assertEquals(refresher.stats().entries, 1, "a used counterparty stays tracked");
  assertEquals(refresher.stats().pending, 1, "the refresh re-armed while the counterparty is not idle");

  timers.setNow(startedAt + 8000 + 1_800_001);
  timers.fireAllDue();
  await waitFor("the idle bound", () => refresher.stats().entries === 0);
  assertEquals(state.executions, 2);
  assertEquals(refresher.stats().pending, 0, "an idle counterparty leaves no pending refresh");

  timers.fireAllDue();
  await delay(20);
  assertEquals(state.executions, 2, "no unbounded background work after the idle bound");
});

Deno.test("a trust event drops the entry and its pending refresh together", async () => {
  const { executor, state } = countingExecutor(() => Promise.resolve({ allow: true, violations: [] }));
  const cache = createScopeCache({ positiveTtlMs: 10_000 });
  const evaluator = harness({ executor, cache });
  const timers = fakeTimers();
  const refresher = refresherOf({ cache, evaluator, timers, positiveTtlMs: 10_000 });
  const gate = gateOf(evaluator, refresher);

  await gate(BIDDER_DID);
  assertEquals(state.executions, 1);
  assertEquals(refresher.stats().entries, 1);
  const pendingRefresh = timers.take()[0];

  refresher.invalidate({ did: BIDDER_DID, rkey: "self" });
  assertEquals(refresher.stats().entries, 0, "the trust event dropped the tracked verdict");
  assertEquals(refresher.stats().pending, 0, "the trust event dropped the pending refresh");
  assertEquals(
    cache.get(IDENTITY, BIDDER_DID, POLICY_ARGS),
    undefined,
    "the trust event dropped the cached verdict",
  );

  timers.fire(pendingRefresh.fn);
  await delay(20);
  assertEquals(state.executions, 1, "the dropped refresh never re-ran the scope lane");
  assertEquals(
    cache.get(IDENTITY, BIDDER_DID, POLICY_ARGS),
    undefined,
    "the stale verdict was never re-served",
  );
});

Deno.test("a refresh that errors leaves no verdict", async () => {
  const { executor, state } = countingExecutor(() => {
    if (state.executions === 2) return Promise.reject(new Error("boom"));
    return Promise.resolve({ allow: true, violations: [] });
  });
  const cache = createScopeCache({ positiveTtlMs: 10_000 });
  const evaluator = harness({ executor, cache });
  const timers = fakeTimers();
  const refresher = refresherOf({ cache, evaluator, timers, positiveTtlMs: 10_000 });
  const gate = gateOf(evaluator, refresher);

  await gate(BIDDER_DID);
  assertEquals(state.executions, 1);
  timers.setNow(timers.now() + 8000);
  timers.fireAllDue();
  await waitFor("the failed refresh", () => refresher.stats().entries === 0);
  assertEquals(state.executions, 2, "the refresher ran the scope lane");
  assertEquals(refresher.stats().pending, 0);
  assertEquals(
    cache.get(IDENTITY, BIDDER_DID, POLICY_ARGS),
    undefined,
    "the failed refresh left no verdict",
  );

  const after = await gate(BIDDER_DID);
  assertEquals(state.executions, 3, "the next contract re-ran the lane instead of trusting a fabricated allow");
  assertEquals(after.allow, true);
  refresher.close();
});
