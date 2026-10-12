import { assert, assertEquals, assertStringIncludes } from "@std/assert";
import { parse } from "yaml";
import {
  buildUserData,
  HOST_KEYS_REPORTER_SCRIPT,
  HOST_KEYS_REPORTER_UNIT,
  hostKeysReporterModule,
} from "@publicdomainrelay/cloud-init-common";

const URL_ = "https://abc.relay.localhost/v1/host-keys";
const TOKEN_PATH = "/root/secrets/publicdomainrelay.com/provisioning/token";

function rendered(opts: Parameters<typeof hostKeysReporterModule>[0]) {
  const doc = parse(buildUserData({ modules: [hostKeysReporterModule(opts)] }).replace(/^#cloud-config\n/, "")) as {
    write_files: Array<{ path: string; content: string; permissions?: string }>;
    runcmd?: unknown[];
  };
  const file = (path: string) => doc.write_files.find((f) => f.path === path)?.content ?? "";
  return { doc, script: file(HOST_KEYS_REPORTER_SCRIPT), unit: file(`/etc/systemd/system/${HOST_KEYS_REPORTER_UNIT}`) };
}

Deno.test("the host-keys reporter renders a unit ordered after sshd and a script that signs the token and posts", () => {
  const { doc, script, unit } = rendered({ url: URL_, tokenPath: TOKEN_PATH });
  assertStringIncludes(unit, "After=network-online.target ssh.service");
  assertStringIncludes(unit, "Type=oneshot");
  assertStringIncludes(unit, `ExecStart=${HOST_KEYS_REPORTER_SCRIPT}`);
  assertStringIncludes(script, "ssh-keygen -Y sign -n prove-sshd -f");
  assertStringIncludes(script, `TOKEN_PATH="${TOKEN_PATH}"`);
  assertStringIncludes(script, `URL="${URL_}"`);
  assert(!script.includes("ssh-keyscan"), "the guest reports its own keys; nothing scans");
  assertEquals(doc.write_files.find((f) => f.path === HOST_KEYS_REPORTER_SCRIPT)?.permissions, "0700");
  const runcmd = JSON.stringify(doc.runcmd);
  assertStringIncludes(runcmd, `systemctl enable ${HOST_KEYS_REPORTER_UNIT}`);
  assertStringIncludes(runcmd, `systemctl start --no-block ${HOST_KEYS_REPORTER_UNIT}`);
});

Deno.test("the rendered script reports every host key with a prove-sshd signature over the token", async () => {
  const dir = await Deno.makeTempDir({ prefix: "hostkeys-script-" });
  const server = Promise.withResolvers<Record<string, unknown>>();
  let attempts = 0;
  const http = Deno.serve({ hostname: "127.0.0.1", port: 0, onListen: () => {} }, async (req) => {
    attempts++;
    if (attempts === 1) return new Response("not yet", { status: 503 });
    server.resolve(await req.json());
    return Response.json({ ok: true });
  });
  try {
    const sshDir = `${dir}/ssh`;
    await Deno.mkdir(sshDir);
    for (const t of ["ed25519", "ecdsa"]) {
      const gen = await new Deno.Command("ssh-keygen", {
        args: ["-q", "-t", t, "-N", "", "-f", `${sshDir}/ssh_host_${t}_key`],
      }).output();
      assert(gen.success);
    }
    const tokenPath = `${dir}/token`;
    const token = "eyJhbGciOiJSUzI1NiJ9.eyJub25jZSI6ImFiYyJ9.c2ln";
    await Deno.writeTextFile(tokenPath, token);
    const { script } = rendered({
      url: `http://127.0.0.1:${http.addr.port}/v1/host-keys`,
      tokenPath,
      sshDir,
      retryDelaySec: 0,
    });
    const scriptPath = `${dir}/send-hostkeys.sh`;
    await Deno.writeTextFile(scriptPath, script, { mode: 0o700 });
    const run = await new Deno.Command("bash", { args: [scriptPath], stdout: "piped", stderr: "piped" }).output();
    const out = new TextDecoder().decode(run.stdout) + new TextDecoder().decode(run.stderr);
    assert(run.success, out);
    const body = await server.promise;

    assertEquals(body.token, token);
    const keys = body.keys as Array<{ type: string; publicKey: string; fingerprint: string }>;
    assertEquals(keys.map((k) => k.type).sort(), ["ecdsa-sha2-nistp256", "ssh-ed25519"]);
    const ed = keys.find((k) => k.type === "ssh-ed25519")!;
    const pub = (await Deno.readTextFile(`${sshDir}/ssh_host_ed25519_key.pub`)).trim().split(/\s+/);
    assertEquals(ed.publicKey, pub[1]);
    assert(ed.fingerprint.startsWith("SHA256:"));

    await Deno.writeTextFile(`${dir}/allowed`, `* ssh-ed25519 ${ed.publicKey}\n`);
    await Deno.writeTextFile(`${dir}/sig`, String(body.signature));
    const check = new Deno.Command("ssh-keygen", {
      args: ["-Y", "check-novalidate", "-n", "prove-sshd", "-f", `${dir}/allowed`, "-s", `${dir}/sig`],
      stdin: "piped",
      stdout: "null",
      stderr: "null",
    }).spawn();
    const w = check.stdin.getWriter();
    await w.write(new TextEncoder().encode(token));
    await w.close();
    assert((await check.status).success, "the signature verifies over the token with the reported ed25519 key");
    assertEquals(attempts, 2, "a refused post is retried");
  } finally {
    await http.shutdown();
    await Deno.remove(dir, { recursive: true });
  }
});
