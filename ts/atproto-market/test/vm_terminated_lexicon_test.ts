import { assert, assertEquals } from "@std/assert";
import {
  COMPUTE_EVENTS_VM_TERMINATED_NSID,
  VM_TERMINATED_REASONS,
} from "@publicdomainrelay/market-common";
import {
  COMPUTE_EVENTS_VM_TERMINATED_NSID as LEXICONS_NSID,
  publicdomainrelay,
} from "@publicdomainrelay/market-lexicons";

const terminated = publicdomainrelay.temp.compute.events.vm.terminated;
const LEXICON_PATH = new URL(
  "../lexicons/com/publicdomainrelay/temp/compute/events/vm/terminated.json",
  import.meta.url,
);

const STRONG_REF = {
  $type: "com.atproto.repo.strongRef",
  uri: "at://did:plc:requester/com.publicdomainrelay.temp.compute.events.vm.delete/3l2abcdefgh2a",
  cid: "bafyreidzhnumm5wvtiu6xyutqxmnfdxs2dlnvx4o3z3xyh2t3ilqgwlq5e",
};

function record(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    $type: COMPUTE_EVENTS_VM_TERMINATED_NSID,
    reason: "guest_poweroff",
    observedAt: "2026-10-12T00:57:03.000Z",
    createdAt: "2026-10-12T00:57:04.000Z",
    ...extra,
  };
}

Deno.test("vm.terminated NSID is registered in market-common and market-lexicons", () => {
  assertEquals(COMPUTE_EVENTS_VM_TERMINATED_NSID, "com.publicdomainrelay.temp.compute.events.vm.terminated");
  assertEquals(LEXICONS_NSID, COMPUTE_EVENTS_VM_TERMINATED_NSID);
  assertEquals(terminated.$nsid, COMPUTE_EVENTS_VM_TERMINATED_NSID);
});

Deno.test("vm.terminated lexicon JSON declares the decided shape", async () => {
  const doc = JSON.parse(await Deno.readTextFile(LEXICON_PATH));
  assertEquals(doc.id, COMPUTE_EVENTS_VM_TERMINATED_NSID);
  const main = doc.defs.main;
  assertEquals(main.type, "record");
  assertEquals(main.key, "tid");
  assertEquals(main.record.required, ["reason", "observedAt", "createdAt"]);
  assertEquals(main.record.properties.reason.knownValues, [...VM_TERMINATED_REASONS]);
  assertEquals(main.record.properties.exitCode.type, "integer");
  assertEquals(main.record.properties.request.ref, "com.atproto.repo.strongRef");
});

Deno.test("vm.terminated validates every known reason, an exit code and a delete request ref", () => {
  for (const reason of VM_TERMINATED_REASONS) {
    assert(terminated.$safeParse(record({ reason })).success, reason);
  }
  assert(terminated.$safeParse(record({ exitCode: 137 })).success, "exitCode");
  assert(
    terminated.$safeParse(record({ reason: "deleted_on_request", request: STRONG_REF })).success,
    "request strongRef",
  );
  assert(terminated.$safeParse(record({ reason: "some_future_reason" })).success, "open enum");
});

Deno.test("vm.terminated rejects missing fields and wrong types", () => {
  const { observedAt: _o, ...noObserved } = record();
  assert(!terminated.$safeParse(noObserved).success, "observedAt required");
  const { reason: _r, ...noReason } = record();
  assert(!terminated.$safeParse(noReason).success, "reason required");
  assert(!terminated.$safeParse(record({ exitCode: 1.5 })).success, "exitCode integer");
  assert(!terminated.$safeParse(record({ observedAt: "yesterday" })).success, "observedAt datetime");
  assert(!terminated.$safeParse(record({ request: { uri: "not-a-ref" } })).success, "request strongRef");
});
