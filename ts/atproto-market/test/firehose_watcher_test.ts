// Firehose watcher impls (subscribeRepos + jetstream): record extraction,
// collection filtering, RelayFrame-vs-flat normalization, and parser parity.
//
// Each test runs a local ws server that emits frames on connect; the watcher
// connects via the global WebSocket (ws:// url, no interception needed) and
// pushes FirehoseRecordEvents into an array we assert on.
//
//   deno test --allow-all test/firehose_watcher_test.ts

import { assertEquals } from "@std/assert";
import { Hono } from "@hono/hono";
import { upgradeWebSocket } from "@hono/hono/deno";
import type { FirehoseRecordEvent } from "@publicdomainrelay/firehose-watcher-abc";
import { createFirehoseWatcher as createSubscribeReposWatcher } from "@publicdomainrelay/firehose-watcher-subscriberepos";
import { createFirehoseWatcher as createJetstreamWatcher } from "@publicdomainrelay/firehose-watcher-jetstream";

const RFP_NSID = "com.publicdomainrelay.temp.market.rfp";
const OTHER_NSID = "app.bsky.feed.post";
const DID = "did:plc:firehosetestrequester0";
const RKEY = "3kabcrfprkey00";
const CID = "bafyreirfpcidexample00000000000000000000000000000000000";

async function serveFrames(frames: unknown[]): Promise<{ port: number; stop: () => void }> {
  const app = new Hono();
  app.get(
    "/",
    upgradeWebSocket(() => ({
      onOpen(_evt, ws) {
        for (const f of frames) ws.send(JSON.stringify(f));
      },
    })),
  );
  const ctl = new AbortController();
  const { promise: portReady, resolve: resolvePort } = Promise.withResolvers<number>();
  Deno.serve({ port: 0, hostname: "127.0.0.1", signal: ctl.signal, onListen: (addr) => resolvePort((addr as Deno.NetAddr).port) }, app.fetch);
  const port = await portReady;
  return { port, stop: () => ctl.abort() };
}

async function collect(
  watch: (push: (e: FirehoseRecordEvent) => void) => { close(): void },
): Promise<FirehoseRecordEvent[]> {
  const events: FirehoseRecordEvent[] = [];
  const w = watch((e) => events.push(e));
  await new Promise((r) => setTimeout(r, 300));
  w.close();
  return events;
}

const subscribeReposEnvelope = (collection: string) => ({
  seq: 7,
  origin: "pds.localhost",
  frame: {
    seq: 7,
    repo: DID,
    rev: "rev7",
    since: null,
    blocks: [],
    ops: [{ action: "create", path: `${collection}/${RKEY}`, cid: { $link: CID }, prev: null }],
    time: new Date().toISOString(),
  },
  time: new Date().toISOString(),
});

const subscribeReposFlat = (collection: string) => ({
  seq: 7,
  repo: DID,
  rev: "rev7",
  since: null,
  blocks: [],
  ops: [{ action: "create", path: `${collection}/${RKEY}`, cid: { $link: CID }, prev: null }],
  time: new Date().toISOString(),
});

const jetstreamFrame = (collection: string) => ({
  did: DID,
  time_us: 1700000000000000,
  kind: "commit",
  commit: { collection, operation: "create", rkey: RKEY, cid: CID, record: {} },
});

const expected: FirehoseRecordEvent = {
  did: DID,
  collection: RFP_NSID,
  rkey: RKEY,
  cid: CID,
  operation: "create",
  uri: `at://${DID}/${RFP_NSID}/${RKEY}`,
};

Deno.test("subscribeRepos watcher: nested RelayFrame envelope -> event", async () => {
  const srv = await serveFrames([subscribeReposEnvelope(RFP_NSID)]);
  try {
    const events = await collect((push) =>
      createSubscribeReposWatcher({
        url: `ws://127.0.0.1:${srv.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    assertEquals(events, [expected]);
  } finally {
    srv.stop();
  }
});

Deno.test("subscribeRepos watcher: flat PdsFirehoseFrame -> event", async () => {
  const srv = await serveFrames([subscribeReposFlat(RFP_NSID)]);
  try {
    const events = await collect((push) =>
      createSubscribeReposWatcher({
        url: `ws://127.0.0.1:${srv.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    assertEquals(events, [expected]);
  } finally {
    srv.stop();
  }
});

Deno.test("subscribeRepos watcher: filters non-wanted collections", async () => {
  const srv = await serveFrames([subscribeReposEnvelope(OTHER_NSID), subscribeReposEnvelope(RFP_NSID)]);
  try {
    const events = await collect((push) =>
      createSubscribeReposWatcher({
        url: `ws://127.0.0.1:${srv.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    assertEquals(events, [expected]);
  } finally {
    srv.stop();
  }
});

Deno.test("jetstream watcher: commit frame -> event", async () => {
  const srv = await serveFrames([jetstreamFrame(OTHER_NSID), jetstreamFrame(RFP_NSID)]);
  try {
    const events = await collect((push) =>
      createJetstreamWatcher({
        url: `ws://127.0.0.1:${srv.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    assertEquals(events, [expected]);
  } finally {
    srv.stop();
  }
});

Deno.test("parser parity: subscribeRepos and jetstream yield identical events", async () => {
  const a = await serveFrames([subscribeReposEnvelope(RFP_NSID)]);
  const b = await serveFrames([jetstreamFrame(RFP_NSID)]);
  try {
    const subEvents = await collect((push) =>
      createSubscribeReposWatcher({
        url: `ws://127.0.0.1:${a.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    const jetEvents = await collect((push) =>
      createJetstreamWatcher({
        url: `ws://127.0.0.1:${b.port}/`,
        wantedCollections: [RFP_NSID],
        onRecord: push,
      })
    );
    assertEquals(subEvents, jetEvents);
  } finally {
    a.stop();
    b.stop();
  }
});
