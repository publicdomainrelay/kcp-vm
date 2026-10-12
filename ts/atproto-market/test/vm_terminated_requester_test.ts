import { assert, assertEquals } from "@std/assert";
import {
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  EVENT_NSID,
  RFP_NSID,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_EVENT_NSID,
} from "@publicdomainrelay/market-common";
import { attestationFor, loadOrGenerateKeypair, toStorableEntry } from "@publicdomainrelay/market-atproto";
import type { CollectedBid, RequesterPDS, SshSessionProvider } from "@publicdomainrelay/requester-abc";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { parseContractState, serializeContractState, terminationOf } from "@publicdomainrelay/compute-request-abc";
import type { ComputeEventEnvelope, ContractState } from "@publicdomainrelay/compute-request-abc";
import { awaitComputeNetwork, releaseCompute, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestOptions } from "@publicdomainrelay/compute-request-xrpc";
import { runComputeContract } from "@publicdomainrelay/requester-xrpc";

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

const bidderKp = await loadOrGenerateKeypair();
let envelopeSeq = 0;

async function signedTerminated(opts: {
  author?: string;
  receiptUri?: string;
  reason?: string;
  exitCode?: number;
  tamper?: boolean;
} = {}): Promise<ComputeEventEnvelope> {
  envelopeSeq++;
  const author = opts.author ?? BIDDER_DID;
  const payloadRef = {
    $type: "com.atproto.repo.strongRef",
    uri: `at://${author}/${COMPUTE_EVENTS_VM_TERMINATED_NSID}/p${envelopeSeq}`,
    cid: `bafyterminatedpayload${envelopeSeq}`,
  };
  const record: Record<string, unknown> = {
    $type: EVENT_NSID,
    receipt: { $type: "com.atproto.repo.strongRef", uri: opts.receiptUri ?? RECEIPT.uri, cid: RECEIPT.cid },
    payload: payloadRef,
  };
  const entry = await attestationFor(bidderKp, author).sign({ record, repository: author });
  const signed: Record<string, unknown> = { ...record, signatures: [toStorableEntry(entry as never)] };
  if (opts.tamper) {
    signed.payload = { ...payloadRef, cid: "bafytampered" };
  }
  return {
    uri: `at://${author}/${EVENT_NSID}/e${envelopeSeq}`,
    cid: `bafyterminatedevent${envelopeSeq}`,
    record: signed,
    payload: {
      $type: COMPUTE_EVENTS_VM_TERMINATED_NSID,
      reason: opts.reason ?? "guest_poweroff",
      ...(opts.exitCode !== undefined ? { exitCode: opts.exitCode } : {}),
      observedAt: "2026-10-12T00:57:03.000Z",
      createdAt: "2026-10-12T00:57:04.000Z",
    },
  };
}

