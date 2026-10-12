import { assert, assertEquals, assertThrows } from "@std/assert";
import { buildUserData, listUserDataModules } from "@publicdomainrelay/cloud-init-common";
import { contractUserData } from "@publicdomainrelay/socialweb-computer-requester-inproc";

Deno.test("no transport other than iroh is requested", () => {
  const userData = contractUserData();
  assertEquals(userData.transport, "iroh");
  const composed = buildUserData({ ctx: { vmName: "g", sshAuthorizedKey: "ssh-ed25519 AAAA k" }, modules: [userData.transport] });
  assert(composed.includes("iroh.service"), "the composed guest runs the iroh listener");
  for (const legacy of ["tunnel", "fedproxy-ssh", "fedproxy-web", "wootty"]) {
    assert(!listUserDataModules().includes(legacy), `${legacy} is still offered to the gateway`);
    assertThrows(() => buildUserData({ modules: [legacy] }), Error, "iroh");
  }
});
