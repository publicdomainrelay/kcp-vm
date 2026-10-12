import { assert, assertEquals } from "@std/assert";
import { createVmBidderCallbacks } from "@publicdomainrelay/market-bidder-compute";
import type { ActiveContract } from "@publicdomainrelay/market-bidder-abc";
import type {
  ComputeProvider,
  GuestHostKeysAck,
  GuestHostKeysHandler,
  GuestHostKeysReport,
} from "@publicdomainrelay/compute-provider-abc";
import {
  checkHostKeys,
  COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
  EVENT_NSID,
  sshHostKeyFingerprint,
  SUBMIT_EVENT_NSID,
} from "@publicdomainrelay/market-common";

const BIDDER_DID = "did:plc:bidder";
const REQUESTER_DID = "did:plc:requester";
const REQUESTER_SUBMIT_EVENT = "https://did-key-requester.relay.test";
const RECEIPT_URI = `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/1`;
const RECEIPT_CID = "bafyreceiptoriginal";
const RECEIPT_KEY = `${RECEIPT_URI}#${RECEIPT_CID}`;
const PROVIDER_ID = "fc-1379be663dd9-0319b646";

async function hostKey(type: "ed25519" | "ecdsa" | "rsa"): Promise<{ type: string; publicKey: string; fingerprint: string }> {
  const dir = await Deno.makeTempDir({ prefix: "hostkeys-test-" });
  try {
    const path = `${dir}/key`;
    const gen = await new Deno.Command("ssh-keygen", {
      args: ["-q", "-t", type, ...(type === "rsa" ? ["-b", "2048"] : []), "-N", "", "-f", path],
    }).output();
    assert(gen.success, "ssh-keygen");
    const [keyType, publicKey] = (await Deno.readTextFile(`${path}.pub`)).trim().split(/\s+/);
    const fp = await new Deno.Command("ssh-keygen", { args: ["-l", "-E", "sha256", "-f", `${path}.pub`] }).output();
    const fingerprint = new TextDecoder().decode(fp.stdout).split(/\s+/)[1];
    return { type: keyType, publicKey, fingerprint };
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
}

const ED25519 = await hostKey("ed25519");
const ECDSA = await hostKey("ecdsa");
const RSA = await hostKey("rsa");
const OTHER_ED25519 = await hostKey("ed25519");

interface Written {
  collection: string;
  uri: string;
  cid: string;
  record: Record<string, unknown>;
  signed: boolean;
}

function harness(opts: { submitEventUrl?: string } = { submitEventUrl: REQUESTER_SUBMIT_EVENT }) {
  let handler: GuestHostKeysHandler | undefined;
  const provider: ComputeProvider = {
    name: "fake",
    provision: () => Promise.reject(new Error("this test provisions nothing")),
    destroy: () => Promise.resolve(),
    createBidConfig: () => Promise.reject(new Error("this test bids nothing")),
    injectAcceptBundle: (userData: string) => userData,
    onHostKeys: (h) => {
      handler = h;
      return () => {
        handler = undefined;
      };
    },
  };
  const written: Written[] = [];
  const submitted: Array<{ url: string; nsid: string; body: Record<string, unknown> }> = [];
  const logged: Array<{ level: string; msg: string; meta?: Record<string, unknown> }> = [];
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
  createVmBidderCallbacks({
    did: BIDDER_DID,
    computeProvider: provider,
    log: (level: string, msg: string, meta?: Record<string, unknown>) => logged.push({ level, msg, meta }),
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
  const report = (keys: unknown[], id: string | number = PROVIDER_ID): Promise<GuestHostKeysAck> => {
    assert(handler, "the bidder subscribed to the provider's host-key reports");
    return handler({ id, keys, observedAt: "2026-10-12T00:57:03.000Z" } as GuestHostKeysReport);
  };
  return {
    written,
    submitted,
    logged,
    report,
    payloads: () => written.filter((w) => w.collection === COMPUTE_EVENTS_VM_HOST_KEYS_NSID),
    events: () =>
      written.filter((w) =>
        w.collection === EVENT_NSID &&
        ((w.record.payload as { uri?: string } | undefined)?.uri ?? "").includes(COMPUTE_EVENTS_VM_HOST_KEYS_NSID)
      ),
  };
}

Deno.test("checkHostKeys computes the fingerprint ssh-keygen prints and refuses what cannot be a known_hosts line", async () => {
  assertEquals(await sshHostKeyFingerprint(ED25519.publicKey), ED25519.fingerprint);
  assertEquals(await sshHostKeyFingerprint(RSA.publicKey), RSA.fingerprint);
  const ok = await checkHostKeys([
    { type: RSA.type, publicKey: RSA.publicKey },
    ED25519,
    ECDSA,
  ]);
  assert(ok.ok);
  if (ok.ok) {
    assertEquals(ok.keys.map((k) => k.type), ["ecdsa-sha2-nistp256", "ssh-ed25519", "ssh-rsa"]);
    assertEquals(ok.keys.find((k) => k.type === "ssh-rsa")?.fingerprint, RSA.fingerprint);
  }
  const refused = [
    [],
    [{ ...ED25519, type: "ssh-ed25519\nevil.example ssh-ed25519" }],
    [{ ...ED25519, publicKey: `${ED25519.publicKey}\n@cert-authority * ssh-ed25519 AAAA` }],
    [{ ...ED25519, type: "ssh-rsa" }],
    [{ ...ED25519, fingerprint: OTHER_ED25519.fingerprint }],
    [ED25519, OTHER_ED25519],
    "ssh-ed25519 AAAA",
  ];
  for (const keys of refused) {
    assertEquals((await checkHostKeys(keys)).ok, false, JSON.stringify(keys).slice(0, 80));
  }
});

Deno.test("a verified host-key report is published as a signed vm.hostKeys event and submitted to the requester", async () => {
  const h = harness();
  const ack = await h.report([ED25519, { type: ECDSA.type, publicKey: ECDSA.publicKey }]);
  assertEquals(ack.ok, true);

  const [payload] = h.payloads();
  assertEquals(payload.record.$type, COMPUTE_EVENTS_VM_HOST_KEYS_NSID);
  assertEquals(payload.record.keys, [ECDSA, ED25519]);
  assertEquals(payload.record.observedAt, "2026-10-12T00:57:03.000Z");
  assert(typeof payload.record.createdAt === "string");
  assert(!payload.signed);

  const [event] = h.events();
  assert(event.signed, "the market.event envelope carries the bidder's inline signature");
  assertEquals(event.record.$type, EVENT_NSID);
  assertEquals((event.record.receipt as { uri: string }).uri, RECEIPT_URI);
  assertEquals((event.record.receipt as { cid: string }).cid, RECEIPT_CID);
  assertEquals((event.record.payload as { uri: string }).uri, payload.uri);

  assertEquals(h.submitted.length, 1);
  assertEquals(h.submitted[0].url, REQUESTER_SUBMIT_EVENT);
  assertEquals(h.submitted[0].nsid, SUBMIT_EVENT_NSID);
  assertEquals(h.submitted[0].body.uri, event.uri);
  assert(h.logged.some((l) => l.msg === "vm.hostKeys event created on PDS (firehose)"));
  assert(h.logged.some((l) => l.msg === "vm.hostKeys event submitted to requester"));
});

Deno.test("with no submitEvent endpoint the vm.hostKeys event goes to the firehose only", async () => {
  const h = harness({});
  assertEquals((await h.report([ED25519])).ok, true);
  assertEquals(h.payloads().length, 1);
  assertEquals(h.events().length, 1);
  assertEquals(h.submitted, []);
});

Deno.test("a second, different report for the receipt is refused and logged; the same report again is idempotent", async () => {
  const h = harness();
  assertEquals((await h.report([ED25519])).ok, true);
  const again = await h.report([ED25519]);
  assertEquals(again.ok, true);
  const different = await h.report([OTHER_ED25519]);
  assertEquals(different.ok, false);
  assertEquals(different.status, 409);
  assertEquals(h.payloads().length, 1, "published once");
  assertEquals(h.submitted.length, 1);
  assert(h.logged.some((l) => l.msg === "vm_hostkeys_second_report_refused" && l.meta?.receiptUri === RECEIPT_URI));
});

Deno.test("a report for a guest no contract holds, or with malformed keys, is refused and nothing is published", async () => {
  const h = harness();
  const unknown = await h.report([ED25519], "fc-someone-else");
  assertEquals(unknown.ok, false);
  assertEquals(unknown.status, 404);
  const malformed = await h.report([{ ...ED25519, type: "ssh-ed25519 x" }]);
  assertEquals(malformed.ok, false);
  assertEquals(malformed.status, 400);
  const noEd25519 = await h.report([ECDSA]);
  assertEquals(noEd25519.ok, false);
  assertEquals(noEd25519.status, 400);
  assertEquals(h.written, []);
  assertEquals(h.submitted, []);
});
