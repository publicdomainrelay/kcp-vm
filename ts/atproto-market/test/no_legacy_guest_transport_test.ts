import { assert, assertEquals, assertThrows } from "@std/assert";
import * as cloudInit from "@publicdomainrelay/cloud-init-common";
import * as requester from "@publicdomainrelay/requester-xrpc";
import { guestAddressKind } from "@publicdomainrelay/compute-request-abc";

const ROOT = new URL("../", import.meta.url).pathname;
const LEGACY_MODULES = ["tunnel", "fedproxy-ssh", "fedproxy-web", "wootty"];
const SCANNED = ["lib", "request-vm-ssh", "hono-bidder", "hono-plc"];
const NOT_OWNED = ["lib/common/market-lexicons/"];
const SCANNED_EXTENSIONS = [".ts", ".json", ".sh", ".yaml", ".yml"];
const REFUSAL_LIST = /^export const REMOVED_TRANSPORT_MODULES = /;
const FORBIDDEN: RegExp[] = [
  /websocat/i,
  /fedproxy-client/,
  /wootty/i,
  /JSR_URL/,
  /jsrUrl/,
  /injectJsrUrl/,
  /tunnel-subscriber/,
  /com\.fedproxy\.temp\.xrpc\.tunnel/,
  /buildTunnelUserData|buildDefaultUserData|TunnelCloudInitContext/,
  /relay-fqdn/,
  /fedingressHost/,
  /transport\s*:\s*["'`](?!iroh["'`])/,
];

async function scannedFiles(): Promise<string[]> {
  const files: string[] = [];
  const stack = SCANNED.map((top) => `${ROOT}${top}`);
  while (stack.length) {
    const dir = stack.pop()!;
    for await (const entry of Deno.readDir(dir)) {
      const path = `${dir}/${entry.name}`;
      const rel = path.slice(ROOT.length);
      if (NOT_OWNED.some((prefix) => rel.startsWith(prefix))) continue;
      if (entry.isDirectory) stack.push(path);
      else if (SCANNED_EXTENSIONS.some((ext) => entry.name.endsWith(ext))) files.push(path);
    }
  }
  return files;
}

Deno.test("the user_data registry holds no tunnel, fedproxy-ssh, fedproxy-web or wootty module", () => {
  const modules = cloudInit.listUserDataModules();
  for (const legacy of LEGACY_MODULES) assert(!modules.includes(legacy), `${legacy} is still registered`);
  assert(modules.includes("iroh"), "the iroh module is the guest transport");
});

Deno.test("asking for a removed transport module is refused with iroh named as the only transport", () => {
  for (const legacy of LEGACY_MODULES) {
    const err = assertThrows(() => cloudInit.buildUserData({ modules: [legacy] }));
    assert(String(err).includes("legacy-transport"), String(err));
    assert(String(err).includes("iroh"), String(err));
  }
});

Deno.test("cloud-init-common and requester-xrpc export no legacy transport helper", () => {
  for (const name of ["buildTunnelUserData", "buildDefaultUserData", "injectJsrUrl"]) {
    assert(!(name in cloudInit), `cloud-init-common still exports ${name}`);
  }
  for (const name of ["ensureWebsocat", "tunnelWsUrl", "sshTunnelArgs"]) {
    assert(!(name in requester), `requester-xrpc still exports ${name}`);
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

Deno.test("no source names websocat, fedproxy-client, wootty, JSR_URL, the tunnel subscriber or a relay-fqdn guest path", async () => {
  const offenders: string[] = [];
  for (const path of await scannedFiles()) {
    const text = (await Deno.readTextFile(path)).split("\n").filter((line) => !REFUSAL_LIST.test(line)).join("\n");
    for (const re of FORBIDDEN) if (re.test(text)) offenders.push(`${path.slice(ROOT.length)} ${re}`);
  }
  assertEquals(offenders, []);
});
