import { assert, assertEquals, assertRejects, assertStringIncludes } from "@std/assert";
import {
  claimContractOf,
  clusterKeyOfClaim,
  contractStateOfClaim,
  createMarketNodeFulfiller,
  defaultOpenBaoCAFile,
} from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import type { ClusterLinkReader, ClusterLinkView, NodeClaim, NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { FINALIZER_NODE_CLAIM, type NodeClaimStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import {
  NODE_BUNDLE_PATHS,
  NODE_CLAIM_DOC_PATH,
  type NodeClaimDocument,
  OPENBAO_CA_PATH,
  OPENBAO_TICKET_PATH,
} from "@publicdomainrelay/socialweb-computer-k8s-common";
import {
  advanceContract,
  type ComputeRequestHandlers,
  type ContractState,
  createContractState,
  type FlowDecision,
  patchContract,
  type RecordRef,
} from "@publicdomainrelay/compute-request-abc";
import type { GrantVars, GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import type { SecretEntry } from "@publicdomainrelay/secrets-common";
import { buildUserData, type UserDataModule } from "@publicdomainrelay/cloud-init-common";
import { type MeshdPin, NODE_NET_UNITS } from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import { COMPUTE_EVENTS_VM_DELETE_NSID, EVENT_NSID, SUBMIT_EVENT_NSID } from "@publicdomainrelay/market-lexicons";

const OWNER = "did:plc:nodeowner";
const OWNER_CLUSTER_KEY = "d90m41be-ci";
const BIDDER = "did:plc:bidder";
const OPENBAO_CA = "-----BEGIN CERTIFICATE-----\nOPENBAO\n-----END CERTIFICATE-----\n";
const OPENBAO_TICKET = "endpointopenbaoticket";
const AT = "2026-10-07T00:00:00.000Z";
const ISSUER = "https://bidder.test/oidc";
const SUBJECT = "actx:a1:plc:nodeowner:role:ci-node1";

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
      cluster: OWNER_CLUSTER_KEY,
      nodeName: name,
      ownerDid: OWNER,
      apiserverPort: 23456,
      advertiseAddress: "100.127.255.254",
      tokenFile: "/state/ci/token",
      nodePasswordFile: `/state/ci/${name}/node-password`,
      serverCAFile: "/state/ci/server/tls/server-ca.crt",
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
  failGrantWrites = false;

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
    if (this.failGrantWrites && status.grant) return Promise.reject(new Error("422 grant refused"));
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

class FakeLinks implements ClusterLinkReader {
  asked: string[] = [];
  constructor(readonly ticket: string | null = OPENBAO_TICKET) {}
  get(workspace: string, name: string): Promise<ClusterLinkView | null> {
    this.asked.push(`${workspace}/${name}`);
    if (this.ticket === null) return Promise.resolve(null);
    return Promise.resolve({ workspace, name, status: { phase: "Listening", ticket: this.ticket } });
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
  "/state/ci/openbao/tls/ca.crt": OPENBAO_CA,
};

function readText(path: string): Promise<string> {
  if (files[path]) return Promise.resolve(files[path]);
  return Promise.reject(new Deno.errors.NotFound(path));
}

class FakeSecretsCapabilities {
  bundles: SecretEntry[][] = [];
  grants: GrantVars[] = [];
  pathsAtGrant: string[][] = [];
  disposed = 0;
  factory = (secrets: SecretEntry[]): GuestCapability => {
    this.bundles.push(secrets);
    return {
      id: "secrets",
      userDataModule: "secrets",
      onContract: (vars) => {
        this.grants.push(vars);
        this.pathsAtGrant.push(secrets.map((s) => s.path));
      },
      dispose: () => {
        this.disposed++;
        return Promise.resolve();
      },
    };
  };
}

type Script = "complete" | "no_fetch" | "no_bids" | "hang_after_receipt" | "hang_before_accept" | "hang_after_network";

interface Decisions {
  onSecretsFetched: FlowDecision[];
  onAccepted: FlowDecision[];
  onReceipt: FlowDecision[];
  grantAtAccept: (NodeClaimStatus["grant"] | undefined)[];
}

function scriptedRequest(script: Script, seen: Record<string, unknown>[], decisions: Decisions, store: FakeStore) {
  return async (_pds: unknown, o: Record<string, unknown>, handlers: ComputeRequestHandlers) => {
    seen.push(o);
    const caps = (o.capabilities as GuestCapability[] | undefined) ?? [];
    let disposed = false;
    const dispose = async () => {
      if (disposed) return;
      disposed = true;
      for (const cap of caps) await cap.dispose?.();
    };
    const handle = (state: ContractState) => ({ state, capabilities: caps, dispose });
    const returned = (s: ContractState) => handle(patchContract(s, { outcome: "returned" }));
    const stop = (d: FlowDecision | undefined) => d === "return";
    let s = createContractState({ vmName: o.vmName as string, requesterDid: OWNER, marketDid: OWNER, at: AT });
    s = advanceContract(s, "rfp_submitted", { payload: VM, rfp: RFP }, AT);
    if (stop(await handlers.onRfpSubmitted?.({ rfp: RFP, payload: VM, bidderDids: [BIDDER] }, structuredClone(s)))) return returned(s);
    if (script === "no_bids") {
      await dispose();
      return { state: patchContract(s, { outcome: "no_bids", bids: 0 }), capabilities: [], dispose: () => Promise.resolve() };
    }
    if (script === "hang_before_accept") await new Promise(() => {});
    const vars: GrantVars = {
      requesterDid: OWNER,
      role: o.vmName as string,
      actx: "a1",
      issuerUri: ISSUER,
      subject: SUBJECT,
      expectedAud: `api://ATProto?actx=${OWNER}`,
    };
    for (const cap of caps) {
      try {
        await cap.onContract?.(vars);
      } catch {
        continue;
      }
    }
    s = advanceContract(s, "accepted", { accept: ACCEPT, bid: BID, winner: { did: BIDDER }, receipt: RECEIPT, submitEventRef: SUBMIT }, AT);
    decisions.grantAtAccept.push(store.claims.get(o.vmName as string)?.status.grant);
    const accepted = await handlers.onAccepted?.({ accept: ACCEPT, bid: BID, winnerDid: BIDDER }, structuredClone(s));
    decisions.onAccepted.push(accepted ?? "continue");
    if (stop(accepted)) return returned(s);
    s = advanceContract(s, "receipt", { receipt: RECEIPT, receiptOk: true, submitEventRef: SUBMIT }, AT);
    const receipt = await handlers.onReceipt?.({ receipt: RECEIPT, receiptOk: true, submitEventRef: SUBMIT }, structuredClone(s));
    decisions.onReceipt.push(receipt ?? "continue");
    if (stop(receipt)) return returned(s);
    if (script === "hang_after_receipt") await new Promise(() => {});
    s = advanceContract(s, "network", { vmAddress: GUEST }, AT);
    if (stop(await handlers.onNetwork?.({ address: GUEST, kind: "iroh-ticket", source: "test" }, structuredClone(s)))) return returned(s);
    if (script === "hang_after_network") await new Promise(() => {});
    if (script === "complete") {
      const decision = await handlers.onSecretsFetched?.({ capability: "secrets", subject: SUBJECT, count: 3 }, structuredClone(s));
      decisions.onSecretsFetched.push(decision ?? "continue");
      if (stop(decision)) return returned(s);
    }
    return handle(patchContract(s, { outcome: "complete" }));
  };
}

interface HarnessOptions {
  script?: Script;
  bidderOk?: boolean;
  store?: FakeStore;
  pds?: FakeRequesterPds;
  links?: FakeLinks;
  vmReadyTimeoutSec?: number;
  meshd?: MeshdPin;
  debugSsh?: { authorizedKey: string; onNetwork(claim: string, address: string): void };
  scopeCache?: unknown;
  scopeRefresher?: unknown;
}

function harness(o: HarnessOptions = {}) {
  const store = o.store ?? new FakeStore();
  const pds = o.pds ?? new FakeRequesterPds(o.bidderOk ?? true);
  const links = o.links ?? new FakeLinks();
  const secrets = new FakeSecretsCapabilities();
  const requested: string[] = [];
  const contracts: Record<string, unknown>[] = [];
  const decisions: Decisions = { onSecretsFetched: [], onAccepted: [], onReceipt: [], grantAtAccept: [] };
  const fulfiller = createMarketNodeFulfiller({
    store,
    links,
    requesterFor: (did) => {
      requested.push(did);
      return Promise.resolve(pds as never);
    },
    readText,
    eventStreams: () => ({ close() {} }),
    secretsCapability: secrets.factory,
    vmReadyTimeoutSec: o.vmReadyTimeoutSec ?? 0.05,
    ...(o.meshd ? { meshd: o.meshd } : {}),
    ...(o.debugSsh ? { debugSsh: o.debugSsh } : {}),
    ...(o.scopeCache ? { scopeCache: o.scopeCache as never } : {}),
    ...(o.scopeRefresher ? { scopeRefresher: o.scopeRefresher as never } : {}),
    requestCompute: scriptedRequest(o.script ?? "complete", contracts, decisions, store) as never,
  });
  return { store, pds, links, secrets, fulfiller, requested, contracts, decisions };
}

async function settle() {
  for (let i = 0; i < 40; i++) await new Promise((r) => setTimeout(r, 0));
}

async function settleFor(ms: number) {
  await new Promise((r) => setTimeout(r, ms));
  await settle();
}

function deleteClaim(store: FakeStore, name: string) {
  store.claims.get(name)!.deletionTimestamp = "2026-10-07T00:00:00Z";
}

Deno.test("the door's cluster key is the claim's spec.cluster, which the provider writes as the key", async () => {
  assertEquals(await clusterKeyOfClaim({ ownerDid: OWNER, cluster: OWNER_CLUSTER_KEY }), OWNER_CLUSTER_KEY);
  assertEquals(
    await clusterKeyOfClaim({ ownerDid: OWNER, cluster: "2wjeqtgx-test-cluster-alice-0001" }),
    "2wjeqtgx-test-cluster-alice-0001",
  );
  assertEquals(defaultOpenBaoCAFile(claim("ci-node1").spec), "/state/ci/openbao/tls/ca.crt");
});

Deno.test("at accept the claim carries the workload grant for its node's audience before the guest can be authorized", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();

  const expected = {
    issuer: ISSUER,
    subject: SUBJECT,
    audience: `socialweb.computer/node/${OWNER_CLUSTER_KEY}/ci-node1`,
    role: "ci-node1",
  };
  assertEquals(h.decisions.grantAtAccept, [expected], "the grant was on the claim when onAccepted ran");
  assertEquals(h.store.claims.get("ci-node1")!.status.grant, expected);
  const grantWrite = h.store.statuses.findIndex((s) => s.status.grant);
  const acceptedWrite = h.store.statuses.findIndex((s) => s.status.contract?.phase === "accepted");
  assert(grantWrite >= 0 && grantWrite < acceptedWrite, `grant write ${grantWrite} precedes accepted write ${acceptedWrite}`);
  assertEquals(h.secrets.grants.length, 1, "the secrets capability is authorized once");
  assertEquals(h.secrets.pathsAtGrant[0], [...NODE_BUNDLE_PATHS], "claim.json is in the bundle before the guest is authorized");
  await h.fulfiller.stop();
});

Deno.test("the secrets bundle carries exactly the OpenBao ticket, the OpenBao CA and claim.json", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();

  assertEquals(h.links.asked, [`root:acct:ci/${OWNER_CLUSTER_KEY}-openbao`]);
  const bundle = h.secrets.bundles[0];
  assertEquals(bundle.map((s) => s.path).sort(), [...NODE_BUNDLE_PATHS].sort());
  const byPath = Object.fromEntries(bundle.map((s) => [s.path, s.value]));
  assertEquals(byPath[OPENBAO_TICKET_PATH], OPENBAO_TICKET);
  assertEquals(byPath[OPENBAO_CA_PATH], OPENBAO_CA);
  const issuerHash = Array.from(
    new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(ISSUER))),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
  const doc = JSON.parse(byPath[NODE_CLAIM_DOC_PATH]) as NodeClaimDocument;
  assertEquals(doc, {
    node: "ci-node1",
    cluster: OWNER_CLUSTER_KEY,
    jwtMount: `jwt-${issuerHash.slice(0, 12)}`,
    role: "ci-node1",
    audience: `socialweb.computer/node/${OWNER_CLUSTER_KEY}/ci-node1`,
    kvMount: "socialweb-nodes",
    kvPath: "ci-node1",
  });
  await h.fulfiller.stop();
});

Deno.test("the flow returns at onSecretsFetched, the claim is Joining, capabilities are disposed and nothing is released", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();

  assertEquals(h.decisions.onSecretsFetched, ["return"]);
  assertEquals(h.store.phase("ci-node1"), "Joining");
  assertEquals(h.secrets.disposed, 1);
  assertEquals(h.fulfiller.active(), [], "the door holds nothing for a node that fetched its secrets");
  assertEquals(h.pds.deletions(), []);
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);
  assertEquals(h.store.contract("ci-node1")?.receipt, RECEIPT);

  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 1, "a Joining claim is not fulfilled again");
  assertEquals(h.pds.deletions(), []);
});

