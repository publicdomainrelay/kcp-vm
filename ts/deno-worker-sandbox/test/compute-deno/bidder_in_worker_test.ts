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

const denoJson = JSON.stringify({
  exports: "./mod.ts",
  imports: {
    "@publicdomainrelay/hono-factory-market-atproto": "jsr:@publicdomainrelay/hono-factory-market-atproto@^0",
    "@publicdomainrelay/market-common": "jsr:@publicdomainrelay/market-common@^0",
    "@publicdomainrelay/market-atproto": "jsr:@publicdomainrelay/market-atproto@^0",
  },
});

const workerSource = [
  `// @ts-nocheck`,
  `import { createMarketFactory } from "@publicdomainrelay/hono-factory-market-atproto";`,
  `import { BID_NSID, SUBMIT_BID_NSID, SUBMIT_RFP_NSID } from "@publicdomainrelay/market-common";`,
  ``,
  `const deps = {`,
  `  hostname: "worker.localhost",`,
  `  idResolver: { resolve: async () => null },`,
  `  resolve: async () => null,`,
  `  log: () => {},`,
  `};`,
  ``,
  `const handlers = {`,
  `  rfp: {`,
  `    [BID_NSID]: {`,
  `      [SUBMIT_BID_NSID]: async ({ rfpUri, rfpCid, issuerDid, log }) => {`,
  `        log("info", "bidder worker received RFP", { rfpUri, rfpCid, issuerDid });`,
  `        return {`,
  `          status: 200,`,
  `          body: {`,
  `            ok: true,`,
  `            rfp: { uri: rfpUri, cid: rfpCid },`,
  `            issuerDid,`,
  `            bidNsid: BID_NSID,`,
  `            submitBidNsid: SUBMIT_BID_NSID,`,
  `            submitRfpNsid: SUBMIT_RFP_NSID,`,
  `          },`,
  `        };`,
  `      },`,
  `    },`,
  `  },`,
  `};`,
  ``,
  `const factory = createMarketFactory(deps, handlers);`,
  `const app = factory.createApp();`,
  `app.get("/health", (c) => c.json({ status: "ok" }));`,
  ``,
  `let requestCount = 0;`,
  ``,
  `self.onmessage = async (e) => {`,
  `  requestCount++;`,
  `  const msg = e.data;`,
  `  const req = new Request(\`http://localhost\${msg.path || "/"}\`, {`,
  `    method: msg.method || "POST",`,
  `    headers: new Headers(msg.headers || {}),`,
  `    body: msg.body ? JSON.stringify(msg.body) : undefined,`,
  `  });`,
  `  const res = await app.fetch(req);`,
  `  const resBody = await res.json().catch(() => null);`,
  `  self.postMessage({`,
  `    status: res.status,`,
  `    headers: Object.fromEntries(res.headers.entries()),`,
  `    body: resBody,`,
  `    count: requestCount,`,
  `  });`,
  `};`,
].join("\n");

Deno.test({
  name: "[integration/bidder-in-worker] spawn hono-compute-deno, register bidder worker, execute, verify persistence",
  sanitizeOps: false,
  sanitizeResources: false,
  fn: async () => {
    // 1. Spawn hono-jsr
    const jsr = await spawnProcess({
      modPath: HONO_JSR,
      args: ["--store", "local", "--base-dir", ORG, "--port", "0"],
      readyEvent: "registry_ready",
      label: "hono-jsr",
    });

    // 2. Spawn hono-compute-deno
    const compute = await spawnProcess({
      modPath: HONO_COMPUTE_DENO,
      args: ["--port=0", "--permission-mode=allow-all"],
      env: { JSR_URL: `http://127.0.0.1:${jsr.port}` },
      readyEvent: "compute_deno_ready",
      label: "compute-deno",
    });

    try {
      const baseUrl = `http://127.0.0.1:${compute.port}`;

      // 3. Register persistent worker (bundler resolves deps via JSR_URL)
      const regJwt = createFakeJwt("127.0.0.1", REGISTER_NSID);
      const regRes = await fetch(`${baseUrl}/xrpc/${REGISTER_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${regJwt}`, "content-type": "application/json" },
        body: JSON.stringify({ source: workerSource, denoJson, persistent: true, permissionMode: "allow-all", permissions: { env: true, net: true, sys: true, read: true } }),
      });
      if (regRes.status !== 200) throw new Error(`Register failed: ${regRes.status} ${await regRes.text()}`);
      const regData = await regRes.json() as { instance: { uri: string; cid: string }; bundle: string };
      assertExists(regData.instance?.uri);
      const { uri, cid } = regData.instance;

      // 4. Execute worker via app.fetch proxy — health endpoint proves routing works
      const execJwt = createFakeJwt("127.0.0.1", EXECUTE_NSID);
      const execRes = await fetch(`${baseUrl}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({
          instance: { uri, cid },
          request: { method: "GET", path: "/health" },
        }),
      });
      assertEquals(execRes.status, 200);
      const execData = await execRes.json() as { status: number; body: Record<string, unknown> };
      assertEquals(execData.status, 200);
      assertEquals(execData.body.status, "ok");

      // 5. Execute again — persistent worker, requestCount increments
      const execRes2 = await fetch(`${baseUrl}/xrpc/${EXECUTE_NSID}`, {
        method: "POST", headers: { "authorization": `Bearer ${execJwt}`, "content-type": "application/json" },
        body: JSON.stringify({ instance: { uri, cid }, request: { method: "GET", path: "/health" } }),
      });
      assertEquals(execRes2.status, 200);
      const execData2 = await execRes2.json() as { status: number; body: Record<string, unknown> };
      assertEquals(execData2.status, 200);
      assertEquals(execData2.body.status, "ok");
    } finally {
      compute.cleanup();
      jsr.cleanup();
      await new Promise((r) => setTimeout(r, 100));
    }
  },
});
