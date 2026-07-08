import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createXrpcRelay } from "@publicdomainrelay/xrpc-relay";
import { createAtprotoMarketRegistry } from "@publicdomainrelay/market-registry-atproto";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidderProviderRef } from "@publicdomainrelay/market-bidder-abc";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createComputeProviderDenoWorker, createWorkerProviderHooks } from "@publicdomainrelay/market-bidder-worker";
import { createATProto, createLocalPDSAgent, createRemoteAgent } from "@publicdomainrelay/atproto-helpers";
import type { LocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { RFP_NSID } from "@publicdomainrelay/market-common";
import { createFirehoseWatcher as createSubscribeReposWatcher } from "@publicdomainrelay/firehose-watcher-subscriberepos";
import { createFirehoseWatcher as createJetstreamWatcher } from "@publicdomainrelay/firehose-watcher-jetstream";
import type { FirehoseRecordEvent, FirehoseWatcher } from "@publicdomainrelay/firehose-watcher-abc";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createOidcProvisioningEnricher } from "@publicdomainrelay/oidc-issuer-hono";
import { createRbacProvisioner } from "@publicdomainrelay/rbac-atproto";
import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { qrcode } from "@libs/qrcode";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_HONO_BIDDER", cliArgsEnv, runtimeConfig).resolve();

const serviceName = (options.serviceName as string) ?? "bidder";
const logger = createLogger({ serviceName });
const BIDDER_ASSOC_SERVICE = "bidder_associate";


// Resolve privateKeyHex: --private-key-hex takes priority, then --private-key-hex-path
const privateKeyHexPath = options.privateKeyHexPath as string | undefined;
let resolvedPrivateKeyHex = options.privateKeyHex as string | undefined;
if (privateKeyHexPath && !resolvedPrivateKeyHex) {
  try {
    const content = await Deno.readTextFile(privateKeyHexPath).then((s) => s.trim());
    if (content) {
      resolvedPrivateKeyHex = content;
      logger.info("private_key_loaded_from_path", { path: privateKeyHexPath });
    }
  } catch { /* file missing — will generate and save below */ }
}

const keypair = resolvedPrivateKeyHex
  ? await Secp256k1Keypair.import(resolvedPrivateKeyHex, { exportable: true })
  : await Secp256k1Keypair.create({ exportable: true });

const privateKeyHex = resolvedPrivateKeyHex ??
  Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

const dispatcherHost = (options.relayDispatcherHost as string) || "xrpc.fedproxy.com";

const plcDirectoryUrl = (options.plcDirectoryUrl as string) || "https://plc.directory";

// Auto-detect local dev: *.localhost isn't in DNS.  If the dispatcher is
// reachable at a local host, patch fetch so the bidder can resolve the
// requester's PDS endpoints (also on *.localhost) through the relay.
// Also patches plc.directory → local PLC when plcDirectoryUrl is local.
const isLocalDev = dispatcherHost.includes("localhost") || dispatcherHost.startsWith("127.");
const _plcHost = (() => { try { return new URL(plcDirectoryUrl).hostname; } catch { return plcDirectoryUrl; } })();
const isLocalPlc = _plcHost === "localhost" || _plcHost.startsWith("127.") || _plcHost === "0.0.0.0";
if (isLocalDev || isLocalPlc) {
  const patchPort = dispatcherHost.includes(":") ? dispatcherHost.split(":").pop()! : "80";
  const realFetch = globalThis.fetch;
  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
    let url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const m = url.match(/^https:\/\/([^/]+)(\/.*)?$/);
    if (m && m[1].endsWith(".localhost")) {
      let host = m[1];
      if (!host.includes(":")) host = `${host}:${patchPort}`;
      url = `http://${host}${m[2] ?? ""}`;
      return realFetch(url, init);
    }
    if (isLocalPlc && url.startsWith("https://plc.directory/")) {
      url = plcDirectoryUrl + url.slice("https://plc.directory".length);
      return realFetch(url, init);
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;
}

// Each relay gets its own keypair so subscribers never share a subdomain/FQDN
// on the dispatcher (collision would route everyone to the last connector).
async function cliCreateXrpcRelay() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  return createXrpcRelay({ logger, dispatcherHost, signer: atproto.signer, keypair: relayKeypair });
}

