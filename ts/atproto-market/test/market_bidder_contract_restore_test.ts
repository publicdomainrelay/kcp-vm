import { assert, assertEquals } from "@std/assert";
import { Hono } from "@hono/hono";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import { createComputeProviderHooks, createVmBidderCallbacks } from "@publicdomainrelay/market-bidder-compute";
import type {
  ActiveContract,
  CallbackSet,
  ContractEvent,
  MarketBidderProviderRef,
} from "@publicdomainrelay/market-bidder-abc";
import type { ComputeProvider, GuestGone } from "@publicdomainrelay/compute-provider-abc";
import type { EventDispatchContext } from "@publicdomainrelay/market-atproto";
import { createFsContractStore } from "@publicdomainrelay/market-bidder-contract-store-fs";
import type { ATProto } from "@publicdomainrelay/atproto-helpers";
import type { ServeHandle } from "@publicdomainrelay/serve";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import {
  COMPUTE_EVENTS_VM_DELETE_NSID,
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  EVENT_NSID,
} from "@publicdomainrelay/market-common";

const BIDDER_DID = "did:plc:restorebidder000000000000";
const REQUESTER_DID = "did:plc:restorerequester000000000";
const RECEIPT_URI = `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/3restored`;
const RECEIPT_CID = "bafyreceiptrestored";
const RECEIPT_KEY = `${RECEIPT_URI}#${RECEIPT_CID}`;
const ACCEPT_URI = `at://${REQUESTER_DID}/com.publicdomainrelay.temp.market.accept/3accept`;
const ACCEPT_CID = "bafyacceptrestored";
const PROVIDER_ID = "fc-1379be663dd9-0319b646";
const SUBMIT_EVENT = "https://did-key-requester.relay.test";

interface StoredContractShape {
  key: string;
  receiptUri: string;
  receiptCid: string;
  acceptUri?: string;
  acceptCid?: string;
  acceptAuthor: string;
  acceptedAt: string;
  providerId?: string | number;
  provider?: string;
  submitEventUrl?: string;
}

interface TombstoneShape {
  receiptUri: string;
  receiptCid?: string;
  acceptUri?: string;
  acceptCid?: string;
  acceptAuthor: string;
  reason: string;
  at: string;
}

function storedContract(): StoredContractShape {
  return {
    key: RECEIPT_KEY,
    receiptUri: RECEIPT_URI,
    receiptCid: RECEIPT_CID,
    acceptUri: ACCEPT_URI,
    acceptCid: ACCEPT_CID,
    acceptAuthor: REQUESTER_DID,
    acceptedAt: "2026-10-12T01:00:00.000Z",
    providerId: PROVIDER_ID,
    provider: "fake",
    submitEventUrl: SUBMIT_EVENT,
  };
}

function memoryStore(contracts: StoredContractShape[] = [], tombstones: TombstoneShape[] = []) {
  const live = new Map(contracts.map((c) => [c.key, c]));
  const dead = new Map(tombstones.map((t) => [t.receiptUri, t]));
  const saves: ContractEvent[] = [];
  const gate = { load: Promise.resolve() };
  let loads = 0;
  return {
    live,
    dead,
    saves,
    gate,
    loads: () => loads,
    async load() {
      await gate.load;
      loads++;
      return { contracts: [...live.values()], tombstones: [...dead.values()] };
    },
    save(event: ContractEvent): Promise<void> {
      saves.push(event);
      if (event.type === "terminated") live.delete(event.key);
      else live.set(event.key, { ...(live.get(event.key) ?? {}), ...event } as StoredContractShape);
      return Promise.resolve();
    },
    saveTombstone(t: TombstoneShape): Promise<void> {
      dead.set(t.receiptUri, t);
      return Promise.resolve();
    },
  };
}

type AdoptAnswer = { state: "running" } | { state: "gone"; gone: GuestGone };

