import { assert, assertEquals } from "@std/assert";
import { createVmBidderCallbacks, terminatedReasonOf } from "@publicdomainrelay/market-bidder-compute";
import { TerminatedReceipts } from "@publicdomainrelay/market-bidder-abc";
import type { ActiveContract } from "@publicdomainrelay/market-bidder-abc";
import type { ComputeProvider, GuestGone } from "@publicdomainrelay/compute-provider-abc";
import type { EventDispatchContext } from "@publicdomainrelay/market-atproto";
import {
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  EVENT_NSID,
  SUBMIT_EVENT_NSID,
} from "@publicdomainrelay/market-common";

const BIDDER_DID = "did:plc:bidder";
const REQUESTER_DID = "did:plc:requester";
const REQUESTER_SUBMIT_EVENT = "https://did-key-requester.relay.test";
const RECEIPT_URI = `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/1`;
const RECEIPT_CID = "bafyreceiptoriginal";
const RECEIPT_KEY = `${RECEIPT_URI}#${RECEIPT_CID}`;
const PROVIDER_ID = "fc-1379be663dd9-0319b646";

interface Written {
  collection: string;
  uri: string;
  cid: string;
  record: Record<string, unknown>;
  signed: boolean;
}

interface Logged {
  level: string;
  msg: string;
  meta?: Record<string, unknown>;
}

function harness(opts: { submitEventUrl?: string } = { submitEventUrl: REQUESTER_SUBMIT_EVENT }) {
  const listeners = new Set<(gone: GuestGone) => void>();
  const destroyed: Array<string | number> = [];
  const destroyGate = { wait: Promise.resolve() };
  const provider: ComputeProvider = {
    name: "fake",
    provision: () => Promise.reject(new Error("this test provisions nothing")),
    destroy: async (id) => {
      await destroyGate.wait;
      destroyed.push(id);
    },
    createBidConfig: () => Promise.reject(new Error("this test bids nothing")),
    injectAcceptBundle: (userData: string) => userData,
    onGuestGone: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
  const written: Written[] = [];
  const submitted: Array<{ url: string; nsid: string; body: Record<string, unknown> }> = [];
  const logged: Logged[] = [];
  let seq = 0;
  const write = (collection: string, record: Record<string, unknown>, signed: boolean) => {
    seq++;
    const entry = {
      collection,
      uri: `at://${BIDDER_DID}/${collection}/${seq}`,
      cid: `bafywritten${seq}`,
      record: signed ? { ...record, signatures: [{ key: "did:key:zbidder" }] } : record,
      signed,
    };
    written.push(entry);
    return entry;
  };
  const activeContracts = new Map<string, ActiveContract>([[RECEIPT_KEY, {
    providerIdPromise: Promise.resolve(PROVIDER_ID),
    acceptAuthor: REQUESTER_DID,
    receiptUri: RECEIPT_URI,
    receiptCid: RECEIPT_CID,
    acceptedAt: "2026-10-12T00:00:00.000Z",
    submitEventUrl: opts.submitEventUrl,
  }]]);
  const log = (level: string, msg: string, meta?: Record<string, unknown>) => {
    logged.push({ level, msg, meta });
  };
  const callbacks = createVmBidderCallbacks({
    did: BIDDER_DID,
    computeProvider: provider,
    log,
    activeContracts,
    createRepoRecord: (collection: string, record: Record<string, unknown>) => {
      const e = write(collection, record, false);
      return Promise.resolve({ uri: e.uri, cid: e.cid });
    },
    createSignedRepoRecord: (collection: string, record: Record<string, unknown>) => {
      const e = write(collection, record, true);
      return Promise.resolve({ uri: e.uri, cid: e.cid, record: e.record });
    },
    callService: (url: string, nsid: string, _lxm: string, body: Record<string, unknown>) => {
      submitted.push({ url, nsid, body });
      return Promise.resolve({ status: 200, ok: true, body: { ok: true } });
    },
  } as never);
  const vmDelete = callbacks.event.pdr_temp_compute_event[COMPUTE_EVENTS_VM_DELETE_NSID];

  let deleteSeq = 0;
  const deleteCtx = (receiptCid = RECEIPT_CID, issuerDid = REQUESTER_DID): EventDispatchContext => {
    deleteSeq++;
    return {
      uri: `at://${REQUESTER_DID}/${EVENT_NSID}/d${deleteSeq}`,
      cid: `bafydeleteevent${deleteSeq}`,
      event: {
        receipt: { $type: "com.atproto.repo.strongRef", uri: RECEIPT_URI, cid: receiptCid },
        payload: {
          $type: "com.atproto.repo.strongRef",
          uri: `at://${REQUESTER_DID}/${COMPUTE_EVENTS_VM_DELETE_NSID}/p${deleteSeq}`,
          cid: `bafydeletepayload${deleteSeq}`,
        },
      },
      payloadNsid: COMPUTE_EVENTS_VM_DELETE_NSID,
      issuerDid,
      serviceId: "pdr_temp_compute_event",
      resolve: { resolve: () => Promise.reject(new Error("unused")) },
      log,
      req: new Request("https://localhost"),
    } as unknown as EventDispatchContext;
  };

  return {
    activeContracts,
    destroyed,
    destroyGate,
    written,
    submitted,
    logged,
    vmDelete,
    deleteCtx,
    lose: (gone: GuestGone) => listeners.forEach((l) => l(gone)),
    terminatedPayloads: () => written.filter((w) => w.collection === COMPUTE_EVENTS_VM_TERMINATED_NSID),
    terminatedEvents: () =>
      written.filter((w) =>
        w.collection === EVENT_NSID &&
        ((w.record.payload as { uri?: string } | undefined)?.uri ?? "").includes(
          COMPUTE_EVENTS_VM_TERMINATED_NSID,
        )
      ),
  };
}

async function until(cond: () => boolean, what: string, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 5));
  }
}

