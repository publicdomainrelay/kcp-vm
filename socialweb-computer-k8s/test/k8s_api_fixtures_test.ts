import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  ApiValidationError,
  CONTRACT_PHASES,
  FINALIZER_NODE_CLAIM,
  isNodeClaimPhase,
  NODE_CLAIM_PHASES,
  NODE_CLAIM_RESOURCE,
  parseNodeClaim,
  parseNodeClaimStatus,
  validateNodeClaim,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

const FIXTURES = new URL("../../testdata/nodeclaim/", import.meta.url);

interface InvalidFixture {
  expectError: string;
  goDecodeRejects: boolean;
  object: unknown;
}

async function fixtures(prefix: string): Promise<{ name: string; value: unknown }[]> {
  const out: { name: string; value: unknown }[] = [];
  for await (const entry of Deno.readDir(FIXTURES)) {
    if (!entry.isFile || !entry.name.startsWith(prefix) || !entry.name.endsWith(".json")) continue;
    out.push({ name: entry.name, value: JSON.parse(await Deno.readTextFile(new URL(entry.name, FIXTURES))) });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

Deno.test("every valid NodeClaim fixture the Go suite round-trips is accepted unchanged", async () => {
  const valid = await fixtures("valid-");
  assert(valid.length >= 4, `expected the shared valid fixtures, found ${valid.length}`);
  for (const { name, value } of valid) {
    assertEquals(validateNodeClaim(value), [], name);
    assertEquals(parseNodeClaim(structuredClone(value)), value, name);
  }
});

Deno.test("every invalid NodeClaim fixture is rejected at the path the Go validator names", async () => {
  const invalid = await fixtures("invalid-");
  assert(invalid.length >= 8, `expected the shared invalid fixtures, found ${invalid.length}`);
  for (const { name, value } of invalid) {
    const fixture = value as InvalidFixture;
    const issues = validateNodeClaim(fixture.object);
    assert(
      issues.some((i) => i.path === fixture.expectError),
      `${name}: expected an issue at ${fixture.expectError}, got ${JSON.stringify(issues)}`,
    );
    const err = assertThrows(() => parseNodeClaim(fixture.object), ApiValidationError);
    assert(err.issues.some((i) => i.path === fixture.expectError), name);
  }
});

Deno.test("the phase enum and the finalizer are the Go constants", () => {
  assertEquals([...NODE_CLAIM_PHASES], ["Pending", "Bidding", "Provisioning", "Joining", "Running", "Failed", "Released"]);
  assertEquals([...CONTRACT_PHASES], ["requested", "rfp_submitted", "winner_selected", "accepted", "receipt", "network", "released"]);
  assertEquals(FINALIZER_NODE_CLAIM, "nodeclaim.socialweb.computer/release");
  assert(isNodeClaimPhase("Running"));
  assert(!isNodeClaimPhase("running"));
  assertEquals(NODE_CLAIM_RESOURCE.plural, "nodeclaims");
  assertEquals(NODE_CLAIM_RESOURCE.group, "socialweb.computer");
  assertEquals(NODE_CLAIM_RESOURCE.version, "v1alpha1");
});

Deno.test("a status the door is about to write is checked before it leaves the process", () => {
  assertEquals(parseNodeClaimStatus({ phase: "Bidding", contract: {} }), { phase: "Bidding", contract: {} });
  const err = assertThrows(() => parseNodeClaimStatus({ phase: "Bidding", contract: { bids: -0.5 } }), ApiValidationError);
  assertEquals(err.issues.map((i) => i.path), ["contract.bids"]);
  assertThrows(() => parseNodeClaimStatus({ phase: "Bidding", contract: { vmUri: null } }), ApiValidationError);
  assertThrows(() => parseNodeClaimStatus([]), ApiValidationError);
});