Deno.test("the door asks for no ssh key and builds no ssh session", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const c = h.contracts[0];
  assertEquals(c.sshPublicKey, undefined);
  assertEquals(c.sshAuthorizedKeys, undefined);
  assertEquals((c.capabilities as GuestCapability[]).map((cap) => cap.id), ["secrets"]);
  const source = await Deno.readTextFile(new URL("../lib/socialweb-computer-k8s-node-market/mod.ts", import.meta.url));
  for (const ssh of ["createSshSessionProvider", "SshSessionProvider", "ssh2", "runNodeSession", "dumbpipe"]) {
    assertEquals(source.includes(ssh), false, `the fulfiller still names ${ssh}`);
  }
});

Deno.test("only a door given a debug key puts it in the guest's authorized_keys, and it is told the guest's address", async () => {
  const seen: [string, string][] = [];
  const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDebugDebugDebugDebugDebugDebugDebugDebug debug-node-ssh";
  const h = harness({ debugSsh: { authorizedKey: key, onNetwork: (claim, address) => seen.push([claim, address]) } });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const c = h.contracts[0];
  assertEquals(c.sshAuthorizedKeys, [key]);
  assertEquals(c.sshPublicKey, undefined);
  assertEquals(seen.length > 0, true, "the debug hook never heard the guest's network address");
  assertEquals(seen[0][0], "ci-node1");
});

