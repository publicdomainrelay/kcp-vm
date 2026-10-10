import { assert, assertEquals, assertStringIncludes } from "@std/assert";
import {
  claimContractOf,
  contractStateOfClaim,
  createMarketNodeFulfiller,
  type NodeSessionPlan,
} from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import type { NodeClaim, NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { K3S_JOIN_PATH } from "@publicdomainrelay/socialweb-computer-k8s-common";
import { FINALIZER_NODE_CLAIM, type NodeClaimStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import {
  advanceContract,
  type ComputeRequestHandlers,
  type ContractState,
  createContractState,
  type FlowDecision,
  patchContract,
  type RecordRef,
} from "@publicdomainrelay/compute-request-abc";
import { COMPUTE_EVENTS_VM_DELETE_NSID, EVENT_NSID, SUBMIT_EVENT_NSID } from "@publicdomainrelay/market-lexicons";

const OWNER = "did:plc:nodeowner";
const BIDDER = "did:plc:bidder";
const CA = "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n";
const AT = "2026-10-07T00:00:00.000Z";

const VM: RecordRef = { uri: `at://${OWNER}/compute.vm/1`, cid: "vmcid" };
const RFP: RecordRef = { uri: `at://${OWNER}/market.rfp/1`, cid: "rfpcid" };
const BID: RecordRef = { uri: `at://${BIDDER}/market.bid/1`, cid: "bidcid" };
const ACCEPT: RecordRef = { uri: `at://${OWNER}/market.accept/1`, cid: "acceptcid" };
const RECEIPT: RecordRef = { uri: `at://${BIDDER}/market.receipt/1`, cid: "receiptcid" };
const SUBMIT = "https://bidder.test#submit_event";
const GUEST = "iroh://guest-ticket";

function claim(name: string, overrides: Partial<NodeClaim> = {}): NodeClaim {
  return {
    workspace: "root:acct:ci",
    name,
    finalizers: [],
    spec: {
      cluster: "ci",
      nodeName: name,
      ownerDid: OWNER,
      apiserverPort: 23456,
      advertiseAddress: "100.127.255.254",
      tokenFile: "/state/ci/token",
      nodePasswordFile: `/state/ci/${name}/node-password`,
      serverCAFile: "/state/ci/server-ca.crt",
    },
    status: {},
    ...overrides,
  };
}

class FakeStore implements NodeClaimStore {
  claims = new Map<string, NodeClaim>();
  statuses: { name: string; status: NodeClaimStatus }[] = [];
  removed: string[] = [];
  gets = 0;
  unreadable = false;

  put(c: NodeClaim) {
    this.claims.set(c.name, structuredClone(c));
  }
  list() {
    return Promise.resolve([...this.claims.values()].map((c) => structuredClone(c)));
  }
  get(c: Pick<NodeClaim, "workspace" | "name">) {
    this.gets++;
    if (this.unreadable) return Promise.reject(new Error("429 held back"));
    const cur = this.claims.get(c.name);
    return Promise.resolve(cur ? structuredClone(cur) : null);
  }
  replaceStatus(c: NodeClaim, status: NodeClaimStatus) {
    const cur = this.claims.get(c.name);
    if (cur) cur.status = JSON.parse(JSON.stringify(status));
    this.statuses.push({ name: c.name, status: JSON.parse(JSON.stringify(status)) });
    return Promise.resolve();
  }
  addFinalizer(c: NodeClaim) {
    const cur = this.claims.get(c.name)!;
    if (!cur.finalizers.includes(FINALIZER_NODE_CLAIM)) cur.finalizers.push(FINALIZER_NODE_CLAIM);
    return Promise.resolve();
  }
  removeFinalizer(c: NodeClaim) {
    const cur = this.claims.get(c.name);
    if (cur) cur.finalizers = cur.finalizers.filter((f) => f !== FINALIZER_NODE_CLAIM);
    this.removed.push(c.name);
    return Promise.resolve();
  }
  phase(name: string) {
    return this.claims.get(name)?.status.phase;
  }
  contract(name: string) {
    return this.claims.get(name)?.status.contract;
  }
}

interface SignedRecord {
  collection: string;
  record: Record<string, unknown>;
  ref: RecordRef;
}

interface BidderCall {
  targetBase: string;
  nsid: string;
  audDid: string;
  body: { uri: string; cid: string; record: { receipt: RecordRef & { $type: string }; payload: RecordRef } };
}

class FakeRequesterPds {
  readonly did = OWNER;
  readonly attestationKp = { did: () => OWNER, privateKey: { bytes: new Uint8Array(32) } };
  records: SignedRecord[] = [];
  calls: BidderCall[] = [];
  resolved: string[] = [];
  #n = 0;

  constructor(readonly bidderOk = true) {}

  createSignedRepoRecord(collection: string, record: Record<string, unknown>): Promise<RecordRef> {
    const ref = { uri: `at://${OWNER}/${collection}/${++this.#n}`, cid: `signed${this.#n}` };
    this.records.push({ collection, record, ref });
    return Promise.resolve(ref);
  }
  resolveBidderEndpoint(endpoint: string) {
    this.resolved.push(endpoint);
    return Promise.resolve({ targetUrl: "https://bidder.test/xrpc", audDid: "did:web:bidder.test" });
  }
  callBidder(targetBase: string, nsid: string, _lxm: string, audDid: string, body: Record<string, unknown>) {
    this.calls.push({ targetBase, nsid, audDid, body: body as unknown as BidderCall["body"] });
    return Promise.resolve({ status: this.bidderOk ? 200 : 502, ok: this.bidderOk, body: {} });
  }
  deletions() {
    return this.calls.filter((c) =>
      c.nsid === SUBMIT_EVENT_NSID &&
      this.records.some((r) => r.collection === COMPUTE_EVENTS_VM_DELETE_NSID && r.ref.uri === c.body.record.payload.uri)
    );
  }
}

const files: Record<string, string> = {
  "/state/ci/token": "k10token\n",
  "/state/ci/server-ca.crt": CA,
};

function readText(path: string): Promise<string> {
  if (files[path]) return Promise.resolve(files[path]);
  if (path.endsWith("/node-password")) return Promise.resolve("k10password\n");
  return Promise.reject(new Deno.errors.NotFound(path));
}

type Script = "complete" | "no_bids" | "hang_after_receipt" | "hang_before_accept";

function scriptedRequest(script: Script, seen: Record<string, unknown>[]) {
  return async (_pds: unknown, o: Record<string, unknown>, handlers: ComputeRequestHandlers) => {
    seen.push(o);
    const handle = (state: ContractState) => ({ state, capabilities: [], dispose: () => Promise.resolve() });
    const returned = (s: ContractState) => handle(patchContract(s, { outcome: "returned" }));
    const stop = (d: FlowDecision | undefined) => d === "return";
    let s = createContractState({ vmName: o.vmName as string, requesterDid: OWNER, marketDid: OWNER, at: AT });
    s = advanceContract(s, "rfp_submitted", { payload: VM, rfp: RFP }, AT);
    if (stop(await handlers.onRfpSubmitted?.({ rfp: RFP, payload: VM, bidderDids: [BIDDER] }, structuredClone(s)))) return returned(s);
    if (script === "no_bids") return handle(patchContract(s, { outcome: "no_bids", bids: 0 }));
    if (script === "hang_before_accept") await new Promise(() => {});
    s = advanceContract(s, "accepted", { accept: ACCEPT, bid: BID, winner: { did: BIDDER } }, AT);
    if (stop(await handlers.onAccepted?.({ accept: ACCEPT, bid: BID, winnerDid: BIDDER }, structuredClone(s)))) return returned(s);
    s = advanceContract(s, "receipt", { receipt: RECEIPT, receiptOk: true, submitEventRef: SUBMIT }, AT);
    if (stop(await handlers.onReceipt?.({ receipt: RECEIPT, receiptOk: true, submitEventRef: SUBMIT }, structuredClone(s)))) return returned(s);
    if (script === "hang_after_receipt") await new Promise(() => {});
    s = advanceContract(s, "network", { vmAddress: GUEST }, AT);
    if (stop(await handlers.onNetwork?.({ address: GUEST, kind: "iroh-ticket", source: "test" }, structuredClone(s)))) return returned(s);
    return handle(patchContract(s, { outcome: "complete" }));
  };
}

interface HarnessOptions {
  behaviour?: "hold" | "exit";
  script?: Script;
  bidderOk?: boolean;
  store?: FakeStore;
  pds?: FakeRequesterPds;
  pollReady?: boolean;
}

function harness(o: HarnessOptions = {}) {
  const store = o.store ?? new FakeStore();
  const pds = o.pds ?? new FakeRequesterPds(o.bidderOk ?? true);
  const requested: string[] = [];
  const contracts: Record<string, unknown>[] = [];
  const plans: NodeSessionPlan[] = [];
  const fulfiller = createMarketNodeFulfiller({
    store,
    requesterFor: (did) => {
      requested.push(did);
      return Promise.resolve(pds as never);
    },
    readText,
    eventStreams: () => ({ close() {} }),
    keypairs: { generateKeypair: () => Promise.resolve({ publicKey: "ssh-ed25519 AAAA", privateKeyPath: "/tmp/k" }) },
    pollReady: () => Promise.resolve(o.pollReady ?? true),
    nodeSession: async (plan) => {
      plans.push(plan);
      plan.onJoining?.();
      plan.onForwarded?.();
      if (o.behaviour === "exit") return { code: 1, reason: "the node's agent exited", tail: "level=fatal msg=boom" };
      await new Promise<void>((resolve) => plan.signal.addEventListener("abort", () => resolve(), { once: true }));
      return { code: 0, reason: "released", tail: "" };
    },
    requestCompute: scriptedRequest(o.script ?? "complete", contracts) as never,
  });
  return { store, pds, fulfiller, requested, contracts, plans };
}

async function settle() {
  for (let i = 0; i < 40; i++) await new Promise((r) => setTimeout(r, 0));
}

function deleteClaim(store: FakeStore, name: string) {
  store.claims.get(name)!.deletionTimestamp = "2026-10-07T00:00:00Z";
}

Deno.test("a door restarted between accept and release still releases the node with vm.delete from the receipt on its NodeClaim", async () => {
  const store = new FakeStore();
  store.put(claim("ci-node1"));

  const before = harness({ store, script: "hang_after_receipt" });
  await before.fulfiller.pass();
  await settle();
  const recorded = store.contract("ci-node1")!;
  assertEquals(store.phase("ci-node1"), "Provisioning");
  assertEquals(recorded.phase, "receipt");
  assertEquals(recorded.receipt, RECEIPT);
  assertEquals(recorded.submitEventRef, SUBMIT);
  assertEquals(recorded.accept, ACCEPT);
  assertEquals(before.pds.calls, [], "the first door died before it released anything");

  const after = harness({ store });
  await after.fulfiller.pass();
  await settle();

  assertEquals(after.contracts, [], "the restarted door requests nothing for a claim that already holds a contract");
  const deletions = after.pds.deletions();
  assertEquals(deletions.length, 1, "exactly one vm.delete reached the bidder");
  const event = deletions[0].body.record;
  assertEquals({ uri: event.receipt.uri, cid: event.receipt.cid }, RECEIPT, "the vm.delete names the receipt recorded on the claim");
  assertEquals(after.pds.resolved, [SUBMIT], "the vm.delete goes to the submitEventRef recorded on the claim");
  assertEquals(after.pds.records.filter((r) => r.collection === EVENT_NSID).length, 1);
  assertEquals(store.phase("ci-node1"), "Released");
  assertEquals(store.contract("ci-node1")?.phase, "released");
  assertEquals(store.contract("ci-node1")?.release?.ok, true);

  await after.fulfiller.pass();
  await settle();
  assertEquals(after.pds.deletions().length, 1, "a released contract is not released twice");
});

Deno.test("releasing a live claim reads the receipt back from the claim, not from the door's memory", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Running");

  const moved: RecordRef = { uri: `at://${BIDDER}/market.receipt/as-recorded`, cid: "recorded" };
  h.store.claims.get("ci-node1")!.status.contract!.receipt = moved;
  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();

  const deletions = h.pds.deletions();
  assertEquals(deletions.length, 1);
  assertEquals({ uri: deletions[0].body.record.receipt.uri, cid: deletions[0].body.record.receipt.cid }, moved);
  assert(h.store.gets > 0, "the claim was read back before the release");
});

Deno.test("the door admits declared bidders and omits a policy it was not given", async () => {
  const store = new FakeStore();
  const contracts: Record<string, unknown>[] = [];
  const fulfiller = createMarketNodeFulfiller({
    store,
    requesterFor: () => Promise.resolve(new FakeRequesterPds() as never),
    readText,
    eventStreams: () => ({ close() {} }),
    keypairs: { generateKeypair: () => Promise.resolve({ publicKey: "k", privateKeyPath: "/tmp/k" }) },
    pollReady: () => Promise.resolve(false),
    extraBidderDids: ["did:plc:mine"],
    requestCompute: scriptedRequest("no_bids", contracts) as never,
  });
  store.put(claim("ci-node1"));
  await fulfiller.pass();
  await settle();
  assertEquals(contracts[0].extraBidderDids, ["did:plc:mine"]);
  assertEquals("policy" in contracts[0], false);
  await fulfiller.stop();
});

Deno.test("a pending claim is finalized and fulfilled through the market as its owner", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();

  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);
  assertEquals(h.requested, [OWNER]);
  assertEquals(h.contracts.length, 1);
  const c = h.contracts[0];
  assertEquals(c.vmName, "ci-node1");
  assertEquals(c.sshPublicKey, "ssh-ed25519 AAAA");
  assertEquals((c.userData as { modules: unknown[] }).modules[0], "k3s");
  assertEquals(h.plans[0].program, K3S_JOIN_PATH);
  assertEquals(h.plans[0].ticket, GUEST);
  assertEquals(h.store.phase("ci-node1"), "Running");
  const contract = h.store.contract("ci-node1")!;
  assertEquals(contract.receiptUri, RECEIPT.uri);
  assertEquals(contract.receiptCid, RECEIPT.cid);
  assertEquals(contract.receipt, RECEIPT);
  assertEquals(contract.vmAddress, GUEST);
  assertEquals(contract.phase, "network");
  assertEquals(h.store.claims.get("ci-node1")!.status.fqdn, GUEST);
  await h.fulfiller.stop();
});

