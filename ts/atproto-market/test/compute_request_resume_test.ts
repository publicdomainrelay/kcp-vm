import { assert, assertEquals } from "@std/assert";
import {
  ACCEPT_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  COMPUTE_VM_NSID,
  EVENT_NSID,
  RFP_NSID,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_RFP_NSID,
} from "@publicdomainrelay/market-common";
import { attestationFor, loadOrGenerateKeypair, toStorableEntry } from "@publicdomainrelay/market-atproto";
import type { CollectedBid, RequesterPDS } from "@publicdomainrelay/requester-abc";
import type { GrantVars, GuestCapability, PrepareContext } from "@publicdomainrelay/guest-capability-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { parseContractState, serializeContractState } from "@publicdomainrelay/compute-request-abc";
import type { ComputeEventEnvelope, ContractState, RecordRef } from "@publicdomainrelay/compute-request-abc";
import { awaitComputeTermination, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestOptions } from "@publicdomainrelay/compute-request-xrpc";

Deno.env.delete("ATPROTO_DID");

const REQUESTER_DID = "did:plc:requesterfake";
const BIDDER_DID = "did:plc:bidder";
const BIDDER_ENDPOINT = "https://bidder.test";
const TICKET = "iroh://endpointfaketicket";
const RECEIPT = {
  uri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/1`,
  cid: "bafyreceipt",
  submitEvent: BIDDER_ENDPOINT,
};
const BID_CONFIG: RecordRef = { uri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.bidConfig/1`, cid: "bafycfg" };
const WIF = { issuer_uri: "https://bidder.test/oidc", actx: "a1" };

const bidderKp = await loadOrGenerateKeypair();
let envelopeSeq = 0;

async function signedTerminated(reason: string): Promise<ComputeEventEnvelope> {
  envelopeSeq++;
  const payloadRef = {
    $type: "com.atproto.repo.strongRef",
    uri: `at://${BIDDER_DID}/${COMPUTE_EVENTS_VM_TERMINATED_NSID}/p${envelopeSeq}`,
    cid: `bafyterminatedpayload${envelopeSeq}`,
  };
  const record: Record<string, unknown> = {
    $type: EVENT_NSID,
    receipt: { $type: "com.atproto.repo.strongRef", uri: RECEIPT.uri, cid: RECEIPT.cid },
    payload: payloadRef,
  };
  const entry = await attestationFor(bidderKp, BIDDER_DID).sign({ record, repository: BIDDER_DID });
  return {
    uri: `at://${BIDDER_DID}/${EVENT_NSID}/e${envelopeSeq}`,
    cid: `bafyterminatedevent${envelopeSeq}`,
    record: { ...record, signatures: [toStorableEntry(entry as never)] },
    payload: {
      $type: COMPUTE_EVENTS_VM_TERMINATED_NSID,
      reason,
      observedAt: "2026-10-12T00:57:03.000Z",
      createdAt: "2026-10-12T00:57:04.000Z",
    },
  };
}

function quietLogger(): StructuredLoggerInterface {
  const none = () => {};
  return { info: none, warn: none, error: none, debug: none } as unknown as StructuredLoggerInterface;
}

function bidFor(rfp: RecordRef, did = BIDDER_DID, i = 0): CollectedBid {
  return {
    did,
    uri: `at://${did}/com.publicdomainrelay.temp.market.bid/${i}`,
    cid: `bafybid${i}`,
    record: {
      rfp: { uri: rfp.uri, cid: rfp.cid },
      payload: { cost: 1 },
      submitAccept: BIDDER_ENDPOINT,
      config: { uri: BID_CONFIG.uri, cid: BID_CONFIG.cid },
    },
  };
}

