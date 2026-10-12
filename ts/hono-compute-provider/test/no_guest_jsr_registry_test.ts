import { assert, assertStringIncludes } from "@std/assert";
import { withOnNetworkReporter } from "@publicdomainrelay/compute-provider-common";

function guestUserData(): string {
  const bundle = JSON.stringify({
    accept: { uri: "at://did:plc:x/com.publicdomainrelay.temp.market.accept/1", cid: "bafya" },
    guest_onnetwork_url: "http://did-key-abc.relay.localhost:42855/v1/on-network",
  });
  return [
    "#cloud-config",
    "write_files:",
    "  - path: /root/secrets/publicdomainrelay.com/market/accept.json",
    `    content: '${bundle}'`,
    "",
  ].join("\n");
}

Deno.test("the guest's on-network reporter waits on the iroh listener, never a tunnel subscriber", () => {
  const out = withOnNetworkReporter(guestUserData(), "https://x");
  assertStringIncludes(out, "After=network-online.target iroh.service");
  assertStringIncludes(out, "Wants=network-online.target iroh.service");
  assert(!out.includes("tunnel"), "the reporter names no tunnel unit");
  assert(!out.includes("JSR_URL"), "user_data names no guest package registry");
});
