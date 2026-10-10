// Integration: start a real bidder whose compute provider is the Firecracker
// one, and drive a real requester against it through a real local xrpc relay
// dispatcher, denying the central/default bidder so the bid must come from the
// locally-started bidder.
//
// This is the container integration test with the provider swapped, so the
// thing under test is the provider and not the market: the same fake PLC, the
// same fetch interception, the same dispatcher.
//
// What it adds over the container test is the startup path. The provider's
// setup() makes the image the configuration resolves to available, building it
// when the store does not already hold it whole, so this test also proves the
// pre-stage is gone: run it against an empty --compute-provider-firecracker
// store and the image is built on the way up rather than being assumed.
//
// It needs a host that can actually boot a KVM guest, and it names what is
// missing rather than failing vaguely. Run it with:
//
//   SOCIALWEB_FIRECRACKER_NODEIMAGE=/path/to/socialweb-nodeimage \
//   SOCIALWEB_FIRECRACKER_NODEBOOT=/path/to/socialweb-nodeboot \
//   SOCIALWEB_FIRECRACKER_CONFIG=/path/to/nodeimage.json \
//   SOCIALWEB_FIRECRACKER_REPO_DIR=/path/to/socialweb-computer-kcp \
//   SOCIALWEB_FIRECRACKER_VMM=/path/to/firecracker \
//   SOCIALWEB_FIRECRACKER_WORK_ROOT=/var/lib/socialweb/guests \
//   deno test --allow-all test/bidder_firecracker_integration_test.ts

import { assert } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Hono } from "@hono/hono";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createATProto, createLocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createFirecrackerComputeProvider } from "@publicdomainrelay/compute-provider-firecracker";
import { createFirecrackerNodeImage } from "@publicdomainrelay/node-image-firecracker";
import { createFirecrackerMicrovm } from "@publicdomainrelay/microvm-firecracker";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRequesterPDS, runComputeContract } from "@publicdomainrelay/requester-xrpc";

const ENV = {
  nodeimage: "SOCIALWEB_FIRECRACKER_NODEIMAGE",
  nodeboot: "SOCIALWEB_FIRECRACKER_NODEBOOT",
  config: "SOCIALWEB_FIRECRACKER_CONFIG",
  repoDir: "SOCIALWEB_FIRECRACKER_REPO_DIR",
  vmm: "SOCIALWEB_FIRECRACKER_VMM",
  workRoot: "SOCIALWEB_FIRECRACKER_WORK_ROOT",
  preinstall: "SOCIALWEB_FIRECRACKER_PREINSTALL",
  rangeBase: "SOCIALWEB_FIRECRACKER_RANGE_BASE",
  reuseImage: "SOCIALWEB_FIRECRACKER_REUSE_IMAGE",
} as const;

function missingEnvironment(): string | null {
  const absent = (Object.keys(ENV) as Array<keyof typeof ENV>)
    .filter((k) => k !== "preinstall" && k !== "rangeBase" && k !== "reuseImage")
    .filter((k) => !Deno.env.get(ENV[k]));
  if (absent.length > 0) {
    return `${absent.map((k) => ENV[k]).join(", ")} unset`;
  }
  for (const k of ["nodeimage", "nodeboot", "vmm"] as const) {
    const path = Deno.env.get(ENV[k])!;
    try {
      Deno.statSync(path);
    } catch {
      return `${ENV[k]}=${path} does not exist`;
    }
  }
  const workRoot = Deno.env.get(ENV.workRoot)!;
  try {
    Deno.statSync(workRoot);
  } catch {
    try {
      Deno.mkdirSync(workRoot, { recursive: true });
    } catch (err) {
      return `${ENV.workRoot}=${workRoot} is neither present nor creatable: ${String(err)}`;
    }
  }
  try {
    const fd = Deno.openSync("/dev/kvm", { read: true, write: true });
    fd.close();
  } catch (err) {
    return `/dev/kvm cannot be opened by uid ${Deno.uid()}: ${String(err)}. A KVM guest needs it, ` +
      `and the two ways this fails are different: a device policy that denies it to anyone but ` +
      `root cannot be worked around by running as root either, because pasta then drops the ` +
      `process that starts the VMM to nobody, which holds no devices at all`;
  }
  return null;
}

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

function createFakePlc() {
  const ops = new Map<string, Record<string, unknown>>();
  const app = new Hono();

  function didFromPath(path: string): string {
    return decodeURIComponent(path.startsWith("/") ? path.slice(1) : path);
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
    return c.json({
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
    });
  });

  return { app };
}

const skipReason = missingEnvironment();
if (skipReason !== null) {
  console.log(`[test] skipping the Firecracker integration test: ${skipReason}`);
}

