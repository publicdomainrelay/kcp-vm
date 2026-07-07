import { assertEquals, assertExists } from "@std/assert";

const ORG = new URL("../../../", import.meta.url).pathname.replace(/\/$/, "");
const HONO_JSR = `${ORG}/hono-jsr/hono-package-registry/main.ts`;
const HONO_COMPUTE_DENO = `${ORG}/deno-worker-sandbox/hono-compute-deno/mod.ts`;

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

// L1 worker imports compute provider deps, sets up in-process stores/runner
const l1DenoJson = JSON.stringify({
  exports: "./mod.ts",
  imports: {
    "@publicdomainrelay/compute-deno-atproto": "jsr:@publicdomainrelay/compute-deno-atproto@^0",
    "@publicdomainrelay/compute-deno-common": "jsr:@publicdomainrelay/compute-deno-common@^0",
    "@publicdomainrelay/sandbox-deno": "jsr:@publicdomainrelay/sandbox-deno@^0",
  },
});

const l2Source = [
  `let count = 0;`,
  `self.onmessage = (e) => {`,
  `  count++;`,
  `  self.postMessage({ status: 200, headers: {}, body: { status: "ok", level: 2, count } });`,
  `};`,
].join("\n");

const l2DenoJson = JSON.stringify({ exports: "./mod.ts" });

const l1Source = [
  `// @ts-nocheck`,
  `import {`,
  `  createDenoComputeManifestStore,`,
  `  createDenoComputeInstanceStore,`,
  `  createDenoComputeInstanceRunner,`,
  `} from "@publicdomainrelay/compute-deno-atproto";`,
  `import { createDenoBundler, createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";`,
  ``,
  `const did = "did:plc:l1";`,
  `const records = new Map();`,
  `records.set(did, new Map());`,
  `let seq = 0;`,
  ``,
  `const pds = {`,
  `  async createRecord(repoDid, collection, record) {`,
  `    const rkey = "r" + (++seq).toString(16).padStart(8, "0");`,
  `    const uri = "at://" + repoDid + "/" + collection + "/" + rkey;`,
  `    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(record))));`,
  `    const hex = Array.from(hash.slice(0, 16), b => b.toString(16).padStart(2, "0")).join("");`,
  `    const cid = "bafyrei" + hex;`,
  `    if (!records.has(repoDid)) records.set(repoDid, new Map());`,
  `    records.get(repoDid).set(uri, { uri, cid, value: record });`,
  `    return { uri, cid };`,
  `  },`,
  `  async getRecord(repoDid, collection, rkey) {`,
  `    const repoRecs = records.get(repoDid);`,
  `    if (!repoRecs) return null;`,
  `    const uri = "at://" + repoDid + "/" + collection + "/" + rkey;`,
  `    return repoRecs.get(uri) || null;`,
  `  },`,
  `};`,
  ``,
  `const bundler = createDenoBundler();`,
  `const manifestStore = createDenoComputeManifestStore(pds, did);`,
  `const instanceStore = createDenoComputeInstanceStore(pds, did);`,
  `const runner = createDenoComputeInstanceRunner({`,
  `  manifestStore, instanceStore, bundler,`,
  `  createWorker: createPersistentDenoWorker,`,
  `  timeoutMs: 5000,`,
  `});`,
  ``,
  `let l2InstanceRef = null;`,
  ``,
  `self.onmessage = async (e) => {`,
  `  const msg = e.data;`,
  `  const body = msg.body || {};`,
  ``,
  `  if (body.action === "register_l2") {`,
  `    const manifestRef = await manifestStore.register({`,
  `      lock: "{}",`,
  `      json: body.l2DenoJson || "{}",`,
  `      bundle: body.l2Source || "",`,
  `    });`,
  `    const instanceRef = await instanceStore.register({ manifest: manifestRef });`,
  `    await runner.start(instanceRef, manifestRef);`,
  `    l2InstanceRef = instanceRef;`,
  `    self.postMessage({ status: 200, headers: {}, body: { registered: true, instance: instanceRef } });`,
  `  } else if (body.action === "execute_l2") {`,
  `    if (!l2InstanceRef) {`,
  `      self.postMessage({ status: 404, headers: {}, body: { error: "no L2 instance registered" } });`,
  `      return;`,
  `    }`,
  `    const response = await runner.execute(l2InstanceRef, {`,
  `      method: body.method || "GET",`,
  `      path: body.path || "/health",`,
  `    });`,
  `    self.postMessage({ status: 200, headers: {}, body: response });`,
  `  } else {`,
  `    self.postMessage({ status: 200, headers: {}, body: { status: "ok", level: 1 } });`,
  `  }`,
  `};`,
].join("\n");

Deno.test({
  name: "[integration/nested] L0 hosts L1 (compute provider), L1 hosts L2 (health app)",
  sanitizeOps: false,
  sanitizeResources: false,
  fn: async () => {
    // Spawn L0 (hono-compute-deno) + hono-jsr
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

      // Register L1 on L0
      const regJwt = createFakeJwt("127.0.0.1", REGISTER_NSID);
      const regRes = await fetch(`${base}/xrpc/${REGISTER_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${regJwt}`, "content-type": "application/json" },
        body: JSON.stringify({ source: l1Source, denoJson: l1DenoJson, persistent: true, permissionMode: "allow-all", permissions: { env: true, sys: true, read: true, net: true } }),
      });
      if (regRes.status !== 200) throw new Error(`L1 register failed: ${regRes.status} ${await regRes.text()}`);
      const { instance: l1Ref } = await regRes.json() as { instance: { uri: string; cid: string } };

      // Execute L1 — register L2 inside L1's in-process runner
      const execJwt = createFakeJwt("127.0.0.1", EXECUTE_NSID);
      const regL2Res = await fetch(`${base}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({
          instance: { uri: l1Ref.uri, cid: l1Ref.cid },
          request: { method: "POST", path: "/", body: { action: "register_l2", l2Source, l2DenoJson } },
        }),
      });
      assertEquals(regL2Res.status, 200);
      const regL2Data = await regL2Res.json() as { status: number; body: Record<string, unknown> };
      assertEquals(regL2Data.status, 200);
      assertEquals(regL2Data.body.registered, true);

      // Execute L1 — call L2 via L1's runner
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

      // Execute L1 again — L2 persists (count increments)
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
