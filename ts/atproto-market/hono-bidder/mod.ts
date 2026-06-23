import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createXrpcRelay } from "@publicdomainrelay/xrpc-relay";
import { createAtprotoMarketRegistry } from "@publicdomainrelay/market-registry-atproto";
import { createMarketBidder, createComputeProviderMarketBidderHooks } from "@publicdomainrelay/market-bidder";
import { createComputeProviderDenoWorker } from "@publicdomainrelay/market-bidder-worker";
import { createATProto, createLocalPDSAgent, createRemoteAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
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
const plcDirectoryUrl = (options.plcDirectoryUrl as string) || "https://plc.directory";

// Each relay gets its own keypair so subscribers never share a subdomain/FQDN
// on the dispatcher (collision would route everyone to the last connector).
async function cliCreateXrpcRelay() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  return createXrpcRelay({ logger, dispatcherHost, signer: atproto.signer, keypair: relayKeypair });
}

let atprotoAgent;
let pdsHostname: string | undefined;
if ((options.atprotoHandle as string | undefined) && (options.atprotoPassword as string | undefined)) {
  const pdsUrl = (options.atprotoPdsUrl as string) || "https://bsky.social";
  atprotoAgent = await createRemoteAgent({
    handle: options.atprotoHandle as string,
    password: options.atprotoPassword as string,
    pdsUrl,
  });
  pdsHostname = new URL(pdsUrl).hostname;
} else {
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: createServe({ logger }),
    plcDirectoryUrl,
    dispatcherHost,
  });
  await atprotoAgent.beginServe();
  const proxyRef: string = (atprotoAgent as { relay?: { proxyRef?: string } }).relay?.proxyRef ?? "";
  if (proxyRef) {
    pdsHostname = proxyRef.startsWith("did:web:")
      ? proxyRef.slice("did:web:".length)
      : proxyRef;
  }
}

const registryEndpoint = (options.registryEndpoint as string) || "";
if (registryEndpoint) {
  if (pdsHostname) {
    const registry = createAtprotoMarketRegistry({ registryUrl: registryEndpoint, log: logger });
    await registry.registerPds(pdsHostname);
  } else {
    logger.warn("market_registry_no_hostname", {
      reason: "could not determine PDS hostname for registry registration",
    });
  }
} else {
  logger.info("market_registry_skipped", {
    reason: "registry-endpoint not configured",
  });
}

const atproto = await createATProto({
  logger,
  badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex }),
  plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
  agent: atprotoAgent,
});

const providers: ReturnType<typeof createComputeProviderMarketBidderHooks>[] = [];
const serves: ReturnType<typeof createServe>[] = [];

if (options.computeProviderDigitaloceanToken) {
  const relay = await cliCreateXrpcRelay();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderMarketBidderHooks({
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
  providers.push(createComputeProviderMarketBidderHooks({
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
  providers.push(createComputeProviderMarketBidderHooks({
    provider: await createComputeProviderDenoWorker({ logger, atproto }),
  }));
}

// Market factory gets its own relay/serve (own keypair -> own subdomain/FQDN).
const bidderRelay = options.noXrpcRelay ? undefined : await cliCreateXrpcRelay();
const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderRelay,
  serve: createServe({
    logger,
    tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.servePort as number) ?? 0 },
    unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
    relays: bidderRelay ? [bidderRelay] : [],
  }),
});

function shutdown() {
  bidder.shutdown();
  for (const s of serves) s.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);
await bidder.beginServe();
