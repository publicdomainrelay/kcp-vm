import { Command } from "@publicdomainrelay/cli-args-env";
import { isValidPolicyMode, type PolicyMode } from "@publicdomainrelay/market-policy-abc";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidderProviderRef } from "@publicdomainrelay/market-bidder-abc";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createComputeProviderDenoWorker, createWorkerProviderHooks } from "@publicdomainrelay/market-bidder-worker";
import { createATProto, createLocalPDSAgent, createRemoteAgent } from "@publicdomainrelay/atproto-helpers";
import type { LocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { ACCEPT_NSID, DEFAULT_RELAY_URLS, EVENT_NSID, OFFERING_NSID, RFP_NSID, relayUrlsToFirehoseUrls } from "@publicdomainrelay/market-common";
import { verifyRelayVisibility } from "@publicdomainrelay/requester-xrpc";
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
import cliArgsEnv from "./cli-args-env.ts";

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

const ingressProxyHost = (options.ingressProxyHost as string) || "xrpc.fedproxy.com";

const plcDirectoryUrl = (options.plcDirectoryUrl as string) || "https://plc.directory";

// Auto-detect local dev: *.localhost isn't in DNS.  If the dispatcher is
// reachable at a local host, patch fetch so the bidder can resolve the
// requester's PDS endpoints (also on *.localhost) through the relay.
// Also patches plc.directory → local PLC when plcDirectoryUrl is local.
const isLocalDev = ingressProxyHost.includes("localhost") || ingressProxyHost.startsWith("127.");
const _plcHost = (() => { try { return new URL(plcDirectoryUrl).hostname; } catch { return plcDirectoryUrl; } })();
const isLocalPlc = _plcHost === "localhost" || _plcHost.startsWith("127.") || _plcHost === "0.0.0.0";
if (isLocalDev || isLocalPlc) {
  const patchPort = ingressProxyHost.includes(":") ? ingressProxyHost.split(":").pop()! : "80";
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
async function cliCreateIngress() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  return createIngress({ logger, ingressProxyHost, signer: atproto.signer, keypair: relayKeypair });
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
  const pdsStatePath = options.pdsStatePath as string | undefined;
  // Relay-only: no TCP listener. associateConfirm arrives via relay →
  // app.fetch programmatic. subscribeRepos firehose is wired via
  // directSubscriptionHandler (in-process callback, no loopback WS).
  const pdsServe = createServe({ logger });
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: pdsServe,
    plcDirectoryUrl,
    ingressProxyHost,
    storagePath: pdsStatePath,
    associateServiceId: BIDDER_ASSOC_SERVICE,
  });
  await atprotoAgent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  isLocal = true;
  const relayHost: string = (atprotoAgent as { relay?: { ingressHost?: string } }).relay?.ingressHost ?? "";
  if (relayHost) {
    pdsHostname = relayHost;
  }
}

const atproto = await createATProto({
  logger,
  badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex }),
  plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
  agent: atprotoAgent,
});

const cliRelayUrl = (options.relayUrl as string) || "";
const relayUrls = cliRelayUrl
  ? [...new Set([...DEFAULT_RELAY_URLS, cliRelayUrl])]
  : DEFAULT_RELAY_URLS;

// Collect local relay URLs for deferred registration after serve starts.
for (const url of relayUrls) {
  if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
    _deferredRelayUrls.push(url);
  }
}

if (pdsHostname) {
  for (const url of relayUrls) {
    await Promise.race([
      atproto.requestCrawl(url, pdsHostname),
      new Promise<void>((r) => setTimeout(r, 5_000)),
    ]);
  }
} else if (cliRelayUrl) {
  logger.warn("relay_no_hostname_for_registration", {
    reason: "could not determine PDS hostname for relay registration",
  });
}

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
let localProviderEnsureImage: (() => Promise<void>) | undefined;

if (options.computeProviderDigitaloceanToken) {
  const relay = await cliCreateIngress();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderHooks({
    provider: createDigitalOceanComputeProvider({
      logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
      getIssuerUrl: () => relay.ingressUrl,
      digitaloceanBaseUrl: (options.computeProviderDigitaloceanBaseUrl as string) || "https://api.digitalocean.com",
      doToken: options.computeProviderDigitaloceanToken as string,
    }),
  }));
  await serve.beginServe();
}

