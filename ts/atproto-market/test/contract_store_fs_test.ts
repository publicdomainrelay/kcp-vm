import { assert, assertEquals, assertRejects } from "@std/assert";
import { createFsContractStore } from "@publicdomainrelay/market-bidder-contract-store-fs";
import type { ContractEvent } from "@publicdomainrelay/market-bidder-abc";

const BIDDER_DID = "did:plc:fsstorebidder000000000000";
const REQUESTER_DID = "did:plc:fsstorerequester000000000";

function receipt(n: number) {
  const receiptUri = `at://${BIDDER_DID}/com.publicdomainrelay.temp.market.receipt/r${n}`;
  const receiptCid = `bafyreceipt${n}`;
  return { receiptUri, receiptCid, key: `${receiptUri}#${receiptCid}` };
}

function event(n: number, type: ContractEvent["type"], extra: Partial<ContractEvent> = {}): ContractEvent {
  const r = receipt(n);
  return {
    type,
    key: r.key,
    receiptUri: r.receiptUri,
    receiptCid: r.receiptCid,
    acceptUri: `at://${REQUESTER_DID}/com.publicdomainrelay.temp.market.accept/a${n}`,
    acceptCid: `bafyaccept${n}`,
    acceptAuthor: REQUESTER_DID,
    acceptedAt: "2026-10-12T01:00:00.000Z",
    provider: "firecracker",
    submitEventUrl: "https://requester.relay.test",
    ...extra,
  };
}

async function withDir(fn: (dir: string) => Promise<void>): Promise<void> {
  const root = await Deno.makeTempDir({ prefix: "contract-store-fs-" });
  try {
    await fn(`${root}/contracts`);
  } finally {
    await Deno.remove(root, { recursive: true }).catch(() => {});
  }
}

async function filesUnder(dir: string): Promise<string[]> {
  const out: string[] = [];
  const walk = async (d: string) => {
    for await (const e of Deno.readDir(d)) {
      if (e.isDirectory) await walk(`${d}/${e.name}`);
      else out.push(`${d}/${e.name}`);
    }
  };
  await walk(dir).catch(() => {});
  return out.sort();
}

Deno.test("a saved contract loads back with its provider id, written 0600 with no temp file left", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(1, "accepted"));
    await store.save(event(1, "provisioned", { providerId: "fc-aaaa-0001" }));

    const loaded = await createFsContractStore({ dir }).load();
    assertEquals(loaded.tombstones, []);
    assertEquals(loaded.contracts.length, 1);
    const c = loaded.contracts[0];
    assertEquals(c.key, receipt(1).key);
    assertEquals(c.providerId, "fc-aaaa-0001");
    assertEquals(c.acceptUri, event(1, "accepted").acceptUri);
    assertEquals(c.provider, "firecracker");
    assertEquals(c.submitEventUrl, "https://requester.relay.test");

    const files = await filesUnder(dir);
    assert(files.every((f) => f.endsWith(".json")), `temp files left behind: ${files}`);
    for (const f of files) {
      assertEquals((await Deno.stat(f)).mode! & 0o777, 0o600, `${f} is not 0600`);
    }
  });
});

Deno.test("a terminated contract is dropped only once its tombstone exists, and the tombstone loads back", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(2, "accepted", { providerId: "fc-bbbb-0002" }));
    await store.saveTombstone({
      receiptUri: receipt(2).receiptUri,
      receiptCid: receipt(2).receiptCid,
      acceptUri: event(2, "accepted").acceptUri,
      acceptCid: event(2, "accepted").acceptCid,
      acceptAuthor: REQUESTER_DID,
      reason: "deleted_on_request",
      at: new Date().toISOString(),
    });
    await store.save(event(2, "terminated", { terminatedAt: new Date().toISOString() }));

    const loaded = await createFsContractStore({ dir }).load();
    assertEquals(loaded.contracts, []);
    assertEquals(loaded.tombstones.length, 1);
    assertEquals(loaded.tombstones[0].receiptUri, receipt(2).receiptUri);
    assertEquals(loaded.tombstones[0].reason, "deleted_on_request");
    assertEquals(loaded.tombstones[0].acceptAuthor, REQUESTER_DID);
  });
});

Deno.test("a late provisioned save never resurrects a tombstoned contract", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(3, "accepted"));
    await store.saveTombstone({
      receiptUri: receipt(3).receiptUri,
      acceptAuthor: REQUESTER_DID,
      reason: "deleted_on_request",
      at: new Date().toISOString(),
    });
    await store.save(event(3, "terminated"));
    await store.save(event(3, "provisioned", { providerId: "fc-cccc-0003" }));
    const loaded = await createFsContractStore({ dir }).load();
    assertEquals(loaded.contracts, []);
  });
});

Deno.test("a torn temp file is ignored on load", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(4, "accepted", { providerId: "fc-dddd-0004" }));
    const [file] = await filesUnder(dir);
    const parent = file.slice(0, file.lastIndexOf("/"));
    await Deno.writeTextFile(`${parent}/.torn.json.tmp-12345`, '{"version":1,"contr');

    const loaded = await createFsContractStore({ dir }).load();
    assertEquals(loaded.contracts.map((c) => c.key), [receipt(4).key]);
  });
});

Deno.test("a corrupt store file is refused loudly and kept, never silently emptied", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(5, "accepted", { providerId: "fc-eeee-0005" }));
    const [file] = await filesUnder(dir);
    await Deno.writeTextFile(file, '{"version":1,"contract":{"key":"at://');

    const err = await assertRejects(() => createFsContractStore({ dir }).load());
    assert(String((err as Error).message).includes(file), `the error does not name ${file}: ${err}`);
    assertEquals(await Deno.readTextFile(file), '{"version":1,"contract":{"key":"at://');
  });
});

Deno.test("a partial store file (valid JSON, missing fields) is refused loudly", async () => {
  await withDir(async (dir) => {
    const store = createFsContractStore({ dir });
    await store.save(event(6, "accepted"));
    const [file] = await filesUnder(dir);
    await Deno.writeTextFile(file, JSON.stringify({ version: 1, contract: { key: receipt(6).key } }));
    const err = await assertRejects(() => createFsContractStore({ dir }).load());
    assert(String((err as Error).message).includes(file), `the error does not name ${file}: ${err}`);
  });
});

Deno.test("tombstones past their TTL are dropped on load and the newest are kept within capacity", async () => {
  await withDir(async (dir) => {
    let now = Date.parse("2026-10-12T00:00:00.000Z");
    const store = createFsContractStore({ dir, tombstoneTtlMs: 60_000, tombstoneCapacity: 2, now: () => now });
    const tomb = (n: number, atMs: number) =>
      store.saveTombstone({
        receiptUri: receipt(n).receiptUri,
        acceptAuthor: REQUESTER_DID,
        reason: "vmm_exited",
        at: new Date(atMs).toISOString(),
      });
    await tomb(10, now - 120_000);
    await tomb(11, now - 3_000);
    await tomb(12, now - 2_000);
    await tomb(13, now - 1_000);

    const loaded = await createFsContractStore({ dir, tombstoneTtlMs: 60_000, tombstoneCapacity: 2, now: () => now })
      .load();
    assertEquals(loaded.tombstones.map((t) => t.receiptUri), [receipt(12).receiptUri, receipt(13).receiptUri]);
    now += 1;
    const again = await createFsContractStore({ dir, tombstoneTtlMs: 60_000, tombstoneCapacity: 2, now: () => now })
      .load();
    assertEquals(again.tombstones.length, 2, "pruned tombstones came back");
  });
});