Deno.test("the user_data the door asks for renders without any secret value", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const c = h.contracts[0];
  const modules = (c.userData as { modules: (string | UserDataModule)[] }).modules;
  assertEquals(modules[0], "k3s");
  const caps = c.capabilities as GuestCapability[];
  const text = buildUserData({
    ctx: { vmName: "ci-node1", secretsUrl: "https://secrets.test", secretsAud: "api://x" },
    modules: ["iroh", ...modules, ...caps.flatMap((cap) => cap.userDataModule ? [cap.userDataModule] : [])],
  });
  for (const secret of [OPENBAO_TICKET, "BEGIN CERTIFICATE", SUBJECT, ISSUER]) {
    assertEquals(text.includes(secret), false, `user_data carries ${secret}`);
  }
  assertStringIncludes(text, "socialweb-k3s-agent.service");
  await h.fulfiller.stop();
});

function renderedUserData(c: Record<string, unknown>): string {
  const modules = (c.userData as { modules: (string | UserDataModule)[] }).modules;
  const caps = c.capabilities as GuestCapability[];
  return buildUserData({
    ctx: { vmName: "ci-node1", secretsUrl: "https://secrets.test", secretsAud: "api://x" },
    modules: ["iroh", ...modules, ...caps.flatMap((cap) => cap.userDataModule ? [cap.userDataModule] : [])],
  });
}

