import { assertEquals, assertStringIncludes } from "@std/assert";
import { createK3sJoinModule, K3S_JOIN_SSHD_DROPIN, renderK3sJoinScript } from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import {
  DEFAULT_DNS_GATEWAY,
  renderResolver,
  RESOLV_CONF_PATH,
} from "@publicdomainrelay/cloud-init-plugin-k3s-join";
import {
  encodeNodeJoinDocument,
  K3S_JOIN_GO,
  K3S_JOIN_PATH,
  K3S_JOIN_READY_MARKER,
  pinnedJoinToken,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

const DOC = {
  nodeName: "ci-node1",
  serverUrl: "https://100.127.255.254:23456",
  advertiseAddress: "100.127.255.254",
  token: pinnedJoinToken("ab".repeat(32), "k10secret"),
  nodePassword: "k10password",
};

async function shims(dir: string) {
  await Deno.mkdir(`${dir}/bin`, { recursive: true });
  await Deno.writeTextFile(`${dir}/bin/ip`, `#!/bin/bash\necho "$*" >> ${dir}/ip.log\n`, { mode: 0o755 });
  await Deno.writeTextFile(`${dir}/bin/k3s`, `#!/bin/bash\necho "K3S $*"\n`, { mode: 0o755 });
  await Deno.writeTextFile(`${dir}/stamp`, "");
  const script = renderK3sJoinScript({
    setupWaitSec: 2,
    setupStamp: `${dir}/stamp`,
    tokenPath: `${dir}/kcp/token`,
    nodePasswordPath: `${dir}/node/password`,
    k3sDataDir: `${dir}/data`,
  });
  await Deno.writeTextFile(`${dir}/join.sh`, script, { mode: 0o755 });
}

async function run(dir: string, stdin: string[]) {
  const child = new Deno.Command("bash", {
    args: [`${dir}/join.sh`],
    env: { PATH: `${dir}/bin:/usr/bin:/bin` },
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  const writer = child.stdin.getWriter();
  for (const part of stdin) await writer.write(new TextEncoder().encode(part));
  await writer.close();
  const out = await child.output();
  return { code: out.code, stdout: new TextDecoder().decode(out.stdout), stderr: new TextDecoder().decode(out.stderr) };
}

Deno.test("the join script takes its secrets on stdin, waits for the go-ahead, then execs the agent", async () => {
  const dir = await Deno.makeTempDir();
  try {
    await shims(dir);
    const out = await run(dir, [encodeNodeJoinDocument(DOC), `${K3S_JOIN_GO}\n`]);
    assertEquals(out.code, 0, out.stderr);
    const lines = out.stdout.trim().split("\n");
    assertEquals(lines[0], K3S_JOIN_READY_MARKER);
    assertEquals(
      lines[1],
      `K3S agent --server ${DOC.serverUrl} --token-file ${dir}/kcp/token --node-name ci-node1 --data-dir ${dir}/data`,
    );
    assertEquals(await Deno.readTextFile(`${dir}/kcp/token`), DOC.token);
    assertEquals(await Deno.readTextFile(`${dir}/node/password`), DOC.nodePassword);
    assertEquals((await Deno.stat(`${dir}/kcp/token`)).mode! & 0o777, 0o600);
    assertStringIncludes(await Deno.readTextFile(`${dir}/ip.log`), `addr add ${DOC.advertiseAddress}/32 dev lo`);
    assertEquals(out.stdout.includes("k10secret") || out.stderr.includes("k10secret"), false, "the token is never echoed");
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the join script never starts the agent without the go-ahead", async () => {
  const dir = await Deno.makeTempDir();
  try {
    await shims(dir);
    const out = await run(dir, [encodeNodeJoinDocument(DOC)]);
    assertEquals(out.code, 65);
    assertEquals(out.stdout.includes("K3S agent"), false);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the join script refuses an incomplete or unknown document", async () => {
  const dir = await Deno.makeTempDir();
  try {
    await shims(dir);
    const partial = encodeNodeJoinDocument(DOC).split("\n").filter((l) => !l.startsWith("TOKEN ")).join("\n");
    assertEquals((await run(dir, [partial, "GO\n"])).code, 64);
    assertEquals((await run(dir, ["EVIL eA==\n\n", "GO\n"])).code, 64);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the join script gives up when k3s never arrives", async () => {
  const dir = await Deno.makeTempDir();
  try {
    await shims(dir);
    await Deno.remove(`${dir}/bin/k3s`);
    const out = await run(dir, [encodeNodeJoinDocument(DOC), "GO\n"]);
    assertEquals(out.code, 69);
  } finally {
    await Deno.remove(dir, { recursive: true });
  }
});

Deno.test("the module carries no secret and only allows reverse forwards", () => {
  const patch = createK3sJoinModule()({});
  const files = Object.fromEntries((patch.write_files ?? []).map((f) => [f.path, f.content]));
  assertEquals(files[K3S_JOIN_SSHD_DROPIN], "AllowTcpForwarding remote\nGatewayPorts clientspecified\n");
  assertStringIncludes(files[K3S_JOIN_PATH]!, "exec k3s agent");
  assertEquals(JSON.stringify(patch).includes("k10"), false);
});

Deno.test("the module gives the guest the resolver the provider's network forwards through", () => {
  const patch = createK3sJoinModule()({});
  const files = Object.fromEntries((patch.write_files ?? []).map((f) => [f.path, f.content]));
  assertEquals(files[RESOLV_CONF_PATH], renderResolver([DEFAULT_DNS_GATEWAY]));
  assertEquals(Boolean(patch.bootcmd?.length), true, "the resolver is written before cloud-init's own stages");
  assertEquals(renderResolver(["10.0.0.1"]), "nameserver 10.0.0.1\noptions timeout:2 attempts:3\n");
});

Deno.test("the pinned token names the server CA hash", () => {
  assertEquals(pinnedJoinToken("ff", "k10abc"), "K10ff::server:k10abc");
});