let atprotoAgent;
let pdsHostname: string | undefined;
let isLocal = false;
const _deferredRelayUrls: string[] = [];
let _deferredPdsPort = 0;
if ((options.atprotoHandle as string | undefined) && (options.atprotoPassword as string | undefined)) {
  const pdsUrl = (options.atprotoPdsUrl as string) || "https://bsky.social";
  atprotoAgent = await createRemoteAgent({
    handle: options.atprotoHandle as string,
    password: options.atprotoPassword as string,
    pdsUrl,
  });
  pdsHostname = new URL(pdsUrl).hostname;
} else {
  // TCP port 0 so the local atproto-relay can crawl this PDS directly
  // (the did-key-relay subscription protocol wraps firehose frames).
  const pdsStatePath = options.pdsStatePath as string | undefined;
  const pdsServe = createServe({ logger, tcp: { port: 0 } });
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: pdsServe,
    plcDirectoryUrl,
    dispatcherHost,
    storagePath: pdsStatePath,
    associateServiceId: BIDDER_ASSOC_SERVICE,
  });
  await atprotoAgent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  isLocal = true;
  const relayHost: string = (atprotoAgent as { relay?: { proxyHost?: string } }).relay?.proxyHost ?? "";
  if (relayHost) {
    pdsHostname = relayHost;
  }
}

const DEFAULT_RELAY_URLS = [
  "https://reg.market.fedfork.com",
  "https://bsky.network",
  "https://relay.mini-cloud-0002.chadig.com",
];

const registryEndpoint = (options.registryEndpoint as string) || "";
const relayUrls = registryEndpoint
  ? [...new Set([...DEFAULT_RELAY_URLS, registryEndpoint])]
  : DEFAULT_RELAY_URLS;

// Collect local relay URLs for deferred registration after serve starts.
for (const url of relayUrls) {
  if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
    _deferredRelayUrls.push(url);
  }
}

if (pdsHostname) {
  for (const url of relayUrls) {
    const registry = createAtprotoMarketRegistry({ registryUrl: url, log: logger });
    await registry.registerPds(pdsHostname);
  }
} else if (registryEndpoint) {
  logger.warn("market_registry_no_hostname", {
    reason: "could not determine PDS hostname for registry registration",
  });
}

const atproto = await createATProto({
  logger,
  badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex }),
  plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
  agent: atprotoAgent,
});

// Persist generated key to path for future runs.
if (privateKeyHexPath) {
  try {
    const parent = privateKeyHexPath.includes("/") ? privateKeyHexPath.slice(0, privateKeyHexPath.lastIndexOf("/")) : ".";
    await Deno.mkdir(parent, { recursive: true });
    await Deno.writeTextFile(privateKeyHexPath, privateKeyHex);
    if (resolvedPrivateKeyHex) {
      logger.info("private_key_rewritten", { path: privateKeyHexPath });
    } else {
      logger.info("private_key_generated_and_saved", { path: privateKeyHexPath });
    }
  } catch (err) {
    logger.warn("private_key_save_failed", { path: privateKeyHexPath, error: String(err) });
  }
}

const providers: MarketBidderProviderRef[] = [];
const serves: ReturnType<typeof createServe>[] = [];

if (options.computeProviderDigitaloceanToken) {
  const relay = await cliCreateXrpcRelay();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderHooks({
    provider: createDigitalOceanComputeProvider({
      logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
      getIssuerUrl: () => relay.proxyUrl,
      digitaloceanBaseUrl: (options.computeProviderDigitaloceanBaseUrl as string) || "https://api.digitalocean.com",
      doToken: options.computeProviderDigitaloceanToken as string,
    }),
  }));
  await serve.beginServe();
}

if (options.computeProviderLocal) {
  const relay = await cliCreateXrpcRelay();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderHooks({
    provider: createLocalComputeProvider({
      logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
      getIssuerUrl: () => relay.proxyUrl,
      oidcProvisioner: createOidcProvisioningEnricher(() => relay.proxyUrl),
      rbacProvisioner: createRbacProvisioner(),
      containerMode: options.computeProviderLocalContainerMode as "vm" | "container" | undefined,
      vmImage: options.computeProviderLocalVmImage as string | undefined,
      containerImage: options.computeProviderLocalContainerImage as string | undefined,
      cacheDir: options.computeProviderLocalCacheDir as string | undefined,
    }),
  }));
  await serve.beginServe();
}

if (options.computeProviderDenoWorker) {
  const workerPermMode = (options.workerPermissionMode as string) || "deny-all";
  let permissionPolicyHandler: import("@publicdomainrelay/compute-deno-abc").PermissionPolicyHandler | undefined;
  if (workerPermMode === "allow-net") {
    const { createAllowNetOnlyPolicyHandler } = await import("@publicdomainrelay/compute-deno-atproto");
    permissionPolicyHandler = createAllowNetOnlyPolicyHandler();
  }
  providers.push(createWorkerProviderHooks({
    provider: await createComputeProviderDenoWorker({ logger, atproto }),
    permissionPolicyHandler,
  }));
}

const rfpFirehoseMode = (options.rfpFirehoseMode as string) || "off";
const rfpFirehoseUrl = options.rfpFirehoseUrl as string | undefined;
const offeringRefreshSec = (options.offeringRefreshSec as number) ?? 300;

