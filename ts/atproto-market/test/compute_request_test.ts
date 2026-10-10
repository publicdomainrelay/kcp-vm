import { assert, assertEquals } from "@std/assert";
import {
  ACCEPT_NSID,
  COMPUTE_EVENTS_VM_DELETE_NSID,
  EVENT_NSID,
  RFP_NSID,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_EVENT_NSID,
} from "@publicdomainrelay/market-common";
import type { CollectedBid, RequesterPDS } from "@publicdomainrelay/requester-abc";
import type { GuestCapability, PrepareContext } from "@publicdomainrelay/guest-capability-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { parseContractState, serializeContractState } from "@publicdomainrelay/compute-request-abc";
import type { ComputeRequestHandlers, ContractState } from "@publicdomainrelay/compute-request-abc";
import {
  awaitComputeNetwork,
  releaseCompute,
  requestCompute,
} from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestOptions } from "@publicdomainrelay/compute-request-xrpc";

Deno.env.delete("ATPROTO_DID");

const REQUESTER_DID = "did:plc:requesterfake";
const BIDDER_ENDPOINT = "https://bidder.test";
const TICKET = "iroh://endpointfaketicket";

interface BidSpec {
  did: string;
  cost: number;
}

interface FakeCall {
  nsid: string;
  body: Record<string, unknown>;
}

interface FakeRecord {
  collection: string;
  uri: string;
  cid: string;
  record: Record<string, unknown>;
}

function quietLogger(events: string[] = []): StructuredLoggerInterface {
  const push = (event: string) => {
    events.push(event);
  };
  return {
    info: push,
    warn: push,
    error: push,
    debug: push,
  } as unknown as StructuredLoggerInterface;
}

function fakePds(bidSpecs: BidSpec[] = []) {
  const calls: FakeCall[] = [];
  const records: FakeRecord[] = [];
  const onNetwork = new Map<string, (address: string) => void>();
  const pendingBids = new Map<string, CollectedBid[]>();
  let seq = 0;

  const write = (collection: string, record: Record<string, unknown>) => {
    seq++;
    const entry = {
      collection,
      uri: `at://${REQUESTER_DID}/${collection}/${seq}`,
      cid: `bafycid${seq}`,
      record,
    };
    records.push(entry);
    if (collection === RFP_NSID) {
      pendingBids.set(
        entry.uri,
        bidSpecs.map((b, i) => ({
          did: b.did,
          uri: `at://${b.did}/com.publicdomainrelay.temp.market.bid/${i}`,
          cid: `bafybid${i}`,
          record: {
            rfp: { uri: entry.uri, cid: entry.cid },
            payload: { cost: b.cost },
            submitAccept: BIDDER_ENDPOINT,
          },
        })),
      );
    }
    return Promise.resolve({ uri: entry.uri, cid: entry.cid });
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
    api: { listRecords: () => Promise.resolve({ records: [] }) },
    createRepoRecord: write,
    createSignedRepoRecord: (collection: string, record: Record<string, unknown>) =>
      write(collection, record),
    resolveBidderEndpoint: (endpoint: string) =>
      Promise.resolve(
        endpoint === BIDDER_ENDPOINT
          ? { targetUrl: `${BIDDER_ENDPOINT}/xrpc`, audDid: "did:web:bidder.test" }
          : null,
      ),
    callBidder: (
      _base: string,
      nsid: string,
      _lxm: string,
      _aud: string,
      body: Record<string, unknown>,
    ) => {
      calls.push({ nsid, body });
      if (nsid === SUBMIT_ACCEPT_NSID) {
        return Promise.resolve({
          status: 200,
          ok: true,
          body: {
            uri: "at://did:plc:bidder/com.publicdomainrelay.temp.market.receipt/1",
            cid: "bafyreceipt",
            submitEvent: BIDDER_ENDPOINT,
          },
        });
      }
      return Promise.resolve({ status: 200, ok: true, body: {} });
    },
    setOnNetworkResolved: (key: string, fn: (address: string) => void) => {
      onNetwork.set(key, fn);
    },
    clearOnNetworkResolved: (key: string) => {
      onNetwork.delete(key);
    },
  };

  return {
    pds: pds as unknown as RequesterPDS,
    calls,
    records,
    fireNetwork(address: string) {
      for (const fn of onNetwork.values()) fn(address);
    },
    registeredNetworkKeys: () => [...onNetwork.keys()],
    resolveRecord: (ref: { uri: string; cid: string }) => {
      const found = records.find((r) => r.uri === ref.uri);
      if (!found) return Promise.reject(new Error(`no record ${ref.uri}`));
      return Promise.resolve(found.record);
    },
  };
}

