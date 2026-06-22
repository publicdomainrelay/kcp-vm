// Integration: start a real bidder (container-mode local compute provider) and
// drive a real requester (request-vm-ssh lib) against it through a real local
// did-key-relay dispatcher, denying the central/default bidder so the bid must
// come from the locally-started bidder.
//
// No external network: a high-fidelity in-process fake PLC directory derives
// DID documents from the genesis ops the components submit, and global fetch is
// patched to send https://plc.directory + https://*.localhost traffic to the
// local PLC / dispatcher (the dispatcher routes by Host-header subdomain).
//
// Container mode only (no full VM, no deno workers). On macOS the local
// provider auto-selects the real `container` backend.
//
// This launches a real container, so it is opt-in. Run it with:
//   RUN_BIDDER_INTEGRATION=1 deno test --allow-all \
//     test/bidder_container_integration_test.ts

import { assert } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Hono } from "@hono/hono";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createXrpcRelay } from "@publicdomainrelay/xrpc-relay";
import { createATProto, createLocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createMarketBidder, createComputeProviderMarketBidderHooks } from "@publicdomainrelay/market-bidder";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-relay-relayer-xrpc";
import { createRequesterPDS, runComputeContract } from "@publicdomainrelay/requester-xrpc";

function allocatePort(): number {
  const l = Deno.listen({ port: 0 });
  const p = (l.addr as Deno.NetAddr).port;
  l.close();
  return p;
}

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

// ── high-fidelity fake PLC directory ──────────────────────────────────────
// POST /<did>  stores the genesis op.  GET /<did>  derives the DID document
// (verificationMethod from op.verificationMethods, service from op.services).

function createFakePlc() {
  const ops = new Map<string, Record<string, unknown>>();
  const app = new Hono();

  function didFromPath(path: string): string {
    const raw = decodeURIComponent(path.startsWith("/") ? path.slice(1) : path);
    return raw;
  }

  app.post("/*", async (c) => {
    const did = didFromPath(new URL(c.req.url).pathname);
    const op = await c.req.json().catch(() => ({}));
    ops.set(did, op as Record<string, unknown>);
    return c.json({ ok: true });
  });

  app.get("/*", (c) => {
    const did = didFromPath(new URL(c.req.url).pathname);
    const op = ops.get(did);
    if (!op) return c.json({ message: `DID not found: ${did}` }, 404);
    const vms = (op.verificationMethods ?? {}) as Record<string, string>;
    const svcs = (op.services ?? {}) as Record<string, { type: string; endpoint: string }>;
    const doc = {
      "@context": [
        "https://www.w3.org/ns/did/v1",
        "https://w3id.org/security/multikey/v1",
      ],
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
    };
    return c.json(doc);
  });

  return { app };
}

