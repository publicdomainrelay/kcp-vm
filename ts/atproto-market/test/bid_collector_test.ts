import { assertEquals, assert } from "@std/assert";
import { BidCollector, selectWinner, bidCost, bidPayloadNsid } from "@publicdomainrelay/requester-abc";
import type { CollectedBid } from "@publicdomainrelay/requester-abc";
import { BIDS_FREE_NSID, BIDS_X402_NSID } from "@publicdomainrelay/market-lexicons";

function bid(opts: {
  uri: string;
  did?: string;
  payloadNsid?: string;
  cost?: number;
}): CollectedBid {
  const nsid = opts.payloadNsid ?? BIDS_X402_NSID;
  return {
    did: opts.did ?? "did:plc:bidder",
    uri: opts.uri,
    cid: "bafy",
    record: {
      payload: {
        $type: "com.atproto.repo.strongRef",
        uri: `at://did:plc:bidder/${nsid}/1`,
        cid: "bafy",
      },
      ...(opts.cost === undefined ? {} : { payloadCost: opts.cost }),
    },
  };
}

function costedBid(uri: string, cost: number): CollectedBid {
  return {
    did: "did:plc:bidder",
    uri,
    cid: "bafy",
    record: { payload: { cost } },
  };
}

const allowAll = () => Promise.resolve(true);

Deno.test("bidPayloadNsid reads the collection off a strongRef payload", () => {
  assertEquals(bidPayloadNsid(bid({ uri: "at://x/1", payloadNsid: BIDS_FREE_NSID })), BIDS_FREE_NSID);
  assertEquals(bidPayloadNsid(bid({ uri: "at://x/2" })), BIDS_X402_NSID);
});

Deno.test("bidPayloadNsid prefers an inline payload $type when present", () => {
  const inline: CollectedBid = {
    did: "did:plc:b",
    uri: "at://x/3",
    cid: "c",
    record: { payload: { $type: BIDS_FREE_NSID } },
  };
  assertEquals(bidPayloadNsid(inline), BIDS_FREE_NSID);
});

Deno.test("selectWinner picks the lowest cost and treats missing cost as infinite", () => {
  const bids = [costedBid("at://x/1", 10), costedBid("at://x/2", 3), costedBid("at://x/3", 7)];
  assertEquals(selectWinner(bids)?.uri, "at://x/2");

  const noCost = bid({ uri: "at://x/4" });
  assertEquals(bidCost(noCost), Infinity);
  assertEquals(selectWinner([noCost, costedBid("at://x/5", 99)])?.uri, "at://x/5");
});

Deno.test("selectWinner returns undefined for an empty set", () => {
  assertEquals(selectWinner([]), undefined);
});

Deno.test("collector dedupes bids by uri", () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: false, allow: allowAll });
  c.add(bid({ uri: "at://x/1" }));
  c.add(bid({ uri: "at://x/1" }));
  c.add(bid({ uri: "at://x/2" }));
  assertEquals(c.all().length, 2);
});

Deno.test("firstFree resolves earlyWinner on a policy-allowed free bid", async () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: true, allow: allowAll });
  c.add(bid({ uri: "at://x/paid", cost: 5 }));
  c.add(bid({ uri: "at://x/free", payloadNsid: BIDS_FREE_NSID }));
  const winner = await c.earlyWinner;
  assertEquals(winner.uri, "at://x/free");
});

Deno.test("firstFree ignores a free bid the policy rejects", async () => {
  const seen: string[] = [];
  const c = new BidCollector({
    freeBidNsid: BIDS_FREE_NSID,
    firstFree: true,
    allow: (b) => {
      seen.push(b.did);
      return Promise.resolve(b.did === "did:plc:good");
    },
  });

  c.add(bid({ uri: "at://x/bad", did: "did:plc:bad", payloadNsid: BIDS_FREE_NSID }));
  await c.drain();

  let settled = false;
  c.earlyWinner.then(() => { settled = true; });
  await new Promise((r) => setTimeout(r, 10));
  assertEquals(settled, false, "a rejected free bid must not win");

  c.add(bid({ uri: "at://x/good", did: "did:plc:good", payloadNsid: BIDS_FREE_NSID }));
  const winner = await c.earlyWinner;
  assertEquals(winner.uri, "at://x/good");
  assertEquals(seen, ["did:plc:bad", "did:plc:good"]);
});

Deno.test("firstFree never fires when no free bid arrives", async () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: true, allow: allowAll });
  c.addAll([bid({ uri: "at://x/1", cost: 1 }), bid({ uri: "at://x/2", cost: 2 })]);
  await c.drain();

  let settled = false;
  c.earlyWinner.then(() => { settled = true; });
  await new Promise((r) => setTimeout(r, 10));
  assertEquals(settled, false);
  assertEquals(c.all().length, 2);
});

Deno.test("firstFree off means a free bid does not short circuit", async () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: false, allow: allowAll });
  c.add(bid({ uri: "at://x/free", payloadNsid: BIDS_FREE_NSID }));
  await c.drain();

  let settled = false;
  c.earlyWinner.then(() => { settled = true; });
  await new Promise((r) => setTimeout(r, 10));
  assertEquals(settled, false);
});

Deno.test("only the first allowed free bid wins", async () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: true, allow: allowAll });
  const calls: string[] = [];
  const c2 = new BidCollector({
    freeBidNsid: BIDS_FREE_NSID,
    firstFree: true,
    allow: allowAll,
    onEarlyWinner: (b) => calls.push(b.uri),
  });
  c.add(bid({ uri: "at://x/free1", payloadNsid: BIDS_FREE_NSID }));
  assertEquals((await c.earlyWinner).uri, "at://x/free1");

  c2.add(bid({ uri: "at://x/a", payloadNsid: BIDS_FREE_NSID }));
  c2.add(bid({ uri: "at://x/b", payloadNsid: BIDS_FREE_NSID }));
  await c2.drain();
  assertEquals(calls, ["at://x/a"]);
});

Deno.test("collector races a bid window and wins early", async () => {
  const c = new BidCollector({ freeBidNsid: BIDS_FREE_NSID, firstFree: true, allow: allowAll });
  const started = performance.now();
  const window = new Promise<string>((r) => setTimeout(() => r("window"), 3000));

  setTimeout(() => c.add(bid({ uri: "at://x/free", payloadNsid: BIDS_FREE_NSID })), 20);

  const outcome = await Promise.race([window, c.earlyWinner.then(() => "early")]);
  const elapsed = performance.now() - started;

  assertEquals(outcome, "early");
  assert(elapsed < 2000, `expected an early exit, took ${elapsed}ms`);
});
