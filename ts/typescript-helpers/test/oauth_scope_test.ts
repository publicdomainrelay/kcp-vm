import { assertEquals, assert } from "@std/assert";
import {
  OAUTH_SCOPE,
  REQUESTER_OAUTH_SCOPE,
  BIDDER_OAUTH_SCOPE,
  DESKTOP_OAUTH_SCOPE,
  COMPUTE_SPA_OAUTH_SCOPE,
  DID_KEY_ASSOCIATOR_OAUTH_SCOPE,
  dedupeScopes,
} from "@publicdomainrelay/oauth-scope";

const POLICY_GHA = "repo:computer.socialweb.temp.policy.ghalite?action=create";
const POLICY_TS = "repo:computer.socialweb.temp.policy.typescript?action=create";
const ASSOCIATE = "rpc:com.publicdomainrelay.temp.requester.associateConfirm?aud=*";

function isSubset(a: readonly string[], b: readonly string[]): boolean {
  const bs = new Set(b);
  return a.every((s) => bs.has(s));
}

Deno.test("requester scope is a subset of bidder scope", () => {
  assert(isSubset(REQUESTER_OAUTH_SCOPE, BIDDER_OAUTH_SCOPE));
});

Deno.test("every role set is a subset of the canonical union", () => {
  for (const set of [REQUESTER_OAUTH_SCOPE, BIDDER_OAUTH_SCOPE, DESKTOP_OAUTH_SCOPE, COMPUTE_SPA_OAUTH_SCOPE]) {
    assert(isSubset(set, OAUTH_SCOPE), `set not ⊆ OAUTH_SCOPE: ${set.join(" ")}`);
  }
});

Deno.test("union includes both policy scopes (requester + bidder mint them)", () => {
  for (const set of [OAUTH_SCOPE, REQUESTER_OAUTH_SCOPE, BIDDER_OAUTH_SCOPE]) {
    assert(set.includes(POLICY_GHA), `missing ${POLICY_GHA}`);
    assert(set.includes(POLICY_TS), `missing ${POLICY_TS}`);
  }
});

// The QR regression this refactor fixes: aliceoa's QR OAuth session was granted
// via did-key-associator's registered client, whose scope omitted the policy
// scopes → policy mint 403 → RFPs published without policies[]. The QR client
// must now cover everything a requester (and bidder) can request.
Deno.test("did-key-associator scope covers requester + bidder (QR client regression)", () => {
  assert(isSubset(REQUESTER_OAUTH_SCOPE, DID_KEY_ASSOCIATOR_OAUTH_SCOPE));
  assert(isSubset(BIDDER_OAUTH_SCOPE, DID_KEY_ASSOCIATOR_OAUTH_SCOPE));
  assert(DID_KEY_ASSOCIATOR_OAUTH_SCOPE.includes(ASSOCIATE));
});

Deno.test("desktop scope keeps rpc: tokens (drives includes(\"rpc:\") gates)", () => {
  assert(DESKTOP_OAUTH_SCOPE.some((s) => s.startsWith("rpc:")));
});

const SHAPE = /^(atproto|repo:[A-Za-z0-9.-]+\?action=[a-z]+(&action=[a-z]+)*|rpc:[A-Za-z0-9.-]+\?aud=\*)$/;

Deno.test("every token is in unified spec form", () => {
  for (const token of OAUTH_SCOPE) {
    assert(SHAPE.test(token), `non-unified scope token: ${token}`);
  }
  // No repo-form RPC scopes anywhere.
  assert(!OAUTH_SCOPE.some((s) => /^repo:.*(submitRfp|submitAccept|submitBid|submitEvent|associateConfirm)/.test(s)));
  // atproto always present and first.
  assertEquals(OAUTH_SCOPE[0], "atproto");
});

Deno.test("dedupeScopes dedups and preserves order", () => {
  assertEquals(dedupeScopes(["a", "b"], ["b", "c"], []), ["a", "b", "c"]);
});