Deno.test("the user_data the door asks for installs the meshd it ships, pinned by sha256, and the pod network", async () => {
  const meshd = { downloadBase: "https://door.test", sha256: { x86_64: "1".repeat(64), aarch64: "2".repeat(64) } };
  const h = harness({ meshd });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const text = renderedUserData(h.contracts[0]);
  for (const want of [NODE_NET_UNITS.meshdInstall, NODE_NET_UNITS.meshd, "1".repeat(64), "2".repeat(64), "https://door.test/downloads/"]) {
    assertStringIncludes(text, want);
  }
  await h.fulfiller.stop();

  const bare = harness();
  bare.store.put(claim("ci-node1"));
  await bare.fulfiller.pass();
  await settle();
  const without = renderedUserData(bare.contracts[0]);
  assertStringIncludes(without, NODE_NET_UNITS.cni);
  assertEquals(without.includes(NODE_NET_UNITS.meshdInstall), false, "a door that ships no meshd asks for none");
  await bare.fulfiller.stop();
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

Deno.test("a door restarted with live claims adopts them and submits no vm.delete", async () => {
  const store = new FakeStore();
  const full = createContractState({ vmName: "ci-node1", requesterDid: OWNER, marketDid: OWNER, at: AT });
  const contract = claimContractOf(advanceContract(full, "network", { accept: ACCEPT, receipt: RECEIPT, submitEventRef: SUBMIT }, AT));
  store.put(claim("ci-node1", { finalizers: [FINALIZER_NODE_CLAIM], status: { phase: "Joining", contract } }));
  store.put(claim("ci-node2", { finalizers: [FINALIZER_NODE_CLAIM], status: { phase: "Running", contract } }));
  store.put(claim("ci-node3", {
    finalizers: [FINALIZER_NODE_CLAIM],
    status: { phase: "Running", contract: { receiptUri: RECEIPT.uri, receiptCid: RECEIPT.cid, submitEventRef: SUBMIT } },
  }));
  const h = harness({ store });
  await h.fulfiller.pass();
  await settle();
  await h.fulfiller.pass();
  await settle();

  assertEquals(h.pds.deletions(), []);
  assertEquals(h.pds.records, []);
  assertEquals(h.contracts, []);
  assertEquals(h.store.statuses, [], "an adopted claim is not rewritten");
  assertEquals(["ci-node1", "ci-node2", "ci-node3"].map((n) => h.store.phase(n)), ["Joining", "Running", "Running"]);
  await h.fulfiller.stop();
});

Deno.test("a door restarted mid-flight marks the orphaned claim Failed, releases nothing, and releases it once deleted", async () => {
  const store = new FakeStore();
  store.put(claim("ci-node1"));
  const before = harness({ store, script: "hang_after_receipt" });
  await before.fulfiller.pass();
  await settle();
  assertEquals(store.phase("ci-node1"), "Provisioning");
  assertEquals(store.contract("ci-node1")?.receipt, RECEIPT);

  const after = harness({ store });
  await after.fulfiller.pass();
  await settle();
  assertEquals(after.contracts, [], "the restarted door requests nothing for a claim that already holds a contract");
  assertEquals(after.pds.deletions(), [], "startup never releases");
  assertEquals(store.phase("ci-node1"), "Failed");
  assertStringIncludes(store.claims.get("ci-node1")!.status.message!, "the door restarted");
  assertEquals(store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);

  deleteClaim(store, "ci-node1");
  await after.fulfiller.pass();
  await settle();
  const deletions = after.pds.deletions();
  assertEquals(deletions.length, 1, "exactly one vm.delete reached the bidder");
  const event = deletions[0].body.record;
  assertEquals({ uri: event.receipt.uri, cid: event.receipt.cid }, RECEIPT);
  assertEquals(after.pds.resolved, [SUBMIT]);
  assertEquals(after.pds.records.filter((r) => r.collection === EVENT_NSID).length, 1);
  assertEquals(store.phase("ci-node1"), "Released");
  assertEquals(store.contract("ci-node1")?.release?.ok, true);
  assertEquals(store.claims.get("ci-node1")!.finalizers, []);

  await after.fulfiller.pass();
  await settle();
  assertEquals(after.pds.deletions().length, 1, "a released contract is not released twice");
});

Deno.test("a deleted Joining claim is released from the receipt on the claim, not the door's memory", async () => {
  const h = harness();
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Joining");

  const moved: RecordRef = { uri: `at://${BIDDER}/market.receipt/as-recorded`, cid: "recorded" };
  h.store.claims.get("ci-node1")!.status.contract!.receipt = moved;
  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();

  const deletions = h.pds.deletions();
  assertEquals(deletions.length, 1);
  assertEquals({ uri: deletions[0].body.record.receipt.uri, cid: deletions[0].body.record.receipt.cid }, moved);
  assertEquals(h.store.phase("ci-node1"), "Released");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assert(h.store.gets > 0, "the claim was read back before the release");
});

Deno.test("a claim deleted while the door waits for the guest's fetch is released from its claim at once", async () => {
  const h = harness({ script: "no_fetch", vmReadyTimeoutSec: 60 });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.store.phase("ci-node1"), "Provisioning");
  assertEquals(h.fulfiller.active(), ["root:acct:ci/ci-node1"]);
  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.store.phase("ci-node1"), "Released");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
  assertEquals(h.secrets.disposed, 1);
  assertEquals(h.fulfiller.active(), []);
});

