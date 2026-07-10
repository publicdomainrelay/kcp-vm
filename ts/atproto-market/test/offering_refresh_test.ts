// Offering refresh: createMarketBidder periodically re-commits its offering
// record, keeping createdAt stable while bumping refreshedAt, so the offering
// stays live in relay/collectiondir indexes.
//
//   deno test --allow-all test/offering_refresh_test.ts

import { assert, assertEquals } from "@std/assert";
import { Hono } from "@hono/hono";
import { createLogger } from "@publicdomainrelay/logger";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { ATProto } from "@publicdomainrelay/atproto-helpers";
import type { ServeHandle } from "@publicdomainrelay/serve";

const OFFERING_NSID = "com.publicdomainrelay.temp.market.offering";
const BIDDER_DID = "did:plc:offeringrefreshbidder0";

interface CapturedWrite {
  action: string;
  collection: string;
  rkey: string;
  record?: Record<string, unknown>;
}

function fakeServe(): ServeHandle {
  const onConnectedCbs: Array<(ingressRef: string) => void | Promise<void>> = [];
  return {
    app: new Hono(),
    tcpPort: 0,
    addRelay: () => {},
    onConnected: (cb) => { onConnectedCbs.push(cb as () => void | Promise<void>); },
    beginServe: async () => {
      for (const cb of onConnectedCbs) await cb("did:web:test.localhost");
    },
    shutdown: () => {},
  };
}

function fakeAtproto(writes: CapturedWrite[]): ATProto {
  const noop = () => Promise.resolve(undefined as never);
  let rkeySeq = 0;
  const nextRkey = () => `rkey${rkeySeq++}`;
  return {
    did: BIDDER_DID,
    getAgentDid: () => BIDDER_DID,
    signer: { did: () => BIDDER_DID, sign: () => Promise.resolve(new Uint8Array()) },
    attestationKp: {} as never,
    idResolver: { did: { resolve: () => Promise.resolve(null) } } as never,
    plcClient: {} as never,
    listRecords: () => Promise.resolve({ records: [] }),
    getRecord: () => Promise.resolve(null),
    applyWrites: (_did: string, ws: Array<{ action: string; collection: string; rkey: string; record?: unknown }>) => {
      for (const w of ws) {
        writes.push({ action: w.action, collection: w.collection, rkey: w.rkey, record: w.record as Record<string, unknown> });
      }
      return Promise.resolve({} as never);
    },
    createRecord: (collection: string, record: Record<string, unknown>) => {
      const rkey = nextRkey();
      writes.push({ action: "create", collection, rkey, record });
      return Promise.resolve({ $type: "com.atproto.repo.strongRef", uri: `at://${BIDDER_DID}/${collection}/${rkey}`, cid: "" } as never);
    },
    updateRecord: (collection: string, rkey: string, record: Record<string, unknown>) => {
      writes.push({ action: "update", collection, rkey, record });
      return Promise.resolve({ $type: "com.atproto.repo.strongRef", uri: `at://${BIDDER_DID}/${collection}/${rkey}`, cid: "" } as never);
    },
    createRepoRecord: noop,
    createSignedRepoRecord: noop,
    deleteRecord: noop,
    callService: noop,
  } as unknown as ATProto;
}

Deno.test({
  name: "offering refresh: createdAt stable, refreshedAt bumped across re-commits",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const writes: CapturedWrite[] = [];
  const bidder = await createMarketBidder({
    logger: createLogger({ serviceName: "offering-refresh-test" }),
    serve: fakeServe(),
    atproto: fakeAtproto(writes),
    offeringRefreshMs: 60,
  });

  await bidder.beginServe();
  await new Promise((r) => setTimeout(r, 260));
  bidder.shutdown();

  const offeringWrites = writes.filter((w) => w.collection === OFFERING_NSID);
  assert(offeringWrites.length >= 2, `expected >= 2 offering writes, got ${offeringWrites.length}`);

  const [first, ...rest] = offeringWrites;
  assertEquals(first.action, "create", "first offering write is a create");
  assert(typeof first.record?.createdAt === "string", "create carries createdAt");
  assert(typeof first.record?.refreshedAt === "string", "create carries refreshedAt");

  const createdAt = first.record!.createdAt as string;
  for (const w of rest) {
    assertEquals(w.action, "update", "subsequent offering writes are updates");
    assertEquals(w.rkey, first.rkey, "updates target the same offering rkey");
    assertEquals(w.record?.createdAt, createdAt, "createdAt stays stable across refreshes");
  }

  const refreshedAts = new Set(offeringWrites.map((w) => w.record?.refreshedAt as string));
  assert(refreshedAts.size > 1, "refreshedAt is bumped across re-commits");

  // refreshedAt never precedes createdAt (ISO strings sort lexicographically).
  for (const w of offeringWrites) {
    assert((w.record!.refreshedAt as string) >= createdAt, "refreshedAt >= createdAt");
  }
});