Deno.test({
  name: "[integration] bidder (container mode) wins bid when central default denied",
  sanitizeOps: false,
  sanitizeResources: false,
  ignore: Deno.build.os !== "darwin" || Deno.env.get("RUN_BIDDER_INTEGRATION") !== "1",
}, async () => {
  const logger = createLogger({ serviceName: "it" });

  const dispPort = allocatePort();
  const plcPort = allocatePort();
  const dispatcherHost = `localhost:${dispPort}`;
  const plcDirectoryUrl = `http://localhost:${plcPort}`;

  // ── fetch interception: plc.directory -> local PLC; https://*.localhost ->
  // local dispatcher (downgrade scheme, ensure dispatcher port, preserve Host
  // subdomain so the dispatcher can route to the right subscriber).
  const realFetch = globalThis.fetch;
  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
    let url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("https://plc.directory/")) {
      url = plcDirectoryUrl + url.slice("https://plc.directory".length);
      return realFetch(new Request(url, input instanceof Request ? input : init));
    }
    const m = url.match(/^https:\/\/([^/]+)(\/.*)?$/);
    if (m && (m[1].endsWith(".localhost") || m[1] === "localhost" || m[1].includes(".localhost:"))) {
      let host = m[1];
      if (!host.includes(":")) host = `${host}:${dispPort}`;
      url = `http://${host}${m[2] ?? ""}`;
      return realFetch(new Request(url, input instanceof Request ? input : init));
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;

  const cleanups: Array<() => void> = [];
  cleanups.push(() => { globalThis.fetch = realFetch; });

  try {
    // ── dispatcher (real did-key-relay relayer) ──────────────────────────
    const dispatcherApp = createRelayFactory({ hostname: "localhost" }).createApp();
    const dispatcherCtl = new AbortController();
    const dispatcherServer = Deno.serve(
      { port: dispPort, hostname: "127.0.0.1", signal: dispatcherCtl.signal, onListen: () => {} },
      dispatcherApp.fetch,
    );
    cleanups.push(() => dispatcherCtl.abort());

    // ── fake PLC ─────────────────────────────────────────────────────────
    const plc = createFakePlc();
    const plcCtl = new AbortController();
    const plcServer = Deno.serve(
      { port: plcPort, hostname: "127.0.0.1", signal: plcCtl.signal, onListen: () => {} },
      plc.app.fetch,
    );
    cleanups.push(() => plcCtl.abort());

    // ── bidder ───────────────────────────────────────────────────────────
    const bidderKeypair = await Secp256k1Keypair.create({ exportable: true });
    const bidderPrivHex = Array.from(await bidderKeypair.export())
      .map((b) => b.toString(16).padStart(2, "0")).join("");

    const pdsAgent = await createLocalPDSAgent({
      logger, keypair: bidderKeypair,
      serve: createServe({ logger }),
      plcDirectoryUrl, dispatcherHost,
    });
    await pdsAgent.beginServe();

    const atproto = await createATProto({
      logger,
      badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex: bidderPrivHex }),
      plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
      agent: pdsAgent,
    });

    const makeRelay = async () => {
      const kp = await Secp256k1Keypair.create({ exportable: true });
      return createXrpcRelay({ logger, dispatcherHost, signer: atproto.signer, keypair: kp });
    };

    // local compute provider (container mode) on its own relay/serve
    const providerRelay = await makeRelay();
    const providerServe = createServe({ logger, relays: [providerRelay] });
    const provider = createComputeProviderMarketBidderHooks({
      provider: createLocalComputeProvider({
        logger,
        atproto: atproto as unknown as ComputeAtproto,
        serve: providerServe,
        getIssuerUrl: () => didWebToHttps(providerRelay.proxyRef),
        containerMode: "container",
      }),
    });
    await providerServe.beginServe();

    // market factory on its own relay/serve
    const bidderRelay = await makeRelay();
    const bidder = await createMarketBidder({
      logger, atproto, providers: [provider], relay: bidderRelay,
      serve: createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 }, relays: [bidderRelay] }),
    });
    await bidder.beginServe();
    cleanups.push(() => bidder.shutdown());

    // ── requester (wires its own submitBid -> pendingBids handler) ────────
    const requester = await createRequesterPDS({
      port: allocatePort(),
      plcDirectoryUrl, dispatcherHost, label: "requester",
    });

    // runComputeContract deletes pendingBids[rfpUri] after collecting, so spy
    // on inserts to capture every bid the requester received.
    const seenBids: Array<{ did: string; uri: string }> = [];
    const origSet = requester.pendingBids.set.bind(requester.pendingBids);
    requester.pendingBids.set = ((k: string, v: Array<{ did: string; uri: string }>) => {
      for (const b of v) seenBids.push({ did: b.did, uri: b.uri });
      return origSet(k, v as never);
    }) as typeof requester.pendingBids.set;

    await requester.relayReady;

    // ── run the contract: deny the central default, include our bidder ────
    let contractErr: unknown;
    const contract = runComputeContract(requester, {
      skipSsh: true,
      noDelete: true,
      bidWindowSec: 8,
      vmReadyTimeoutSec: 1,
      execProgram: "true",
      extraBidderDids: [atproto.did],
      denyBidderDids: ["did:plc:centraldefaultbidder000000"],
    }).catch((e) => { contractErr = e; });
    // The bid is collected within the bid window; cap total time so real
    // container provisioning on accept cannot hang the test.
    await Promise.race([
      contract,
      new Promise((r) => setTimeout(r, 40_000)),
    ]);

    // ── assert: a bid from our locally-started bidder was collected ───────
    const ourBids = seenBids.filter((b) => b.did === atproto.did);
    assert(
      ourBids.length > 0,
      `expected >=1 bid from our bidder ${atproto.did}; saw ${seenBids.length} bid(s) from ${
        JSON.stringify(seenBids.map((b) => b.did))
      }; contractErr=${contractErr ? String(contractErr) : "none"}`,
    );
    assert(
      !seenBids.some((b) => b.did === "did:plc:centraldefaultbidder000000"),
      "central default bidder must not have bid (it was denied)",
    );
  } finally {
    for (const c of cleanups.reverse()) {
      try { c(); } catch { /* best effort */ }
    }
    await new Promise((r) => setTimeout(r, 200));
  }
});
