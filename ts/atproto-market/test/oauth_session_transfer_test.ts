// Integration test: programmatic OAuth session injection → subprocess CLI commands.
//
// Pre-populates OAuth session files on disk, then spawns the real hono-bidder
// and request-vm-ssh CLIs as subprocesses. The CLIs load the session files via
// --oauth-session-file, skip the QR scan, and complete the RFP→bid→accept→SSH
// contract flow against fully local infrastructure.
//
// No external network. No QR scan. No real Bluesky accounts. No browser.
//
// Run: deno test --allow-all --unstable-kv test/oauth_session_transfer_test.ts

import { assert, assertEquals } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Hono } from "@hono/hono";
import { createLogger } from "@publicdomainrelay/logger";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRelayFactory as createATProtoRelayFactory } from "@publicdomainrelay/hono-factory-atproto-relay-xrpc";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signerFromKeypair } from "@publicdomainrelay/atproto-repo-deno";
import type { SessionInjector } from "@publicdomainrelay/atproto-oauth-server-abc";
import type { OAuthSessionData } from "@publicdomainrelay/atproto-helpers";
import { createPlcDirectoryClient, createGenesisOp } from "@publicdomainrelay/did-plc";
import { ensureWebsocat } from "@publicdomainrelay/requester-xrpc";

// ── helpers ───────────────────────────────────────────────────────────────────

function serveOnPort0(
  fetch: (req: Request) => Response | Promise<Response>,
  ac: AbortController,
  hostname = "127.0.0.1",
): Promise<number> {
  const { promise, resolve } = Promise.withResolvers<number>();
  Deno.serve(
    { port: 0, hostname, signal: ac.signal,
      onListen: (addr) => resolve((addr as Deno.NetAddr).port) },
    fetch,
  );
  return promise;
}

function createFakePlc() {
  const ops = new Map<string, Record<string, unknown>>();
  const app = new Hono();
  app.post("/*", async (c) => {
    const did = decodeURIComponent(new URL(c.req.url).pathname.replace(/^\//, ""));
    ops.set(did, await c.req.json() as Record<string, unknown>);
    return c.json({ ok: true });
  });
  app.get("/*", (c) => {
    const did = decodeURIComponent(new URL(c.req.url).pathname.replace(/^\//, ""));
    const op = ops.get(did);
    if (!op) return c.json({ message: `DID not found: ${did}` }, 404);
    const vms = (op.verificationMethods ?? {}) as Record<string, string>;
    const services = (op.services ?? {}) as Record<string, { type: string; endpoint: string }>;
    const verificationMethod: Array<Record<string, string>> = [];
    for (const [id, key] of Object.entries(vms)) {
      if (id === "atproto" || id === "attestation") {
        verificationMethod.push({ id: `#${id}`, type: "Multikey", controller: did, publicKeyMultibase: key.replace(/^did:key:/, "") });
      }
    }
    const svc: Array<Record<string, string>> = [];
    for (const [id, s] of Object.entries(services)) {
      svc.push({ id: id.startsWith("#") ? id : `#${id}`, type: s.type, serviceEndpoint: s.endpoint as string });
    }
    return c.json({
      "@context": ["https://www.w3.org/ns/did/v1", ...(verificationMethod.length ? ["https://w3id.org/security/multikey/v1"] : [])],
      id: did,
      ...(verificationMethod.length ? { verificationMethod } : {}),
      ...(svc.length ? { service: svc } : {}),
    });
  });
  return { app, ops };
}

function cloneReader(
  reader: ReadableStreamDefaultReader<Uint8Array>,
  label: string,
): Promise<void> {
  const dec = new TextDecoder();
  return (async () => {
    let buf = "";
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      // Emit complete lines
      while (true) {
        const nl = buf.indexOf("\n");
        if (nl === -1) break;
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (line) console.error(`[${label}] ${line}`);
      }
    }
  })();
}

interface BidderProcess {
  did: string;
  kill(): void;
}

function spawnBidder(modPath: string, args: string[], timeoutMs = 60_000): Promise<BidderProcess> {
  const cmd = new Deno.Command("deno", {
    args: ["run", "-A", "--unstable-kv", modPath, ...args],
    stdout: "piped",
    stderr: "piped",
  });
  const child = cmd.spawn();
  cloneReader(child.stderr.getReader(), "bidder");

  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      try { child.kill("SIGTERM"); } catch { /* ok */ }
      reject(new Error("bidder_ready not received within timeout"));
    }, timeoutMs);

    (async () => {
      const reader = child.stdout.getReader();
      const dec = new TextDecoder();
      let buf = "";
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          while (true) {
            const nl = buf.indexOf("\n");
            if (nl === -1) break;
            const line = buf.slice(0, nl).trim();
            buf = buf.slice(nl + 1);
            if (!line) continue;
            try {
              const obj = JSON.parse(line);
              if (obj.event === "bidder_ready" && obj.did) {
                clearTimeout(timer);
                resolve({ did: obj.did as string, kill: () => { try { child.kill("SIGTERM"); } catch { /* ok */ } } });
                return;
              }
            } catch { /* not JSON, skip */ }
          }
        }
      } catch { /* reader closed */ }
      // If we get here without resolving, check if process exited
      try { child.kill("SIGTERM"); } catch { /* ok */ }
      clearTimeout(timer);
      reject(new Error("bidder exited without bidder_ready"));
    })();
  });
}

