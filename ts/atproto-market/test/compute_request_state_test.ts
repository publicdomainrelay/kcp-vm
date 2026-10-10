import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  advanceContract,
  createContractState,
  guestAddressKind,
  hasContract,
  InvalidContractStateError,
  parseContractState,
  receiptKeyOf,
  releaseTargetOf,
  ReturnLatch,
  serializeContractState,
} from "@publicdomainrelay/compute-request-abc";

const init = {
  vmName: "compute-ab12",
  requesterDid: "did:plc:req",
  marketDid: "did:plc:user",
  at: "2026-10-07T00:00:00.000Z",
};

Deno.test("a new contract state is requested, stamped, and has no contract", () => {
  const s = createContractState(init);
  assertEquals(s.phase, "requested");
  assertEquals(s.timestamps, { requested: init.at });
  assertEquals(hasContract(s), false);
  assertEquals(releaseTargetOf(s), null);
});

Deno.test("advancing returns a new state and leaves the old one untouched", () => {
  const s0 = createContractState(init);
  const s1 = advanceContract(
    s0,
    "accepted",
    { accept: { uri: "at://a/b/c", cid: "c1" } },
    "2026-10-07T00:00:01.000Z",
  );
  assertEquals(s0.phase, "requested");
  assertEquals(s0.timestamps, { requested: init.at });
  assertEquals(s1.phase, "accepted");
  assertEquals(s1.timestamps.accepted, "2026-10-07T00:00:01.000Z");
  assertEquals(hasContract(s1), true);
});

Deno.test("a contract with a receipt and a submitEvent target is releasable", () => {
  const s = advanceContract(createContractState(init), "receipt", {
    receipt: { uri: "at://bidder/receipt/1", cid: "r1" },
    submitEventRef: "https://bidder.test",
  }, init.at);
  assertEquals(releaseTargetOf(s), {
    receipt: { uri: "at://bidder/receipt/1", cid: "r1" },
    submitEventRef: "https://bidder.test",
  });
  assertEquals(receiptKeyOf(s.receipt!), "at://bidder/receipt/1#r1");
});

Deno.test("serialize then parse round-trips every field", () => {
  const s = advanceContract(createContractState(init), "network", {
    payload: { uri: "at://u/vm/1", cid: "p" },
    rfp: { uri: "at://u/rfp/1", cid: "r" },
    bid: { uri: "at://b/bid/1", cid: "b" },
    winner: { did: "did:plc:bidder", submitAccept: "https://bidder.test" },
    accept: { uri: "at://u/accept/1", cid: "a" },
    receipt: { uri: "at://b/receipt/1", cid: "rc" },
    receiptOk: true,
    submitEventRef: "https://bidder.test",
    vmAddress: "iroh://ticket",
    bids: 2,
  }, init.at);
  assertEquals(parseContractState(serializeContractState(s)), s);
  assertEquals(parseContractState(JSON.parse(serializeContractState(s))), s);
});

Deno.test("parse rejects what is not a contract state", () => {
  assertThrows(() => parseContractState("{"), InvalidContractStateError);
  assertThrows(() => parseContractState({}), InvalidContractStateError);
  const good = JSON.parse(serializeContractState(createContractState(init)));
  assertThrows(() => parseContractState({ ...good, phase: "flying" }), InvalidContractStateError);
  assertThrows(
    () => parseContractState({ ...good, receipt: { uri: "x" } }),
    InvalidContractStateError,
  );
  assertThrows(() => parseContractState({ ...good, version: 2 }), InvalidContractStateError);
});

Deno.test("guest addresses are told apart by their shape", () => {
  assertEquals(guestAddressKind("iroh://abc"), "iroh-ticket");
  assertEquals(guestAddressKind("vm--did-plc-x.relay.localhost"), "relay-fqdn");
  assertEquals(guestAddressKind("172.17.0.3"), "ip");
  assertEquals(guestAddressKind("172.17.0.3:22"), "ip");
});

Deno.test("the return latch fires once and remembers who asked", async () => {
  const latch = new ReturnLatch();
  assertEquals(latch.apply("continue", "onAccepted"), false);
  assertEquals(latch.apply(undefined, "onAccepted"), false);
  assertEquals(latch.apply("return", "onReceipt"), true);
  assertEquals(latch.apply("return", "onNetwork"), true);
  assertEquals(latch.reason, "onReceipt");
  await latch.returned;
  assert(latch.requested);
});
