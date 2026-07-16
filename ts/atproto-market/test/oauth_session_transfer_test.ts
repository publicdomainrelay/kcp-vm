// Integration test: OAuth session injection → subprocess CLIs → full RFP→bid→accept→SSH.
// Verifies: firehose-only relay delivery, tangled-vouch policy, container provisioning,
// tunnel subscriber guest registration, and SSH command execution over the relay.
//
// Architecture:
//   fake PLC + dispatcher relay + atproto-relay + fetch interceptor
//   → one OAuth-PDS (JSON firehose, PLC support, multi-tenant)
//   → bidder + requester accounts (created via createAccount)
//   → OAuth session injection (programmatic tokens)
//   → tangled-vouch records (mutual vouch + badgeBlueKeys)
//   → bidder subprocess (firehose-only, compute-provider-local)
//   → requester subprocess (firehose-only, tangled-vouch)
//   → SSH shell output from guest VM over relay
import { assert } from "@std/assert";
import { Hono } from "@hono/hono";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createLogger } from "@publicdomainrelay/logger";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signerFromKeypair } from "@publicdomainrelay/atproto-repo-deno";
import { createRelayFactory as createDispatcherFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRelayFactory as createAtprotoRelayFactory } from "@publicdomainrelay/hono-factory-atproto-relay-xrpc";
import type { SessionInjector } from "@publicdomainrelay/atproto-oauth-server-abc";
import type { ContainerBackend } from "@publicdomainrelay/container-backend-abc";
import { createContainerBackend } from "@publicdomainrelay/container-backend-container";
import { createDockerBackend } from "@publicdomainrelay/container-backend-docker";
import { generateLocalhostTlsCert } from "@publicdomainrelay/tls-localhost";

const ORG = new URL("../../", import.meta.url).pathname.replace(/\/$/, "");

// ── Helpers ────────────────────────────────────────────────────────────────

function serveOnPort0(
  f: (r: Request) => Response | Promise<Response>,
  ac: AbortController,
  hostname = "127.0.0.1",
  cert?: string,
  key?: string,
): Promise<number> {
  const { promise, resolve } = Promise.withResolvers<number>();
  const tlsOpts = cert && key ? { cert, key } : {};
  Deno.serve(
    {
      port: 0,
      hostname,
      signal: ac.signal,
      onListen: (a) => resolve((a as Deno.NetAddr).port),
      ...tlsOpts,
    },
    f,
  );
  return promise;
}

function b64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function createAccount(
  pdsUrl: string, handle: string, password = "test", email = `${handle}@test`,
): Promise<{ did: string; accessJwt: string; refreshJwt: string }> {
  return fetch(`${pdsUrl}/xrpc/com.atproto.server.createAccount`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ handle, email, password }),
  }).then((r) => r.json() as Promise<{ did: string; accessJwt: string; refreshJwt: string }>);
}

// ── Fake PLC ────────────────────────────────────────────────────────────────

function createFakePlc() {
  const ops = new Map<string, { op: Record<string, unknown>; did: string }>();
  const app = new Hono();

  // POST /<did> — store genesis op under the DID in the URL path.
  // Both PLC clients in this flow (hono-pds createAccount and did-plc submitOp)
  // derive did:plc themselves per spec and publish at /<did:plc>; submitOp
  // ignores the response body entirely. Deriving our own DID here would store
  // under a key nobody ever resolves.
  app.post("/*", async (c) => {
    const did = decodeURIComponent(new URL(c.req.url).pathname.slice(1));
    const op = await c.req.json().catch(() => ({}));
    ops.set(did, { op: op as Record<string, unknown>, did });
    return c.json({ did });
  });

  // GET /<did> — resolve DID document
  app.get("/*", (c) => {
    const did = decodeURIComponent(new URL(c.req.url).pathname.slice(1));
    const entry = ops.get(did);
    if (!entry) return c.json({ message: `DID not found: ${did}` }, 404);
    const op = entry.op;
    const vms = (op.verificationMethods ?? {}) as Record<string, string>;
    const svcs = (op.services ?? {}) as Record<string, { type: string; endpoint: string }>;
    return c.json({
      "@context": ["https://www.w3.org/ns/did/v1", "https://w3id.org/security/multikey/v1"],
      id: did,
      alsoKnownAs: (op.alsoKnownAs ?? []) as string[],
      verificationMethod: Object.entries(vms).map(([name, didKey]) => ({
        id: `${did}#${name}`,
        type: "Multikey",
        controller: did,
        publicKeyMultibase: String(didKey).replace(/^did:key:/, ""),
      })),
      service: Object.entries(svcs).map(([name, s]) => ({
        id: `#${name}`,
        type: s.type,
        serviceEndpoint: s.endpoint,
      })),
    });
  });

  return { app };
}