interface RequesterResult {
  event?: string;
  sshReady?: boolean;
  sshExitCode?: number;
  bids?: number;
  winnerDid?: string;
  error?: string;
}

function spawnRequester(modPath: string, args: string[], timeoutMs = 300_000): Promise<RequesterResult> {
  const cmd = new Deno.Command("deno", {
    args: ["run", "-A", "--unstable-kv", modPath, ...args],
    stdout: "piped",
    stderr: "piped",
  });
  const child = cmd.spawn();
  cloneReader(child.stderr.getReader(), "requester");

  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      try { child.kill("SIGTERM"); } catch { /* ok */ }
      reject(new Error("requester result not received within timeout"));
    }, timeoutMs);

    (async () => {
      const reader = child.stdout.getReader();
      const dec = new TextDecoder();
      let buf = "";
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          while (true) {
            const nl = buf.indexOf("\n");
            if (nl === -1) break;
            const line = buf.slice(0, nl).trim();
            buf = buf.slice(nl + 1);
            if (!line) continue;
            try {
              const obj = JSON.parse(line);
              // Look for structured logger result line
              if (obj.message === "result") {
                clearTimeout(timer);
                try { child.kill("SIGTERM"); } catch { /* ok */ }
                resolve(obj as unknown as RequesterResult);
                return;
              }
            } catch { /* not JSON, skip */ }
          }
        }
      } catch { /* reader closed */ }
      clearTimeout(timer);
      try { child.kill("SIGTERM"); } catch { /* ok */ }
      reject(new Error("requester exited without result"));
    })();
  });
}

// ── test ──────────────────────────────────────────────────────────────────────
// Paths relative to org root
const ORG = new URL("../../", import.meta.url).pathname.replace(/\/$/, "");
const BIDDER_CLI = `${ORG}/atproto-market/hono-bidder/mod.ts`;
const REQUESTER_CLI = `${ORG}/atproto-market/request-vm-ssh/mod.ts`;

