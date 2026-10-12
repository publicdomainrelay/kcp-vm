import { assert, assertEquals } from "@std/assert";
import { COMPUTE_EVENTS_VM_HOST_KEYS_NSID, VM_HOST_KEY_TYPES } from "@publicdomainrelay/market-common";
import {
  COMPUTE_EVENTS_VM_HOST_KEYS_NSID as LEXICONS_NSID,
  publicdomainrelay,
} from "@publicdomainrelay/market-lexicons";

const hostKeys = publicdomainrelay.temp.compute.events.vm.hostKeys;
const LEXICON_PATH = new URL(
  "../lexicons/com/publicdomainrelay/temp/compute/events/vm/hostKeys.json",
  import.meta.url,
);

const ED25519 = {
  type: "ssh-ed25519",
  publicKey: "AAAAC3NzaC1lZDI1NTE5AAAAIGWqx0pQ7m3nB9P2cCjv1xTt0lI7yU6m0O4m8r1yqk8H",
  fingerprint: "SHA256:3kQ0d2xv1Y8Yb1oQe1c8v0m4m4b3tC2Yp5x9yA7mJ0c",
};

function record(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    $type: COMPUTE_EVENTS_VM_HOST_KEYS_NSID,
    keys: [ED25519],
    observedAt: "2026-10-12T00:57:03.000Z",
    createdAt: "2026-10-12T00:57:04.000Z",
    ...extra,
  };
}

Deno.test("vm.hostKeys NSID is registered in market-common and market-lexicons", () => {
  assertEquals(COMPUTE_EVENTS_VM_HOST_KEYS_NSID, "com.publicdomainrelay.temp.compute.events.vm.hostKeys");
  assertEquals(LEXICONS_NSID, COMPUTE_EVENTS_VM_HOST_KEYS_NSID);
  assertEquals(hostKeys.$nsid, COMPUTE_EVENTS_VM_HOST_KEYS_NSID);
});

Deno.test("vm.hostKeys lexicon JSON declares the decided shape", async () => {
  const doc = JSON.parse(await Deno.readTextFile(LEXICON_PATH));
  assertEquals(doc.id, COMPUTE_EVENTS_VM_HOST_KEYS_NSID);
  const main = doc.defs.main;
  assertEquals(main.type, "record");
  assertEquals(main.key, "tid");
  assertEquals(main.record.required, ["keys", "observedAt", "createdAt"]);
  assertEquals(main.record.properties.keys.type, "array");
  assertEquals(main.record.properties.keys.minLength, 1);
  assertEquals(main.record.properties.keys.items.ref, "#hostKey");
  const hostKey = doc.defs.hostKey;
  assertEquals(hostKey.required, ["type", "publicKey", "fingerprint"]);
  assertEquals(hostKey.properties.type.knownValues, [...VM_HOST_KEY_TYPES]);
});

Deno.test("vm.hostKeys validates one or several keys of every known type", () => {
  assert(hostKeys.$safeParse(record()).success, "one ed25519 key");
  const all = VM_HOST_KEY_TYPES.map((type) => ({ ...ED25519, type }));
  assert(hostKeys.$safeParse(record({ keys: all })).success, "every known type");
  assert(
    hostKeys.$safeParse(record({ keys: [{ ...ED25519, type: "ssh-future" }] })).success,
    "open enum",
  );
});

Deno.test("vm.hostKeys rejects missing fields, an empty key list and wrong types", () => {
  const { keys: _k, ...noKeys } = record();
  assert(!hostKeys.$safeParse(noKeys).success, "keys required");
  const { observedAt: _o, ...noObserved } = record();
  assert(!hostKeys.$safeParse(noObserved).success, "observedAt required");
  assert(!hostKeys.$safeParse(record({ keys: [] })).success, "at least one key");
  const { fingerprint: _f, ...noFingerprint } = ED25519;
  assert(!hostKeys.$safeParse(record({ keys: [noFingerprint] })).success, "fingerprint required");
  const { publicKey: _p, ...noPublicKey } = ED25519;
  assert(!hostKeys.$safeParse(record({ keys: [noPublicKey] })).success, "publicKey required");
  assert(!hostKeys.$safeParse(record({ observedAt: "yesterday" })).success, "observedAt datetime");
  assert(!hostKeys.$safeParse(record({ keys: "ssh-ed25519 AAAA" })).success, "keys is an array");
});
