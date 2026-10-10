import { assert, assertEquals } from "@std/assert";
import { IROH_SCHEME, sshProxyCommandFor } from "@publicdomainrelay/requester-xrpc";

// A guest announces one address on one report -- the on-network event every
// transport has always used -- and the address says which transport it belongs
// to by its scheme. These hold the two commands to that: an iroh address is a
// ticket the guest's own dumbpipe endpoint minted, anything else is a relay
// hostname.
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

Deno.test("a relay hostname keeps the websocket ProxyCommand", () => {
  const command = sshProxyCommandFor("did-key-abc.xrpc.fedproxy.com");
  assert(command.startsWith("websocat --binary wss://"), command);
  assert(command.includes("did-key-abc.xrpc.fedproxy.com"), command);
});

Deno.test("a hostname is never read as a ticket, however it is spelled", () => {
  // The websocket transports' addresses are hostnames, and a hostname with no
  // dot in it would be a ticket to anyone guessing from the shape alone. The
  // scheme is what decides.
  assert(sshProxyCommandFor("localhost").startsWith("websocat --binary"));
});
