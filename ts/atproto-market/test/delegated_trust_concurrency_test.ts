import { assert, assertEquals } from "@std/assert";
import { createBadgeBlueKeysDelegatedTrustResolver } from "@publicdomainrelay/delegated-trust-badge-blue-keys";
import { BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";

const SELF = "did:plc:requester";
const OPERATOR_A = "did:plc:operatora";
const OPERATOR_B = "did:plc:operatorb";
const LATENCY_MS = 200;

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

Deno.test("delegated trust reads its own vouches, its badges and each operator's vouches concurrently", async () => {
  const vouches: Record<string, string[]> = {
    [SELF]: ["did:plc:direct"],
    [OPERATOR_A]: ["did:plc:viaa"],
    [OPERATOR_B]: ["did:plc:viab"],
  };
  const resolver = createBadgeBlueKeysDelegatedTrustResolver({
    vouchResolver: {
      getVouchedDids: async (did) => {
        await delay(LATENCY_MS);
        return new Set(vouches[did] ?? []);
      },
      isVouched: () => Promise.resolve(false),
    },
    listOwnRecords: async (collection) => {
      await delay(LATENCY_MS);
      assertEquals(collection, BADGE_BLUE_KEYS_NSID);
      return [
        { uri: "at://x/b/1", value: { service: "requester_associate", challenge: OPERATOR_A, keyId: SELF } },
        { uri: "at://x/b/2", value: { service: "requester_associate", challenge: OPERATOR_B, keyId: SELF } },
      ];
    },
  });

  const startedAt = Date.now();
  const trusted = await resolver.getDelegatedTrustedDids(SELF);
  const elapsedMs = Date.now() - startedAt;

  assertEquals([...trusted].sort(), ["did:plc:direct", "did:plc:viaa", "did:plc:viab"]);
  assert(
    elapsedMs < 3 * LATENCY_MS,
    `delegated trust took ${elapsedMs}ms; serial reads would take ${4 * LATENCY_MS}ms`,
  );
});

Deno.test("one operator whose vouches fail still leaves the others trusted", async () => {
  const resolver = createBadgeBlueKeysDelegatedTrustResolver({
    vouchResolver: {
      getVouchedDids: (did) =>
        did === OPERATOR_A
          ? Promise.reject(new Error("offline"))
          : Promise.resolve(new Set(did === OPERATOR_B ? ["did:plc:viab"] : [])),
      isVouched: () => Promise.resolve(false),
    },
    listOwnRecords: () =>
      Promise.resolve([
        { uri: "at://x/b/1", value: { service: "requester_associate", challenge: OPERATOR_A, keyId: SELF } },
        { uri: "at://x/b/2", value: { service: "requester_associate", challenge: OPERATOR_B, keyId: SELF } },
      ]),
  });
  assertEquals([...await resolver.getDelegatedTrustedDids(SELF)], ["did:plc:viab"]);
});
