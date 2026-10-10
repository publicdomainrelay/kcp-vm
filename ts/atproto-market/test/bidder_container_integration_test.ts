// Acceptance: the iroh transport, end to end.
//
// This suite drives a complete RFP -> bid -> accept -> cloud-init -> SSH
// contract against a real container guest whose cloud-init the RFP itself
// composed, and it proves the run reached that guest over the transport the
// cloud-init deployed: the guest's own dumbpipe listener, reached by the ticket
// the guest reported outbound.
//
// No external network: a high-fidelity in-process fake PLC directory derives
// DID documents (including the atproto signing key the relay and the market
// resolve) from the genesis ops the components submit, and global fetch is
// patched to send https://plc.directory + https://*.localhost traffic to the
// local PLC / dispatcher.
//
// The guest is provisioned locally in a container. The requester's report route
// must be reachable from inside that container, so the dispatcher is served on
// a plain port and a TLS port from one app whose ingress name is
// relay.localhost (two labels; a single-label *.localhost cert is rejected),
// the fetch interceptor carries the CA and the TLS port, and the local compute
// provider is given the same CA plus the TLS port -- the provisioning backend
// maps relay.localhost to the container gateway in the guest's /etc/hosts and
// substitutes that port for the portless https report URL the requester
// composed. Nothing host-side reads the ticket out of the guest.
//
// Run it with:
//   deno test -A test/bidder_container_integration_test.ts

import { assert } from "@std/assert";
import { Hono } from "@hono/hono";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createATProto, createLocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import type { ContainerBackend } from "@publicdomainrelay/container-backend-abc";
import { createContainerBackend } from "@publicdomainrelay/container-backend-container";
import { createDockerBackend } from "@publicdomainrelay/container-backend-docker";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRequesterPDS, runComputeContract } from "@publicdomainrelay/requester-xrpc";
import { generateLocalhostTlsCert } from "@publicdomainrelay/tls-localhost";

const MARKER = "SSH_OK_VIA_IROH";

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

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