function fakePds(opts: { bidders?: string[]; acceptBody?: Record<string, unknown> } = {}) {
  const calls: Array<{ nsid: string; body: Record<string, unknown> }> = [];
  const records: Array<{ collection: string; uri: string; cid: string; record: Record<string, unknown> }> = [];
  const onNetwork = new Map<string, (address: string) => void>();
  const onTerminated = new Map<string, (envelope: ComputeEventEnvelope) => void>();
  const pendingBids = new Map<string, CollectedBid[]>();
  let seq = 0;
  const write = (collection: string, record: Record<string, unknown>) => {
    seq++;
    const entry = { collection, uri: `at://${REQUESTER_DID}/${collection}/${seq}`, cid: `bafycid${seq}`, record };
    records.push(entry);
    if (collection === RFP_NSID) {
      pendingBids.set(entry.uri, (opts.bidders ?? []).map((did, i) => bidFor(entry, did, i)));
    }
    return Promise.resolve({ uri: entry.uri, cid: entry.cid });
  };
  const pds = {
    did: REQUESTER_DID,
    privateKeyHex: "",
    relay: { ingressRef: "fake-ref", ingressUrl: "", ingressHost: "", close() {}, onServe: () => Promise.resolve() },
    relaySubdomain: "sub.relay.test",
    pendingBids,
    attestationKp: { did: () => "did:key:zfake", privateKey: { bytes: new Uint8Array() } },
    api: { listRecords: () => Promise.resolve({ records: [] }) },
    createRepoRecord: write,
    createSignedRepoRecord: (collection: string, record: Record<string, unknown>) => write(collection, record),
    resolveBidderEndpoint: (endpoint: string) =>
      Promise.resolve(
        endpoint === BIDDER_ENDPOINT ? { targetUrl: `${BIDDER_ENDPOINT}/xrpc`, audDid: "did:web:bidder.test" } : null,
      ),
    callBidder: (_b: string, nsid: string, _l: string, _a: string, body: Record<string, unknown>) => {
      calls.push({ nsid, body });
      if (nsid === SUBMIT_ACCEPT_NSID) return Promise.resolve({ status: 200, ok: true, body: opts.acceptBody ?? RECEIPT });
      return Promise.resolve({ status: 200, ok: true, body: {} });
    },
    setOnNetworkResolved: (key: string, fn: (address: string) => void) => {
      onNetwork.set(key, fn);
    },
    clearOnNetworkResolved: (key: string) => {
      onNetwork.delete(key);
    },
    setOnTerminated: (receiptUri: string, fn: (envelope: ComputeEventEnvelope) => void) => {
      onTerminated.set(receiptUri, fn);
    },
    clearOnTerminated: (receiptUri: string) => {
      onTerminated.delete(receiptUri);
    },
  };
  return {
    pds: pds as unknown as RequesterPDS,
    calls,
    records,
    fireNetwork: (address: string) => onNetwork.forEach((fn) => fn(address)),
    networkKeys: () => [...onNetwork.keys()],
    fireTerminated: (envelope: ComputeEventEnvelope) => onTerminated.forEach((fn) => fn(envelope)),
    terminatedKeys: () => [...onTerminated.keys()],
    resolveRecord: (ref: { uri: string; cid: string }) => {
      if (ref.uri === BID_CONFIG.uri) return Promise.resolve(WIF as Record<string, unknown>);
      if (ref.uri.includes(".market.bid/")) {
        return Promise.resolve({ submitAccept: BIDDER_ENDPOINT, config: { uri: BID_CONFIG.uri, cid: BID_CONFIG.cid } });
      }
      const found = [...records, ...firstRecords].find((r) => r.uri === ref.uri);
      return found ? Promise.resolve(found.record) : Promise.reject(new Error(`no record ${ref.uri}`));
    },
    created: (collection: string) => records.filter((r) => r.collection === collection),
    called: (nsid: string) => calls.filter((c) => c.nsid === nsid),
  };
}

function fakeCapability() {
  const grants: GrantVars[] = [];
  let prepares = 0;
  let ctx: PrepareContext | undefined;
  const capability: GuestCapability = {
    id: "secrets",
    prepare(c) {
      prepares++;
      ctx = c;
      return Promise.resolve({ ctx: { secretsUrl: "https://did-key-stored.relay.test" } });
    },
    onContract(vars) {
      grants.push(vars);
    },
    dispose() {
      return Promise.resolve();
    },
  };
  return {
    capability,
    grants,
    prepares: () => prepares,
    serveGuest: () => ctx?.onGuestFetched?.({ capability: "secrets", subject: "sub-of-guest", count: 2 }),
  };
}

function baseOpts(fake: ReturnType<typeof fakePds>, extra: Partial<ComputeRequestOptions> = {}): ComputeRequestOptions {
  return {
    vmName: "node1",
    logger: quietLogger(),
    policy: { name: "open", args: { bidWindowSec: 0, firstFree: false } },
    vmReadyTimeoutSec: 30,
    recordResolver: fake.resolveRecord,
    receiptVerifier: () => Promise.resolve({ ok: true, sigOk: true, bindOk: true }),
    receiptFinder: () => Promise.resolve(null),
    ...extra,
  };
}

let firstRecords: ReturnType<typeof fakePds>["records"] = [];

