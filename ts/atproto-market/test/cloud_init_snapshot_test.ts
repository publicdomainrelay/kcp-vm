// Snapshot + semantics tests for cloud-init-common buildUserData.
//
// Fixtures are byte-stable outputs of the composer (generated once during the
// migration; regenerate only when intentionally changing the composed YAML):
//   test/fixtures/cloud-init/{tunnel,fedproxy-ssh,fedproxy-web-wootty}.yaml
//
// Run:
//   deno test -A test/cloud_init_snapshot_test.ts

import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  acceptBundleModule,
  buildDefaultUserData,
  buildTunnelUserData,
  buildUserData,
  getUserDataModules,
  injectJsrUrl,
  listUserDataModules,
} from "@publicdomainrelay/cloud-init-common";

const SSH = "ssh-ed25519 AAAA test@example";
const CTX = {
  vmName: "vm-test",
  didPlc: "did:plc:abc123",
  didPlcKey: "abc123",
  relayHost: "xrpc.fedproxy.com",
  xrpcRelaySubdomain: "rly-xyz",
  sshHandle: "did:plc:abc123",
  woottyDistUrl: "https://ui.fedfork.com",
  sshAuthorizedKey: SSH,
  ingressProxyHost: "relay.local:443",
  audHost: "relay.local",
};

function fixture(name: string): Promise<string> {
  return Deno.readTextFile(
    new URL(`./fixtures/cloud-init/${name}`, import.meta.url),
  );
}

Deno.test("composer snapshots match fixtures", async () => {
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["tunnel"] }),
    await fixture("tunnel.yaml"),
  );
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["fedproxy-ssh"] }),
    await fixture("fedproxy-ssh.yaml"),
  );
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["fedproxy-web", "wootty"] }),
    await fixture("fedproxy-web-wootty.yaml"),
  );
});

Deno.test("bug fixes baked into fedproxy-ssh", () => {
  const y = buildUserData({ ctx: CTX, modules: ["fedproxy-ssh"] });
  assert(y.includes("ListenAddress 127.0.0.1"), "loopback-only sshd");
  assert(!y.includes("ListenAddress 0.0.0.0"), "no 0.0.0.0 exposure");
  assert(y.includes(`[ -f "\${STAMP}" ]`), "single-escaped ${STAMP}");
  assert(!y.includes("\\${STAMP}"), "no double-escape backslash");
  assert(y.startsWith("#cloud-config\n"));
});

Deno.test("tunnel has no ListenAddress (direct-TCP probe)", () => {
  const y = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  assert(!y.includes("ListenAddress"));
  assert(y.includes("tunnel-subscriber.service"));
  assert(y.includes(`--target-port 22`));
});

Deno.test("wootty combo carries token handoff + hardening", () => {
  const y = buildUserData({ ctx: CTX, modules: ["fedproxy-web", "wootty"] });
  assert(y.includes("get-ttyd-password-vm-test"));
  assert(y.includes("com.fedproxy.ttydCredentials"));
  assert(y.includes("ssh_pwauth: false"));
  assert(y.includes("disable_root: false"));
  assert(y.includes("WOOTTY_AUTH_TOKEN"));
});

Deno.test("deprecated wrappers equal composer outputs", () => {
  assertEquals(buildTunnelUserData({ ingressProxyHost: CTX.ingressProxyHost, audHost: CTX.audHost, sshAuthorizedKey: SSH }), buildUserData({ ctx: CTX, modules: ["tunnel"] }));
  assertEquals(buildDefaultUserData({ vmName: CTX.vmName, didPlc: CTX.didPlc!, didPlcKey: CTX.didPlcKey!, relayHost: CTX.relayHost!, xrpcRelaySubdomain: CTX.xrpcRelaySubdomain!, sshAuthorizedKey: SSH }), buildUserData({ ctx: CTX, modules: ["fedproxy-ssh"] }));
});

Deno.test("merge semantics: base + module append, dedupe, override", () => {
  const base = `#cloud-config
packages:
  - curl
write_files:
  - path: /etc/base.conf
    content: base
runcmd:
  - echo base
`;

  const merged = buildUserData({
    ctx: CTX,
    base,
    modules: ["tunnel"],
  });
  // base preserved
  assert(merged.includes("- curl"));
  assert(merged.includes("/etc/base.conf"));
  assert(merged.includes("echo base"));
  // tunnel appended
  assert(merged.includes("tunnel-subscriber.service"));

  // module write_file path dedupes with base (later wins), base entry kept
  const withOverride = buildUserData({
    ctx: CTX,
    base: `write_files:\n  - path: /etc/systemd/system/tunnel-subscriber.service\n    content: base-unit\n`,
    modules: ["tunnel"],
  });
  assertEquals((withOverride.match(/\/etc\/systemd\/system\/tunnel-subscriber\.service/g) ?? []).length, 1, "dedupe by path");
  assert(withOverride.includes("base-unit") === false, "module wins on dup path");

  // overrides beat modules
  const overridden = buildUserData({
    ctx: CTX,
    modules: ["tunnel"],
    overrides: { ssh_pwauth: true },
  });
  assert(overridden.includes("ssh_pwauth: true"));
  assert(!overridden.includes("ssh_pwauth: false"));
});

Deno.test("runcmdPrepend ordering + acceptBundleModule", () => {
  const y = buildUserData({
    ctx: CTX,
    base: `runcmd:\n  - echo base\n`,
    modules: [acceptBundleModule("/root/accept.json", { ok: true })],
  });
  const run = y.split("runcmd:")[1];
  const installIdx = run.indexOf("install -d");
  const echoIdx = run.indexOf("echo base");
  assert(installIdx >= 0 && echoIdx >= 0 && installIdx < echoIdx, "prepend before base");
  assert(y.includes("/root/accept.json"));
});

Deno.test("unparseable / empty base -> fresh build", () => {
  const fresh = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  assertEquals(buildUserData({ ctx: CTX, base: "", modules: ["tunnel"] }), fresh);
  assertEquals(buildUserData({ ctx: CTX, base: "not: [valid", modules: ["tunnel"] }), fresh);
});

Deno.test("injectJsrUrl adds JSR_URL env to tunnel unit", () => {
  const y = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  const patched = injectJsrUrl(y, "jsr.local:8080");
  assert(patched.includes(`Environment="JSR_URL=jsr.local:8080"`));
});

Deno.test("registry: built-ins present, unknown id throws", () => {
  assert(listUserDataModules().includes("tunnel"));
  assert(listUserDataModules().includes("fedproxy-ssh"));
  assert(listUserDataModules().includes("fedproxy-web"));
  assert(listUserDataModules().includes("wootty"));
  assertEquals(getUserDataModules(["tunnel"]).length, 1);
  assertThrows(() => getUserDataModules(["nope"]));
});
