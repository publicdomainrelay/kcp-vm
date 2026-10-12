import { assert, assertEquals, assertStringIncludes } from "@std/assert";
import {
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
  EVENT_NSID,
  RFP_NSID,
  SUBMIT_ACCEPT_NSID,
} from "@publicdomainrelay/market-common";
import { attestationFor, loadOrGenerateKeypair, toStorableEntry } from "@publicdomainrelay/market-atproto";
import type { CollectedBid, GuestHostKeys, RequesterPDS, SshSessionProvider } from "@publicdomainrelay/requester-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import {
  hostKeyAliasOf,
  hostKeysOf,
  knownHostsOf,
  parseContractState,
  serializeContractState,
} from "@publicdomainrelay/compute-request-abc";
import type { ComputeEventEnvelope } from "@publicdomainrelay/compute-request-abc";
import { requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestOptions } from "@publicdomainrelay/compute-request-xrpc";
import { createSshSessionProvider, runComputeContract, sshProxyArgs } from "@publicdomainrelay/requester-xrpc";

Deno.env.delete("ATPROTO_DID");

const REQUESTER_DID = "did:plc:requesterfake";
const BIDDER_DID = "did:plc:bidder";
const BIDDER_ENDPOINT = "https://bidder.test";
const TICKET = "iroh://endpointfaketicket";
const RECEIPT = {
  uri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/3lreceipt22`,
  cid: "bafyreceipt",
  submitEvent: BIDDER_ENDPOINT,
};

interface KeyPair {
  dir: string;
  type: string;
  publicKey: string;
  fingerprint: string;
}

async function keyPair(): Promise<KeyPair> {
  const dir = await Deno.makeTempDir({ prefix: "hostkeys-requester-" });
  const gen = await new Deno.Command("ssh-keygen", {
    args: ["-q", "-t", "ed25519", "-N", "", "-f", `${dir}/key`],
  }).output();
  assert(gen.success, "ssh-keygen");
  const [type, publicKey] = (await Deno.readTextFile(`${dir}/key.pub`)).trim().split(/\s+/);
  const fp = await new Deno.Command("ssh-keygen", { args: ["-l", "-E", "sha256", "-f", `${dir}/key.pub`] }).output();
  return { dir, type, publicKey, fingerprint: new TextDecoder().decode(fp.stdout).split(/\s+/)[1] };
}

const GUEST = await keyPair();
const OTHER = await keyPair();
const CLIENT = await keyPair();
globalThis.addEventListener("unload", () => {
  for (const k of [GUEST, OTHER, CLIENT]) Deno.removeSync(k.dir, { recursive: true });
});
const keysOf = (k: KeyPair) => [{ type: k.type, publicKey: k.publicKey, fingerprint: k.fingerprint }];

const bidderKp = await loadOrGenerateKeypair();
let envelopeSeq = 0;

async function signedHostKeys(opts: {
  author?: string;
  receiptUri?: string;
  keys?: unknown;
  tamper?: boolean;
  payloadNsid?: string;
} = {}): Promise<ComputeEventEnvelope> {
  envelopeSeq++;
  const author = opts.author ?? BIDDER_DID;
  const payloadRef = {
    $type: "com.atproto.repo.strongRef",
    uri: `at://${author}/${opts.payloadNsid ?? COMPUTE_EVENTS_VM_HOST_KEYS_NSID}/p${envelopeSeq}`,
    cid: `bafyhostkeyspayload${envelopeSeq}`,
  };
  const record: Record<string, unknown> = {
    $type: EVENT_NSID,
    receipt: { $type: "com.atproto.repo.strongRef", uri: opts.receiptUri ?? RECEIPT.uri, cid: RECEIPT.cid },
    payload: payloadRef,
  };
  const entry = await attestationFor(bidderKp, author).sign({ record, repository: author });
  const signed: Record<string, unknown> = { ...record, signatures: [toStorableEntry(entry as never)] };
  if (opts.tamper) signed.payload = { ...payloadRef, cid: "bafytampered" };
  return {
    uri: `at://${author}/${EVENT_NSID}/e${envelopeSeq}`,
    cid: `bafyhostkeysevent${envelopeSeq}`,
    record: signed,
    payload: {
      $type: COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
      keys: opts.keys ?? keysOf(GUEST),
      observedAt: "2026-10-12T00:57:03.000Z",
      createdAt: "2026-10-12T00:57:04.000Z",
    },
  };
}