function fakeProvider(adopt?: (id: string | number) => AdoptAnswer) {
  const destroyed: Array<string | number> = [];
  const adopted: Array<string | number> = [];
  let provisioned = 0;
  const provider = {
    name: "fake",
    provision: () => {
      provisioned++;
      return Promise.resolve({ providerId: "fc-new-00000000", metadata: {} });
    },
    destroy: (id: string | number) => {
      destroyed.push(id);
      return Promise.resolve();
    },
    createBidConfig: () => Promise.reject(new Error("this test bids nothing")),
    injectAcceptBundle: (userData: string) => userData,
    onGuestGone: () => () => {},
    ...(adopt
      ? {
        adopt: (id: string | number) => {
          adopted.push(id);
          return Promise.resolve(adopt(id));
        },
      }
      : {}),
  } as ComputeProvider;
  return { provider, destroyed, adopted, provisioned: () => provisioned };
}

function fakeServe(): ServeHandle {
  return {
    app: new Hono(),
    tcpPort: 0,
    addRelay: () => {},
    onConnected: () => {},
    beginServe: () => Promise.resolve(),
    shutdown: () => {},
  } as unknown as ServeHandle;
}

interface Written {
  collection: string;
  record: Record<string, unknown>;
}

function fakeAtproto(written: Written[]): ATProto {
  let seq = 0;
  const write = (collection: string, record: Record<string, unknown>) => {
    seq++;
    written.push({ collection, record });
    return { uri: `at://${BIDDER_DID}/${collection}/${seq}`, cid: `bafywritten${seq}`, record };
  };
  return {
    did: BIDDER_DID,
    getAgentDid: () => BIDDER_DID,
    signer: { did: () => BIDDER_DID, sign: () => Promise.resolve(new Uint8Array()) },
    attestationKp: {} as never,
    idResolver: { did: { resolve: () => Promise.resolve(null) } } as never,
    listRecords: () => Promise.resolve({ records: [] }),
    getRecord: () => Promise.resolve(null),
    createRecord: (collection: string, record: Record<string, unknown>) => Promise.resolve(write(collection, record)),
    updateRecord: (collection: string, record: Record<string, unknown>) => Promise.resolve(write(collection, record)),
    createRepoRecord: (collection: string, record: Record<string, unknown>) => {
      const e = write(collection, record);
      return Promise.resolve({ uri: e.uri, cid: e.cid });
    },
    createSignedRepoRecord: (collection: string, record: Record<string, unknown>) =>
      Promise.resolve(write(collection, { ...record, signatures: [{ key: "did:key:zbidder" }] })),
    deleteRecord: () => Promise.resolve(),
    callService: () => Promise.resolve({ status: 200, ok: true, body: {} }),
  } as unknown as ATProto;
}

function quietLogger(lines: string[] = []): StructuredLoggerInterface {
  const at = (level: string) => (msg: string) => {
    lines.push(`${level} ${msg}`);
  };
  return { info: at("info"), warn: at("warn"), error: at("error"), debug: at("debug") } as never;
}

function capturing(ref: MarketBidderProviderRef) {
  const captured: { set?: CallbackSet } = {};
  const wrapped: MarketBidderProviderRef = {
    ...ref,
    buildCallbacks(deps) {
      captured.set = ref.buildCallbacks(deps);
      return captured.set;
    },
  };
  return { wrapped, captured };
}

async function aBidder(opts: {
  store: Pick<ReturnType<typeof memoryStore>, "load" | "save" | "saveTombstone">;
  provider: ComputeProvider;
  written?: Written[];
  eventStreams?: unknown;
  logs?: string[];
}) {
  const { wrapped, captured } = capturing(createComputeProviderHooks({ provider: opts.provider }));
  const config = {
    logger: quietLogger(opts.logs),
    serve: fakeServe(),
    atproto: fakeAtproto(opts.written ?? []),
    providers: [wrapped],
    skipServeBegin: true,
    contractStore: opts.store,
    ...(opts.eventStreams ? { eventStreams: opts.eventStreams } : {}),
  } as Parameters<typeof createMarketBidder>[0];
  const bidder = await createMarketBidder(config);
  return { bidder, captured };
}

