import { assertEquals, assertStringIncludes } from "@std/assert";
import {
  contractFromEvent,
  createMarketNodeFulfiller,
  type NodeSessionPlan,
} from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import type { NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  K3S_JOIN_PATH,
  NODECLAIM_FINALIZER,
  type NodeClaim,
  type NodeClaimStatus,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

const OWNER = "did:plc:nodeowner";
const CA = "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n";

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

  put(c: NodeClaim) {
    this.claims.set(c.name, structuredClone(c));
  }
  list() {
    return Promise.resolve([...this.claims.values()].map((c) => structuredClone(c)));
  }
  replaceStatus(c: NodeClaim, status: NodeClaimStatus) {
    const cur = this.claims.get(c.name);
    if (cur) cur.status = JSON.parse(JSON.stringify(status));
    this.statuses.push({ name: c.name, status: JSON.parse(JSON.stringify(status)) });
    return Promise.resolve();
  }
  addFinalizer(c: NodeClaim) {
    const cur = this.claims.get(c.name)!;
    if (!cur.finalizers.includes(NODECLAIM_FINALIZER)) cur.finalizers.push(NODECLAIM_FINALIZER);
    return Promise.resolve();
  }
  removeFinalizer(c: NodeClaim) {
    const cur = this.claims.get(c.name);
    if (cur) cur.finalizers = cur.finalizers.filter((f) => f !== NODECLAIM_FINALIZER);
    this.removed.push(c.name);
    return Promise.resolve();
  }
  phase(name: string) {
    return this.claims.get(name)?.status.phase;
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

const fakePds = { did: "did:plc:ephemeral" } as never;

function harness(behaviour: "hold" | "exit" | "no_bids" = "hold", reapConfirmed = true) {
  const store = new FakeStore();
  const requested: string[] = [];
  const contracts: Record<string, unknown>[] = [];
  const plans: NodeSessionPlan[] = [];
  const reaped: unknown[] = [];
  const fulfiller = createMarketNodeFulfiller({
    store,
    requesterFor: (did) => {
      requested.push(did);
      return Promise.resolve(fakePds);
    },
    readText,
    eventStreams: () => ({ close() {} }),
    keypairs: { generateKeypair: () => Promise.resolve({ publicKey: "ssh-ed25519 AAAA", privateKeyPath: "/tmp/k" }) },
    pollReady: () => Promise.resolve(true),
    reap: (_pds, contract) => {
      reaped.push(contract);
      return Promise.resolve(reapConfirmed);
    },
    nodeSession: async (plan) => {
      plans.push(plan);
      plan.onJoining?.();
      plan.onForwarded?.();
      if (behaviour === "exit") return { code: 1, reason: "the node's agent exited", tail: "level=fatal msg=boom" };
      await new Promise<void>((resolve) => plan.signal.addEventListener("abort", () => resolve(), { once: true }));
      return { code: 0, reason: "released", tail: "" };
    },
    runContract: (async (_pds: unknown, o: Record<string, unknown>) => {
      contracts.push(o);
      const logger = o.logger as { info(e: string, m: Record<string, unknown>): void };
      logger.info("vm_record_created", { uri: `at://owner/vm/${o.vmName}`, cid: "c" });
      if (behaviour === "no_bids") return { event: "no_bids", bids: 0 };
      logger.info("accept_created", { uri: "at://owner/accept/1", cid: "c" });
      logger.info("submitAccept_result", { receiptUri: "at://bidder/receipt/1", receiptCid: "rc", submitEventRef: "https://bidder/xrpc" });
      const provider = o.sshProvider as { runSession(k: string, f: string, p: string): Promise<number> };
      const code = await provider.runSession("/tmp/k", "guest.example", o.execProgram as string);
      return { event: "done", sshReady: true, sshExitCode: code };
    }) as never,
  });
  return { store, fulfiller, requested, contracts, plans, reaped };
}

async function settle() {
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0));
}