Deno.test("every contract milestone reaches the claim before the flow moves on", async () => {
  const h = harness({ script: "hang_after_receipt" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const phases = h.store.statuses.map((s) => s.status.contract?.phase).filter(Boolean);
  for (const phase of ["rfp_submitted", "accepted", "receipt"]) {
    assert(phases.includes(phase as never), `no status write carried contract phase ${phase}: ${phases.join(",")}`);
  }
  const accepted = h.store.statuses.find((s) => s.status.contract?.phase === "accepted")!.status;
  assertEquals(accepted.phase, "Provisioning");
  assertEquals(accepted.contract?.acceptUri, ACCEPT.uri);
  assertEquals(accepted.contract?.rfpUri, RFP.uri);
  assertEquals(accepted.contract?.vmUri, VM.uri);
});

Deno.test("the join document pins the cluster CA and the forward targets the claim's apiserver", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const plan = h.plans[0];
  assertEquals(plan.forwardAddress, "100.127.255.254");
  assertEquals(plan.apiserverPort, 23456);
  assertEquals(plan.apiserverHost, "127.0.0.1");
  const doc = Object.fromEntries(plan.joinDocument.trim().split("\n").map((l) => {
    const [k, v] = l.split(" ");
    return [k, atob(v)];
  }));
  const caHash = Array.from(
    new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(CA))),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
  assertEquals(doc.TOKEN, `K10${caHash}::server:k10token`);
  assertEquals(doc.NODE_PASSWORD, "k10password");
  assertEquals(doc.SERVER_URL, "https://100.127.255.254:23456");
  assertEquals(doc.NODE_NAME, "ci-node1");
  assertEquals(JSON.stringify(h.contracts[0]).includes("k10token"), false, "the token never enters the contract");
  assertEquals(JSON.stringify(h.store.statuses).includes("k10token"), false, "the token never enters the claim");
  await h.fulfiller.stop();
});