let deleteSeq = 0;
function deleteCtx(issuerDid = REQUESTER_DID): EventDispatchContext {
  deleteSeq++;
  return {
    uri: `at://${issuerDid}/${EVENT_NSID}/d${deleteSeq}`,
    cid: `bafydeleteevent${deleteSeq}`,
    event: {
      receipt: { $type: "com.atproto.repo.strongRef", uri: RECEIPT_URI, cid: "bafyreceiptrewritten" },
      payload: {
        $type: "com.atproto.repo.strongRef",
        uri: `at://${issuerDid}/${COMPUTE_EVENTS_VM_DELETE_NSID}/p${deleteSeq}`,
        cid: `bafydeletepayload${deleteSeq}`,
      },
    },
    payloadNsid: COMPUTE_EVENTS_VM_DELETE_NSID,
    issuerDid,
    serviceId: "pdr_temp_compute_event",
    resolve: { resolve: () => Promise.reject(new Error("unused")) },
    log: () => {},
    req: new Request("https://localhost"),
  } as unknown as EventDispatchContext;
}

function vmDeleteOf(captured: { set?: CallbackSet }) {
  const handler = captured.set?.eventCallbacks?.pdr_temp_compute_event?.[COMPUTE_EVENTS_VM_DELETE_NSID];
  assert(handler, "the compute provider registered no vm.delete handler");
  return handler as (ctx: EventDispatchContext) => Promise<{ status?: number; body?: unknown } | void>;
}

const TEST_OPTS = { sanitizeOps: false, sanitizeResources: false };

