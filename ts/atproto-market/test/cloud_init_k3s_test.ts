// Snapshot + semantics tests for the k3s user-data module.
//
// Fixtures are byte-stable outputs of the composer, generated from the module
// source itself:
//   test/fixtures/cloud-init/{k3s,tunnel-k3s}.yaml
//
// Run:
//   deno test -A test/cloud_init_k3s_test.ts

import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  buildUserData,
  getUserDataModules,
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
  sshAuthorizedKey: SSH,
  ingressProxyHost: "relay.local:443",
  audHost: "relay.local",
};

const K3S_SETUP_PATH = "/usr/local/bin/setup-k3s.sh";

function fixture(name: string): Promise<string> {
  return Deno.readTextFile(
    new URL(`./fixtures/cloud-init/${name}`, import.meta.url),
  );
}

Deno.test("k3s composes alone and with the tunnel transport", async () => {
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["k3s"] }),
    await fixture("k3s.yaml"),
  );
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["tunnel", "k3s"] }),
    await fixture("tunnel-k3s.yaml"),
  );
});

Deno.test("k3s resolves by registry id and is byte-deterministic", () => {
  assert(listUserDataModules().includes("k3s"));
  assertEquals(getUserDataModules(["k3s"]).length, 1);
  assertThrows(() => getUserDataModules(["k3s-agent"]));
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["k3s"] }),
    buildUserData({ ctx: CTX, modules: ["k3s"] }),
    "the composer's bytes are the provenance the spec asserts against",
  );
});

Deno.test("k3s installs a pinned release and starts nothing", () => {
  const y = buildUserData({ ctx: CTX, modules: ["k3s"] });
  assert(y.includes("INSTALL_K3S_VERSION=v1.36.4+k3s1"), "release pinned");
  assert(y.includes("INSTALL_K3S_SKIP_START=true"), "installer does not start k3s");
  assert(y.includes("INSTALL_K3S_SKIP_ENABLE=true"), "installer does not enable k3s.service");
  assert(y.includes("k3s --version"), "the binary is proven present");
  assert(!y.includes("systemctl start k3s"), "no server start");
  assert(!y.includes("systemctl enable k3s"), "no server enable");
  assert(!y.includes("k3s server"), "ADR 0012 runs the server gateway-side");
});

Deno.test("k3s carries neither the token nor the node name", () => {
  const y = buildUserData({ ctx: CTX, modules: ["k3s"] });
  assert(!y.includes("--token"), "token travels in the command, never in user_data");
  assert(!y.includes("--node-name"), "node name travels in the command");
  assert(!y.includes("--server"), "the join target travels in the command");
  assert(!/token/i.test(y), "no token-shaped value anywhere in the document");
  assert(!y.includes("/etc/rancher/k3s/config.yaml"), "no config.yaml carrier for the token");
});

Deno.test("negative control: the discriminator is a write_files path, not the header", () => {
  const handWritten = `#cloud-config
packages:
  - curl
runcmd:
  - curl -sfL https://get.k3s.io | sh -
`;
  assert(
    handWritten.startsWith("#cloud-config\n"),
    "a hand-written document carries the same header",
  );
  assert(
    handWritten.includes("| sh -"),
    "and it WOULD start a server, the defect this module prevents",
  );
  assert(!handWritten.includes(K3S_SETUP_PATH), "and none of the module's write_files paths");
  assert(!handWritten.includes("INSTALL_K3S_SKIP_START=true"), "no skip-start either");
  assertEquals(
    buildUserData({ ctx: CTX, modules: ["k3s"] }).includes(K3S_SETUP_PATH),
    true,
    "the composer's document carries the path, so the path discriminates",
  );
  assert(
    !buildUserData({ ctx: CTX, modules: [] }).includes(K3S_SETUP_PATH),
    "and it comes from the module",
  );
});