if (options.computeProviderLocal) {
  const relay = await cliCreateIngress();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  const localProvider = createLocalComputeProvider({
    logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
    getIssuerUrl: () => relay.ingressUrl,
    oidcProvisioner: createOidcProvisioningEnricher(() => relay.ingressUrl),
    rbacProvisioner: createRbacProvisioner(),
    containerMode: options.computeProviderLocalMode as "vm" | "container" | undefined,
    vmImage: options.computeProviderLocalVmImage as string | undefined,
    containerImage: options.computeProviderLocalContainerImage as string | undefined,
    cacheDir: options.computeProviderLocalCacheDir as string | undefined,
  });
  localProviderEnsureImage = () => (localProvider as { ensureImage?(): Promise<void> }).ensureImage?.() ?? Promise.resolve();
  providers.push(createComputeProviderHooks({
    provider: localProvider,
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

const firehoseMode = (options.firehoseMode as string) || "off";
const firehoseUrlOverride = options.firehoseUrl as string | undefined;
const offeringRefreshSec = (options.offeringRefreshSec as number) ?? 300;

let rfpWatcherFactory: ((onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher) | undefined;
let rfpWatcherFactories: Array<(onRecord: (e: FirehoseRecordEvent) => void) => FirehoseWatcher> | undefined;

if (firehoseMode !== "off") {
  const firehoseUrls = firehoseUrlOverride
    ? firehoseUrlOverride.split(",").map((s) => s.trim()).filter(Boolean)
    : relayUrlsToFirehoseUrls(relayUrls);
  if (firehoseUrls.length > 0) {
    const make = firehoseMode === "jetstream" ? createJetstreamWatcher : createSubscribeReposWatcher;
    const build = (url: string) => (onRecord: (e: FirehoseRecordEvent) => void) =>
      make({ url, wantedCollections: [RFP_NSID], onRecord, log: logger });
    if (firehoseUrls.length > 1) {
      rfpWatcherFactories = firehoseUrls.map(build);
    } else {
      rfpWatcherFactory = build(firehoseUrls[0]);
    }
  }
}

// Firehose watchers for ACCEPT_NSID and EVENT_NSID — fallbacks when submit*
// service endpoint properties are absent on records.
let acceptWatcherFactory: typeof rfpWatcherFactory | undefined;
let eventWatcherFactory: typeof rfpWatcherFactory | undefined;
if (firehoseMode !== "off") {
  const firehoseUrls = firehoseUrlOverride
    ? firehoseUrlOverride.split(",").map((s: string) => s.trim()).filter(Boolean)
    : relayUrlsToFirehoseUrls(relayUrls);
  if (firehoseUrls.length > 0) {
    const make = firehoseMode === "jetstream" ? createJetstreamWatcher : createSubscribeReposWatcher;
    acceptWatcherFactory = (onRecord) =>
      make({ url: firehoseUrls[0], wantedCollections: [ACCEPT_NSID], onRecord, log: logger });
    eventWatcherFactory = (onRecord) =>
      make({ url: firehoseUrls[0], wantedCollections: [EVENT_NSID], onRecord, log: logger });
  }
}

// Market factory gets its own relay/serve (own keypair -> own subdomain/FQDN).
const bidderIngress = options.noIngressProxy ? undefined : await cliCreateIngress();
const bidderServe = createServe({
  logger,
  tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.servePort as number) ?? 0 },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  relays: bidderIngress ? [bidderIngress] : [],
});

const policyModeRaw = options.policyMode as string | undefined;
const policyMode = isValidPolicyMode(policyModeRaw) ? policyModeRaw : undefined;

const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderIngress,
  rfpWatcherFactory,
  rfpWatcherFactories,
  acceptWatcherFactory,
  eventWatcherFactory,
  offeringRefreshMs: offeringRefreshSec > 0 ? offeringRefreshSec * 1000 : undefined,
  serve: bidderServe,
  policyMode,
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
  ingressRef: bidderIngress?.ingressRef,
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

// Pre-build container image after association so first provision is fast.
if (localProviderEnsureImage) {
  logger.info("ensuring container image", {});
  await localProviderEnsureImage();
  logger.info("container image ready", {});
}

const _localServePort = _deferredPdsPort || bidderServe.tcpPort;
if (_deferredRelayUrls.length > 0 && _localServePort > 0) {
  const localHostname = `127.0.0.1:${_localServePort}`;
  for (const url of _deferredRelayUrls) {
    try {
      await atproto.requestCrawl(url, localHostname);
      logger.info("relay_local_reregistered", { registry: url, hostname: localHostname });
    } catch (err) {
      logger.warn("relay_local_reregister_failed", { registry: url, error: String(err) });
    }
  }
}

// Re-commit offering so relays index it immediately
// rather than waiting for the periodic offering refresh.
try {
  await bidder.refreshOffering();
  logger.info("relay_offering_refreshed_after_local_registration", {});
} catch (err) {
  logger.warn("relay_offering_refresh_failed", { error: String(err) });
}

// Verify the offering is discoverable through at least one relay
// that supports listReposByCollection. Probes each relay, then polls
// capable ones until the bidder's DID appears or the poll budget expires.
// Non-blocking on failure — bidder still boots, just warns.
const _allRelayUrls = [...relayUrls, ..._deferredRelayUrls.map((u) => `http://${u}`)];
const _visibilityHostname = pdsHostname ?? (_localServePort > 0 ? `127.0.0.1:${_localServePort}` : undefined);

// Re-request crawl on ALL relays now that the offering record is committed.
// The initial requestCrawl (during atprotoAgent.beginServe) ran before the
// offering was created/corrected. Production relays need a second ping so they
// re-subscribe and see the fresh offering commit.
if (_visibilityHostname) {
  for (const url of relayUrls) {
    try {
      await atproto.requestCrawl(url, _visibilityHostname);
      logger.info("relay_reregistered_after_offering_refresh", { url, hostname: _visibilityHostname });
    } catch (err) {
      logger.warn("relay_reregister_failed", { url, error: String(err) });
    }
  }
}

if (_allRelayUrls.length > 0 && _visibilityHostname) {
  const relayResult = await verifyRelayVisibility({
    relayUrls: _allRelayUrls,
    bidderDid: atproto.did,
    collection: OFFERING_NSID,
    log: logger,
  });
  if (relayResult.ok) {
    logger.info("relay_visibility_confirmed", {
      indexedBy: relayResult.indexedBy,
      capableRelays: relayResult.capableRelays,
    });
  } else {
    logger.warn("relay_visibility_failed", {
      capableRelays: relayResult.capableRelays,
      failures: relayResult.failures,
      hint: "Requester-side discovery via listReposByCollection will not find this bidder. The periodic offering refresh may eventually fix this.",
    });
  }
}