Deno.test({
  ...TEST_OPTS,
  name: "a bidder seeded from a ContractStore answers vm.delete for that receipt with 200 and destroys once",
}, async () => {
  const store = memoryStore([storedContract()]);
  const fake = fakeProvider(() => ({ state: "running" }));
  const { bidder, captured } = await aBidder({ store, provider: fake.provider });
  await bidder.beginServe();
  try {
    const vmDelete = vmDeleteOf(captured);
    const first = await vmDelete(deleteCtx());
    assertEquals(first?.status, undefined, `vm.delete answered ${JSON.stringify(first)}`);
    assertEquals((first?.body as { ok?: boolean }).ok, true);
    assertEquals(fake.destroyed, [PROVIDER_ID]);

    const again = await vmDelete(deleteCtx());
    assertEquals(again?.status, undefined);
    assertEquals((again?.body as { alreadyTerminated?: boolean }).alreadyTerminated, true);
    assertEquals(fake.destroyed, [PROVIDER_ID], "a second vm.delete destroyed the guest again");

    assertEquals(store.live.has(RECEIPT_KEY), false, "the contract file outlived its destroy");
    assertEquals(store.dead.get(RECEIPT_URI)?.reason, "deleted_on_request");
  } finally {
    bidder.shutdown();
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "a tombstone survives a restart: a late vm.delete after the restart answers 200 already-terminated",
}, async () => {
  const store = memoryStore([storedContract()]);
  const fake = fakeProvider(() => ({ state: "running" }));
  const first = await aBidder({ store, provider: fake.provider });
  await first.bidder.beginServe();
  const ok = await vmDeleteOf(first.captured)(deleteCtx());
  assertEquals(ok?.status, undefined);
  first.bidder.shutdown();

  const second = await aBidder({ store, provider: fake.provider });
  await second.bidder.beginServe();
  try {
    const late = await vmDeleteOf(second.captured)(deleteCtx());
    assertEquals(late?.status, undefined, `a late vm.delete after a restart answered ${JSON.stringify(late)}`);
    assertEquals((late?.body as { alreadyTerminated?: boolean }).alreadyTerminated, true);
    assertEquals((late?.body as { reason?: string }).reason, "deleted_on_request");
    assertEquals(fake.destroyed, [PROVIDER_ID]);

    const stranger = await vmDeleteOf(second.captured)(deleteCtx("did:plc:someoneelse0000000000000"));
    assertEquals(stranger?.status, 403, "a restored tombstone answered a party that is not the accept author");
  } finally {
    second.bidder.shutdown();
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "load() completes before the first stream subscription",
}, async () => {
  const store = memoryStore([storedContract()]);
  const release = Promise.withResolvers<void>();
  store.gate.load = release.promise;
  const watchedAt: number[] = [];
  const eventStreams = {
    watch: () => {
      watchedAt.push(store.loads());
      return { close: () => {} };
    },
    close: () => {},
  };
  const fake = fakeProvider(() => ({ state: "running" }));
  const { bidder } = await aBidder({ store, provider: fake.provider, eventStreams });
  const serving = bidder.beginServe();
  await new Promise((r) => setTimeout(r, 30));
  assertEquals(watchedAt, [], "the bidder subscribed to a stream before its contracts were loaded");
  release.resolve();
  await serving;
  try {
    assert(watchedAt.length > 0, "the bidder never subscribed");
    assert(watchedAt.every((n) => n === 1), `a subscription ran before load() completed: ${watchedAt}`);
  } finally {
    bidder.shutdown();
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "a restored guest whose container is gone is terminated at load with the provider's reason",
}, async () => {
  const store = memoryStore([storedContract()]);
  const fake = fakeProvider((id) => ({ state: "gone", gone: { id, reason: "host_reboot" } }));
  const written: Written[] = [];
  const { bidder, captured } = await aBidder({ store, provider: fake.provider, written });
  await bidder.beginServe();
  try {
    assertEquals(fake.adopted, [PROVIDER_ID]);
    assertEquals(store.dead.get(RECEIPT_URI)?.reason, "host_reboot");
    assertEquals(store.live.has(RECEIPT_KEY), false);

    for (let i = 0; i < 50 && !written.some((w) => w.collection === EVENT_NSID); i++) {
      await new Promise((r) => setTimeout(r, 10));
    }
    const payload = written.find((w) => w.collection === COMPUTE_EVENTS_VM_TERMINATED_NSID);
    assertEquals(payload?.record.reason, "host_reboot", "no vm.terminated was published for the lost guest");

    const late = await vmDeleteOf(captured)(deleteCtx());
    assertEquals(late?.status, undefined);
    assertEquals((late?.body as { alreadyTerminated?: boolean }).alreadyTerminated, true);
    assertEquals(fake.destroyed, [], "a guest already gone was destroyed again");
  } finally {
    bidder.shutdown();
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "a restored running guest is adopted, never booted again, even when its accept is delivered again",
}, async () => {
  const store = memoryStore([storedContract()]);
  const fake = fakeProvider(() => ({ state: "running" }));
  const { bidder, captured } = await aBidder({ store, provider: fake.provider });
  await bidder.beginServe();
  try {
    assertEquals(fake.adopted, [PROVIDER_ID]);
    const onAccept = captured.set?.onAccept;
    assert(onAccept, "no accept handler");
    const replay = await onAccept({
      acceptUri: ACCEPT_URI,
      acceptCid: ACCEPT_CID,
      accept: { bid: { uri: "at://x/y/z", cid: "bafybid" } } as never,
      issuerDid: REQUESTER_DID,
      resolve: { resolve: () => Promise.resolve(null) } as never,
      log: () => {},
      req: new Request("https://localhost"),
    } as never);
    assertEquals(fake.provisioned(), 0, "a replayed accept booted a second guest for a restored contract");
    assertEquals((replay?.body as { uri?: string }).uri, RECEIPT_URI);
    assertEquals(store.live.get(RECEIPT_KEY)?.providerId, PROVIDER_ID);
  } finally {
    bidder.shutdown();
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "a crash between accept and save leaves no contract the bidder acknowledged unsaved",
}, async () => {
  const gate = Promise.withResolvers<void>();
  const saved: ContractEvent[] = [];
  const fake = fakeProvider();
  const written: Written[] = [];
  const atproto = fakeAtproto(written);
  const activeContracts = new Map<string, ActiveContract>();
  const callbacks = createVmBidderCallbacks({
    did: BIDDER_DID,
    computeProvider: fake.provider,
    log: () => {},
    activeContracts,
    relay: { ingressRef: "", ingressUrl: "https://bidder.relay.test" },
    onContractChange: async (event: ContractEvent) => {
      await gate.promise;
      saved.push(event);
    },
    createRepoRecord: atproto.createRepoRecord,
    createSignedRepoRecord: atproto.createSignedRepoRecord,
    callService: atproto.callService,
  } as never);
  let acked = false;
  const acking = Promise.resolve(callbacks.accept({
    acceptUri: ACCEPT_URI,
    acceptCid: ACCEPT_CID,
    accept: {
      $type: "com.publicdomainrelay.temp.market.accept",
      bid: { uri: "at://x/y/z", cid: "bafybid" },
      submitEvent: SUBMIT_EVENT,
      createdAt: "2026-10-12T01:00:00.000Z",
    } as never,
    issuerDid: REQUESTER_DID,
    resolve: { resolve: () => Promise.resolve(null) } as never,
    log: () => {},
    req: new Request("https://localhost"),
  } as never)).then((r) => {
    acked = true;
    return r;
  });
  await new Promise((r) => setTimeout(r, 30));
  assertEquals(acked, false, "the accept was acknowledged before its contract was saved");
  gate.resolve();
  const ack = await acking;
  const accepted = saved.find((e) => e.type === "accepted");
  assert(accepted, "no accepted event was saved");
  assertEquals((ack?.body as { uri?: string }).uri, accepted.receiptUri);
  const shape = accepted as ContractEvent & Record<string, unknown>;
  assertEquals(shape.acceptUri, ACCEPT_URI);
  assertEquals(shape.acceptCid, ACCEPT_CID);
  assertEquals(shape.provider, "fake");
  assertEquals(shape.submitEventUrl, SUBMIT_EVENT);
});

Deno.test({
  ...TEST_OPTS,
  name: "file store: a bidder restarted twice keeps its receipt and then its tombstone",
}, async () => {
  const root = await Deno.makeTempDir({ prefix: "bidder-restore-fs-" });
  try {
    const dir = `${root}/contracts`;
    const seed = createFsContractStore({ dir });
    const c = storedContract();
    await seed.save({ type: "accepted", ...c });
    await seed.save({ type: "provisioned", ...c });
    const fake = fakeProvider(() => ({ state: "running" }));

    const first = await aBidder({ store: createFsContractStore({ dir }), provider: fake.provider });
    await first.bidder.beginServe();
    const ok = await vmDeleteOf(first.captured)(deleteCtx());
    assertEquals(ok?.status, undefined, `vm.delete after a restart answered ${JSON.stringify(ok)}`);
    assertEquals(fake.destroyed, [PROVIDER_ID]);
    first.bidder.shutdown();

    const second = await aBidder({ store: createFsContractStore({ dir }), provider: fake.provider });
    await second.bidder.beginServe();
    try {
      const late = await vmDeleteOf(second.captured)(deleteCtx());
      assertEquals(late?.status, undefined);
      assertEquals((late?.body as { alreadyTerminated?: boolean }).alreadyTerminated, true);
      assertEquals(fake.destroyed, [PROVIDER_ID]);
      const loaded = await createFsContractStore({ dir }).load();
      assertEquals(loaded.contracts, []);
      assertEquals(loaded.tombstones.map((t) => t.receiptUri), [RECEIPT_URI]);
    } finally {
      second.bidder.shutdown();
    }
  } finally {
    await Deno.remove(root, { recursive: true }).catch(() => {});
  }
});

Deno.test({
  ...TEST_OPTS,
  name: "file store: a bidder whose store is corrupt refuses to start",
}, async () => {
  const root = await Deno.makeTempDir({ prefix: "bidder-restore-corrupt-" });
  try {
    const dir = `${root}/contracts`;
    const seed = createFsContractStore({ dir });
    await seed.save({ type: "accepted", ...storedContract() });
    const file = `${dir}/contracts/${encodeURIComponent(RECEIPT_URI)}.json`;
    await Deno.writeTextFile(file, "{\"version\":1,\"contr");
    const fake = fakeProvider(() => ({ state: "running" }));
    const { bidder } = await aBidder({ store: createFsContractStore({ dir }), provider: fake.provider });
    let refused: unknown;
    await bidder.beginServe().catch((err) => {
      refused = err;
    });
    assert(refused instanceof Error, "a bidder with a corrupt contract store started");
    assert(refused.message.includes(file), `the refusal does not name the file: ${refused.message}`);
    assertEquals(await Deno.readTextFile(file), "{\"version\":1,\"contr");
  } finally {
    await Deno.remove(root, { recursive: true }).catch(() => {});
  }
});
