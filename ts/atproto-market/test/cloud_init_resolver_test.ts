// Resolver fallback semantics for the shared cloud-init composer.
//
// A market guest reaches the world only through its provider's single forwarded
// nameserver (the pasta gateway). One hiccup there fails every apt/deno fetch and
// strands the guest mid-provision, so buildUserData forces /etc/resolv.conf to
// name the gateway first and public fallbacks behind it, written in bootcmd,
// write_files, and last of all runcmd.
//
// Run:
//   deno test -A test/cloud_init_resolver_test.ts

import { assert, assertEquals } from "@std/assert";
import { buildUserData } from "@publicdomainrelay/cloud-init-common";

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

function section(y: string, key: string): string {
  const lines = y.split("\n");
  const start = lines.findIndex((l) => l === `${key}:`);
  if (start < 0) return "";
  const out: string[] = [];
  for (let i = start + 1; i < lines.length; i++) {
    const l = lines[i];
    if (l.length > 0 && !l.startsWith(" ") && !l.startsWith("-")) break;
    out.push(l);
  }
  return out.join("\n");
}

function resolvContent(y: string): string {
  const i = y.indexOf("path: /etc/resolv.conf");
  if (i < 0) return "";
  const lines = y.slice(i).split("\n");
  const start = lines.findIndex((l) => l === "    content: |");
  if (start < 0) return "";
  const out: string[] = [];
  for (let j = start + 1; j < lines.length; j++) {
    if (!lines[j].startsWith("      ")) break;
    out.push(lines[j].slice(6));
  }
  return out.join("\n");
}

function resolvWriter(content: string) {
  return () => ({
    write_files: [{
      path: "/etc/resolv.conf",
      owner: "root:root",
      permissions: "0644",
      content,
    }],
  });
}

Deno.test("default request names the gateway first, then both public fallbacks", () => {
  const y = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  const resolver = resolvContent(y);
  assertEquals(resolver.split("\n")[0], "nameserver 172.30.0.1", "gateway first");
  assert(resolver.includes("nameserver 1.1.1.1"), "first fallback");
  assert(resolver.includes("nameserver 9.9.9.9"), "second fallback");
  assertEquals(
    resolver.indexOf("nameserver 172.30.0.1") < resolver.indexOf("nameserver 1.1.1.1"),
    true,
    "gateway precedes the fallbacks",
  );
});

Deno.test("a caller-written resolver is not left empty or gateway-less", () => {
  const empty = buildUserData({ ctx: CTX, modules: [resolvWriter("")] });
  assertEquals(resolvContent(empty).split("\n")[0], "nameserver 172.30.0.1");
  assert(resolvContent(empty).includes("nameserver 1.1.1.1"));

  const callerOwn = buildUserData({
    ctx: CTX,
    modules: [resolvWriter("nameserver 8.8.8.8\n")],
  });
  const resolver = resolvContent(callerOwn);
  assertEquals(resolver.split("\n")[0], "nameserver 172.30.0.1", "gateway still first");
  assert(resolver.includes("nameserver 8.8.8.8"), "the caller's server is preserved");
  assert(resolver.includes("nameserver 9.9.9.9"), "fallbacks still present");
});

Deno.test("the resolver is written in write_files, bootcmd, and runcmd", () => {
  const y = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  assert(resolvContent(y).includes("nameserver 172.30.0.1"), "write_files entry");
  assert(section(y, "bootcmd").includes("> /etc/resolv.conf"), "bootcmd runs first");
  assert(section(y, "runcmd").includes("> /etc/resolv.conf"), "runcmd re-asserts late");
  assert(section(y, "bootcmd").includes("nameserver 172.30.0.1"));
  assert(section(y, "runcmd").includes("nameserver 172.30.0.1"));
});

Deno.test("the runcmd re-assert is the last word, after any caller runcmd", () => {
  const y = buildUserData({
    ctx: CTX,
    modules: [() => ({ runcmd: ["echo caller-step"] })],
  });
  const lines = section(y, "runcmd").trimEnd().split("\n");
  assertEquals(lines[lines.length - 1].trim(), "' > /etc/resolv.conf");
  assert(
    section(y, "runcmd").indexOf("echo caller-step") <
      section(y, "runcmd").indexOf("> /etc/resolv.conf"),
    "a later stage cannot take the resolver away",
  );
});

Deno.test("gateway and fallbacks are configurable", () => {
  const y = buildUserData({
    ctx: CTX,
    modules: ["tunnel"],
    dnsGateway: "10.0.0.1",
    dnsFallbacks: ["8.8.8.8"],
  });
  const resolver = resolvContent(y);
  assertEquals(resolver.split("\n")[0], "nameserver 10.0.0.1");
  assert(resolver.includes("nameserver 8.8.8.8"));
  assert(!resolver.includes("1.1.1.1"), "the default fallback is replaced");

  const viaCtx = buildUserData({
    ctx: { ...CTX, dnsGateway: "10.0.0.1", dnsFallbacks: ["8.8.8.8"] },
    modules: ["tunnel"],
  });
  assertEquals(resolvContent(viaCtx).split("\n")[0], "nameserver 10.0.0.1");
});

Deno.test("the resolver does not disturb the transport the ssh door needs", () => {
  const y = buildUserData({ ctx: CTX, modules: ["tunnel"] });
  assert(y.includes("tunnel-subscriber.service"));
  assert(y.includes("systemctl enable --now tunnel-subscriber.service"));
  assert(y.includes("openssh-server"));
});