async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 1));
}

Deno.test("provider gone reasons map onto the lexicon's known values", () => {
  assertEquals(terminatedReasonOf({ id: "a", reason: "runner container exited", exitCode: 0 }), {
    reason: "guest_poweroff",
    exitCode: 0,
  });
  assertEquals(terminatedReasonOf({ id: "a", reason: "runner container exited", exitCode: 1 }), {
    reason: "vmm_exited",
    exitCode: 1,
  });
  assertEquals(terminatedReasonOf({ id: "a", reason: "runner container removed" }), { reason: "vmm_exited" });
  assertEquals(terminatedReasonOf({ id: "a", reason: "runner container exited", exitCode: 137 }).reason, "vmm_exited");
  assertEquals(terminatedReasonOf({ id: "a", reason: "guest killed: out of memory" }).reason, "oom");
  assertEquals(terminatedReasonOf({ id: "a", reason: "oom" }).reason, "oom");
  assertEquals(terminatedReasonOf({ id: "a", reason: "host_reboot" }).reason, "host_reboot");
  assertEquals(terminatedReasonOf({ id: "a", reason: "identity_mismatch" }).reason, "identity_mismatch");
});

Deno.test("a guest that dies on its own publishes a signed vm.terminated and submits it to the requester", async () => {
  const h = harness();
  h.lose({ id: PROVIDER_ID, reason: "runner container exited", exitCode: 0 });
  await until(() => h.submitted.length === 1, "vm.terminated submitEvent");

  const [payload] = h.terminatedPayloads();
  assertEquals(payload.record.$type, COMPUTE_EVENTS_VM_TERMINATED_NSID);
  assertEquals(payload.record.reason, "guest_poweroff");
  assertEquals(payload.record.exitCode, 0);
  assert(typeof payload.record.observedAt === "string");
  assert(typeof payload.record.createdAt === "string");
  assertEquals(payload.record.request, undefined);

  const [event] = h.terminatedEvents();
  assert(event.signed, "the market.event envelope carries the bidder's inline signature");
  assertEquals(event.record.$type, EVENT_NSID);
  assertEquals((event.record.receipt as { uri: string }).uri, RECEIPT_URI);
  assertEquals((event.record.receipt as { cid: string }).cid, RECEIPT_CID);
  assertEquals((event.record.payload as { uri: string }).uri, payload.uri);
  assertEquals((event.record.payload as { cid: string }).cid, payload.cid);

  assertEquals(h.submitted[0].url, REQUESTER_SUBMIT_EVENT);
  assertEquals(h.submitted[0].nsid, SUBMIT_EVENT_NSID);
  assertEquals(h.submitted[0].body.uri, event.uri);
  assertEquals(h.submitted[0].body.cid, event.cid);
  assert(h.logged.some((l) => l.msg === "vm.terminated event created on PDS (firehose)"));
  assert(h.logged.some((l) => l.msg === "vm.terminated event submitted to requester"));
  assertEquals(h.destroyed, [], "a guest already gone is not destroyed again");
});

Deno.test("without a requester endpoint vm.terminated goes to the firehose only", async () => {
  const h = harness({ submitEventUrl: undefined });
  h.lose({ id: PROVIDER_ID, reason: "runner container exited", exitCode: 0 });
  await until(() => h.terminatedEvents().length === 1, "vm.terminated event");
  await settle();
  assertEquals(h.submitted, []);
});