async function savedAt(stop: "onRfpSubmitted" | "onAccepting" | "onReceipt"): Promise<ContractState> {
  const first = fakePds({ bidders: [BIDDER_DID] });
  firstRecords = first.records;
  let pending: ContractState | null = null;
  const handle = await requestCompute(first.pds, baseOpts(first), {
    onRfpSubmitted: () => stop === "onRfpSubmitted" ? "return" : "continue",
    onAccepting: (_e, s) => {
      pending = s;
      return stop === "onAccepting" ? "return" : "continue";
    },
    onReceipt: () => stop === "onReceipt" ? "return" : "continue",
  });
  await handle.dispose();
  const state = stop === "onAccepting" ? pending! : handle.state;
  return parseContractState(serializeContractState(state));
}

Deno.test("onAccepting runs before the accept record is written and the state it sees carries acceptPending", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const seen: Array<{ accepts: number; bidUri?: string; phase: string }> = [];
  const handle = await requestCompute(fake.pds, baseOpts(fake), {
    onAccepting: (e, s) => {
      seen.push({ accepts: fake.created(ACCEPT_NSID).length, bidUri: s.acceptPending?.bidUri, phase: s.phase });
      assertEquals(e.bid.uri, s.acceptPending?.bidUri);
      assertEquals(e.winnerDid, BIDDER_DID);
      return "continue";
    },
    onReceipt: () => "return",
  });
  assertEquals(seen, [{ accepts: 0, bidUri: bidFor({ uri: "", cid: "" }).uri, phase: "winner_selected" }]);
  assertEquals(fake.created(ACCEPT_NSID).length, 1);
  assertEquals(handle.state.acceptPending, undefined, "the write-ahead is cleared once the accept exists");
  await handle.dispose();
});

Deno.test("return at onAccepting writes no accept", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const handle = await requestCompute(fake.pds, baseOpts(fake), { onAccepting: () => "return" });
  assertEquals(fake.created(ACCEPT_NSID), []);
  assertEquals(fake.called(SUBMIT_ACCEPT_NSID), []);
  assertEquals(handle.state.outcome, "returned");
  await handle.dispose();
});

Deno.test("a request resumed after its RFP was posted posts no second RFP or vm record and accepts a bid found in the bidder's repo", async () => {
  const saved = await savedAt("onRfpSubmitted");
  assertEquals(saved.phase, "rfp_submitted");
  const fake = fakePds();
  const cap = fakeCapability();
  const asked: string[] = [];
  const order: string[] = [];
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, {
      resume: saved,
      capabilities: [cap.capability],
      extraBidderDids: [BIDDER_DID],
      bidFinder: (did, rfpUri) => {
        asked.push(`${did} ${rfpUri}`);
        return Promise.resolve([bidFor(saved.rfp!)]);
      },
    }),
    {
      onRfpSubmitted: () => {
        order.push("onRfpSubmitted");
        return "continue";
      },
      onAccepted: () => {
        order.push("onAccepted");
        return "continue";
      },
      onReceipt: () => {
        order.push("onReceipt");
        setTimeout(() => fake.fireNetwork(TICKET), 0);
        return "continue";
      },
    },
  );
  assertEquals(fake.created(RFP_NSID), [], "no second RFP");
  assertEquals(fake.created(COMPUTE_VM_NSID), [], "no second vm record");
  assertEquals(fake.called(SUBMIT_RFP_NSID), [], "the RFP is not submitted again");
  assertEquals(asked, [`${BIDDER_DID} ${saved.rfp!.uri}`]);
  assertEquals(fake.created(ACCEPT_NSID).length, 1);
  assertEquals((fake.created(ACCEPT_NSID)[0].record.rfp as RecordRef).uri, saved.rfp!.uri);
  assertEquals(order, ["onRfpSubmitted", "onAccepted", "onReceipt"]);
  assertEquals(cap.prepares(), 1, "the capability serves again");
  assertEquals(cap.grants.length, 1);
  assertEquals(handle.state.payload, saved.payload);
  assertEquals(handle.state.rfp, saved.rfp);
  assertEquals(handle.state.timestamps.rfp_submitted, saved.timestamps.rfp_submitted);
  assertEquals(handle.state.outcome, "complete");
  await handle.dispose();
});

