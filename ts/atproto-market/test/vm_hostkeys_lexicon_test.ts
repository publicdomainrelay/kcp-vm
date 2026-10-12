import { assert } from "@std/assert";
import { COMPUTE_EVENTS_VM_HOST_KEYS_NSID, VM_HOST_KEY_TYPES } from "@publicdomainrelay/market-common";
import {
  publicdomainrelay,
} from "@publicdomainrelay/market-lexicons";

const hostKeys = publicdomainrelay.temp.compute.events.vm.hostKeys;
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