Deno.test("a guest that never fetches its secrets is Failed, and released only when its claim is deleted", async () => {
  const h = harness({ script: "no_fetch" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settleFor(120);
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertEquals(h.store.claims.get("ci-node1")!.status.message, "a VM was provisioned but it never fetched its join secrets");
  assertEquals(h.pds.deletions(), []);
  assertEquals(h.secrets.disposed, 1);
  assertEquals(h.fulfiller.active(), []);

  deleteClaim(h.store, "ci-node1");
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
});

Deno.test("a grant that cannot be written stops the flow once the receipt is known and fails the claim", async () => {
  const store = new FakeStore();
  store.failGrantWrites = true;
  const h = harness({ store });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.decisions.onAccepted, ["continue"], "returning at accept could lose a receipt that has not arrived");
  assertEquals(h.decisions.onReceipt, ["return"]);
  assertEquals(h.store.contract("ci-node1")?.receipt, RECEIPT, "the VM stays releasable from its claim");
  assertEquals(h.secrets.grants, [], "the guest is never authorized without a grant on its claim");
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertStringIncludes(h.store.claims.get("ci-node1")!.status.message!, "workload grant could not be written");
});

Deno.test("a cluster whose OpenBao link has no ticket fails the claim without starting a contract", async () => {
  const h = harness({ links: new FakeLinks(null) });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 0);
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertStringIncludes(h.store.claims.get("ci-node1")!.status.message!, `${OWNER_CLUSTER_KEY}-openbao carries no ticket`);
});