function baseOpts(
  fake: ReturnType<typeof fakePds>,
  extra: Partial<ComputeRequestOptions> = {},
): ComputeRequestOptions {
  return {
    vmName: "compute-test",
    logger: quietLogger(),
    policy: { name: "open", args: { bidWindowSec: 0, firstFree: false } },
    vmReadyTimeoutSec: 10,
    sshPublicKey: "ssh-ed25519 AAAAfake test",
    recordResolver: fake.resolveRecord,
    receiptVerifier: () => Promise.resolve({ ok: true, sigOk: true, bindOk: true }),
    ...extra,
  };
}

function recordingHandlers(
  order: string[],
  over: ComputeRequestHandlers = {},
  fake?: ReturnType<typeof fakePds>,
): ComputeRequestHandlers {
  return {
    onRfpSubmitted: (e, s) => {
      order.push("onRfpSubmitted");
      return over.onRfpSubmitted?.(e, s) ?? "continue";
    },
    onBid: (e, s) => {
      order.push(`onBid:${e.bid.did}`);
      return over.onBid?.(e, s) ?? "accept";
    },
    onAccepted: (e, s) => {
      order.push("onAccepted");
      return over.onAccepted?.(e, s) ?? "continue";
    },
    onReceipt: (e, s) => {
      order.push("onReceipt");
      if (fake) setTimeout(() => fake.fireNetwork(TICKET), 0);
      return over.onReceipt?.(e, s) ?? "continue";
    },
    onNetwork: (e, s) => {
      order.push("onNetwork");
      return over.onNetwork?.(e, s) ?? "continue";
    },
    onSecretsFetched: (e, s) => {
      order.push("onSecretsFetched");
      return over.onSecretsFetched?.(e, s) ?? "continue";
    },
  };
}

function fakeCapability() {
  const counts = { prepare: 0, revoke: 0, dispose: 0 };
  let ctx: PrepareContext | undefined;
  const capability: GuestCapability = {
    id: "secrets",
    prepare(c) {
      counts.prepare++;
      ctx = c;
      return Promise.resolve({});
    },
    onRevoke() {
      counts.revoke++;
    },
    dispose() {
      counts.dispose++;
      return Promise.resolve();
    },
  };
  return {
    capability,
    counts,
    serveGuest: () =>
      ctx?.onGuestFetched?.({ capability: "secrets", subject: "sub-of-guest", count: 2 }),
  };
}

Deno.test("events fire in contract order and the flow completes at onNetwork", async () => {
  const fake = fakePds([{ did: "did:plc:bidderone", cost: 3 }, {
    did: "did:plc:biddertwo",
    cost: 1,
  }]);
  const order: string[] = [];
  const handle = await requestCompute(fake.pds, baseOpts(fake), recordingHandlers(order, {}, fake));

  assertEquals(order, [
    "onRfpSubmitted",
    "onBid:did:plc:bidderone",
    "onBid:did:plc:biddertwo",
    "onAccepted",
    "onReceipt",
    "onNetwork",
  ]);
  assertEquals(handle.state.outcome, "complete");
  assertEquals(handle.state.phase, "network");
  assertEquals(handle.state.vmAddress, TICKET);
  assertEquals(handle.state.winner?.did, "did:plc:biddertwo");
  assertEquals(fake.registeredNetworkKeys(), []);
  for (
    const phase of [
      "requested",
      "rfp_submitted",
      "winner_selected",
      "accepted",
      "receipt",
      "network",
    ] as const
  ) {
    assert(handle.state.timestamps[phase], `timestamp for ${phase}`);
  }
});

