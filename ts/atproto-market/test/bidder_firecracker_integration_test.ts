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
import { createDockerBackend } from "@publicdomainrelay/container-backend-docker";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRequesterPDS, runComputeContract } from "@publicdomainrelay/requester-xrpc";

const ENV = {
  nodeimage: "SOCIALWEB_FIRECRACKER_NODEIMAGE",
  config: "SOCIALWEB_FIRECRACKER_CONFIG",
  repoDir: "SOCIALWEB_FIRECRACKER_REPO_DIR",
  runnerImage: "SOCIALWEB_FIRECRACKER_RUNNER_IMAGE",
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
  for (const k of ["nodeimage"] as const) {
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
  // The bidder does not need /dev/kvm or /dev/net/tun: each guest is given a
  // container that has them. What the bidder needs is a container runtime.
  try {
    const probe = new Deno.Command("docker", {
      args: ["info", "--format", "{{.ServerVersion}}"],
      stdout: "piped",
      stderr: "piped",
    }).outputSync();
    if (probe.code !== 0) {
      return `docker is not usable: ${new TextDecoder().decode(probe.stderr).trim()}`;
    }
  } catch (err) {
    return `docker is not on PATH, and each guest is booted in a container: ${String(err)}`;
  }
  const image = Deno.env.get(ENV.runnerImage)!;
  try {
    const probe = new Deno.Command("docker", {
      args: ["image", "inspect", image],
      stdout: "piped",
      stderr: "piped",
    }).outputSync();
    if (probe.code !== 0) {
      return `the runner image ${image} is not present locally. Build it with ` +
        `deno task build:firecracker-runner in hono-compute-provider`;
    }
  } catch (err) {
    return `could not check the runner image ${image}: ${String(err)}`;
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

  // A bare "TypeError: fetch failed" says nothing about which call failed, and a
  // resolver that cannot reach a PDS fails several steps before provisioning.
  // Record every rejected fetch with its URL and the underlying cause.
  const fetchFailures: Array<{ url: string; cause: string }> = [];
  {
    const patched = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string"
        ? input
        : input instanceof URL
        ? input.toString()
        : input.url;
      try {
        return await patched(input, init);
      } catch (err) {
        const cause = (err as { cause?: unknown }).cause;
        fetchFailures.push({ url, cause: cause === undefined ? String(err) : String(cause) });
        throw err;
      }
    }) as typeof fetch;
    cleanups.push(() => {
      globalThis.fetch = patched;
    });
  }

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
        backend: createDockerBackend(),
        runnerImage: Deno.env.get(ENV.runnerImage)!,
        logger,
      }),
      workRoot: Deno.env.get(ENV.workRoot)!,
      rangeBase: Deno.env.get(ENV.rangeBase),
      reuseStale: Deno.env.get(ENV.reuseImage) === "1",
    });
    // The provider is wrapped rather than used directly so the test can assert on
    // what provisioning actually did. Asserting only that a bid was collected
    // would pass on a provider that collects bids and then fails to place
    // anything, which is the failure this test exists to catch.
    const provisions: Array<{ providerId: string | number; ip: unknown; mode: unknown; console: unknown }> = [];
    const provisionFailures: unknown[] = [];
    const provider = createComputeProviderHooks({
      provider: {
        ...firecracker.provider,
        async provision(vm, requesterDid, spec) {
          try {
            const result = await firecracker.provider.provision(vm, requesterDid, spec);
            const metadata = result.metadata as Record<string, unknown>;
            provisions.push({
              providerId: result.providerId,
              ip: metadata?.ip,
              mode: metadata?.mode,
              console: metadata?.console,
            });
            return result;
          } catch (err) {
            provisionFailures.push(err);
            throw err;
          }
        },
      },
    });

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

    // The accept handler starts its provisioning in a detached promise and
    // returns the receipt without waiting for it, so nothing here can await the
    // provider directly: the work arrives after this call. Waiting for it is the
    // point -- the previous version of this test tore its servers and its fetch
    // patch down while that promise was still in flight, which is what turned a
    // provision that was about to run into `TypeError: fetch failed`, and made
    // the assertion below it vacuous.
    const provisionDeadline = Date.now() + 180_000;
    while (
      Date.now() < provisionDeadline && provisions.length === 0 && provisionFailures.length === 0
    ) {
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
      `the accept path did not reach provision() within 180s, so this flow ends at the accept and ` +
        `no guest was ever asked for. contractErr=${
          contractErr ? String(contractErr) : "none"
        }; rejected fetches=${JSON.stringify(fetchFailures.slice(0, 4))}`,
    );
    const placed = provisions[0];
    assert(
      placed.mode === "firecracker" && typeof placed.ip === "string" && placed.ip.length > 0,
      `provision() must place a firecracker guest and report its address; it reported ` +
        `${JSON.stringify(placed)}`,
    );
    console.log(`[test] the accept path provisioned ${placed.providerId} at ${placed.ip}`);
    cleanups.push(() => {
      firecracker.provider.destroy(placed.providerId).catch(() => {});
    });

    // And the guest is the one the RFP asked for: the requester's composed
    // user_data is what cloud-init reads inside it.
    const consolePath = placed.console as string;
    const consoleDeadline = Date.now() + 120_000;
    let guestConsole = "";
    while (Date.now() < consoleDeadline) {
      try {
        guestConsole = await Deno.readTextFile(consolePath);
      } catch {
        await new Promise((r) => setTimeout(r, 2000));
        continue;
      }
      if (/Datasource DataSourceNoCloud/.test(guestConsole)) break;
      await new Promise((r) => setTimeout(r, 2000));
    }
    assert(
      /Datasource DataSourceNoCloud/.test(guestConsole),
      `the guest the market provisioned did not read its user_data as a NoCloud seed within 120s, ` +
        `so the guest at ${placed.ip} is not running what the RFP asked for. Last 800 bytes of its ` +
        `console:\n${guestConsole.slice(-800)}`,
    );
    console.log(`[test] the guest the accept path placed read its user_data (NoCloud)`);
  } finally {
    for (const c of cleanups.reverse()) {
      try { c(); } catch { /* best effort */ }
    }
    await new Promise((r) => setTimeout(r, 200));
  }
});