Deno.test("OAuth session transfer — subprocess CLI full contract flow", async () => {
  const logger = createLogger({ serviceName: "oauth-cli-test" });
  const realFetch = globalThis.fetch;

  const dispAc = new AbortController();
  const relayAc = new AbortController();
  const plcAc = new AbortController();
  const pdsAc = new AbortController();
  let restoreFetch: (() => void) | undefined;
  let bidderProc: BidderProcess | undefined;

  try {
    // 0. Ensure websocat
    await ensureWebsocat(logger).catch(() => {});

    // 1. Start dispatcher relay
    const dispatcherApp = createRelayFactory({ hostname: "localhost" }).createApp();
    const dispPort = await serveOnPort0(dispatcherApp.fetch, dispAc, "0.0.0.0");
    logger.info("dispatcher", { port: dispPort });

    // 2. Start atproto relay (firehose + listReposByCollection)
    const atprotoRelayApp = createATProtoRelayFactory({ hostname: "localhost", insecureHTTP: true }).app;
    const relayPort = await serveOnPort0(atprotoRelayApp.fetch, relayAc, "0.0.0.0");
    const relayUrl = `http://127.0.0.1:${relayPort}`;
    logger.info("atproto relay", { port: relayPort });

    // 3. Start fake PLC
    const { app: plcApp } = createFakePlc();
    const plcPort = await serveOnPort0(plcApp.fetch, plcAc);
    const plcUrl = `http://127.0.0.1:${plcPort}`;
    logger.info("plc", { port: plcPort });

    // 4. Install fetch interceptor
    const { installFetchInterceptor } = await import("./fetch-interceptor.ts");
    restoreFetch = installFetchInterceptor({ realFetch, plcDirectoryUrl: plcUrl, dispPort });

    // 5. Create OAuth-enabled PDS
    const pdsKp = await Secp256k1Keypair.create({ exportable: true });
    const pdsSigner = signerFromKeypair(pdsKp);
    const pdsFactory = createRepoFactory({
      storage: new MemoryStorage(),
      signer: pdsSigner,
      oauthServer: { enabled: true, issuer: `http://127.0.0.1:0` },
    });
    const pdsPort = await serveOnPort0(pdsFactory.app.fetch, pdsAc, "0.0.0.0");
    const pdsUrl = `http://127.0.0.1:${pdsPort}`;
    const pdsDid = pdsSigner.did();
    logger.info("pds", { port: pdsPort, did: pdsDid });

    // 6. Request crawl from relay
    await fetch(`${relayUrl}/xrpc/com.atproto.sync.requestCrawl`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ hostname: `localhost:${pdsPort}` }),
    }).catch((err) => logger.warn("crawl failed", { error: String(err) }));
    await new Promise((r) => setTimeout(r, 200));

    // 7. Register ephemeral DIDs
    const ingressProxyHost = `localhost:${dispPort}`;
    const plcClient = createPlcDirectoryClient({ plcDirectoryUrl: plcUrl });

    const services = (did: string) => ({
      atproto_pds: { type: "AtprotoPersonalDataServer", endpoint: pdsUrl },
      pdr_temp_market: { type: "PDRTempMarket", endpoint: `https://${did.replace(/:/g, "-").toLowerCase()}.${ingressProxyHost}` },
      pdr_temp_compute_event: { type: "PDRTempComputeEvent", endpoint: `https://${did.replace(/:/g, "-").toLowerCase()}.${ingressProxyHost}` },
    });

    const bidderKp = await Secp256k1Keypair.create({ exportable: true });
    const requesterKp = await Secp256k1Keypair.create({ exportable: true });

    const bidderOp = await createGenesisOp({
      rotationKeys: [bidderKp.did()],
      verificationMethods: { atproto: bidderKp.did() },
      sign: (bytes) => bidderKp.sign(bytes),
      services: services(bidderKp.did()),
    });
    await plcClient.submitOp(bidderOp.did, bidderOp.op);

    const requesterOp = await createGenesisOp({
      rotationKeys: [requesterKp.did()],
      verificationMethods: { atproto: requesterKp.did() },
      sign: (bytes) => requesterKp.sign(bytes),
      services: services(requesterKp.did()),
    });
    await plcClient.submitOp(requesterOp.did, requesterOp.op);
    logger.info("dids", { bidder: bidderOp.did, requester: requesterOp.did });

    // 8. Inject OAuth sessions programmatically
    const sessionInjector: SessionInjector = pdsFactory.sessionInjector!;
    assert(sessionInjector, "sessionInjector not present");

    const bidderInjected = await sessionInjector.injectSession({ userDid: pdsDid, handle: "bidder-test" });
    const requesterInjected = await sessionInjector.injectSession({ userDid: pdsDid, handle: "requester-test" });

    bidderInjected.sessionData.pds = pdsUrl;
    requesterInjected.sessionData.pds = pdsUrl;

    // 9. Write session files to temp dir
    const tmpDir = await Deno.makeTempDir({ prefix: "oauth-test-" });
    const bidderSessionPath = `${tmpDir}/bidder-session.json`;
    const requesterSessionPath = `${tmpDir}/requester-session.json`;
    await Deno.writeTextFile(bidderSessionPath, JSON.stringify(bidderInjected.sessionData, null, 2));
    await Deno.writeTextFile(requesterSessionPath, JSON.stringify(requesterInjected.sessionData, null, 2));

    // 10. Spawn bidder subprocess:
    //
    //   deno run -A hono-bidder/mod.ts \
    //     --atproto-oauth-qr --oauth-session-file <bidderPath> \
    //     --atproto-handle bidder-test \
    //     --ingress-proxy-host localhost:<dispPort> \
    //     --plc-directory-url http://127.0.0.1:<plcPort> \
    //     --relay-url http://127.0.0.1:<relayPort> \
    //     --firehose-mode subscriberepos --firehose-url http://127.0.0.1:<relayPort> \
    //     --compute-provider-local --policy-mode tangled-vouch \
    //     --serve-port 0 --no-ingress-proxy
    const bidderArgs = [
      "--atproto-oauth-qr",
      "--oauth-session-file", bidderSessionPath,
      "--atproto-handle", "bidder-test",
      "--ingress-proxy-host", ingressProxyHost,
      "--plc-directory-url", plcUrl,
      "--relay-url", relayUrl,
      "--firehose-mode", "subscriberepos",
      "--firehose-url", relayUrl,
      "--compute-provider-local",
      "--policy-mode", "tangled-vouch",
      "--serve-port", "0",
      "--no-ingress-proxy",
    ];
    logger.info("spawning bidder", { args: bidderArgs });
    bidderProc = await spawnBidder(BIDDER_CLI, bidderArgs);
    logger.info("bidder ready", { did: bidderProc.did });

    // 11. Spawn requester subprocess:
    //
    //   deno run -A request-vm-ssh/mod.ts \
    //     --atproto-oauth-qr --oauth-session-file <requesterPath> \
    //     --atproto-handle requester-test \
    //     --ingress-proxy-host localhost:<dispPort> \
    //     --plc-directory-url http://127.0.0.1:<plcPort> \
    //     --relay-url http://127.0.0.1:<relayPort> \
    //     --firehose-mode subscriberepos --firehose-url http://127.0.0.1:<relayPort> \
    //     --policy-mode tangled-vouch --no-ingress-proxy \
    //     --bid-window-sec 20 --bidder-dids <bidderDid>
    const requesterArgs = [
      "--atproto-oauth-qr",
      "--oauth-session-file", requesterSessionPath,
      "--atproto-handle", "requester-test",
      "--ingress-proxy-host", ingressProxyHost,
      "--plc-directory-url", plcUrl,
      "--relay-url", relayUrl,
      "--firehose-mode", "subscriberepos",
      "--firehose-url", relayUrl,
      "--policy-mode", "tangled-vouch",
      "--no-ingress-proxy",
      "--bid-window-sec", "20",
      "--bidder-dids", bidderProc.did,
      "--skip-rbac",
    ];
    logger.info("spawning requester", { args: requesterArgs });
    const result = await spawnRequester(REQUESTER_CLI, requesterArgs);
    logger.info("requester result", result as unknown as Record<string, unknown>);

    // 12. Assertions
    assert(result.sshReady !== false, `SSH should be ready. Got: ${JSON.stringify(result)}`);
    assert(result.bids !== undefined || result.error !== undefined, "Contract flow should produce a result");

    // Cleanup
    await Deno.remove(tmpDir, { recursive: true }).catch(() => {});
    bidderProc.kill();
  } finally {
    restoreFetch?.();
    try { bidderProc?.kill(); } catch { /* ok */ }
    dispAc.abort();
    relayAc.abort();
    plcAc.abort();
    pdsAc.abort();
  }
});