// -- high-fidelity fake PLC directory --------------------------------------
// POST /<did> stores the genesis op.  GET /<did> derives the DID document:
// verificationMethod from op.verificationMethods (yielding each DID's did:key,
// which is what the relay authenticates the requester's subscriber
// registration against and what the market verifies receipt signatures with)
// and service from op.services. Resolving a did:plc DID must work end to end
// through whatever resolver the relay and the market actually call.

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
  name: "[acceptance] iroh: RFP -> bid -> accept -> SSH over the guest's own ticket",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const logEvents: Array<{ event: string; data: Record<string, unknown> }> = [];
  const baseLogger = createLogger({ serviceName: "iroh-acceptance" });
  // Record every structured event (the flow's own address-discovery events and
  // the relay's registration errors) so the run-level facts can be asserted
  // without reaching into any component's internals.
  const logger = new Proxy(baseLogger, {
    get(target, prop, receiver) {
      if (prop === "info") {
        return (event: string, data?: Record<string, unknown>) => {
          logEvents.push({ event, data: data ?? {} });
          (target as unknown as { info(e: string, d?: Record<string, unknown>): void })
            .info(event, data);
        };
      }
      return Reflect.get(target, prop, receiver);
    },
  }) as typeof baseLogger;

  const cleanups: Array<() => void> = [];

  const backend: ContainerBackend = Deno.build.os === "darwin"
    ? createContainerBackend()
    : createDockerBackend();
  if (!(await backend.ensureRunning())) {
    // The acceptance suite needs a container runtime: there is no guest to
    // reach otherwise. Skipping loudly beats asserting against a phantom guest.
    console.log(`[SKIP] container backend not available (${Deno.build.os})`);
    return;
  }
  const gateway = await backend.defaultGateway();
  logger.info("container_backend", { type: backend.type, gateway });

  // The one dispatcher app, served twice. Its ingress name is relay.localhost,
  // not localhost: the guest dials <sub>.relay.localhost, which must resolve
  // both in the container (via the gateway /etc/hosts alias the provider
  // writes) and here. The cert covers the two-label base and its wildcard.
  const { caCertPem, serverCertPem, serverKeyPem } = await generateLocalhostTlsCert({
    extraDnsSans: ["relay.localhost", "*.relay.localhost"],
  });
  const dispatcherApp = createRelayFactory({
    hostname: "relay.localhost",
    additionalHosts: [gateway],
  }).createApp();
  const dispAc = new AbortController();
  const dispTlsAc = new AbortController();
  const dispPort = await serveOnPort0(dispatcherApp.fetch, dispAc, "0.0.0.0");
  const dispTlsPort = await serveOnPort0(
    dispatcherApp.fetch,
    dispTlsAc,
    "0.0.0.0",
    serverCertPem,
    serverKeyPem,
  );
  cleanups.push(() => { dispAc.abort(); dispTlsAc.abort(); });
  // The requester, bidder and provider subscribe over plain ws to the plain
  // listener; only the guest's https report needs the TLS one.
  const ingressProxyHost = `relay.localhost:${dispPort}`;
  logger.info("dispatcher", { port: dispPort, tlsPort: dispTlsPort, ingressProxyHost });

  const plc = createFakePlc();
  const plcAc = new AbortController();
  const plcPort = await serveOnPort0(plc.app.fetch, plcAc);
  cleanups.push(() => plcAc.abort());
  const plcDirectoryUrl = `http://localhost:${plcPort}`;

  // The interceptor carries the TLS port and the CA -- not the plain port --
  // so an in-process fetch of a portless https *.localhost name reaches the TLS
  // listener and keeps its scheme.
  const { installFetchInterceptor } = await import("./fetch-interceptor.ts");
  const restoreFetch = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl,
    dispPort: dispTlsPort,
    caCertPem,
  });
  cleanups.push(restoreFetch);

  try {
    // -- bidder -----------------------------------------------------------
    const bidderKeypair = await Secp256k1Keypair.create({ exportable: true });
    const bidderPrivHex = Array.from(await bidderKeypair.export())
      .map((b) => b.toString(16).padStart(2, "0")).join("");

    const pdsAgent = await createLocalPDSAgent({
      logger,
      keypair: bidderKeypair,
      serve: createServe({ logger }),
      plcDirectoryUrl,
      ingressProxyHost,
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
      return createIngress({ logger, ingressProxyHost, signer: atproto.signer, keypair: kp });
    };

    // local compute provider (container mode): the CA and the guest-reachable
    // TLS port are what let the guest's curl trust the dispatcher cert and
    // reach the requester's ticket-report route at a portless *.localhost URL.
    const providerRelay = await makeRelay();
    const providerServe = createServe({ logger, relays: [providerRelay] });
    const provisions: Array<{ providerId: string | number; ip: unknown; mode: unknown }> = [];
    const provisionFailures: unknown[] = [];
    const local = createLocalComputeProvider({
      logger,
      atproto: atproto as unknown as ComputeAtproto,
      serve: providerServe,
      getIssuerUrl: () => didWebToHttps(providerRelay.ingressRef),
      containerMode: "container",
    });
    const provider = createComputeProviderHooks({
      provider: createLocalComputeProvider({
        logger,
        atproto: atproto as unknown as ComputeAtproto,
        serve: providerServe,
        getIssuerUrl: () => didWebToHttps(providerRelay.ingressRef),
        containerMode: "container",
        ingressProxyHost,
        caCertPem,
        guestTlsPort: dispTlsPort,
      }),
    });
    await providerServe.beginServe();
    cleanups.push(() => providerServe.shutdown());

    const bidderRelay = await makeRelay();
    const bidder = await createMarketBidder({
      logger,
      atproto,
      providers: [provider],
      relay: bidderRelay,
      serve: createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 }, relays: [bidderRelay] }),
    });
    await bidder.beginServe();
    cleanups.push(() => bidder.shutdown());

    // -- requester (wires its own submitBid -> pendingBids handler) --------
    const requesterServe = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
    const requester = await createRequesterPDS({
      logger,
      serve: requesterServe,
      plcDirectoryUrl,
      ingressProxyHost,
      label: "requester",
    });
    cleanups.push(() => requesterServe.shutdown());
    await requester.beginServe();

    // -- run the contract: deny the central default, include our bidder ----
    // skipSsh false, and the whole promise is awaited -- no Promise.race and no
    // wall-clock deadline, so provisioning is never torn down while it runs.
    const result = await runComputeContract(requester, {
      logger,
      ingressProxyHost,
      skipSsh: false,
      keepVm: true,
      policy: { name: "open", args: { bidWindowSec: 20 } },
      vmReadyTimeoutSec: 300,
      execProgram: `echo ${MARKER} && uname -a`,
      extraBidderDids: [atproto.did],
      denyBidderDids: ["did:plc:centraldefaultbidder000000"],
    });

    logger.info("contract_result", {
      receiptOk: result.receiptOk,
      sshReady: result.sshReady,
      sshExitCode: result.sshExitCode,
      sshProxyCommand: result.sshProxyCommand,
    });

    // -- run-level facts ---------------------------------------------------
    // Receipt verification resolves the bidder's did:plc through the fake PLC
    // and must succeed; the requester's subscriber registered with the relay
    // (a 401 there would leave the guest's report unroutable and the run
    // without a ticket).
    assert(result.receiptOk === true, "receipt verification should pass");
    const unauthorized = logEvents.filter((e) =>
      e.event.includes("401") || JSON.stringify(e.data).includes("401")
    );
    assert(
      unauthorized.length === 0,
      `subscriber registration must not be refused: ${JSON.stringify(unauthorized)}`,
    );

    // -- the guest's own ticket reached the requester ----------------------
    const ticketEvent = logEvents.find((e) =>
      e.event === "vm_fqdn_discovered" && e.data.kind === "iroh-ticket"
    );
    assert(
      ticketEvent,
      `the guest's own outbound ticket report should reach the requester: ${
        JSON.stringify(logEvents.map((e) => e.event))
      }`,
    );
    const ticket = String(ticketEvent!.data.fqdn);

    // -- the session went over the iroh transport, not the websocket relay --
    assert(result.sshReady === true, "the guest should become reachable over iroh");
    assert(result.sshExitCode === 0, `ssh session exited ${result.sshExitCode}`);
    const proxyCommand = result.sshProxyCommand ?? "";
    assert(
      /dumbpipe connect /.test(proxyCommand) && proxyCommand.trim().endsWith(ticket),
      `ProxyCommand should be the dumbpipe connect command for the reported ticket, got: ${proxyCommand}`,
    );
    assert(!proxyCommand.includes("websocat"), "must not fall back to the websocat relay");
    assert(
      result.sshOutput?.includes(MARKER) === true,
      `the guest's program should print ${MARKER}, got: ${result.sshOutput ?? "<no output>"}`,
    );

    // The accept handler returns its receipt while its provisioning runs
    // detached, so a test that stops here stops before anything was placed and
    // cannot say whether the market flow provisions at all. Wait for it.
    const deadline = Date.now() + 240_000;
    while (Date.now() < deadline && provisions.length === 0 && provisionFailures.length === 0) {
      await new Promise((r) => setTimeout(r, 1000));
    }
    assert(
      provisionFailures.length === 0,
      `the accept path reached the provider and provisioning threw: ${
        provisionFailures.map(String).join("; ")
      }`,
    );
    assert(
      provisions.length > 0,
      `the accept path did not reach provision() within 240s, so this flow ends at the accept and ` +
        `no guest was ever asked for`,
    );
    console.log(
      `[test] the accept path provisioned ${provisions[0].providerId} at ${provisions[0].ip}`,
    );
    cleanups.push(() => {
      local.destroy(provisions[0].providerId).catch(() => {});
    });
  } finally {
    // Servers, bidder, provider and requester go away only now, after the
    // contract promise settled.
    for (const c of cleanups.reverse()) {
      try { c(); } catch { /* best effort */ }
    }
    await new Promise((r) => setTimeout(r, 200));
  }
});
