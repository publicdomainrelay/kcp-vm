import { assert, assertEquals, assertThrows } from "@std/assert";
import * as cloudInit from "@publicdomainrelay/cloud-init-common";
import * as requester from "@publicdomainrelay/requester-xrpc";
import { guestAddressKind } from "@publicdomainrelay/compute-request-abc";

const LEGACY_MODULES = ["tunnel", "fedproxy-ssh", "fedproxy-web", "wootty"];

Deno.test("asking for a removed transport module is refused with iroh named as the only transport", () => {
  for (const legacy of LEGACY_MODULES) {
    const err = assertThrows(() => cloudInit.buildUserData({ modules: [legacy] }));
    assert(String(err).includes("legacy-transport"), String(err));
    assert(String(err).includes("iroh"), String(err));
  }
});

Deno.test("a guest address that is a relay name is refused, only an iroh ticket is a guest address", () => {
  assertEquals(guestAddressKind("iroh://abc"), "iroh-ticket");
  assertEquals(guestAddressKind("vm--did-plc-x.relay.localhost"), "refused");
  assertEquals(guestAddressKind("did-key-abc.xrpc.fedproxy.com"), "refused");
});

Deno.test("the ssh ProxyCommand for a non-iroh address is refused instead of a websocket bridge", () => {
  assertThrows(() => requester.sshProxyCommandFor("did-key-abc.xrpc.fedproxy.com"), Error, "iroh");
  assertThrows(() => requester.sshProxyCommandFor("localhost"), Error, "iroh");
});

Deno.test("a runComputeContract asking for a non-iroh transport is refused before anything is sent", () => {
  assertThrows(() => requester.assertGuestTransport("tunnel"), Error, "legacy-transport");
  assertThrows(() => requester.assertGuestTransport("fedproxy-ssh"), Error, "iroh");
  requester.assertGuestTransport("iroh");
  requester.assertGuestTransport(undefined);
});
