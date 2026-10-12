import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  dumbpipeConnectArgv,
  IROH_SCHEME,
  NonIrohGuestAddressError,
  sshProxyCommandFor,
} from "@publicdomainrelay/requester-xrpc";

// A guest announces one address on one report -- the on-network event every
// transport has always used -- and the address says which transport it belongs
// to by its scheme. An iroh address is a ticket the guest's own dumbpipe
// endpoint minted; anything else is refused.
const TICKET = "b2k7q5m3n4p6r8s9t2v4w6x8y2z4a6c8e2g4j6m8p2r4t6v8x2z4";

Deno.test("an iroh address becomes a dumbpipe ProxyCommand against its ticket", () => {
  assertEquals(
    sshProxyCommandFor(`${IROH_SCHEME}${TICKET}`),
    `dumbpipe connect ${TICKET}`,
  );
  assert(
    !sshProxyCommandFor(`${IROH_SCHEME}${TICKET}`).includes(IROH_SCHEME),
    "dumbpipe is handed the ticket, not the scheme that introduced it",
  );
});

Deno.test("an operator's own dumbpipe is the one the ProxyCommand runs", () => {
  assertEquals(
    sshProxyCommandFor(`${IROH_SCHEME}${TICKET}`, "/opt/dumbpipe"),
    `/opt/dumbpipe connect ${TICKET}`,
  );
});

Deno.test("a relay hostname is refused rather than given a websocket ProxyCommand", () => {
  assertThrows(() => sshProxyCommandFor("did-key-abc.xrpc.fedproxy.com"), NonIrohGuestAddressError, "iroh");
});

Deno.test("a hostname is never read as a ticket, however it is spelled", () => {
  assertThrows(() => sshProxyCommandFor("localhost"), NonIrohGuestAddressError);
});

Deno.test("an in-process dialer gets the same dumbpipe argv the ProxyCommand runs", () => {
  assertEquals(dumbpipeConnectArgv(`${IROH_SCHEME}${TICKET}`), ["dumbpipe", "connect", TICKET]);
  assertEquals(
    dumbpipeConnectArgv(`${IROH_SCHEME}${TICKET}`, "/opt/dumbpipe"),
    ["/opt/dumbpipe", "connect", TICKET],
  );
  assertEquals(
    sshProxyCommandFor(`${IROH_SCHEME}${TICKET}`, "/opt/dumbpipe"),
    dumbpipeConnectArgv(`${IROH_SCHEME}${TICKET}`, "/opt/dumbpipe")!.join(" "),
  );
});

Deno.test("a non-iroh address has no dumbpipe argv", () => {
  assertEquals(dumbpipeConnectArgv("did-key-abc.xrpc.fedproxy.com"), undefined);
  assertEquals(dumbpipeConnectArgv("localhost"), undefined);
  assertEquals(dumbpipeConnectArgv("10.0.0.2:22"), undefined);
});
