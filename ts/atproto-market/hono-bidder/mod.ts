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
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { RFP_NSID } from "@publicdomainrelay/market-common";
import { createFirehoseWatcher as createSubscribeReposWatcher } from "@publicdomainrelay/firehose-watcher-subscriberepos";
import { createFirehoseWatcher as createJetstreamWatcher } from "@publicdomainrelay/firehose-watcher-jetstream";
import type { FirehoseRecordEvent } from "@publicdomainrelay/firehose-watcher-abc";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { Secp256k1Keypair } from "@atproto/crypto";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_HONO_BIDDER", cliArgsEnv, runtimeConfig).resolve();

const serviceName = (options.serviceName as string) ?? "bidder";
const logger = createLogger({ serviceName });

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

const keypair = (options.privateKeyHex as string | undefined)
  ? await Secp256k1Keypair.import(options.privateKeyHex as string)
  : await Secp256k1Keypair.create({ exportable: true });

const privateKeyHex = (options.privateKeyHex as string) ?? "";

const dispatcherHost = (options.relayDispatcherHost as string) || "xrpc.fedproxy.com";

// Auto-detect local dev: *.localhost isn't in DNS.  If the dispatcher is
// reachable at a local host, patch fetch so the bidder can resolve the
// requester's PDS endpoints (also on *.localhost) through the relay.
if (dispatcherHost.includes("localhost") || dispatcherHost.startsWith("127.")) {
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
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;
}
const plcDirectoryUrl = (options.plcDirectoryUrl as string) || "https://plc.directory";

// Each relay gets its own keypair so subscribers never share a subdomain/FQDN
// on the dispatcher (collision would route everyone to the last connector).
async function cliCreateXrpcRelay() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  return createXrpcRelay({ logger, dispatcherHost, signer: atproto.signer, keypair: relayKeypair });
}

let atprotoAgent;
let pdsHostname: string | undefined;
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
  const pdsServe = createServe({ logger, tcp: { port: 0 } });
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: pdsServe,
    plcDirectoryUrl,
    dispatcherHost,
  });
  await atprotoAgent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  const proxyRef: string = (atprotoAgent as { relay?: { proxyRef?: string } }).relay?.proxyRef ?? "";
  if (proxyRef) {
    pdsHostname = proxyRef.startsWith("did:web:")
      ? proxyRef.slice("did:web:".length)
      : proxyRef;
  }
}

const DEFAULT_RELAY_URLS = [
  "https://reg.market.fedfork.com",
  "https://bsky.network",
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

const providers: MarketBidderProviderRef[] = [];
const serves: ReturnType<typeof createServe>[] = [];

if (options.computeProviderDigitaloceanToken) {
  const relay = await cliCreateXrpcRelay();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderHooks({
    provider: createDigitalOceanComputeProvider({
      logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
      getIssuerUrl: () => didWebToHttps(relay.proxyRef),
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
      getIssuerUrl: () => didWebToHttps(relay.proxyRef),
      containerMode: options.computeProviderLocalContainerMode as "vm" | "container" | undefined,
      vmImage: options.computeProviderLocalVmImage as string | undefined,
      containerImage: options.computeProviderLocalContainerImage as string | undefined,
      cacheDir: options.computeProviderLocalCacheDir as string | undefined,
    }),
  }));
  await serve.beginServe();
}

if (options.computeProviderDenoWorker) {
  providers.push(createWorkerProviderHooks({
    provider: await createComputeProviderDenoWorker({ logger, atproto }),
  }));
}

const rfpFirehoseMode = (options.rfpFirehoseMode as string) || "off";
const rfpFirehoseUrl = options.rfpFirehoseUrl as string | undefined;
const offeringRefreshSec = (options.offeringRefreshSec as number) ?? 300;

const rfpWatcherFactory = rfpFirehoseMode !== "off" && rfpFirehoseUrl
  ? (onRecord: (e: FirehoseRecordEvent) => void) => {
    const make = rfpFirehoseMode === "jetstream" ? createJetstreamWatcher : createSubscribeReposWatcher;
    return make({ url: rfpFirehoseUrl, wantedCollections: [RFP_NSID], onRecord, log: logger });
  }
  : undefined;

// Market factory gets its own relay/serve (own keypair -> own subdomain/FQDN).
const bidderRelay = options.noXrpcRelay ? undefined : await cliCreateXrpcRelay();
const bidderServe = createServe({
  logger,
  tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.servePort as number) ?? 0 },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  relays: bidderRelay ? [bidderRelay] : [],
});
const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderRelay,
  rfpWatcherFactory,
  offeringRefreshMs: offeringRefreshSec > 0 ? offeringRefreshSec * 1000 : undefined,
  serve: bidderServe,
});

function shutdown() {
  bidder.shutdown();
  for (const s of serves) s.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await bidder.beginServe();

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