// ── Vouch helpers ───────────────────────────────────────────────────────────

const VOUCH_NSID = "sh.tangled.graph.vouch";
const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";

async function createRecordDpop(
  pdsUrl: string, session: { accessJwt: string; dpopPublicJwk: Record<string, string>; dpopPrivateJwk: Record<string, string> },
  userDid: string, collection: string, rkey: string, record: Record<string, unknown>,
): Promise<{ uri: string }> {
  // Build DPoP proof
  const enc = new TextEncoder();
  const now = Math.floor(Date.now() / 1000);
  const htu = `${pdsUrl}/xrpc/com.atproto.repo.createRecord`;
  const proofHeader = { alg: "ES256", typ: "dpop+jwt", jwk: session.dpopPublicJwk };
  const proofPayload = {
    htm: "POST", htu, iat: now, jti: crypto.randomUUID(),
  };
  const headerB64 = b64url(enc.encode(JSON.stringify(proofHeader)));
  const payloadB64 = b64url(enc.encode(JSON.stringify(proofPayload)));
  const signingInput = `${headerB64}.${payloadB64}`;
  const key = await crypto.subtle.importKey(
    "jwk", session.dpopPrivateJwk, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"],
  );
  const sig = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, key, enc.encode(signingInput));
  const dpopProof = `${signingInput}.${b64url(new Uint8Array(sig))}`;

  const res = await fetch(`${pdsUrl}/xrpc/com.atproto.repo.createRecord`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "Authorization": `DPoP ${session.accessJwt}`,
      "DPoP": dpopProof,
    },
    body: JSON.stringify({ repo: userDid, collection, rkey, record, validate: false }),
  });
  if (!res.ok) {
    const err = await res.text().catch(() => "");
    throw new Error(`createRecord ${collection} failed: ${res.status} ${err}`);
  }
  return res.json() as Promise<{ uri: string }>;
}

// ── Tests ───────────────────────────────────────────────────────────────────

Deno.test("OAuth session restore from CLI subprocess", async () => {
  const log = createLogger({ serviceName: "test" });
  const pdsAc = new AbortController();

  try {
    // 1. Create OAuth-enabled PDS
    const kp = await Secp256k1Keypair.create({ exportable: true });
    const pds = createRepoFactory({
      storage: new MemoryStorage(), signer: signerFromKeypair(kp),
      oauthServer: { enabled: true, issuer: "http://127.0.0.1:0" },
    });
    const port = await serveOnPort0(pds.app.fetch, pdsAc, "0.0.0.0");
    const pdsUrl = `http://127.0.0.1:${port}`;
    log.info("pds", { url: pdsUrl });

    // 2. Create account + inject OAuth session
    const acct = await createAccount(pdsUrl, "test");
    log.info("account", { did: acct.did });

    const inj: SessionInjector = pds.sessionInjector!; assert(inj);
    const sess = (await inj.injectSession({ userDid: acct.did, handle: "test" })).sessionData;
    sess.pds = pdsUrl;

    // 3. Write session file
    const tmp = await Deno.makeTempDir({ prefix: "oas-" });
    const path = `${tmp}/session.json`;
    await Deno.writeTextFile(path, JSON.stringify(sess, null, 2));

    // 4. Spawn bidder CLI — verify session restores
    const cmd = new Deno.Command("deno", {
      args: ["run", "-A", "--unstable-kv", `${ORG}/atproto-market/hono-bidder/mod.ts`,
        "--atproto-oauth-qr", "--oauth-session-file", path,
        "--atproto-handle", "test", "--skip-qr",
        "--firehose-mode", "off",
      ],
      stdout: "piped", stderr: "piped",
      env: { ...Deno.env.toObject(), ATPROTO_DID: "" },
    });
    const child = cmd.spawn();
    const dec = new TextDecoder();

    const stdoutReader = child.stdout.getReader();
    const stderrReader = child.stderr.getReader();
    let foundRestored = false;
    let foundReady = false;
    const timeout = setTimeout(() => { try { child.kill("SIGTERM"); } catch { /*ok*/ } }, 30_000);

    const readStream = async (r: ReadableStreamDefaultReader<Uint8Array>, label: string) => {
      let buf = "";
      while (true) {
        const { done, value } = await r.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        while (buf.includes("\n")) {
          const nl = buf.indexOf("\n");
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          console.error(`[${label}] ${line}`);
          try {
            const o = JSON.parse(line);
            if (o.message === "oauth_qr_session_restored") foundRestored = true;
            if (o.event === "bidder_ready") foundReady = true;
          } catch { /* not JSON */ }
        }
      }
    };

    await Promise.race([
      Promise.all([readStream(stdoutReader, "out"), readStream(stderrReader, "err")]),
      new Promise((r) => setTimeout(r, 10_000)),
    ]);

    clearTimeout(timeout);
    try { child.kill("SIGTERM"); } catch { /*ok*/ }

    assert(foundRestored, "Session should be restored from file");
    assert(foundReady, "Bidder should emit bidder_ready");
    log.info("PASS — CLI subprocess restored OAuth session");
  } finally {
    pdsAc.abort();
  }
});