async function signedHostKeys(): Promise<ComputeEventEnvelope> {
  envelopeSeq++;
  const payloadRef = {
    $type: "com.atproto.repo.strongRef",
    uri: `at://${BIDDER_DID}/${COMPUTE_EVENTS_VM_HOST_KEYS_NSID}/p${envelopeSeq}`,
    cid: `bafyhostkeyspayload${envelopeSeq}`,
  };
  const record: Record<string, unknown> = {
    $type: EVENT_NSID,
    receipt: { $type: "com.atproto.repo.strongRef", uri: RECEIPT.uri, cid: RECEIPT.cid },
    payload: payloadRef,
  };
  const entry = await attestationFor(bidderKp, BIDDER_DID).sign({ record, repository: BIDDER_DID });
  return {
    uri: `at://${BIDDER_DID}/${EVENT_NSID}/e${envelopeSeq}`,
    cid: `bafyhostkeysevent${envelopeSeq}`,
    record: { ...record, signatures: [toStorableEntry(entry as never)] },
    payload: {
      $type: COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
      keys: [{
        type: "ssh-ed25519",
        publicKey: "AAAAC3NzaC1lZDI1NTE5AAAAIGWqx0pQ7m3nB9P2cCjv1xTt0lI7yU6m0O4m8r1yqk8H",
        fingerprint: "SHA256:3kQ0d2xv1Y8Yb1oQe1c8v0m4m4b3tC2Yp5x9yA7mJ0c",
      }],
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

function fakePds(opts: { bidders?: string[]; autoNetwork?: boolean; autoHostKeys?: boolean } = {}) {
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
    callBidder: (_b: string, nsid: string, _l: string, _a: string, body: Record<string, unknown>) => {
      calls.push({ nsid, body });
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
    setOnTerminated: (receiptUri: string, fn: (envelope: ComputeEventEnvelope) => void) => {
      onTerminated.set(receiptUri, fn);
    },
    clearOnTerminated: (receiptUri: string) => {
      onTerminated.delete(receiptUri);
    },
    setOnHostKeys: (_receiptUri: string, fn: (envelope: ComputeEventEnvelope) => void) => {
      if (opts.autoHostKeys) setTimeout(async () => fn(await signedHostKeys()), 0);
    },
    clearOnHostKeys: () => {},
  };
  return {
    pds: pds as unknown as RequesterPDS,
    calls,
    records,
    fireNetwork: (address: string) => onNetwork.forEach((fn) => fn(address)),
    fireTerminated: (envelope: ComputeEventEnvelope) => onTerminated.forEach((fn) => fn(envelope)),
    terminatedKeys: () => [...onTerminated.keys()],
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

async function until(cond: () => boolean, what: string, timeoutMs = 3_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

Deno.test("terminationOf binds the event to the contract's receipt and its bidder", async () => {
  const receipt = { uri: RECEIPT.uri, cid: RECEIPT.cid };
  const ok = terminationOf(await signedTerminated({ exitCode: 0 }), receipt);
  assert(ok.ok, "bound event accepted");
  if (ok.ok) {
    assertEquals(ok.termination.reason, "guest_poweroff");
    assertEquals(ok.termination.exitCode, 0);
    assertEquals(ok.termination.observedAt, "2026-10-12T00:57:03.000Z");
    assert(ok.termination.event.uri.includes(EVENT_NSID));
    assert(ok.termination.payload.uri.includes(COMPUTE_EVENTS_VM_TERMINATED_NSID));
  }
  const otherReceipt = terminationOf(
    await signedTerminated({ receiptUri: `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/2` }),
    receipt,
  );
  assertEquals(otherReceipt.ok, false);
  const stranger = terminationOf(await signedTerminated({ author: "did:plc:stranger" }), receipt);
  assertEquals(stranger.ok, false);
  const wrongPayload = await signedTerminated();
  (wrongPayload.record.payload as { uri: string }).uri = `at://${BIDDER_DID}/${COMPUTE_EVENTS_VM_DELETE_NSID}/x`;
  assertEquals(terminationOf(wrongPayload, receipt).ok, false);
  const noReason = await signedTerminated();
  delete noReason.payload.reason;
  assertEquals(terminationOf(noReason, receipt).ok, false);
});

Deno.test("a signed vm.terminated ends the network wait as terminated and fires onTerminated", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const seen: string[] = [];
  const started = Date.now();
  const handle = await requestCompute(fake.pds, baseOpts(fake, { logger: quietLogger(events) }), {
    onReceipt: () => {
      setTimeout(async () => fake.fireTerminated(await signedTerminated({ exitCode: 0 })), 0);
      return "continue";
    },
    onTerminated: (e, s) => {
      seen.push(`${e.reason}:${s.termination?.reason}`);
    },
  });

  assert(Date.now() - started < 10_000, "the network wait ended on termination");
  assertEquals(handle.state.outcome, "returned");
  assertEquals(handle.state.phase, "receipt");
  assertEquals(handle.state.termination?.reason, "guest_poweroff");
  assertEquals(handle.state.termination?.exitCode, 0);
  assertEquals(handle.state.vmAddress, undefined);
  assertEquals(seen, ["guest_poweroff:guest_poweroff"]);
  assert(events.some((e) => e.event === "vm_terminated_by_bidder"));
  await handle.dispose();
  assertEquals(fake.terminatedKeys(), []);
});

Deno.test("forged and unbound vm.terminated events are rejected; the bound one wins", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const handle = await requestCompute(fake.pds, baseOpts(fake, { logger: quietLogger(events) }), {
    onReceipt: () => {
      setTimeout(async () => {
        fake.fireTerminated(await signedTerminated({ tamper: true, reason: "oom" }));
        fake.fireTerminated(await signedTerminated({ author: "did:plc:stranger", reason: "host_reboot" }));
        const unsigned = await signedTerminated({ reason: "identity_mismatch" });
        delete unsigned.record.signatures;
        fake.fireTerminated(unsigned);
        await new Promise((r) => setTimeout(r, 50));
        fake.fireTerminated(await signedTerminated({ reason: "vmm_exited", exitCode: 1 }));
      }, 0);
      return "continue";
    },
  });

  assertEquals(handle.state.outcome, "returned");
  assertEquals(handle.state.termination?.reason, "vmm_exited");
  const rejected = events.filter((e) => e.event === "vm_terminated_rejected");
  assertEquals(rejected.length, 3);
  await handle.dispose();
});

Deno.test("a termination after the flow completed resolves the handle's terminated promise", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true });
  const handle = await requestCompute(fake.pds, baseOpts(fake), {});
  assertEquals(handle.state.outcome, "complete");
  assertEquals(fake.terminatedKeys(), [RECEIPT.uri], "the handle keeps listening until disposed");
  fake.fireTerminated(await signedTerminated({ reason: "host_reboot" }));
  const after = await handle.terminated!;
  assertEquals(after.outcome, "complete");
  assertEquals(after.termination?.reason, "host_reboot");
  await handle.dispose();
  assertEquals(fake.terminatedKeys(), []);
});

Deno.test("awaitComputeNetwork returns terminated from a saved state", async () => {
  const first = fakePds({ bidders: [BIDDER_DID] });
  const handle = await requestCompute(first.pds, baseOpts(first), { onReceipt: () => "return" });
  await handle.dispose();
  const saved: ContractState = parseContractState(serializeContractState(handle.state));

  const second = fakePds();
  const seen: string[] = [];
  const waiting = awaitComputeNetwork(second.pds as never, saved, {
    logger: quietLogger(),
    timeoutMs: 20_000,
    backfill: false,
    onTerminated: (e) => {
      seen.push(e.reason);
    },
  });
  await until(() => second.terminatedKeys().length === 1, "terminated registration");
  second.fireTerminated(await signedTerminated({ reason: "oom" }));
  const resumed = await waiting;
  assertEquals(resumed.outcome, "returned");
  assertEquals(resumed.phase, "receipt");
  assertEquals(resumed.termination?.reason, "oom");
  assertEquals(seen, ["oom"]);
  assertEquals(second.terminatedKeys(), []);
  assertEquals(parseContractState(serializeContractState(resumed)).termination?.reason, "oom");
});

Deno.test("releaseCompute does not submit a vm.delete for a contract the bidder terminated", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID] });
  const handle = await requestCompute(fake.pds, baseOpts(fake), {
    onReceipt: () => {
      setTimeout(async () => fake.fireTerminated(await signedTerminated()), 0);
      return "continue";
    },
  });
  await handle.dispose();
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  const after = await releaseCompute(fake.pds, handle.state, { logger: quietLogger(events) });
  assertEquals(fake.calls.filter((c) => c.nsid === SUBMIT_EVENT_NSID), []);
  assertEquals(fake.records.filter((r) => r.collection === COMPUTE_EVENTS_VM_DELETE_NSID), []);
  assertEquals(after.termination?.reason, "guest_poweroff");
  assert(events.some((e) => e.event === "vm_delete_skipped" && e.extra?.reason === "terminated_by_bidder"));
});

