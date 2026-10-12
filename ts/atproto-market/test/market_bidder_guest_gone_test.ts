import { assert, assertEquals } from "@std/assert";
import { createVmBidderCallbacks } from "@publicdomainrelay/market-bidder-compute";
import type { ActiveContract, ContractEvent } from "@publicdomainrelay/market-bidder-abc";
import type { ComputeProvider, GuestGone } from "@publicdomainrelay/compute-provider-abc";

function providerThatLosesGuests(): { provider: ComputeProvider; lose: (gone: GuestGone) => void } {
  const listeners = new Set<(gone: GuestGone) => void>();
  const provider: ComputeProvider = {
    name: "fake",
    provision: () => Promise.reject(new Error("this test provisions nothing")),
    destroy: () => Promise.resolve(),
    createBidConfig: () => Promise.reject(new Error("this test bids nothing")),
    injectAcceptBundle: (userData: string) => userData,
    onGuestGone: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
  return { provider, lose: (gone) => listeners.forEach((l) => l(gone)) };
}

Deno.test("a guest that dies on its own releases its contract as terminated", async () => {
  const { provider, lose } = providerThatLosesGuests();
  const activeContracts = new Map<string, ActiveContract>([
    ["at://bidder/receipt/1#cid1", {
      providerIdPromise: Promise.resolve("fc-1379be663dd9-0319b646"),
      acceptAuthor: "did:plc:requester",
      receiptUri: "at://bidder/receipt/1",
      receiptCid: "cid1",
      acceptedAt: "2026-10-12T00:00:00.000Z",
    }],
    ["at://bidder/receipt/2#cid2", {
      providerIdPromise: Promise.resolve("fc-1379be663dd9-aaaaaaaa"),
      acceptAuthor: "did:plc:requester",
      receiptUri: "at://bidder/receipt/2",
      receiptCid: "cid2",
      acceptedAt: "2026-10-12T00:00:00.000Z",
    }],
  ]);
  const events: ContractEvent[] = [];
  const terminated = Promise.withResolvers<ContractEvent>();

  createVmBidderCallbacks({
    did: "did:plc:bidder",
    computeProvider: provider,
    log: () => {},
    activeContracts,
    onContractChange: (event: ContractEvent) => {
      events.push(event);
      if (event.type === "terminated") terminated.resolve(event);
    },
  } as never);

  lose({ id: "fc-1379be663dd9-0319b646", reason: "runner container exited", exitCode: 0 });
  const event = await terminated.promise;

  assertEquals(event.key, "at://bidder/receipt/1#cid1");
  assertEquals(event.providerId, "fc-1379be663dd9-0319b646");
  assert(event.terminatedAt, "a terminated contract carries when it ended");
  assert(!activeContracts.has("at://bidder/receipt/1#cid1"), "the dead guest's contract is still active");
  assert(activeContracts.has("at://bidder/receipt/2#cid2"), "a live guest's contract was released");
  assertEquals(events.length, 1);
});