Deno.test("concurrent claims for one owner each get their own contract", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  h.store.put(claim("ci-node2"));
  h.store.put(claim("ci-node3", { workspace: "root:acct:other", spec: { ...claim("x").spec, cluster: "other", nodeName: "other-node1" } }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 3);
  assertEquals(h.requested, [OWNER, OWNER, OWNER]);
  assertEquals(h.fulfiller.active().length, 3);
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 3, "a live claim is not fulfilled twice");
  await h.fulfiller.stop();
});

Deno.test("deleting a live claim releases the node and only then drops the finalizer", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.removed, []);
  assertEquals(h.pds.deletions(), []);

  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Released");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assertEquals(h.store.contract("ci-node1")?.phase, "released");
  assertEquals(h.fulfiller.active(), []);
  assertEquals(h.pds.deletions().length, 1);

  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1, "the finalizer path does not release a released contract again");
});

Deno.test("a claim deleted before any bid was accepted asks the flow to return and releases nothing", async () => {
  const h = harness({ script: "hang_before_accept" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.contract("ci-node1")?.phase, "rfp_submitted");
  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions(), []);
});

Deno.test("a node whose agent dies is reported Failed with the guest's words, its VM released, and keeps its finalizer", async () => {
  const h = harness({ behaviour: "exit" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertStringIncludes(h.store.claims.get("ci-node1")!.status.message!, "level=fatal msg=boom");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);
  assertEquals(h.pds.deletions().length, 1);

  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assertEquals(h.pds.deletions().length, 1, "nothing left to release once the contract ended");
});

Deno.test("an unreadable claim at the end of a run defers the release to the next pass, which reads it again", async () => {
  const h = harness({ behaviour: "exit" });
  h.store.put(claim("ci-node1"));
  h.store.unreadable = true;
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions(), [], "no claim, no release");
  assertEquals(h.fulfiller.active(), []);
  assertEquals(h.store.contract("ci-node1")?.receipt, RECEIPT);

  h.store.unreadable = false;
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.store.phase("ci-node1"), "Released");
});