Deno.test("runComputeContract stops waiting for host keys when the bidder terminates the guest", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  let polled = false;
  const sshProvider: SshSessionProvider = {
    generateKeypair: () => Promise.resolve({ publicKey: "ssh-ed25519 AAAAfake test", privateKeyPath: "/nonexistent" }),
    pollReady: () => {
      polled = true;
      return Promise.resolve(false);
    },
    runSession: () => Promise.resolve(0),
  };
  const started = Date.now();
  setTimeout(async () => {
    while (fake.terminatedKeys().length === 0 && Date.now() - started < 10_000) {
      await new Promise((r) => setTimeout(r, 20));
    }
    await new Promise((r) => setTimeout(r, 200));
    fake.fireTerminated(await signedTerminated({ exitCode: 0 }));
  }, 0);
  const result = await runComputeContract(fake.pds, {
    ...baseOpts(fake, { logger: quietLogger(events) }),
    sshProvider,
  } as never);
  assert(Date.now() - started < 10_000, `host-key wait ended promptly (${Date.now() - started}ms)`);
  assertEquals(polled, false, "no ssh without host keys");
  assertEquals(result.sshReady, false);
  assertEquals(result.terminated?.reason, "guest_poweroff");
});

Deno.test("runComputeContract stops waiting on ssh when the bidder terminates the guest", async () => {
  const fake = fakePds({ bidders: [BIDDER_DID], autoNetwork: true, autoHostKeys: true });
  const events: Array<{ event: string; extra?: Record<string, unknown> }> = [];
  let aborted = false;
  const sshProvider: SshSessionProvider = {
    generateKeypair: () => Promise.resolve({ publicKey: "ssh-ed25519 AAAAfake test", privateKeyPath: "/nonexistent" }),
    pollReady: (_key, _target, timeoutMs, signal) => {
      setTimeout(async () => fake.fireTerminated(await signedTerminated({ exitCode: 0 })), 0);
      return new Promise<boolean>((resolve) => {
        const timer = setTimeout(() => resolve(false), timeoutMs);
        signal?.addEventListener("abort", () => {
          aborted = true;
          clearTimeout(timer);
          resolve(false);
        }, { once: true });
      });
    },
    runSession: () => Promise.resolve(0),
  };
  const started = Date.now();
  const result = await runComputeContract(fake.pds, {
    ...baseOpts(fake, { logger: quietLogger(events) }),
    sshProvider,
  } as never);

  assert(Date.now() - started < 10_000, `ssh wait ended promptly (${Date.now() - started}ms)`);
  assert(aborted, "the ssh poll was aborted");
  assertEquals(result.sshReady, false);
  assertEquals(result.terminated?.reason, "guest_poweroff");
  assertEquals(fake.calls.filter((c) => c.nsid === SUBMIT_EVENT_NSID), [], "no redundant vm.delete");
  assert(events.some((e) => e.event === "vm_terminated_by_bidder"));
  assert(events.some((e) => e.event === "vm_session_ended_by_bidder"));
  assertEquals(fake.terminatedKeys(), []);
});