Deno.test("a request resumed with a pending accept that reached the requester's repo writes no second accept and completes", async () => {
  const saved = await savedAt("onAccepting");
  assertEquals(saved.acceptPending?.bidUri, bidFor(saved.rfp!).uri);
  const fake = fakePds();
  const cap = fakeCapability();
  const accept: RecordRef = { uri: `at://${REQUESTER_DID}/${ACCEPT_NSID}/written-before-crash`, cid: "bafyaccept" };
  const looked: string[] = [];
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, {
      resume: saved,
      capabilities: [cap.capability],
      bidFinder: () => Promise.reject(new Error("no bid lookup once an accept exists")),
      acceptFinder: (rfpUri) => {
        looked.push(rfpUri);
        return Promise.resolve({ accept, bid: { uri: saved.bid!.uri, cid: saved.bid!.cid } });
      },
    }),
    { onReceipt: () => "return" },
  );
  assertEquals(looked, [saved.rfp!.uri]);
  assertEquals(fake.created(ACCEPT_NSID), [], "the accept is not written twice");
  assertEquals(fake.called(SUBMIT_ACCEPT_NSID).length, 1, "the accept is handed to the bidder, which is idempotent");
  assertEquals(fake.called(SUBMIT_ACCEPT_NSID)[0].body.acceptUri, accept.uri);
  assertEquals(handle.state.accept, accept);
  assertEquals(handle.state.acceptPending, undefined);
  assertEquals(handle.state.receipt, { uri: RECEIPT.uri, cid: RECEIPT.cid });
  assertEquals(cap.grants.map((g) => g.issuerUri), [WIF.issuer_uri], "the grant is rebuilt from the winning bid");
  await handle.dispose();
});

Deno.test("a request resumed with a pending accept that never reached the repo accepts exactly once", async () => {
  const saved = await savedAt("onAccepting");
  const fake = fakePds();
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, {
      resume: saved,
      extraBidderDids: [BIDDER_DID],
      bidFinder: () => Promise.resolve([bidFor(saved.rfp!)]),
      acceptFinder: () => Promise.resolve(null),
    }),
    { onReceipt: () => "return" },
  );
  assertEquals(fake.created(RFP_NSID), []);
  assertEquals(fake.created(ACCEPT_NSID).length, 1);
  assertEquals(handle.state.accept?.uri, fake.created(ACCEPT_NSID)[0].uri);
  await handle.dispose();
});

Deno.test("a request resumed after its receipt re-serves the guest, writes nothing and reaches onSecretsFetched", async () => {
  const saved = await savedAt("onReceipt");
  assertEquals(saved.phase, "receipt");
  const fake = fakePds();
  const cap = fakeCapability();
  const order: string[] = [];
  const handle = await requestCompute(
    fake.pds,
    baseOpts(fake, {
      resume: saved,
      capabilities: [cap.capability],
      bidFinder: () => Promise.reject(new Error("no bid lookup after a receipt")),
      acceptFinder: () => Promise.reject(new Error("no accept lookup after a receipt")),
    }),
    {
      onReceipt: () => {
        order.push("onReceipt");
        setTimeout(() => fake.fireNetwork(TICKET), 0);
        return "continue";
      },
      onNetwork: () => {
        order.push("onNetwork");
        setTimeout(() => cap.serveGuest(), 0);
        return "continue";
      },
      onSecretsFetched: () => {
        order.push("onSecretsFetched");
        return "return";
      },
    },
  );
  for (let i = 0; i < 100 && !order.includes("onSecretsFetched"); i++) await new Promise((r) => setTimeout(r, 5));
  assertEquals(fake.records, [], "a resumed contract writes no record");
  assertEquals(fake.calls, [], "and calls no bidder");
  assertEquals(cap.prepares(), 1);
  assertEquals(cap.grants.length, 1, "the guest's grant is installed again");
  assert(order.includes("onNetwork"));
  assertEquals(order.at(-1), "onSecretsFetched");
  assertEquals(handle.state.accept, saved.accept);
  assertEquals(handle.state.vmAddress, TICKET);
  assertEquals(handle.state.timestamps.accepted, saved.timestamps.accepted);
  await handle.dispose();
});

Deno.test("awaitComputeTermination reports the bidder's termination of a joined contract and stops on abort", async () => {
  const saved = await savedAt("onReceipt");
  const fake = fakePds();
  const ended = awaitComputeTermination(fake.pds as never, { ...saved, vmAddress: TICKET }, {
    logger: quietLogger(),
    backfill: false,
  });
  for (let i = 0; i < 100 && fake.terminatedKeys().length === 0; i++) await new Promise((r) => setTimeout(r, 5));
  assertEquals(fake.terminatedKeys(), [RECEIPT.uri]);
  fake.fireTerminated(await signedTerminated("host_reboot"));
  const state = await ended;
  assertEquals(state?.termination?.reason, "host_reboot");
  assertEquals(fake.terminatedKeys(), []);

  const other = fakePds();
  const abort = new AbortController();
  const waiting = awaitComputeTermination(other.pds as never, saved, { logger: quietLogger(), backfill: false, signal: abort.signal });
  abort.abort();
  assertEquals(await waiting, null);
  assertEquals(other.terminatedKeys(), []);
});