let rfpWatcherFactory: ((onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher) | undefined;
let rfpWatcherFactories: Array<(onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher> | undefined;

if (rfpFirehoseMode !== "off" && rfpFirehoseUrl) {
  const urls = rfpFirehoseUrl.split(",").map((s) => s.trim()).filter(Boolean);
  const make = rfpFirehoseMode === "jetstream" ? createJetstreamWatcher : createSubscribeReposWatcher;
  const build = (url: string) => (onRecord: (e: FirehoseRecordEvent) => void) =>
    make({ url, wantedCollections: [RFP_NSID], onRecord, log: logger });
  if (urls.length > 1) {
    rfpWatcherFactories = urls.map(build);
  } else {
    rfpWatcherFactory = build(urls[0]);
  }
}

// Market factory gets its own relay/serve (own keypair -> own subdomain/FQDN).
const bidderRelay = options.noXrpcRelay ? undefined : await cliCreateXrpcRelay();
const bidderServe = createServe({
  logger,
  tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.servePort as number) ?? 0 },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  relays: bidderRelay ? [bidderRelay] : [],
});

const acceptScopeRaw = options.acceptScope as string | undefined;
const acceptScope = (acceptScopeRaw === "only_me" || acceptScopeRaw === "direct_network" || acceptScopeRaw === "policy_based")
  ? acceptScopeRaw : undefined;

const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderRelay,
  rfpWatcherFactory,
  rfpWatcherFactories,
  offeringRefreshMs: offeringRefreshSec > 0 ? offeringRefreshSec * 1000 : undefined,
  serve: bidderServe,
  acceptScope,
});

function shutdown() {
  bidder.shutdown();
  for (const s of serves) s.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await bidder.beginServe();

// Machine-readable line for test harness subprocess spawn
console.log(JSON.stringify({
  event: "bidder_ready",
  did: atproto.did,
  proxyRef: bidderRelay?.proxyRef,
  servePort: bidderServe.tcpPort,
}));

const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
let hasAssociation = false;
if (!options.noQr) {
  let cursor: string | undefined;
  do {
    const result = await atproto.listRecords(atproto.did, BADGE_BLUE_KEYS_NSID, { limit: 100 });
    for (const rec of result.records) {
      const v = rec.value as Record<string, unknown>;
      if (v.challenge === atproto.did && v.service === BIDDER_ASSOC_SERVICE) {
        hasAssociation = true;
        break;
      }
    }
    cursor = (result as { cursor?: string }).cursor;
  } while (cursor && !hasAssociation);
}

if (hasAssociation) {
  logger.info("existing_association_found", { did: atproto.did, hint: "skipping QR — prior association exists" });
}

if (!options.noQr && !hasAssociation) {
  if (!isLocal) {
    logger.warn("qr_association_requires_local_pds", {
      hint: "QR association only works with a local PDS (no --atproto-handle). Use --no-qr to skip.",
    });
  } else {
    const qrUrl = `https://qr.fedfork.com/#bdr=${atproto.did}`;
    logger.info("qr_url", { url: qrUrl });
    const qr = qrcode(qrUrl, { output: "console", ecl: "HIGH" });
    console.log(qr);

    logger.info("waiting_for_association", {
      hint: "Scan QR code, then confirm on your phone",
      bidderDid: atproto.did,
    });
    const localAgent = atprotoAgent as LocalPDSAgent;
    const callerDid = await localAgent.associateCalled;

    const assocIdResolver = new IdResolver({ plcUrl: plcDirectoryUrl });
    let handle = callerDid;
    try {
      const doc = await assocIdResolver.did.resolve(callerDid);
      const aka = (doc as Record<string, unknown>)?.alsoKnownAs as string[] | undefined;
      if (aka?.[0]) handle = aka[0].replace("at://", "");
    } catch { /* use DID as fallback */ }

    const answer = prompt(`Associate with ${handle}? [y/N] `);
    if (!answer || !answer.toLowerCase().startsWith("y")) {
      logger.info("association_rejected", { callerDid, handle });
      localAgent.rejectAssociation(new Error("User rejected association"));
      Deno.exit(0);
    }
    localAgent.approveAssociation();
    logger.info("association_confirmed", { callerDid, handle });
  }
}

// Re-register with local relays using the direct serve port.
// Needed so local atproto-relays can connect to subscribeRepos
// without going through the did-key-relay subscription protocol.
const _localServePort = _deferredPdsPort || bidderServe.tcpPort;
if (_deferredRelayUrls.length > 0 && _localServePort > 0) {
  const localHostname = `127.0.0.1:${_localServePort}`;
  for (const url of _deferredRelayUrls) {
    try {
      const registry = createAtprotoMarketRegistry({ registryUrl: url, log: logger });
      await registry.registerPds(localHostname);
      logger.info("market_registry_local_reregistered", { registry: url, hostname: localHostname });
    } catch (err) {
      logger.warn("market_registry_local_reregister_failed", { registry: url, error: String(err) });
    }
  }
}