// Drives the provider's own provision() and reads the guest's console back.
//
// This is the assertion that says the firecracker provider works, and it is
// deliberately separate from the market above: the market's accept path does
// not reach any provider's provision() in this harness (see the note there),
// so a test that only drives the market cannot tell a provider that places a
// guest from one that places nothing. Here the provider is called directly and
// the guest is asked to prove itself.
Deno.test({
  name: "[integration] firecracker provider boots a guest that consumes its user_data",
  ignore: skipReason !== null,
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const logger = createLogger({ serviceName: "it-firecracker-direct" });
  const workRoot = Deno.env.get(ENV.workRoot)!;
  const userDataFile = await Deno.makeTempFile({ prefix: "fc-direct-", suffix: ".yaml" });
  const marker = `DIRECT-PROVISION-${crypto.randomUUID().slice(0, 8)}`;
  await Deno.writeTextFile(
    userDataFile,
    `#cloud-config\n` +
      `write_files:\n` +
      `  - path: /root/proof.txt\n` +
      `    owner: root:root\n` +
      `    permissions: "0644"\n` +
      `    content: "the seed was read by cloud-init\\n"\n` +
      `runcmd:\n` +
      `  - [ sh, -c, "echo ${marker} > /dev/console" ]\n` +
      `  - [ sh, -c, "cat /root/proof.txt > /dev/console" ]\n`,
  );

  const provider = createFirecrackerComputeProvider({
    logger,
    atproto: {
      getAgentDid: () => "did:plc:directprovisiontest0000000",
      createRecord: () => Promise.reject(new Error("this test does not write records")),
      deleteRecord: () => Promise.resolve(),
    } as unknown as ComputeAtproto,
    serve: {
      app: new Hono(),
      onConnected: () => {},
    },
    getIssuerUrl: () => "http://127.0.0.1:1",
    image: createFirecrackerNodeImage({
      binary: Deno.env.get(ENV.nodeimage)!,
      configPath: Deno.env.get(ENV.config)!,
      repoDir: Deno.env.get(ENV.repoDir)!,
      preinstallPath: Deno.env.get(ENV.preinstall),
      logger,
    }),
    microvm: createFirecrackerMicrovm({
      backend: createDockerBackend(),
      runnerImage: Deno.env.get(ENV.runnerImage)!,
      logger,
    }),
    workRoot,
    rangeBase: Deno.env.get(ENV.rangeBase),
    // The same switch the bidder above honours. Whether this host builds the
    // image or is told to use the one already in the store is a property of the
    // host -- making the image needs a chroot, which needs privileges a test
    // runner does not have -- and not of which of these two tests is running, so
    // a run that must not build has to be able to say so to both.
    reuseStale: Deno.env.get(ENV.reuseImage) === "1",
  });

  const result = await provider.provider.provision(
    {
      cpus: 2,
      mem: "2G",
      disk: "2G",
      network: "default",
      role: "test",
      user_data: await Deno.readTextFile(userDataFile),
    },
    "did:plc:directprovisiontest0000000",
  );

  const metadata = result.metadata as Record<string, unknown>;
  assert(
    typeof metadata.ip === "string" && (metadata.ip as string).length > 0,
    `provision() must report the address the host routed to the guest; got ${JSON.stringify(metadata.ip)}`,
  );
  assert(
    metadata.mode === "firecracker",
    `provision() must say the placement is a firecracker guest; got ${JSON.stringify(metadata.mode)}`,
  );
  const consolePath = metadata.console as string;
  assert(typeof consolePath === "string" && consolePath.length > 0, "provision() must report the guest's console");

  const deadline = Date.now() + 120_000;
  let console = "";
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2000));
    try {
      console = await Deno.readTextFile(consolePath);
    } catch {
      continue;
    }
    if (console.includes(marker)) break;
  }
  assert(
    console.includes(marker),
    `the guest did not run the runcmd from its user_data within 120s, so cloud-init never acted on the ` +
      `seed. provision() reported ip=${metadata.ip}. Last 800 bytes of the guest's console:\n` +
      console.slice(-800),
  );
  assert(
    console.includes("the seed was read by cloud-init"),
    "the file the user_data writes was never read back inside the guest",
  );
  assert(
    /Datasource DataSourceNoCloud/.test(console),
    "cloud-init did not report the seed as its datasource; the user_data reached the guest by some " +
      "other path than the one this provider claims to use",
  );
  await provider.provider.destroy(result.providerId).catch(() => {});
  await Deno.remove(workRoot, { recursive: true }).catch(() => {});
});