Deno.test("a guest whose ssh never comes up is Failed and released", async () => {
  const h = harness({ pollReady: false });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertEquals(h.store.claims.get("ci-node1")!.status.message, "a VM was provisioned but its SSH never came up");
  assertEquals(h.pds.deletions().length, 1);
});

Deno.test("no bids is a failure that says so", async () => {
  const h = harness({ script: "no_bids" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertEquals(h.store.claims.get("ci-node1")!.status.message, "no bids arrived for this node");
  assertEquals(h.pds.deletions(), []);
});

Deno.test("a no-bids end keeps the rfp it asked with and names no accept", async () => {
  const h = harness({ script: "no_bids" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const status = h.store.claims.get("ci-node1")!.status;
  assertEquals(status.phase, "Failed");
  assertEquals(status.contract?.rfpUri, RFP.uri);
  assertEquals(status.contract?.acceptUri, undefined);
  assertEquals(status.contract?.outcome, "no_bids");
});

Deno.test("a live claim this process never started is released from its legacy receipt, never adopted", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", {
    finalizers: [FINALIZER_NODE_CLAIM],
    status: {
      phase: "Running",
      contract: { vmUri: "at://owner/vm/1", receiptUri: RECEIPT.uri, receiptCid: RECEIPT.cid, submitEventRef: SUBMIT },
    },
  }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 0);
  const deletions = h.pds.deletions();
  assertEquals(deletions.length, 1);
  assertEquals({ uri: deletions[0].body.record.receipt.uri, cid: deletions[0].body.record.receipt.cid }, RECEIPT);
  assertEquals(
    h.store.phase("ci-node1"),
    "Released",
    "a claim the door reaped and CONFIRMED released must say Released, not Failed. The cluster's " +
      "Node object is still there and still reads Ready, and the two phases are the only thing that " +
      "tells a reader which claims have a machine behind them: Released is the one whose machine the " +
      "door took away, and it is what the clusterprovider deletes the corpse Node on.",
  );
  assertEquals(h.store.contract("ci-node1")?.phase, "released");
  assertEquals(h.store.contract("ci-node1")?.vmUri, "at://owner/vm/1");
});

Deno.test("a claim the door could not confirm released is Failed, not Released", async () => {
  const h = harness({ bidderOk: false });
  h.store.put(claim("ci-node1", {
    finalizers: [FINALIZER_NODE_CLAIM],
    status: {
      phase: "Running",
      contract: { vmUri: "at://owner/vm/1", receiptUri: RECEIPT.uri, receiptCid: RECEIPT.cid, submitEventRef: SUBMIT },
    },
  }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(
    h.store.phase("ci-node1"),
    "Failed",
    "the door could not confirm the machine gone, so the claim may still have one, and only " +
      "Released is safe to delete a cluster Node on",
  );
  assertEquals(
    h.store.claims.get("ci-node1")!.status.message,
    "the door restarted while this node was live; its VM could not be confirmed released",
  );
});

Deno.test("a deleted claim with an unreleased contract is released before its finalizer goes", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", {
    deletionTimestamp: "2026-10-06T00:00:00Z",
    finalizers: [FINALIZER_NODE_CLAIM],
    status: { phase: "Failed", contract: { receiptUri: "r", receiptCid: "c", submitEventRef: "s" } },
  }));
  await h.fulfiller.pass();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.pds.resolved, ["s"]);
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
});

