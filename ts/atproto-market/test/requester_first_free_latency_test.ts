import { assert, assertEquals } from "@std/assert";
import { createScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import type { ScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import { BIDS_FREE_NSID } from "@publicdomainrelay/market-lexicons";
import { RFP_NSID, SUBMIT_ACCEPT_NSID, VOUCH_NSID } from "@publicdomainrelay/market-common";
import type { CollectedBid, RequesterPDS } from "@publicdomainrelay/requester-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestOptions } from "@publicdomainrelay/compute-request-xrpc";
import { createScopeRefresher } from "@publicdomainrelay/scope-refresh-timers";

Deno.env.delete("ATPROTO_DID");

const REQUESTER_DID = "did:plc:requesterfake";
const BIDDER_DID = "did:plc:bidderone";
const BIDDER_ENDPOINT = "https://bidder.test";
const TICKET = "iroh://endpointfaketicket";
const UNREACHABLE_PLC = "http://127.0.0.1:9";
const POLICY = { name: "tangled-vouch", args: { bidWindowSec: 20, firstFree: true } };
const LIST_RECORDS_MS = 400;
const BID_AFTER_RFP_MS = 1500;
const CONTRACTS = 3;
const ACCEPT_BODY = {
  uri: "at://did:plc:bidder/com.publicdomainrelay.temp.market.receipt/1",
  cid: "bafyreceipt",
  submitEvent: BIDDER_ENDPOINT,
};

interface LoggedEvent {
  event: string;
  extra: Record<string, unknown>;
  at: number;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function recordingLogger(events: LoggedEvent[]): StructuredLoggerInterface {
  const push = (event: string, extra?: Record<string, unknown>) => {
    events.push({ event, extra: extra ?? {}, at: Date.now() });
  };
  return { info: push, warn: push, error: push, debug: push } as unknown as StructuredLoggerInterface;
}

async function waitForEvent(events: LoggedEvent[], event: string, timeoutMs = 30_000): Promise<LoggedEvent> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const found = events.find((e) => e.event === event);
    if (found) return found;
    await delay(25);
  }
  throw new Error(`timed out waiting for log event ${event}`);
}

function countingScopeCache() {
  const inner = createScopeCache();
  const counts = { runs: 0 };
  const cache: ScopeCache = {
    ...inner,
    set: (identity, did, args, result) => {
      counts.runs++;
      inner.set(identity, did, args, result);
    },
    once: (identity, did, args, work) =>
      inner.once(identity, did, args, () => {
        counts.runs++;
        return work();
      }),
  };
  return { cache, counts };
}

function slowNetworkPds(withOauthAgent: boolean) {
  const records: Array<{ uri: string; cid: string; record: Record<string, unknown> }> = [];
  const onNetwork = new Map<string, (address: string) => void>();
  const pendingBids = new Map<string, CollectedBid[]>();
  const timing = { bidVisibleAt: 0 };
  let seq = 0;

  const write = (collection: string, record: Record<string, unknown>) => {
    seq++;
    const entry = { uri: `at://${REQUESTER_DID}/${collection}/${seq}`, cid: `bafycid${seq}`, record };
    records.push(entry);
    if (collection === RFP_NSID) {
      const bidSeq = seq;
      setTimeout(() => {
        timing.bidVisibleAt = Date.now();
        pendingBids.set(entry.uri, [{
          did: BIDDER_DID,
          uri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.bid/${bidSeq}`,
          cid: `bafybid${bidSeq}`,
          record: {
            rfp: { uri: entry.uri, cid: entry.cid },
            payload: { $type: BIDS_FREE_NSID, cost: 0 },
            submitAccept: BIDDER_ENDPOINT,
          },
        }]);
      }, BID_AFTER_RFP_MS);
    }
    return Promise.resolve({ uri: entry.uri, cid: entry.cid });
  };

  const listRecords = async (repo: string, collection: string) => {
    await delay(LIST_RECORDS_MS);
    if (repo === REQUESTER_DID && collection === VOUCH_NSID) {
      return {
        records: [{
          uri: `at://${REQUESTER_DID}/${VOUCH_NSID}/${BIDDER_DID}`,
          value: { $type: VOUCH_NSID, kind: "vouch" },
        }],
      };
    }
    return { records: [] };
  };

  const pds = {
    did: REQUESTER_DID,
    privateKeyHex: "",
    relay: {
      ingressRef: "fake-ref",
      ingressUrl: "",
      ingressHost: "",
      close() {},
      onServe: () => Promise.resolve(),
    },
    relaySubdomain: "sub.relay.test",
    pendingBids,
    attestationKp: { did: () => "did:key:zfake", privateKey: { bytes: new Uint8Array() } },
    api: { listRecords },
    ...(withOauthAgent ? { oauthAgent: { listRecords } } : {}),
    createRepoRecord: write,
    createSignedRepoRecord: (collection: string, record: Record<string, unknown>) => write(collection, record),
    resolveBidderEndpoint: (endpoint: string) =>
      Promise.resolve(
        endpoint === BIDDER_ENDPOINT
          ? { targetUrl: `${BIDDER_ENDPOINT}/xrpc`, audDid: "did:web:bidder.test" }
          : null,
      ),
    callBidder: (_base: string, nsid: string) =>
      Promise.resolve(
        nsid === SUBMIT_ACCEPT_NSID
          ? { status: 200, ok: true, body: ACCEPT_BODY }
          : { status: 200, ok: true, body: {} },
      ),
    setOnNetworkResolved: (key: string, fn: (address: string) => void) => {
      onNetwork.set(key, fn);
    },
    clearOnNetworkResolved: (key: string) => {
      onNetwork.delete(key);
    },
  };

  return {
    pds: pds as unknown as RequesterPDS,
    timing,
    fireNetwork(address: string) {
      for (const fn of onNetwork.values()) fn(address);
    },
    resolveRecord: (ref: { uri: string; cid: string }) => {
      const found = records.find((r) => r.uri === ref.uri);
      if (!found) return Promise.reject(new Error(`no record ${ref.uri}`));
      return Promise.resolve(found.record);
    },
  };
}

async function contract(
  scope: { cache: ScopeCache; refresher: ReturnType<typeof createScopeRefresher> },
  withOauthAgent = false,
) {
  const fake = slowNetworkPds(withOauthAgent);
  const events: LoggedEvent[] = [];
  const opts: ComputeRequestOptions = {
    vmName: "compute-test",
    logger: recordingLogger(events),
    policy: POLICY,
    plcUrl: UNREACHABLE_PLC,
    extraBidderDids: [BIDDER_DID],
    vmReadyTimeoutSec: 10,
    sshPublicKey: "ssh-ed25519 AAAAfake test",
    recordResolver: fake.resolveRecord,
    receiptVerifier: () => Promise.resolve({ ok: true, sigOk: true, bindOk: true }),
    scopeCache: scope.cache,
    scopeRefresher: scope.refresher,
  };
  const handle = await requestCompute(fake.pds, opts, {
    onReceipt: () => {
      setTimeout(() => fake.fireNetwork(TICKET), 0);
      return "continue" as const;
    },
  });
  const prewarmDone = await waitForEvent(events, "scope_prewarm_done");
  return { handle, events, fake, prewarmDone };
}

function winnerLagMs(run: Awaited<ReturnType<typeof contract>>): number {
  const winner = run.events.find((e) => e.event === "first_free_winner");
  assert(winner, "the free bid won inside the window");
  const readyAt = Math.max(run.fake.timing.bidVisibleAt, run.prewarmDone.at);
  return winner.at - readyAt;
}

Deno.test("concurrent first-free contracts share one scope run and win as soon as it lands", async () => {
  const { cache, counts } = countingScopeCache();
  const refresher = createScopeRefresher({ scopeCache: cache, log: () => {} });
  try {
    const first = await Promise.all(
      Array.from({ length: CONTRACTS }, () => contract({ cache, refresher })),
    );
    for (const run of first) {
      assertEquals(run.handle.state.outcome, "complete");
      assertEquals(run.handle.state.winner?.did, BIDDER_DID);
      const lagMs = winnerLagMs(run);
      assert(
        lagMs < 300,
        `first_free_winner came ${lagMs}ms after both the bid and the prewarm were in`,
      );
    }
    assertEquals(
      counts.runs,
      1,
      `${CONTRACTS} concurrent contracts for one bidder ran the scope lane ${counts.runs} times`,
    );

    const second = await contract({ cache, refresher });
    assertEquals(second.handle.state.outcome, "complete");
    assertEquals(counts.runs, 1, "a contract inside the TTL ran no scope evaluation");
    const winner = second.events.find((e) => e.event === "first_free_winner");
    assert(winner, "the free bid won inside the window");
    const lagMs = winner.at - second.fake.timing.bidVisibleAt;
    assert(lagMs < 300, `a warm contract's first_free_winner came ${lagMs}ms after the bid`);
  } finally {
    refresher.close();
  }
});

Deno.test("a scope run reads the requester's own records from every source at once", async () => {
  const { cache, counts } = countingScopeCache();
  const refresher = createScopeRefresher({ scopeCache: cache, log: () => {} });
  try {
    const run = await contract({ cache, refresher }, true);
    assertEquals(run.handle.state.outcome, "complete");
    assertEquals(counts.runs, 1);
    const prewarmMs = Number(run.prewarmDone.extra.durationMs);
    assert(
      prewarmMs < 3 * LIST_RECORDS_MS,
      `a cold scope run took ${prewarmMs}ms over ${LIST_RECORDS_MS}ms reads; the oauth and own reads ran one after the other`,
    );
  } finally {
    refresher.close();
  }
});
