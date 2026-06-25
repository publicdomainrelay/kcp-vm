// Integration: ephemeral market registry (atproto-relay). A bidder registers
// its PDS via createAtprotoMarketRegistry -> requestCrawl; the relay crawls a
// minimal fake PDS firehose, indexes the offering collection, and the bidder
// then becomes discoverable through listReposByCollection.
//
// Fully in-process, no external network, no container. A real atproto-relay
// app runs on an in-memory KV; a fake PDS serves describeServer + a
// subscribeRepos firehose emitting one offering commit. fetch + WebSocket are
// patched so https://reg.localhost and wss://pds.localhost reach the local
// servers.
//
// Unstable KV enabled via deno.json ("unstable": ["kv"]). Run it with:
//   deno test --allow-all test/registry_discovery_test.ts

import { assert } from "@std/assert";
import { Hono } from "@hono/hono";
import { upgradeWebSocket } from "@hono/hono/deno";
import { createLogger } from "@publicdomainrelay/logger";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-atproto-relay-xrpc";
import { createAtprotoMarketRegistry } from "@publicdomainrelay/market-registry-atproto";
import { OFFERING_NSID } from "@publicdomainrelay/market-common";

Deno.test({
  name: "[integration] bidder PDS becomes discoverable after registry requestCrawl",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const logger = createLogger({ serviceName: "it-registry" });

  const relayHost = "reg.localhost";
  const pdsHost = "pds.localhost";
  const bidderDid = "did:plc:registrytestbidder000";

  const cleanups: Array<() => void> = [];

  // ── real atproto-relay (the market registry) on in-memory KV ──────────
  const kv = await Deno.openKv(":memory:");
  cleanups.push(() => kv.close());

  const relay = createRelayFactory({ hostname: relayHost, kv, log: logger });
  const relayCtl = new AbortController();
  const { promise: relayPortReady, resolve: resolveRelayPort } = Promise.withResolvers<number>();
  Deno.serve(
    { port: 0, hostname: "127.0.0.1", signal: relayCtl.signal, onListen: (addr) => resolveRelayPort((addr as Deno.NetAddr).port) },
    relay.app.fetch,
  );
  const relayPort = await relayPortReady;
  cleanups.push(() => relayCtl.abort());

  // ── minimal fake PDS: describeServer + subscribeRepos firehose ────────
  const pds = new Hono();
  pds.get("/xrpc/com.atproto.server.describeServer", (c) =>
    c.json({
      did: bidderDid,
      version: "0.0.0",
      availableUserDomains: [],
      inviteCodeRequired: false,
    }));
  pds.get(
    "/xrpc/com.atproto.sync.subscribeRepos",
    upgradeWebSocket(() => ({
      onOpen(_evt, ws) {
        ws.send(JSON.stringify({
          seq: 1,
          repo: bidderDid,
          rev: "rev1",
          since: null,
          blocks: [],
          ops: [{ action: "create", path: `${OFFERING_NSID}/self`, cid: null, prev: null }],
          time: new Date().toISOString(),
        }));
      },
    })),
  );
  const pdsCtl = new AbortController();
  const { promise: pdsPortReady, resolve: resolvePdsPort } = Promise.withResolvers<number>();
  Deno.serve(
    { port: 0, hostname: "127.0.0.1", signal: pdsCtl.signal, onListen: (addr) => resolvePdsPort((addr as Deno.NetAddr).port) },
    pds.fetch,
  );
  const pdsPort = await pdsPortReady;
  cleanups.push(() => pdsCtl.abort());

  // ── fetch interception: https://reg.localhost -> local relay,
  // https://pds.localhost -> local fake PDS (downgrade scheme + add port).
  const realFetch = globalThis.fetch;
  const routes: Array<[string, number]> = [[relayHost, relayPort], [pdsHost, pdsPort]];
  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
    let url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    for (const [host, port] of routes) {
      if (url.startsWith(`https://${host}`)) {
        url = `http://127.0.0.1:${port}` + url.slice(`https://${host}`.length);
        return realFetch(new Request(url, input instanceof Request ? input : init));
      }
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;
  cleanups.push(() => { globalThis.fetch = realFetch; });

  // ── WebSocket interception: wss://pds.localhost -> ws://127.0.0.1:pdsPort.
  const RealWS = globalThis.WebSocket;
  globalThis.WebSocket = class extends RealWS {
    constructor(url: string | URL, protocols?: string | string[]) {
      let u = typeof url === "string" ? url : url.href;
      if (u.startsWith(`wss://${pdsHost}`)) {
        u = `ws://127.0.0.1:${pdsPort}` + u.slice(`wss://${pdsHost}`.length);
      }
      super(u, protocols);
    }
  } as typeof WebSocket;
  cleanups.push(() => { globalThis.WebSocket = RealWS; });

  try {
    // ── bidder registers its PDS with the registry ───────────────────────
    const registry = createAtprotoMarketRegistry({ registryUrl: `https://${relayHost}`, log: logger });
    const result = await registry.registerPds(pdsHost);
    assert(result.ok, `registerPds failed: ${result.error}`);

    // ── crawl is async: poll until the offering collection indexes the DID ─
    let found = false;
    for (let i = 0; i < 50 && !found; i++) {
      const res = await fetch(
        `https://${relayHost}/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(OFFERING_NSID)}`,
      );
      const body = await res.json() as { repos?: Array<{ did: string }> };
      found = (body.repos ?? []).some((r) => r.did === bidderDid);
      if (!found) await new Promise((r) => setTimeout(r, 100));
    }

    assert(
      found,
      `bidder ${bidderDid} not discoverable via listReposByCollection after requestCrawl`,
    );
  } finally {
    for (const c of cleanups.reverse()) c();
  }
});
