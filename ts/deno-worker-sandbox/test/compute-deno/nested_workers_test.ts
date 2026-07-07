import { assertEquals } from "@std/assert";

const ORG = new URL("../../../", import.meta.url).pathname.replace(/\/$/, "");
const HONO_JSR = `${ORG}/hono-jsr/hono-package-registry/main.ts`;
const HONO_COMPUTE_DENO = `${ORG}/deno-worker-sandbox/hono-compute-deno/mod.ts`;
const FIXTURES = new URL("./fixtures/", import.meta.url);

const REGISTER_NSID = "com.publicdomainrelay.temp.compute.deno.registerWorkerManifest";
const EXECUTE_NSID = "com.publicdomainrelay.temp.compute.deno.executeWorkerInstance";

function b64urlJson(v: unknown): string {
  return btoa(JSON.stringify(v)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function createFakeJwt(audHostname: string, lxm: string): string {
  const header = { typ: "JWT", alg: "ES256K" };
  const payload = {
    iss: "did:plc:local",
    aud: `did:web:${audHostname}`,
    iat: Math.floor(Date.now() / 1000),
    exp: Math.floor(Date.now() / 1000) + 300,
    jti: crypto.randomUUID(),
    lxm,
  };
  return `${b64urlJson(header)}.${b64urlJson(payload)}.fakesig`;
}

interface Subprocess {
  port: number;
  cleanup: () => void;
}

async function spawnProcess(opts: {
  modPath: string;
  args: string[];
  env?: Record<string, string>;
  readyEvent: string;
  label: string;
}): Promise<Subprocess> {
  const decoder = new TextDecoder();
  const cmdOpts: Deno.CommandOptions = {
    args: ["run", "-A", opts.modPath, ...opts.args],
    stdout: "piped",
    stderr: "piped",
  };
  if (opts.env) cmdOpts.env = { ...Deno.env.toObject(), ...opts.env };
  const child = new Deno.Command("deno", cmdOpts).spawn();
  let killed = false;
  const cleanup = () => {
    killed = true;
    try { child.kill("SIGTERM"); } catch { /* already exited */ }
  };

  const { promise, resolve, reject } = Promise.withResolvers<number>();

  (async () => {
    for await (const chunk of child.stderr) {
      Deno.stderr.writeSync(new TextEncoder().encode(`[${opts.label}] ${decoder.decode(chunk)}`));
    }
  })();

  (async () => {
    for await (const chunk of child.stdout) {
      const text = decoder.decode(chunk, { stream: true });
      for (const line of text.split("\n")) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          const parsed = JSON.parse(trimmed);
          if (parsed.event === opts.readyEvent && typeof parsed.port === "number") {
            resolve(parsed.port);
            return;
          }
        } catch { /* not JSON, skip */ }
      }
    }
    if (!killed) reject(new Error(`[${opts.label}] process exited without ${opts.readyEvent}`));
  })();

  const timeout = setTimeout(() => {
    if (!killed) reject(new Error(`[${opts.label}] ${opts.readyEvent} timeout after 30s`));
  }, 30_000);

  try {
    const port = await promise;
    clearTimeout(timeout);
    return { port, cleanup };
  } catch (e) {
    clearTimeout(timeout);
    cleanup();
    throw e;
  }
}

// Read fixture sources
const l1Source = await Deno.readTextFile(new URL("l1-bidder/main.ts", FIXTURES));
const l1DenoJson = await Deno.readTextFile(new URL("l1-bidder/deno.json", FIXTURES));
const l2Source = await Deno.readTextFile(new URL("l2-app/main.ts", FIXTURES));
const l2DenoJson = await Deno.readTextFile(new URL("l2-app/deno.json", FIXTURES));

Deno.test({
  name: "[integration/nested] L0 hosts L1 (compute provider), L1 hosts L2 (health app)",
  sanitizeOps: false,
  sanitizeResources: false,
  fn: async () => {
    // Spawn hono-jsr + L0 (hono-compute-deno)
    const jsr = await spawnProcess({
      modPath: HONO_JSR,
      args: ["--store", "local", "--base-dir", ORG, "--port", "0"],
      readyEvent: "registry_ready",
      label: "hono-jsr",
    });

    const l0 = await spawnProcess({
      modPath: HONO_COMPUTE_DENO,
      args: ["--port=0", "--permission-mode=allow-all"],
      env: { JSR_URL: `http://127.0.0.1:${jsr.port}` },
      readyEvent: "compute_deno_ready",
      label: "l0-compute",
    });

    try {
      const base = `http://127.0.0.1:${l0.port}`;

      // Register L1 on L0 (L0 bundler runs deno check on L1 source)
      const regJwt = createFakeJwt("127.0.0.1", REGISTER_NSID);
      const regRes = await fetch(`${base}/xrpc/${REGISTER_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${regJwt}`, "content-type": "application/json" },
        body: JSON.stringify({ source: l1Source, denoJson: l1DenoJson, persistent: true, permissionMode: "allow-all", permissions: { env: true, sys: true, read: true, write: true, net: true, import: true } }),
      });
      if (regRes.status !== 200) throw new Error(`L1 register failed: ${regRes.status} ${await regRes.text()}`);
      const { instance: l1Ref } = await regRes.json() as { instance: { uri: string; cid: string } };

      // Register L2 inside L1 — test sends pre-bundled source, L1 stores + starts
      const execJwt = createFakeJwt("127.0.0.1", EXECUTE_NSID);
      const regL2Res = await fetch(`${base}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({
          instance: { uri: l1Ref.uri, cid: l1Ref.cid },
          request: { method: "POST", path: "/", body: { action: "register_l2", l2Source, l2DenoJson, l2Permissions: { net: true, env: true, import: true } } },
        }),
      });
      assertEquals(regL2Res.status, 200);
      const regL2Data = await regL2Res.json() as { status: number; body: Record<string, unknown> };
      assertEquals(regL2Data.body.registered, true);

      // Execute L1 → L1 executes L2 → L2 Hono app responds
      const execL2Res = await fetch(`${base}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({
          instance: { uri: l1Ref.uri, cid: l1Ref.cid },
          request: { method: "POST", path: "/", body: { action: "execute_l2", method: "GET", path: "/health" } },
        }),
      });
      assertEquals(execL2Res.status, 200);
      const execL2Data = await execL2Res.json() as { status: number; body: { status: number; body: Record<string, unknown> } };
      assertEquals(execL2Data.body.status, 200);
      assertEquals(execL2Data.body.body.level, 2);
      assertEquals(execL2Data.body.body.status, "ok");

      // Execute again — L2 persists (count field from Hono app)
      const execL2Res2 = await fetch(`${base}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({
          instance: { uri: l1Ref.uri, cid: l1Ref.cid },
          request: { method: "POST", path: "/", body: { action: "execute_l2", method: "GET", path: "/health" } },
        }),
      });
      assertEquals(execL2Res2.status, 200);
      const execL2Data2 = await execL2Res2.json() as { body: { body: { count: number } } };
      assertEquals(execL2Data2.body.body.count, 2, "L2 count incremented — persistent across calls");
    } finally {
      l0.cleanup();
      jsr.cleanup();
      await new Promise((r) => setTimeout(r, 100));
    }
  },
});