Deno.test("an unreadable token fails the claim without starting a contract", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", { spec: { ...claim("ci-node1").spec, tokenFile: "/missing" } }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 0);
  assertEquals(h.store.phase("ci-node1"), "Failed");
});

Deno.test("a claim's contract round-trips to the requester's saved state and keeps the legacy refs", () => {
  let s = createContractState({ vmName: "ci-node1", requesterDid: OWNER, marketDid: OWNER, at: AT });
  s = advanceContract(s, "receipt", { payload: VM, rfp: RFP, accept: ACCEPT, receipt: RECEIPT, submitEventRef: SUBMIT }, AT);
  const contract = claimContractOf(s);
  assertEquals(contract.vmUri, VM.uri);
  assertEquals(contract.rfpUri, RFP.uri);
  assertEquals(contract.acceptUri, ACCEPT.uri);
  assertEquals(contract.receiptUri, RECEIPT.uri);
  assertEquals(contract.receiptCid, RECEIPT.cid);
  const back = contractStateOfClaim(claim("ci-node1", { status: { contract: JSON.parse(JSON.stringify(contract)) } }));
  assertEquals(back, s);
  assertEquals(contractStateOfClaim(claim("ci-node1")), null);
  const legacy = contractStateOfClaim(claim("ci-node1", { status: { contract: { receiptUri: "r", receiptCid: "c", submitEventRef: "s" } } }));
  assertEquals(legacy?.receipt, { uri: "r", cid: "c" });
  assertEquals(legacy?.submitEventRef, "s");
  assertEquals(legacy?.requesterDid, OWNER);
});
