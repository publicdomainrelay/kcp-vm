import { assertEquals } from "@std/assert";
import { withDeadline } from "@publicdomainrelay/requester-xrpc";

Deno.test("work that answers in time is returned as it is", async () => {
  const out = await withDeadline("did:plc:a", 5_000, Promise.resolve("bid"), () => {
    throw new Error("the deadline fired over work that answered");
  });
  assertEquals(out, "bid");
});

Deno.test("work that never answers loses its place instead of holding the auction", async () => {
  const expired: string[] = [];
  const started = Date.now();
  // A bidder whose endpoint resolution or RFP POST never returns is the shape
  // that stalled every contract on 2026-10-07: the fan-out waited on it while
  // bidders that DID answer had their bids sitting unread.
  const out = await withDeadline("did:plc:silent", 120, new Promise<never>(() => {}), (what, ms) => {
    expired.push(`${what}:${ms}`);
  });
  assertEquals(out, undefined);
  assertEquals(expired, ["did:plc:silent:120"]);
  assertEquals(Date.now() - started < 5_000, true);
});

Deno.test("work that rejects still rejects, so a caller's own catch keeps working", async () => {
  let caught = "";
  try {
    await withDeadline("did:plc:bad", 5_000, Promise.reject(new Error("pds refused")), () => {});
  } catch (err) {
    caught = String(err);
  }
  assertEquals(caught.includes("pds refused"), true);
});