Deno.test("the door admits declared bidders and omits a policy it was not given", async () => {
  const store = new FakeStore();
  const contracts: Record<string, unknown>[] = [];
  const fulfiller = createMarketNodeFulfiller({
    store,
    requesterFor: () => Promise.resolve(fakePds),
    readText,
    eventStreams: () => ({ close() {} }),
    keypairs: { generateKeypair: () => Promise.resolve({ publicKey: "k", privateKeyPath: "/tmp/k" }) },
    pollReady: () => Promise.resolve(false),
    extraBidderDids: ["did:plc:mine"],
    runContract: (async (_pds: unknown, o: Record<string, unknown>) => {
      contracts.push({ ...o, sshProvider: undefined, logger: undefined, eventStreams: undefined });
      return { event: "no_bids", bids: 0 };
    }) as never,
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

  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [NODECLAIM_FINALIZER]);
  assertEquals(h.requested, [OWNER]);
  assertEquals(h.contracts.length, 1);
  const c = h.contracts[0];
  assertEquals(c.vmName, "ci-node1");
  assertEquals(c.execProgram, K3S_JOIN_PATH);
  assertEquals((c.userData as { modules: unknown[] }).modules[0], "k3s");
  assertEquals(h.store.phase("ci-node1"), "Running");
  assertEquals(h.store.claims.get("ci-node1")!.status.contract?.receiptUri, "at://bidder/receipt/1");
  assertEquals(h.store.claims.get("ci-node1")!.status.fqdn, "guest.example");
  await h.fulfiller.stop();
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

  h.store.claims.get("ci-node1")!.deletionTimestamp = "2026-10-06T00:00:00Z";
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Released");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assertEquals(h.store.claims.get("ci-node1")!.status.contract?.receiptUri, undefined);
  assertEquals(h.fulfiller.active(), []);
  assertEquals(h.reaped, [], "runComputeContract already deleted the VM");
});

Deno.test("a node whose agent dies is reported Failed with the guest's words, and keeps its finalizer", async () => {
  const h = harness("exit");
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertStringIncludes(h.store.claims.get("ci-node1")!.status.message!, "level=fatal msg=boom");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [NODECLAIM_FINALIZER]);

  h.store.claims.get("ci-node1")!.deletionTimestamp = "2026-10-06T00:00:00Z";
  await h.fulfiller.pass();
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assertEquals(h.reaped, [], "nothing left to reap once the contract ended");
});

Deno.test("no bids is a failure that says so", async () => {
  const h = harness("no_bids");
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertEquals(h.store.claims.get("ci-node1")!.status.message, "no bids arrived for this node");
});

Deno.test("a live claim this process never started is reaped, never adopted", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", {
    finalizers: [NODECLAIM_FINALIZER],
    status: {
      phase: "Running",
      contract: { vmUri: "at://owner/vm/1", receiptUri: "at://bidder/receipt/1", receiptCid: "rc", submitEventRef: "https://bidder/xrpc" },
    },
  }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 0);
  assertEquals(h.reaped.length, 1);
  assertEquals(
    h.store.phase("ci-node1"),
    "Released",
    "a claim the door reaped and CONFIRMED released must say Released, not Failed. The cluster's " +
      "Node object is still there and still reads Ready, and the two phases are the only thing that " +
      "tells a reader which claims have a machine behind them: Released is the one whose machine the " +
      "door took away, and it is what the clusterprovider deletes the corpse Node on.",
  );
  assertEquals(h.store.claims.get("ci-node1")!.status.contract?.receiptUri, undefined);
  assertEquals(h.store.claims.get("ci-node1")!.status.contract?.vmUri, "at://owner/vm/1");
});

Deno.test("a claim the door could not confirm released is Failed, not Released", async () => {
  const h = harness("hold", false);
  h.store.put(claim("ci-node1", {
    finalizers: [NODECLAIM_FINALIZER],
    status: {
      phase: "Running",
      contract: { vmUri: "at://owner/vm/1", receiptUri: "at://bidder/receipt/1", receiptCid: "rc", submitEventRef: "https://bidder/xrpc" },
    },
  }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.reaped.length, 1);
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

Deno.test("a deleted claim with an unreleased contract is reaped before its finalizer goes", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", {
    deletionTimestamp: "2026-10-06T00:00:00Z",
    finalizers: [NODECLAIM_FINALIZER],
    status: { phase: "Failed", contract: { receiptUri: "r", receiptCid: "c", submitEventRef: "s" } },
  }));
  await h.fulfiller.pass();
  assertEquals(h.reaped.length, 1);
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

Deno.test("contract refs are read from the requester's own log events", () => {
  assertEquals(contractFromEvent("vm_record_created", { uri: "at://v" }), { vmUri: "at://v" });
  assertEquals(contractFromEvent("receipt_from_firehose", { receiptUri: "r", receiptCid: "c", submitEventRef: "s" }), {
    receiptUri: "r",
    receiptCid: "c",
    submitEventRef: "s",
  });
  assertEquals(contractFromEvent("bid_received", { uri: "at://b" }), {});
});