Deno.test("return at onRfpSubmitted stops before any bid is weighed or accepted", async () => {
  const fake = fakePds([{ did: "did:plc:bidderone", cost: 1 }]);
  const cap = fakeCapability();
  const order: string[] = [];
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, { capabilities: [cap.capability] }),
    recordingHandlers(order, { onRfpSubmitted: () => "return" }, fake),
  );

  assertEquals(order, ["onRfpSubmitted"]);
  assertEquals(handle.state.outcome, "returned");
  assertEquals(handle.state.phase, "rfp_submitted");
  assertEquals(handle.state.accept, undefined);
  assertEquals(fake.records.filter((r) => r.collection === ACCEPT_NSID), []);
  assertEquals(fake.calls.filter((c) => c.nsid === SUBMIT_ACCEPT_NSID), []);
  assertEquals(
    cap.counts.dispose,
    1,
    "no contract formed, so the core disposes capabilities itself",
  );
  assertEquals(handle.capabilities, []);
});

Deno.test("return at onNetwork yields a ContractState with a receipt and submits no vm.delete", async () => {
  const fake = fakePds([{ did: "did:plc:bidderone", cost: 1 }]);
  const cap = fakeCapability();
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, { capabilities: [cap.capability] }),
    recordingHandlers([], { onNetwork: () => "return" }, fake),
  );

  assertEquals(handle.state.outcome, "returned");
  assertEquals(handle.state.phase, "network");
  assertEquals(handle.state.vmAddress, TICKET);
  assertEquals(handle.state.receipt, {
    uri: "at://did:plc:bidder/com.publicdomainrelay.temp.market.receipt/1",
    cid: "bafyreceipt",
  });
  assertEquals(handle.state.receiptOk, true);
  assertEquals(handle.state.submitEventRef, BIDDER_ENDPOINT);
  assertEquals(fake.calls.map((c) => c.nsid), [SUBMIT_ACCEPT_NSID]);
  assertEquals(fake.records.filter((r) => r.collection === COMPUTE_EVENTS_VM_DELETE_NSID), []);
  assertEquals(cap.counts, { prepare: 1, revoke: 0, dispose: 0 });
  assertEquals(handle.capabilities, [cap.capability]);
  await handle.dispose();
  await handle.dispose();
  assertEquals(cap.counts.dispose, 1);
});

Deno.test("releaseCompute from a JSON round-tripped state submits vm.delete from another process", async () => {
  const first = fakePds([{ did: "did:plc:bidderone", cost: 1 }]);
  const handle = await requestCompute(
    first.pds,
    baseOpts(first),
    recordingHandlers([], { onReceipt: () => "return" }),
  );
  const saved = serializeContractState(handle.state);
  await handle.dispose();
  assertEquals(first.calls.filter((c) => c.nsid === SUBMIT_EVENT_NSID), []);

  const restored = parseContractState(JSON.parse(JSON.stringify(JSON.parse(saved))));
  const second = fakePds();
  const cap = fakeCapability();
  const released = await releaseCompute(second.pds, restored, {
    logger: quietLogger(),
    capabilities: [cap.capability],
  });

  const submitted = second.calls.filter((c) => c.nsid === SUBMIT_EVENT_NSID);
  assertEquals(submitted.length, 1);
  const deletion = second.records.find((r) => r.collection === COMPUTE_EVENTS_VM_DELETE_NSID);
  assert(deletion, "a vm.delete record was written");
  assertEquals(deletion.record.reason, "session_ended");
  const wrapped = submitted[0].body.record as Record<string, Record<string, string>>;
  assertEquals(wrapped.$type as unknown as string, EVENT_NSID);
  assertEquals(wrapped.receipt.uri, restored.receipt?.uri);
  assertEquals(wrapped.receipt.cid, restored.receipt?.cid);
  assertEquals(wrapped.payload.uri, deletion.uri);
  assertEquals(released.phase, "released");
  assertEquals(released.release?.ok, true);
  assertEquals(released.release?.payload.uri, deletion.uri);
  assertEquals(cap.counts.revoke, 1);
});

Deno.test("releaseCompute without receipt refs submits nothing", async () => {
  const fake = fakePds();
  const state = parseContractState({
    version: 1,
    phase: "accepted",
    vmName: "compute-x",
    requesterDid: REQUESTER_DID,
    marketDid: REQUESTER_DID,
    accept: { uri: "at://x/y/z", cid: "c" },
    timestamps: {},
  });
  const after = await releaseCompute(fake.pds, state, { logger: quietLogger() });
  assertEquals(fake.calls, []);
  assertEquals(after, state);
});

