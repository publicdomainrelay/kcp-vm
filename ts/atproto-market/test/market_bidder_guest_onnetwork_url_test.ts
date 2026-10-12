import { assertEquals } from "@std/assert";
import { guestOnNetworkUrl } from "@publicdomainrelay/market-bidder-compute";

Deno.test("a guest reports to the relay's own /v1/on-network", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.relay.localhost"),
    "https://did-key-abc.relay.localhost/v1/on-network",
  );
});

Deno.test("a trailing slash does not double up", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.xrpc.fedproxy.com/"),
    "https://did-key-abc.xrpc.fedproxy.com/v1/on-network",
  );
});