Deno.test({
  name: "[integration] bidder (firecracker provider) wins bid when central default denied",
  ignore: skipReason !== null,
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const logger = createLogger({ serviceName: "it-firecracker" });
  const cleanups: Array<() => void> = [];

  const dispatcherApp = createRelayFactory({ hostname: "localhost" }).createApp();
  const dispatcherCtl = new AbortController();
  const { promise: dispPortReady, resolve: resolveDispPort } = Promise.withResolvers<number>();
  Deno.serve(
    { port: 0, hostname: "127.0.0.1", signal: dispatcherCtl.signal, onListen: (addr) => resolveDispPort((addr as Deno.NetAddr).port) },
    dispatcherApp.fetch,
  );
  const dispPort = await dispPortReady;
  cleanups.push(() => dispatcherCtl.abort());
  const ingressProxyHost = `localhost:${dispPort}`;

  const plc = createFakePlc();
  const plcCtl = new AbortController();
  const { promise: plcPortReady, resolve: resolvePlcPort } = Promise.withResolvers<number>();
  Deno.serve(
    { port: 0, hostname: "127.0.0.1", signal: plcCtl.signal, onListen: (addr) => resolvePlcPort((addr as Deno.NetAddr).port) },
    plc.app.fetch,
  );
  const plcPort = await plcPortReady;
  cleanups.push(() => plcCtl.abort());
  const plcDirectoryUrl = `http://localhost:${plcPort}`;

  const { installFetchInterceptor } = await import("./fetch-interceptor.ts");
  const restoreFetch = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl,
    dispPort,
  });
  cleanups.push(restoreFetch);

  try {
    const bidderKeypair = await Secp256k1Keypair.create({ exportable: true });
    const bidderPrivHex = Array.from(await bidderKeypair.export())
      .map((b) => b.toString(16).padStart(2, "0")).join("");

    const pdsAgent = await createLocalPDSAgent({
      logger, keypair: bidderKeypair,
      serve: createServe({ logger }),
      plcDirectoryUrl, ingressProxyHost,
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

    const providerRelay = await makeRelay();
    const providerServe = createServe({ logger, relays: [providerRelay] });
    const firecracker = createFirecrackerComputeProvider({
      logger,
      atproto: atproto as unknown as ComputeAtproto,
      serve: providerServe,
      getIssuerUrl: () => didWebToHttps(providerRelay.ingressRef),
      image: createFirecrackerNodeImage({
        binary: Deno.env.get(ENV.nodeimage)!,
        configPath: Deno.env.get(ENV.config)!,
        repoDir: Deno.env.get(ENV.repoDir)!,
        preinstallPath: Deno.env.get(ENV.preinstall),
        logger,
      }),
      microvm: createFirecrackerMicrovm({
        binary: Deno.env.get(ENV.nodeboot)!,
        firecracker: Deno.env.get(ENV.vmm)!,
        logger,
      }),
      workRoot: Deno.env.get(ENV.workRoot)!,
      rangeBase: Deno.env.get(ENV.rangeBase),
      reuseStale: Deno.env.get(ENV.reuseImage) === "1",
    });
    const provider = createComputeProviderHooks({ provider: firecracker.provider });

    // The startup path under test: setup() is what replaces the pre-stage.
    await firecracker.ensureImage();
    await providerServe.beginServe();

    const bidderRelay = await makeRelay();
    const bidder = await createMarketBidder({
      logger, atproto, providers: [provider], relay: bidderRelay,
      serve: createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 }, relays: [bidderRelay] }),
    });
    await bidder.beginServe();
    cleanups.push(() => bidder.shutdown());

    const requesterServe = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
    const requester = await createRequesterPDS({
      logger, serve: requesterServe,
      plcDirectoryUrl, ingressProxyHost, label: "requester",
    });
    cleanups.push(() => requesterServe.shutdown());

    const seenBids: Array<{ did: string; uri: string }> = [];
    const origSet = requester.pendingBids.set.bind(requester.pendingBids);
    requester.pendingBids.set = ((k: string, v: Array<{ did: string; uri: string }>) => {
      for (const b of v) seenBids.push({ did: b.did, uri: b.uri });
      return origSet(k, v as never);
    }) as typeof requester.pendingBids.set;

    await requester.beginServe();

    let contractErr: unknown;
    const contract = runComputeContract(requester, {
      logger,
      ingressProxyHost,
      skipSsh: true,
      keepVm: true,
      policy: { name: "open", args: { bidWindowSec: 8 } },
      vmReadyTimeoutSec: 1,
      execProgram: "true",
      extraBidderDids: [atproto.did],
      denyBidderDids: ["did:plc:centraldefaultbidder000000"],
    }).catch((e) => { contractErr = e; });
    await Promise.race([
      contract,
      new Promise((r) => setTimeout(r, 60_000)),
    ]);

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