Deno.test("a bid denied by onBid is never accepted, even when it is the cheapest", async () => {
  const fake = fakePds([{ did: "did:plc:cheap", cost: 1 }, { did: "did:plc:dear", cost: 9 }]);
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake),
    recordingHandlers(
      [],
      { onBid: (e) => e.bid.did === "did:plc:cheap" ? "deny" : "accept" },
      fake,
    ),
  );

  assertEquals(handle.state.winner?.did, "did:plc:dear");
  const accepts = fake.records.filter((r) => r.collection === ACCEPT_NSID);
  assertEquals(accepts.length, 1);
  const acceptedBid = accepts[0].record.bid as { uri: string };
  assert(acceptedBid.uri.startsWith("at://did:plc:dear/"), `accepted ${acceptedBid.uri}`);
  await handle.dispose();
});

Deno.test("when onBid denies every bid there is no accept and no contract", async () => {
  const fake = fakePds([{ did: "did:plc:cheap", cost: 1 }, { did: "did:plc:dear", cost: 9 }]);
  const cap = fakeCapability();
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, { capabilities: [cap.capability] }),
    recordingHandlers([], { onBid: () => "deny" }, fake),
  );

  assertEquals(handle.state.outcome, "bids_denied");
  assertEquals(handle.state.bids, 2);
  assertEquals(fake.records.filter((r) => r.collection === ACCEPT_NSID), []);
  assertEquals(fake.calls.filter((c) => c.nsid === SUBMIT_ACCEPT_NSID), []);
  assertEquals(cap.counts.dispose, 1);
});

Deno.test("onSecretsFetched fires when the capability serves the guest, and return ends the wait", async () => {
  const fake = fakePds([{ did: "did:plc:bidderone", cost: 1 }]);
  const cap = fakeCapability();
  const order: string[] = [];
  const started = Date.now();
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, { capabilities: [cap.capability], vmReadyTimeoutSec: 30 }),
    recordingHandlers(order, {
      onReceipt: () => {
        setTimeout(() => cap.serveGuest(), 0);
        return "continue";
      },
      onSecretsFetched: (e) => {
        assertEquals(e, { capability: "secrets", subject: "sub-of-guest", count: 2 });
        return "return";
      },
    }),
  );

  assert(Date.now() - started < 10_000, "the network wait ended early");
  assertEquals(order.slice(-2), ["onReceipt", "onSecretsFetched"]);
  assertEquals(handle.state.outcome, "returned");
  assertEquals(handle.state.phase, "receipt");
  assertEquals(handle.state.vmAddress, undefined);
  assertEquals(handle.capabilities, [cap.capability]);
  assertEquals(cap.counts.dispose, 0);
  await handle.dispose();
});

Deno.test("awaitComputeNetwork resumes the onNetwork wait from a saved state", async () => {
  const first = fakePds([{ did: "did:plc:bidderone", cost: 1 }]);
  const handle = await requestCompute(
    first.pds,
    baseOpts(first),
    recordingHandlers([], { onReceipt: () => "return" }),
  );
  await handle.dispose();
  const saved: ContractState = parseContractState(serializeContractState(handle.state));
  assertEquals(saved.phase, "receipt");
  assertEquals(first.registeredNetworkKeys(), []);

  const second = fakePds();
  const seen: string[] = [];
  const waiting = awaitComputeNetwork(second.pds as never, saved, {
    logger: quietLogger(),
    timeoutMs: 10_000,
    backfill: false,
    onNetwork: (e) => {
      seen.push(e.address);
      return "continue";
    },
  });
  await new Promise((r) => setTimeout(r, 0));
  assertEquals(second.registeredNetworkKeys(), [`${saved.receipt!.uri}#${saved.receipt!.cid}`]);
  second.fireNetwork("10.0.0.5");
  second.fireNetwork(TICKET);
  const resumed = await waiting;

  assertEquals(seen, [TICKET]);
  assertEquals(resumed.phase, "network");
  assertEquals(resumed.vmAddress, TICKET);
  assertEquals(second.registeredNetworkKeys(), []);
});
