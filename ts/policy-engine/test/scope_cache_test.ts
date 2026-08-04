/**
 * scope-cache unit tests — hit/miss/TTL, per-DID invalidation, LRU bound.
 * Run: deno test test/scope_cache_test.ts
 */

import { assertEquals } from "@std/assert";
import { createScopeCache, scopeCacheKey, type PolicyIdentity } from "@publicdomainrelay/policy-engine-scope-cache";

const ID_NAME: PolicyIdentity = { kind: "name", name: "bidder-only-me" };
const ID_REF: PolicyIdentity = { kind: "ref", uri: "at://did:plc:req/com.example.policy/rec", cid: "bafyreia" };

const ALLOW = { allow: true, violations: [] };
const DENY = { allow: false, violations: [{ msg: "denied", policyId: "bidder-only-me" }] };

Deno.test("scope cache hits after set, misses before", () => {
  const c = createScopeCache();
  assertEquals(c.get(ID_NAME, "did:plc:bidder", {}), undefined);
  c.set(ID_NAME, "did:plc:bidder", {}, ALLOW);
  assertEquals(c.get(ID_NAME, "did:plc:bidder", {}), ALLOW);
  // Different counterparty → miss.
  assertEquals(c.get(ID_NAME, "did:plc:other", {}), undefined);
  // Different args → miss.
  c.set(ID_NAME, "did:plc:bidder", { maxCpus: 4 }, DENY);
  assertEquals(c.get(ID_NAME, "did:plc:bidder", { maxCpus: 4 }), DENY);
  assertEquals(c.get(ID_NAME, "did:plc:bidder", {}), ALLOW);
});

Deno.test("scope cache invalidation drops a counterparty on a trust event", () => {
  const c = createScopeCache();
  c.set(ID_NAME, "did:plc:bidder", {}, ALLOW);
  c.set(ID_NAME, "did:plc:other", {}, DENY);
  c.applyEvent({ did: "did:plc:bidder", rkey: "did:plc:vouchee" });
  assertEquals(c.get(ID_NAME, "did:plc:bidder", {}), undefined);
  assertEquals(c.get(ID_NAME, "did:plc:other", {}), DENY);
});

Deno.test("scope cache invalidation drops the vouchee rkey side too", () => {
  const c = createScopeCache();
  c.set(ID_REF, "did:plc:vouchee", {}, ALLOW);
  c.applyEvent({ did: "did:plc:voucher", rkey: "did:plc:vouchee" });
  assertEquals(c.get(ID_REF, "did:plc:vouchee", {}), undefined);
});

Deno.test("negative verdicts expire faster than positives", async () => {
  const c = createScopeCache({ positiveTtlMs: 60_000, negativeTtlMs: 20 });
  c.set(ID_NAME, "did:plc:a", {}, DENY);
  c.set(ID_NAME, "did:plc:b", {}, ALLOW);
  await new Promise((r) => setTimeout(r, 40));
  assertEquals(c.get(ID_NAME, "did:plc:a", {}), undefined); // negative expired
  assertEquals(c.get(ID_NAME, "did:plc:b", {}), ALLOW);      // positive still live
});

Deno.test("scope cache is LRU-bounded", () => {
  const c = createScopeCache({ maxEntries: 3 });
  for (let i = 0; i < 5; i++) c.set(ID_NAME, `did:plc:${i}`, {}, ALLOW);
  assertEquals(c.get(ID_NAME, "did:plc:0", {}), undefined); // evicted
  assertEquals(c.get(ID_NAME, "did:plc:4", {}), ALLOW);
  assertEquals(c.stats().entries, 3);
});

Deno.test("scopeCacheKey is stable and distinct", () => {
  const a = scopeCacheKey(ID_NAME, "did:plc:x", {});
  const b = scopeCacheKey(ID_NAME, "did:plc:x", {});
  const c = scopeCacheKey(ID_NAME, "did:plc:x", { bidWindowSec: 30 });
  const d = scopeCacheKey(ID_REF, "did:plc:x", {});
  assertEquals(a, b);
  assertNotEquals(a, c);
  assertNotEquals(a, d);
});

function assertNotEquals(a: string, b: string): void {
  if (a === b) throw new Error(`expected not equal: ${a}`);
}