function quietLogger(events: Array<{ event: string; extra?: Record<string, unknown> }> = []): StructuredLoggerInterface {
  const push = (event: string, extra?: Record<string, unknown>) => {
    events.push({ event, extra });
  };
  return { info: push, warn: push, error: push, debug: push } as unknown as StructuredLoggerInterface;
}

function fakePds(opts: { bidders?: string[]; autoNetwork?: boolean } = {}) {
  const records: Array<{ collection: string; uri: string; cid: string; record: Record<string, unknown> }> = [];
  const onNetwork = new Map<string, (address: string) => void>();
  const onHostKeys = new Map<string, (envelope: ComputeEventEnvelope) => void>();
  const pendingBids = new Map<string, CollectedBid[]>();
  let seq = 0;
  const write = (collection: string, record: Record<string, unknown>) => {
    seq++;
    const entry = { collection, uri: `at://${REQUESTER_DID}/${collection}/${seq}`, cid: `bafycid${seq}`, record };
    records.push(entry);
    if (collection === RFP_NSID) {
      pendingBids.set(
        entry.uri,
        (opts.bidders ?? []).map((did, i) => ({
          did,
          uri: `at://${did}/com.publicdomainrelay.temp.market.bid/${i}`,
          cid: `bafybid${i}`,
          record: { rfp: { uri: entry.uri, cid: entry.cid }, payload: { cost: 1 }, submitAccept: BIDDER_ENDPOINT },
        })),
      );
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
    callBidder: (_b: string, nsid: string) => {
      if (nsid === SUBMIT_ACCEPT_NSID) return Promise.resolve({ status: 200, ok: true, body: RECEIPT });
      return Promise.resolve({ status: 200, ok: true, body: {} });
    },
    setOnNetworkResolved: (key: string, fn: (address: string) => void) => {
      onNetwork.set(key, fn);
      if (opts.autoNetwork) setTimeout(() => fn(TICKET), 0);
    },
    clearOnNetworkResolved: (key: string) => {
      onNetwork.delete(key);
    },
    setOnHostKeys: (receiptUri: string, fn: (envelope: ComputeEventEnvelope) => void) => {
      onHostKeys.set(receiptUri, fn);
    },
    clearOnHostKeys: (receiptUri: string) => {
      onHostKeys.delete(receiptUri);
    },
  };
  return {
    pds: pds as unknown as RequesterPDS,
    records,
    fireHostKeys: (envelope: ComputeEventEnvelope) => onHostKeys.forEach((fn) => fn(envelope)),
    hostKeysKeys: () => [...onHostKeys.keys()],
    resolveRecord: (ref: { uri: string; cid: string }) => {
      const found = records.find((r) => r.uri === ref.uri);
      return found ? Promise.resolve(found.record) : Promise.reject(new Error(`no record ${ref.uri}`));
    },
  };
}

function baseOpts(fake: ReturnType<typeof fakePds>, extra: Partial<ComputeRequestOptions> = {}): ComputeRequestOptions {
  return {
    vmName: "compute-test",
    logger: quietLogger(),
    policy: { name: "open", args: { bidWindowSec: 0, firstFree: false } },
    vmReadyTimeoutSec: 30,
    sshPublicKey: "ssh-ed25519 AAAAfake test",
    recordResolver: fake.resolveRecord,
    receiptVerifier: () => Promise.resolve({ ok: true, sigOk: true, bindOk: true }),
    ...extra,
  };
}

const RECEIPT_REF = { uri: RECEIPT.uri, cid: RECEIPT.cid };

Deno.test("hostKeysOf binds the event to the contract's receipt and its bidder and refuses unusable keys", async () => {
  const ok = hostKeysOf(await signedHostKeys(), RECEIPT_REF);
  assert(ok.ok, "bound event accepted");
  if (ok.ok) {
    assertEquals(ok.hostKeys.keys, keysOf(GUEST));
    assertEquals(ok.hostKeys.observedAt, "2026-10-12T00:57:03.000Z");
    assert(ok.hostKeys.event.uri.includes(EVENT_NSID));
    assert(ok.hostKeys.payload.uri.includes(COMPUTE_EVENTS_VM_HOST_KEYS_NSID));
  }
  const refused = [
    await signedHostKeys({ receiptUri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/other` }),
    await signedHostKeys({ author: "did:plc:stranger" }),
    await signedHostKeys({ payloadNsid: COMPUTE_EVENTS_VM_DELETE_NSID }),
    await signedHostKeys({ keys: [] }),
    await signedHostKeys({ keys: [{ ...keysOf(GUEST)[0], type: "ssh-ed25519\n* ssh-ed25519" }] }),
    await signedHostKeys({ keys: [{ ...keysOf(GUEST)[0], publicKey: `${GUEST.publicKey} evil` }] }),
    await signedHostKeys({ keys: "ssh-ed25519 AAAA" }),
  ];
  for (const envelope of refused) assertEquals(hostKeysOf(envelope, RECEIPT_REF).ok, false, JSON.stringify(envelope.payload.keys));
});

Deno.test("the known_hosts entry is exactly the reported keys under the receipt's host alias", () => {
  const alias = hostKeyAliasOf(RECEIPT_REF);
  assertEquals(alias, "swc-guest-did-plc-bidder-3lreceipt22");
  assertEquals(knownHostsOf(alias, keysOf(GUEST)), `${alias} ${GUEST.type} ${GUEST.publicKey}\n`);
  const args = sshProxyArgs("/k/id_ed25519", "dumbpipe connect x", { alias, knownHostsFile: "/k/known_hosts" });
  const joined = args.join(" ");
  assertStringIncludes(joined, "StrictHostKeyChecking=yes");
  assertStringIncludes(joined, "UserKnownHostsFile=/k/known_hosts");
  assertStringIncludes(joined, `HostKeyAlias=${alias}`);
  assertStringIncludes(joined, "GlobalKnownHostsFile=/dev/null");
  assert(!joined.includes("StrictHostKeyChecking=no"));
});

Deno.test("a signed vm.hostKeys sets ContractState.hostKeys and fires onHostKeys; forged and unbound ones are rejected", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const seen: string[] = [];
  const handle = await requestCompute(fake.pds, baseOpts(fake, { logger: quietLogger(events) }), {
    onReceipt: () => {
      setTimeout(async () => {
        fake.fireHostKeys(await signedHostKeys({ tamper: true }));
        fake.fireHostKeys(await signedHostKeys({ author: "did:plc:stranger" }));
        const unsigned = await signedHostKeys();
        delete unsigned.record.signatures;
        fake.fireHostKeys(unsigned);
        await new Promise((r) => setTimeout(r, 50));
        fake.fireHostKeys(await signedHostKeys());
      }, 0);
      return "continue";
    },
    onHostKeys: (e) => {
      seen.push(e.keys[0].fingerprint);
    },
  });
  const hostKeys = await handle.hostKeys!;
  assertEquals(hostKeys.keys, keysOf(GUEST));
  assertEquals(seen, [GUEST.fingerprint]);
  assertEquals(events.filter((e) => e.event === "vm_hostkeys_rejected").length, 3);
  assert(events.some((e) => e.event === "vm_hostkeys_received"));
  await handle.dispose();
  assertEquals(fake.hostKeysKeys(), []);
});

function recordingSsh(calls: Array<{ target: string; hostKeys?: GuestHostKeys }>): SshSessionProvider {
  return {
    generateKeypair: () => Promise.resolve({ publicKey: "ssh-ed25519 AAAAfake test", privateKeyPath: "/nonexistent" }),
    pollReady: (_key, target, _timeoutMs, _signal, hostKeys) => {
      calls.push({ target, hostKeys });
      return Promise.resolve(false);
    },
    runSession: () => Promise.resolve(0),
  };
}

Deno.test("runComputeContract does not ssh to a guest that reported no host keys", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const calls: Array<{ target: string; hostKeys?: GuestHostKeys }> = [];
  const result = await runComputeContract(fake.pds, {
    ...baseOpts(fake, { logger: quietLogger(events) }),
    hostKeysTimeoutSec: 0.2,
    keepVm: true,
    sshProvider: recordingSsh(calls),
  } as never);
  assertEquals(calls, [], "no ssh without the guest's reported host keys");
  assertEquals(result.sshReady, false);
  const missing = events.find((e) => e.event === "vm_hostkeys_missing");
  assert(missing, "the requester logs which receipt is missing its report");
  assertEquals(missing.extra?.receiptUri, RECEIPT.uri);
});

Deno.test("runComputeContract hands the reported keys and the receipt's alias to the ssh provider", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true });
  const calls: Array<{ target: string; hostKeys?: GuestHostKeys }> = [];
  setTimeout(async () => {
    for (let i = 0; i < 4000 && fake.hostKeysKeys().length === 0; i++) await new Promise((r) => setTimeout(r, 5));
    fake.fireHostKeys(await signedHostKeys());
  }, 0);
  const result = await runComputeContract(fake.pds, {
    ...baseOpts(fake),
    keepVm: true,
    sshProvider: recordingSsh(calls),
  } as never);
  assertEquals(calls.length, 1);
  assertEquals(calls[0].target, TICKET);
  assertEquals(calls[0].hostKeys?.alias, hostKeyAliasOf(RECEIPT_REF));
  assertEquals(calls[0].hostKeys?.keys.map((k) => k.publicKey), [GUEST.publicKey]);
  assertEquals(result.sshHostKeyAlias, hostKeyAliasOf(RECEIPT_REF));
  assertEquals(result.hostKeyFingerprints, [GUEST.fingerprint]);
});

Deno.test("ContractState.hostKeys survives serialization", async () => {
  const bound = hostKeysOf(await signedHostKeys(), RECEIPT_REF);
  assert(bound.ok);
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const handle = await requestCompute(fake.pds, baseOpts(fake), { onReceipt: () => "return" });
  await handle.dispose();
  const state = parseContractState(serializeContractState({ ...handle.state, hostKeys: bound.hostKeys }));
  assertEquals(state.hostKeys?.keys, keysOf(GUEST));
});

Deno.test("ssh verifies the guest against the reported key: a mismatched key fails host-key verification and never authenticates", async () => {
  const hostDir = GUEST.dir;
  const proxy = () =>
    `/usr/sbin/sshd -i -f /dev/null -h ${hostDir}/key -o StrictModes=no -o UsePAM=no -o PidFile=none`;
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const ssh = createSshSessionProvider(quietLogger(events), { proxyCommandFn: proxy });
  const alias = hostKeyAliasOf(RECEIPT_REF);
  const errors = () =>
    events.filter((e) => e.event === "vm_ssh_poll").map((e) => String(e.extra?.fullError ?? "")).join("\n");
  const mismatch = () => events.some((e) => e.event === "vm_ssh_host_key_mismatch");

  const matched = await ssh.pollReady(`${CLIENT.dir}/key`, TICKET, 1_500, undefined, { alias, keys: keysOf(GUEST) });
  assertEquals(matched, false, "the stand-in sshd authenticates no one");
  assert(!mismatch(), errors());
  assertStringIncludes(errors(), "Permission denied", "the session reached authentication");

  events.length = 0;
  const mismatched = await ssh.pollReady(`${CLIENT.dir}/key`, TICKET, 1_500, undefined, { alias, keys: keysOf(OTHER) });
  assertEquals(mismatched, false);
  assert(mismatch(), "the mismatch is reported as such");
  assertStringIncludes(errors(), "REMOTE HOST IDENTIFICATION HAS CHANGED");
  assert(!errors().includes("Permission denied"), "a mismatched host key never reaches authentication");
  assertEquals(events.filter((e) => e.event === "vm_ssh_poll").length, 1, "a mismatch is not retried");

  events.length = 0;
  const none = await ssh.pollReady(`${CLIENT.dir}/key`, TICKET, 1_500);
  assertEquals(none, false);
  assertEquals(events.filter((e) => e.event === "vm_ssh_poll"), [], "no ssh is attempted without host keys");
  assert(events.some((e) => e.event === "vm_ssh_refused_no_host_keys"));
  const code = await ssh.runSession(`${CLIENT.dir}/key`, TICKET, "true");
  assertEquals(code, 255);
});
