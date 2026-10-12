/**
 * Legacy-name aliasing + scopeDecide unit tests.
 * Run: deno test test/resolve_policy_name_test.ts
 */

import { assertEquals, assertThrows } from "@std/assert";
import { createPolicyRegistry } from "@publicdomainrelay/policy-deno-typescript";
import { resolvePolicyName, scopeDecide } from "@publicdomainrelay/policy-deno-typescript-shared";

const REG = createPolicyRegistry();

Deno.test("bidder passing only-me resolves to bidder-only-me", () => {
  assertEquals(resolvePolicyName(REG, "only-me", "bidder"), "bidder-only-me");
});

Deno.test("requester passing only-me resolves to requester-only-me", () => {
  assertEquals(resolvePolicyName(REG, "only-me", "requester"), "requester-only-me");
});

Deno.test("tangled-vouch and mutuals alias by side too", () => {
  assertEquals(resolvePolicyName(REG, "tangled-vouch", "bidder"), "bidder-tangled-vouch");
  assertEquals(resolvePolicyName(REG, "mutuals", "requester"), "requester-mutuals");
});

Deno.test("canonical names pass through when the side matches", () => {
  assertEquals(resolvePolicyName(REG, "bidder-only-me", "bidder"), "bidder-only-me");
  assertEquals(resolvePolicyName(REG, "requester-only-me", "requester"), "requester-only-me");
});

Deno.test("wrong-side canonical name throws — never silently runs the other side", () => {
  assertThrows(() => resolvePolicyName(REG, "requester-only-me", "bidder"));
  assertThrows(() => resolvePolicyName(REG, "bidder-only-me", "requester"));
});

Deno.test("open is symmetric", () => {
  assertEquals(resolvePolicyName(REG, "open", "bidder"), "open");
  assertEquals(resolvePolicyName(REG, "open", "requester"), "open");
});

Deno.test("work policies validate their declared side", () => {
  assertEquals(resolvePolicyName(REG, "under-4-cpus", "bidder"), "under-4-cpus");
  assertThrows(() => resolvePolicyName(REG, "under-4-cpus", "requester"));
  assertEquals(resolvePolicyName(REG, "bid-payload", "requester"), "bid-payload");
  assertThrows(() => resolvePolicyName(REG, "bid-payload", "bidder"));
});

Deno.test("unknown name throws", () => {
  assertThrows(() => resolvePolicyName(REG, "nope", "bidder"));
});

// ── scopeDecide ──────────────────────────────────────────────────────────────

Deno.test("scopeDecide allows a same-operator counterparty under only-me", async () => {
  const result = await scopeDecide({
    registry: REG,
    named: [{ name: "only-me", args: {} }],
    perspective: "bidder",
    selfDid: "did:plc:bidder",
    counterpartyDid: "did:plc:req",
    resolveOperatorDid: async (did) => (did === "did:plc:bidder" || did === "did:plc:req") ? "did:plc:op" : null,
    getVouchedDids: async () => new Set(),
  });
  assertEquals(result?.allow, true);
});

Deno.test("scopeDecide denies a different-operator counterparty under only-me", async () => {
  const result = await scopeDecide({
    registry: REG,
    named: [{ name: "only-me", args: {} }],
    perspective: "bidder",
    selfDid: "did:plc:bidder",
    counterpartyDid: "did:plc:req",
    resolveOperatorDid: async (did) =>
      did === "did:plc:bidder" ? "did:plc:op" : did === "did:plc:req" ? "did:plc:other" : null,
    getVouchedDids: async () => new Set(),
  });
  assertEquals(result?.allow, false);
});

Deno.test("scopeDecide allows a vouched counterparty under tangled-vouch", async () => {
  const result = await scopeDecide({
    registry: REG,
    named: [{ name: "bidder-tangled-vouch", args: {} }],
    perspective: "bidder",
    selfDid: "did:plc:bidder",
    counterpartyDid: "did:plc:req",
    resolveOperatorDid: async () => "did:plc:op",
    getVouchedDids: async (did) => new Set(did === "did:plc:bidder" ? ["did:plc:req"] : []),
  });
  assertEquals(result?.allow, true);
});

Deno.test("scopeDecide abstains (undefined) when the graph cannot decide", async () => {
  const result = await scopeDecide({
    registry: REG,
    named: [{ name: "only-me", args: {} }],
    perspective: "bidder",
    selfDid: "did:plc:bidder",
    counterpartyDid: "did:plc:req",
    resolveOperatorDid: async (did) => (did === "did:plc:bidder" ? "did:plc:op" : null),
    getVouchedDids: async () => new Set(),
  });
  assertEquals(result, undefined);
});
