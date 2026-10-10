import { assertEquals } from "@std/assert";
import { guestOnNetworkUrl } from "@publicdomainrelay/market-bidder-compute";

const USER_DATA = [
  "#cloud-config",
  "write_files:",
  "  - path: /etc/systemd/system/tunnel-subscriber.service",
  "    content: |",
  "      [Service]",
  "      ExecStart=/usr/local/bin/deno run -A jsr:@publicdomainrelay/hono-did-key-ingress-proxy-tunnel-subscriber --ingress-proxy-host relay.localhost:42855 --aud-host relay.localhost",
  "",
].join("\n");

Deno.test("a relay behind a local dispatcher is reported to at the port the dispatcher listens on", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.relay.localhost", USER_DATA),
    "http://did-key-abc.relay.localhost:42855/v1/on-network",
  );
});

Deno.test("a publicly terminated relay is reported to at its own URL", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.xrpc.fedproxy.com", USER_DATA),
    "https://did-key-abc.xrpc.fedproxy.com/v1/on-network",
  );
});

Deno.test("a portless ingress proxy leaves the relay URL alone", () => {
  const portless = USER_DATA.replace("relay.localhost:42855", "relay.localhost");
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.relay.localhost", portless),
    "https://did-key-abc.relay.localhost/v1/on-network",
  );
});

Deno.test("a dispatcher on another domain is not assumed to front this relay", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.relay.example.com", USER_DATA),
    "https://did-key-abc.relay.example.com/v1/on-network",
  );
});

Deno.test("a trailing slash does not double up", () => {
  assertEquals(
    guestOnNetworkUrl("https://did-key-abc.xrpc.fedproxy.com/", "no proxy here"),
    "https://did-key-abc.xrpc.fedproxy.com/v1/on-network",
  );
});