Deno.test({
  name: "[integration] Full RFP→bid→accept→SSH via subprocess CLIs (firehose-only, tangled-vouch)",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const log = createLogger({ serviceName: "it" });
  const cleanups: Array<() => void> = [];

  // ── 1. Start infrastructure ────────────────────────────────────────────

  // 1a. Container backend — the guest is provisioned locally, so without a
  // running runtime there is nothing to SSH into and the test cannot mean
  // anything. Skip loudly rather than assert against a phantom guest.
  const backend: ContainerBackend = Deno.build.os === "darwin"
    ? createContainerBackend()
    : createDockerBackend();
  if (!(await backend.ensureRunning())) {
    console.log(`[SKIP] container backend not available (${Deno.build.os})`);
    return;
  }
  const gateway = await backend.defaultGateway();
  log.info("container_backend", { type: backend.type, gateway });

  // 1b. Dispatcher relay (did-key-ingress-proxy).
  // Named relay.localhost, not localhost: the guest announces its FQDN as
  // <subdomain>.<ingressProxyHost>, so that host must resolve BOTH inside the
  // container (via the gateway /etc/hosts alias below) and here (*.localhost →
  // loopback). additionalHosts admits the gateway IP the guest dials.
  // TLS for the guest: the OIDC prove + onNetwork endpoints are https-only, and
  // the guest reaches them at <provider-sub>.relay.localhost — so the cert needs a
  // two-label base (a single-label wildcard like *.localhost is rejected).
  const { caCertPem, serverCertPem, serverKeyPem } = await generateLocalhostTlsCert({
    extraDnsSans: ["relay.localhost", "*.relay.localhost"],
  });

  const dispatcherApp = createDispatcherFactory({
    hostname: "relay.localhost",
    additionalHosts: [gateway],
  }).createApp();
  const dispAc = new AbortController();
  // Two listeners on one app: plain HTTP for in-process components and the
  // subprocess CLIs, TLS for the guest (OIDC prove / onNetwork require https).
  // Both on 0.0.0.0 so the guest can reach them across the container network.
  const dispPort = await serveOnPort0(dispatcherApp.fetch, dispAc, "0.0.0.0");
  const dispTlsAc = new AbortController();
  const dispTlsPort = await serveOnPort0(
    dispatcherApp.fetch, dispTlsAc, "0.0.0.0", serverCertPem, serverKeyPem,
  );
  cleanups.push(() => { dispAc.abort(); dispTlsAc.abort(); });
  const ingressProxyHost = `relay.localhost:${dispPort}`;
  log.info("dispatcher", { port: dispPort, tlsPort: dispTlsPort, ingressProxyHost });

  // 1b. Fake PLC directory (returns did:plc DIDs, PDS endpoint updated after port known)
  const { app: plcApp } = createFakePlc();
  const plcAc = new AbortController();
  const plcPort = await serveOnPort0(plcApp.fetch, plcAc);
  cleanups.push(() => plcAc.abort());
  const plcDirectoryUrl = `http://localhost:${plcPort}`;
  log.info("plc", { port: plcPort });

  // 1c. atproto-relay
  const relayApp = createAtprotoRelayFactory({
    hostname: "localhost",
    insecureHTTP: true,
  }).app;
  const relayAc = new AbortController();
  const relayPort = await serveOnPort0(relayApp.fetch, relayAc, "0.0.0.0");
  cleanups.push(() => relayAc.abort());
  const relayUrl = `http://localhost:${relayPort}`;
  log.info("relay", { port: relayPort });

  // 1d. Fetch interceptor (plc.directory → local PLC, *.localhost → local dispatcher)
  const { installFetchInterceptor } = await import("./fetch-interceptor.ts");
  const restoreFetch = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl,
    dispPort,
  });
  cleanups.push(restoreFetch);

  try {
    // ── 2. Create OAuth-enabled PDS with PLC support + JSON firehose ──────
    const pdsKp = await Secp256k1Keypair.create({ exportable: true });
    const pdsPortPromise = Promise.withResolvers<number>();
    const pdsAc = new AbortController();
    cleanups.push(() => pdsAc.abort());

    // publicHostname is read lazily (per request / per write), so we can serve on
    // port 0 and fill in the real authority once the port is known. The genesis op
    // written by createAccount and the requestCrawl both depend on it being right.
    const pdsOpts = {
      storage: new MemoryStorage(),
      signer: signerFromKeypair(pdsKp),
      oauthServer: { enabled: true, issuer: "http://127.0.0.1:0" },
      plcDirectoryUrl,
      subscribeReposFormat: "json" as const,
      publicHostname: undefined as string | undefined,
      crawlers: [relayUrl],
    };
    const pds = createRepoFactory(pdsOpts);

    const pdsPort = await serveOnPort0(pds.app.fetch, pdsAc, "0.0.0.0");
    const pdsUrl = `http://127.0.0.1:${pdsPort}`;
    pdsOpts.publicHostname = `127.0.0.1:${pdsPort}`;
    log.info("pds", { url: pdsUrl });

    // ── 3. Create bidder + requester accounts ──────────────────────────────
    // createAccount derives did:plc per spec and publishes a genesis op whose
    // atproto_pds endpoint points at this PDS, so DID resolution finds the repo.
    const bidderAcct = await createAccount(pdsUrl, "bidder");
    const requesterAcct = await createAccount(pdsUrl, "requester");
    log.info("accounts", { bidder: bidderAcct.did, requester: requesterAcct.did });

    assert(bidderAcct.did?.startsWith("did:plc:"), `bidder must get did:plc, got ${bidderAcct.did}`);
    assert(requesterAcct.did?.startsWith("did:plc:"), `requester must get did:plc, got ${requesterAcct.did}`);

    // ── 4. Inject OAuth sessions ───────────────────────────────────────────
    const inj: SessionInjector = pds.sessionInjector!; assert(inj);

    const bidderInj = await inj.injectSession({ userDid: bidderAcct.did, handle: "bidder" });
    bidderInj.sessionData.pds = pdsUrl;
    log.info("injected", { role: "bidder", did: bidderAcct.did });

    const requesterInj = await inj.injectSession({ userDid: requesterAcct.did, handle: "requester" });
    requesterInj.sessionData.pds = pdsUrl;
    log.info("injected", { role: "requester", did: requesterAcct.did });

    // ── 5. Create tangled-vouch records ────────────────────────────────────
    // Vouch records use rkey = vouchee DID (required by VouchResolver).
    // Mutual vouch: bidder vouches for requester, requester vouches for bidder.
    await createRecordDpop(pdsUrl, bidderInj.sessionData, bidderAcct.did, VOUCH_NSID, requesterAcct.did, {
      $type: VOUCH_NSID,
      vouchee: requesterAcct.did,
      createdAt: new Date().toISOString(),
    });
    log.info("vouch", { voucher: "bidder", vouchee: "requester" });

    await createRecordDpop(pdsUrl, requesterInj.sessionData, requesterAcct.did, VOUCH_NSID, bidderAcct.did, {
      $type: VOUCH_NSID,
      vouchee: bidderAcct.did,
      createdAt: new Date().toISOString(),
    });
    log.info("vouch", { voucher: "requester", vouchee: "bidder" });

    // Requester badgeBlueKeys: associate bidder as a trusted operator.
    await createRecordDpop(pdsUrl, requesterInj.sessionData, requesterAcct.did, BADGE_BLUE_KEYS_NSID,
      crypto.randomUUID().replace(/-/g, "").slice(0, 13), {
      $type: BADGE_BLUE_KEYS_NSID,
      keyId: bidderAcct.did,
      challenge: requesterAcct.did,
      service: "requester_associate",
      createdAt: new Date().toISOString(),
    });
    log.info("badgeBlueKeys", { requester: requesterAcct.did, bidder: bidderAcct.did });

    // ── 6. Write session files ─────────────────────────────────────────────
    const bidderTmp = await Deno.makeTempDir({ prefix: "oas-bidder-" });
    const bidderSessionFile = `${bidderTmp}/session.json`;
    await Deno.writeTextFile(bidderSessionFile, JSON.stringify(bidderInj.sessionData, null, 2));

    const requesterTmp = await Deno.makeTempDir({ prefix: "oas-req-" });
    const requesterSessionFile = `${requesterTmp}/session.json`;
    await Deno.writeTextFile(requesterSessionFile, JSON.stringify(requesterInj.sessionData, null, 2));

    // ── 7. Relay crawls PDS ────────────────────────────────────────────────
    const crawlRes = await fetch(`${relayUrl}/xrpc/com.atproto.sync.requestCrawl`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ hostname: `127.0.0.1:${pdsPort}` }),
    });
    log.info("crawl", { status: crawlRes.status, ok: crawlRes.ok });

    // ── 8. Spawn bidder subprocess ─────────────────────────────────────────
    // Single firehose source (relay) — avoid duplicate relayUrl instances.
    const bidderArgs = [
      "run", "-A", "--unstable-kv", `${ORG}/atproto-market/hono-bidder/mod.ts`,
      "--atproto-oauth-qr", "--oauth-session-file", bidderSessionFile,
      "--atproto-handle", "bidder", "--skip-qr",
      "--firehose-mode", "subscriberepos",
      "--firehose-url", relayUrl,
      "--plc-directory-url", plcDirectoryUrl,
      "--ingress-proxy-host", ingressProxyHost,
      "--compute-provider-local",
      "--policy-mode", "tangled-vouch",
      "--no-ingress-proxy",
      "--serve-port", "0",
      // The guest proves its SSH host key to the provider's OIDC issuer over
      // https and only then reports onNetwork. Without the TLS port the issuer
      // URL keeps :443 and the guest's curl gets connection-refused, so no token,
      // no onNetwork, and the requester never learns the guest FQDN.
      "--guest-tls-port", String(dispTlsPort),
    ];
    log.info("bidder_cmd", { args: bidderArgs });

    const bidderCmd = new Deno.Command("deno", {
      args: bidderArgs,
      stdout: "piped", stderr: "piped",
      // CA_CERT_PEM is injected into the guest's trust store by the provider so
      // its curl accepts our self-signed dispatcher cert.
      env: { ...Deno.env.toObject(), ATPROTO_DID: "", CA_CERT_PEM: caCertPem },
    });
    const bidderChild = bidderCmd.spawn();
    const dec = new TextDecoder();

    // Parse bidder stdout until bidder_ready
    let bidderDid = "";
    let bidderIngressRef = "";
    let vmDestroyed = false;
    const guestContainers = new Set<string>();
    const bidderReady = Promise.withResolvers<void>();

    // Stream subprocess output as it arrives — the bidder keeps logging (bids,
    // provisioning, failures) long after bidder_ready, and buffering to print
    // later drops exactly the lines that explain a failure.
    const readBidder = async (r: ReadableStreamDefaultReader<Uint8Array>, label: string) => {
      let buf = "";
      while (true) {
        const { done, value } = await r.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        while (buf.includes("\n")) {
          const nl = buf.indexOf("\n");
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          console.error(`[${label}] ${line}`);
          try {
            const o = JSON.parse(line);
            if (typeof o.containerName === "string") guestContainers.add(o.containerName);
            if (o.message === "submitEvent: VM destroyed") vmDestroyed = true;
            if (o.event === "bidder_ready") {
              bidderDid = o.did as string;
              bidderIngressRef = o.ingressRef as string;
              bidderReady.resolve();
            }
          } catch { /* not JSON */ }
        }
      }
    };

    const bidderStdout = bidderChild.stdout.getReader();
    const bidderStderr = bidderChild.stderr.getReader();
    readBidder(bidderStdout, "bidder-out");
    readBidder(bidderStderr, "bidder-err");

    const bidderReadyTimeout = Promise.withResolvers<never>();
    const bidderReadyTimer = setTimeout(
      () => bidderReadyTimeout.reject(new Error("bidder did not emit bidder_ready within 60s")),
      60_000,
    );
    try {
      await Promise.race([bidderReady.promise, bidderReadyTimeout.promise]);
    } finally {
      clearTimeout(bidderReadyTimer);
    }

    assert(bidderDid.startsWith("did:plc:"), `bidder_ready must carry a did:plc, got ${bidderDid}`);
    log.info("bidder_ready", { did: bidderDid, ingressRef: bidderIngressRef });

    // ── 9. Spawn requester subprocess ──────────────────────────────────────
    const requesterArgs = [
      "run", "-A", "--unstable-kv", `${ORG}/atproto-market/request-vm-ssh/mod.ts`,
      "--atproto-oauth-qr", "--oauth-session-file", requesterSessionFile,
      "--atproto-handle", "requester", "--skip-qr",
      "--firehose-mode", "subscriberepos",
      "--firehose-url", relayUrl,
      "--relay-url", relayUrl,
      "--plc-directory-url", plcDirectoryUrl,
      "--ingress-proxy-host", ingressProxyHost,
      "--policy-mode", "tangled-vouch",
      "--no-ingress-proxy",
      "--skip-rbac",
      // The guest's "localhost" is its own loopback — point relay.localhost at the
      // container gateway so the tunnel subscriber can reach this dispatcher.
      "--guest-host-aliases", `${gateway} relay.localhost`,
      "--exec", "echo SSH_OK && cat /etc/hostname",
      // Bids arrive over the firehose within milliseconds; the window only has to
      // cover firehose propagation, not human latency.
      "--bid-window-sec", "20",
      "--vm-ready-timeout-sec", "180",
      // No --keep-vm: teardown is part of the contract. The requester must issue a
      // signed vm.delete event and the bidder must destroy the guest, otherwise
      // every run leaks a container and the delete path is never exercised.
    ];
    log.info("requester_cmd", { args: requesterArgs });

    const requesterCmd = new Deno.Command("deno", {
      args: requesterArgs,
      stdout: "piped", stderr: "piped",
      env: { ...Deno.env.toObject(), ATPROTO_DID: "" },
    });
    const requesterChild = requesterCmd.spawn();

    let requesterDone = false;
    let sshOutput = "";
    let contractResult: Record<string, unknown> | undefined;
    let sawFirehoseBid = false;
    const requesterBuffer: string[] = [];

    // Must outlast the requester's own budget (bid window + vm-ready timeout),
    // otherwise we kill it mid-flow and lose the result it was about to print.
    const requesterTimeout = setTimeout(() => {
      try { requesterChild.kill("SIGTERM"); } catch { /*ok*/ }
    }, 300_000);

    const readRequester = async (r: ReadableStreamDefaultReader<Uint8Array>, label: string) => {
      let buf = "";
      while (true) {
        const { done, value } = await r.read();
        if (done) { requesterDone = true; break; }
        buf += dec.decode(value, { stream: true });
        while (buf.includes("\n")) {
          const nl = buf.indexOf("\n");
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          requesterBuffer.push(`[${label}] ${line}`);
          console.error(`[${label}] ${line}`);

          // SSH_OK only counts as guest shell output — a structured log line that
          // merely quotes the exec program back at us is not proof of a session.
          if (line.includes("SSH_OK") && !line.includes('"message":')) sshOutput = line;

          try {
            const o = JSON.parse(line);
            // Track key events
            if (o.message === "rfp_created") log.info("rfp_created", { uri: o.uri });
            if (o.message === "firehose_bid_discovered") {
              sawFirehoseBid = true;
              log.info("bid_discovered", { bidUri: o.bidUri });
            }
            if (o.message === "bid_accepted") log.info("bid_accepted", {});
            if (o.message === "vm_fqdn_discovered") log.info("vm_fqdn", { fqdn: o.fqdn });
            if (o.message === "vm_ssh_ready") log.info("ssh_ready", { fqdn: o.fqdn });
            if (o.message === "result") {
              contractResult = o as Record<string, unknown>;
              log.info("contract_result", o as unknown as Record<string, unknown>);
            }
          } catch { /* not JSON */ }
        }
      }
    };

    const reqStdout = requesterChild.stdout.getReader();
    const reqStderr = requesterChild.stderr.getReader();
    await Promise.race([
      Promise.all([readRequester(reqStdout, "req-out"), readRequester(reqStderr, "req-err")]),
      new Promise((r) => setTimeout(r, 290_000)),
    ]);
    clearTimeout(requesterTimeout);
    try { requesterChild.kill("SIGTERM"); } catch { /*ok*/ }

    // ── 10. Assert ──────────────────────────────────────────────────────────
    // The point of this test is that the whole contract flow completes over the
    // firehose and yields a real shell on the guest. None of these are optional:
    // a run that provisions nothing must fail, not report a partial pass.
    log.info("assertions", {
      sawFirehoseBid,
      sshOutput: sshOutput.includes("SSH_OK"),
      result: contractResult,
    });

    assert(contractResult, "requester must emit a contract result");

    // Discovery happened over the firehose — the reason this test exists.
    assert(sawFirehoseBid, "bid must be discovered over the firehose (firehose_bid_discovered)");
    assert(contractResult.winnerDid === bidderDid, `winner must be the bidder ${bidderDid}, got ${contractResult.winnerDid}`);

    // Contract chain settled.
    assert(contractResult.receiptOk === true, `receipt must verify, got ${contractResult.receiptOk}`);

    // SSH into the guest over the relay tunnel — the payload of the whole flow.
    assert(contractResult.sshReady === true, `guest SSH must become ready, got ${contractResult.sshReady}`);
    assert(contractResult.sshExitCode === 0, `SSH exec must exit 0, got ${contractResult.sshExitCode}`);
    assert(sshOutput.includes("SSH_OK"), "guest must return SSH_OK shell output over the relay");

    // Teardown is the last leg of the contract: the requester issues a signed
    // vm.delete event and the bidder must destroy the guest. Give the bidder a
    // moment — the event is submitted in the background and answered 200 first.
    const destroyed = await (async () => {
      const deadline = Date.now() + 60_000;
      while (Date.now() < deadline) {
        if (vmDestroyed) return true;
        await new Promise((r) => setTimeout(r, 1000));
      }
      return false;
    })();
    assert(destroyed, "bidder must destroy the guest on the requester's vm.delete event");

    // ...and the container must actually be gone, not merely reported gone.
    // inspectIp only resolves while the container still exists.
    const stillRunning: string[] = [];
    for (const name of guestContainers) {
      try {
        const ip = await backend.inspectIp(name);
        if (ip) stillRunning.push(name);
      } catch { /* gone — what we want */ }
    }
    assert(
      stillRunning.length === 0,
      `guest container(s) still running after vm.delete: ${stillRunning.join(", ")}`,
    );

    log.info("PASS — SSH_OK from guest VM over relay, discovered via firehose; guest destroyed", {
      guestContainers: [...guestContainers],
    });

    // Cleanup bidder
    try { bidderChild.kill("SIGTERM"); } catch { /*ok*/ }
  } finally {
    for (const c of cleanups.reverse()) {
      try { c(); } catch { /* best effort */ }
    }
    await new Promise((r) => setTimeout(r, 300));
  }
});
