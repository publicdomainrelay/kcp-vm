import { assert, assertEquals, assertThrows } from "@std/assert";
import {
  buildUserData,
  getUserDataModules,
  listUserDataModules,
} from "@publicdomainrelay/cloud-init-common";

const SSH = "ssh-ed25519 AAAA test@example";
const CTX = {
  vmName: "vm-test",
  sshAuthorizedKey: SSH,
};

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