Deno.test("an unreadable OpenBao CA fails the claim without starting a contract", async () => {
  const h = harness();
  h.store.put(claim("ci-node1", { spec: { ...claim("ci-node1").spec, tokenFile: "/elsewhere/token" } }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 0);
  assertEquals(h.store.phase("ci-node1"), "Failed");
});

Deno.test("stopping the door leaves in-flight claims for the next door and releases nothing", async () => {
  const h = harness({ script: "no_fetch", vmReadyTimeoutSec: 60 });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  await h.fulfiller.stop();
  assertEquals(h.pds.deletions(), []);
  assertEquals(h.store.phase("ci-node1"), "Provisioning");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);
});

Deno.test("the door admits declared bidders and omits a policy it was not given", async () => {
  const store = new FakeStore();
  const contracts: Record<string, unknown>[] = [];
  const fulfiller = createMarketNodeFulfiller({
    store,
    links: new FakeLinks(),
    requesterFor: () => Promise.resolve(new FakeRequesterPds() as never),
    readText,
    eventStreams: () => ({ close() {} }),
    secretsCapability: new FakeSecretsCapabilities().factory,
    extraBidderDids: ["did:plc:mine"],
    requestCompute: scriptedRequest("no_bids", contracts, { onSecretsFetched: [], onAccepted: [], onReceipt: [], grantAtAccept: [] }, store) as never,
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
  assertEquals(h.contracts[0].vmName, "ci-node1");
  const contract = h.store.contract("ci-node1")!;
  assertEquals(contract.receiptUri, RECEIPT.uri);
  assertEquals(contract.receipt, RECEIPT);
  assertEquals(contract.vmAddress, GUEST);
  assertEquals(h.store.claims.get("ci-node1")!.status.fqdn, GUEST);
  await h.fulfiller.stop();
});

Deno.test("concurrent claims for one owner each get their own contract", async () => {
  const h = harness({ script: "hang_after_network", vmReadyTimeoutSec: 60 });
  h.store.put(claim("ci-node1"));
  h.store.put(claim("ci-node2"));
  h.store.put(claim("ci-node3", { workspace: "root:acct:other", spec: { ...claim("x").spec, cluster: "d90m41be-other", nodeName: "other-node1" } }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 3);
  assertEquals(h.requested, [OWNER, OWNER, OWNER]);
  assertEquals(h.fulfiller.active().length, 3);
  assertEquals(h.links.asked.includes(`root:acct:other/d90m41be-other-openbao`), true);
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 3, "a live claim is not fulfilled twice");
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

Deno.test("an unreadable claim at deletion defers the release to the next pass, which reads it again", async () => {
  const h = harness({ script: "no_fetch" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settleFor(120);
  deleteClaim(h.store, "ci-node1");
  h.store.unreadable = true;
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions(), [], "no claim, no release");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, [FINALIZER_NODE_CLAIM]);

  h.store.unreadable = false;
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.store.phase("ci-node1"), "Released");
});

Deno.test("a deleted claim the door could not confirm released is Failed, and its finalizer still goes", async () => {
  const h = harness({ bidderOk: false });
  h.store.put(claim("ci-node1", {
    deletionTimestamp: "2026-10-06T00:00:00Z",
    finalizers: [FINALIZER_NODE_CLAIM],
    status: { phase: "Running", contract: { receiptUri: RECEIPT.uri, receiptCid: RECEIPT.cid, submitEventRef: SUBMIT } },
  }));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.pds.deletions().length, 1);
  assertEquals(h.store.phase("ci-node1"), "Failed");
  assertEquals(h.store.claims.get("ci-node1")!.status.message, "the claim was deleted but the node's VM could not be confirmed released");
  assertEquals(h.store.claims.get("ci-node1")!.finalizers, []);
});

Deno.test("no bids is a failure that says so, keeps the rfp it asked with and names no accept", async () => {
  const h = harness({ script: "no_bids" });
  h.store.put(claim("ci-node1"));
  await h.fulfiller.pass();
  await settle();
  const status = h.store.claims.get("ci-node1")!.status;
  assertEquals(status.phase, "Failed");
  assertEquals(status.message, "no bids arrived for this node");
  assertEquals(status.contract?.rfpUri, RFP.uri);
  assertEquals(status.contract?.acceptUri, undefined);
  assertEquals(status.contract?.outcome, "no_bids");
  assertEquals(status.grant, undefined);
  assertEquals(h.pds.deletions(), []);
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

Deno.test("every contract the door starts shares the one host scope cache it was given", async () => {
  const scopeCache = { marker: "the door's one ScopeCache" };
  const h = harness({ scopeCache });
  h.store.put(claim("ci-node1"));
  h.store.put(claim("ci-node2"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 2);
  for (const c of h.contracts) {
    assertEquals(c.scopeCache, scopeCache, "a contract ran without the door's scope cache, so its bidder's trust verdict is computed again from nothing");
  }
});

Deno.test("every contract shares the door's one scope refresher, so a bidder's verdict stays warm between contracts", async () => {
  const scopeRefresher = { marker: "the door's one ScopeRefresher" };
  const h = harness({ scopeRefresher });
  h.store.put(claim("ci-node1"));
  h.store.put(claim("ci-node2"));
  await h.fulfiller.pass();
  await settle();
  assertEquals(h.contracts.length, 2);
  for (const c of h.contracts) assertEquals(c.scopeRefresher, scopeRefresher);
});
