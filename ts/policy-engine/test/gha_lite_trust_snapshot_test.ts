import { assert, assertEquals } from "@std/assert";
import { POLICY_GHA_LITE_NSID } from "@publicdomainrelay/policy-common";
import { GhaLiteExecutor } from "../lib/policy-engine-executor-gha-lite/mod.ts";
import { WORKFLOWS } from "../lib/policies/gha-lite/mod.ts";

const SELF = "did:plc:req";
const BIDDER = "did:plc:bidder";

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

Deno.test("the gha-lite trust snapshot resolves its lookups concurrently, once per DID", async () => {
  const LATENCY_MS = 1000;
  const operatorLookups: string[] = [];
  const vouchLookups: string[] = [];
  const executor = new GhaLiteExecutor({ sandbox: { netOnly: false } });
  const name = "requester-tangled-vouch";
  const startedAt = Date.now();
  const verdict = await executor.scope({
    policyRecord: {
      uri: `at://${SELF}/policy-gha-lite/${name}`,
      cid: name,
      value: { $type: POLICY_GHA_LITE_NSID, name, workflow: WORKFLOWS[name] },
    },
    scope: {
      perspective: "requester",
      selfDid: SELF,
      counterpartyDid: BIDDER,
      args: {},
      resolveOperatorDid: async (did) => {
        operatorLookups.push(did);
        await delay(LATENCY_MS);
        return did === BIDDER ? "did:plc:bidderop" : null;
      },
      getVouchedDids: async (did) => {
        vouchLookups.push(did);
        await delay(LATENCY_MS);
        return new Set(did === SELF ? ["did:plc:bidderop", BIDDER] : []);
      },
    },
  });
  const elapsedMs = Date.now() - startedAt;
  assertEquals(verdict?.allow, true, JSON.stringify(verdict?.violations));
  assertEquals(operatorLookups.sort(), [BIDDER, SELF].sort());
  assertEquals(vouchLookups.sort(), ["did:plc:bidderop", SELF].sort(), "each DID's vouches were read once");
  assert(
    elapsedMs < 3 * LATENCY_MS,
    `trust snapshot took ${elapsedMs}ms; serial lookups would take ${5 * LATENCY_MS}ms`,
  );
});