Deno.test("vm.delete is acknowledged by vm.terminated deleted_on_request referencing the delete", async () => {
  const h = harness();
  const ctx = h.deleteCtx();
  const res = await h.vmDelete(ctx);
  assertEquals((res as { status?: number } | undefined)?.status ?? 200, 200);
  assertEquals(h.destroyed, [PROVIDER_ID]);
  await until(() => h.submitted.length === 1, "vm.terminated ack");

  const [payload] = h.terminatedPayloads();
  assertEquals(payload.record.reason, "deleted_on_request");
  const deletePayload = (ctx.event as unknown as { payload: { uri: string; cid: string } }).payload;
  assertEquals(payload.record.request, {
    $type: "com.atproto.repo.strongRef",
    uri: deletePayload.uri,
    cid: deletePayload.cid,
  });
  const [event] = h.terminatedEvents();
  assertEquals((event.record.receipt as { uri: string }).uri, RECEIPT_URI);
  assertEquals(h.submitted[0].url, REQUESTER_SUBMIT_EVENT);
  assert(!h.activeContracts.has(RECEIPT_KEY));
});

Deno.test("a second vm.delete for a terminated receipt answers 200 with no second destroy or event", async () => {
  const h = harness();
  await h.vmDelete(h.deleteCtx());
  await until(() => h.submitted.length === 1, "first ack");

  const again = await h.vmDelete(h.deleteCtx("bafyreceiptrewritten"));
  assertEquals((again as { status?: number } | undefined)?.status ?? 200, 200);
  assertEquals((again as { body?: { alreadyTerminated?: boolean } }).body?.alreadyTerminated, true);
  await settle();
  assertEquals(h.destroyed, [PROVIDER_ID]);
  assertEquals(h.terminatedPayloads().length, 1);
  assertEquals(h.submitted.length, 1);
  assert(!h.logged.some((l) => l.msg === "submitEvent: unknown receipt"), "no unknown receipt warning");
  assert(h.logged.some((l) => l.msg === "submitEvent: vm.delete for already-terminated receipt"));
});

Deno.test("a vm.delete after the guest died on its own is idempotent", async () => {
  const h = harness();
  h.lose({ id: PROVIDER_ID, reason: "runner container exited", exitCode: 0 });
  await until(() => h.submitted.length === 1, "gone vm.terminated");

  const res = await h.vmDelete(h.deleteCtx());
  assertEquals((res as { status?: number } | undefined)?.status ?? 200, 200);
  await settle();
  assertEquals(h.destroyed, []);
  assertEquals(h.terminatedPayloads().length, 1);
  assertEquals(h.terminatedPayloads()[0].record.reason, "guest_poweroff");
  assertEquals(h.submitted.length, 1);
  assert(!h.logged.some((l) => l.msg === "submitEvent: unknown receipt"));
});

Deno.test("a vm.delete for a tombstoned receipt from a stranger is forbidden", async () => {
  const h = harness();
  await h.vmDelete(h.deleteCtx());
  const res = await h.vmDelete(h.deleteCtx(RECEIPT_CID, "did:plc:stranger"));
  assertEquals((res as { status?: number }).status, 403);
});

Deno.test("concurrent vm.delete deliveries (HTTP and firehose) destroy once and ack once", async () => {
  const h = harness();
  const gate = Promise.withResolvers<void>();
  h.destroyGate.wait = gate.promise;
  const first = h.vmDelete(h.deleteCtx());
  const second = h.vmDelete(h.deleteCtx());
  await settle();
  gate.resolve();
  const results = await Promise.all([first, second]);
  for (const r of results) assertEquals((r as { status?: number } | undefined)?.status ?? 200, 200);
  await until(() => h.submitted.length >= 1, "ack");
  await settle();
  assertEquals(h.destroyed, [PROVIDER_ID]);
  assertEquals(h.terminatedPayloads().length, 1);
  assertEquals(h.submitted.length, 1);
});

Deno.test("an unknown receipt is still rejected", async () => {
  const h = harness();
  h.activeContracts.clear();
  const res = await h.vmDelete(h.deleteCtx());
  assertEquals((res as { status?: number }).status, 400);
  assert(h.logged.some((l) => l.msg === "submitEvent: unknown receipt"));
});

Deno.test("the terminated-receipt tombstone set is bounded and forgets the oldest", () => {
  const tombstones = new TerminatedReceipts(2);
  tombstones.add("at://b/r/1", { acceptAuthor: "did:plc:a", reason: "guest_poweroff", at: "t1" });
  tombstones.add("at://b/r/2", { acceptAuthor: "did:plc:a", reason: "vmm_exited", at: "t2" });
  tombstones.add("at://b/r/3", { acceptAuthor: "did:plc:a", reason: "deleted_on_request", at: "t3" });
  assertEquals(tombstones.size, 2);
  assertEquals(tombstones.get("at://b/r/1"), undefined);
  assertEquals(tombstones.get("at://b/r/2")?.reason, "vmm_exited");
  assertEquals(tombstones.get("at://b/r/3")?.reason, "deleted_on_request");
  assert(tombstones.has("at://b/r/3"));
});
