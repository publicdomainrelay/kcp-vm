// CLI entrypoint: all orchestration, env resolution, lifecycle management.
// Delegates route wiring to the slim createBidderRoutes factory.

import { Command } from "@publicdomainrelay/cli-args-env";
import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { PlcClient, createGenesisOp } from "@publicdomainrelay/did-plc";
import { loadOrGenerateKeypair } from "@publicdomainrelay/market-atproto";
import { createSubscriber } from "@publicdomainrelay/did-key-relay-subscriber-xrpc";
import { createSubscriberFactory } from "@publicdomainrelay/hono-factory-did-key-relay-subscriber-xrpc";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import type { ComputeProvider, ComputeProviderMode } from "@publicdomainrelay/compute-provider-abc";
import { createOidcIssuer } from "@publicdomainrelay/oidc-issuer-hono";
import { createBidderRoutes } from "@publicdomainrelay/hono-factory-compute-bidder";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

// ---------------------------------------------------------------------------
// Resolve config
// ---------------------------------------------------------------------------

let runtimeConfig: Record<string, unknown> | null = null;
try {
  const mod = await import("./config.json", { with: { type: "json" } });
  runtimeConfig = mod.default;
} catch { /* optional */ }

const { options } = await new Command(
  "CONFIG_PATH_HONO_BIDDER",
  cliArgsEnv,
  runtimeConfig,
).resolve();

const port = options.port as number;
const privateKeyHex = options.privateKeyHex as string | undefined;
const plcDirectoryUrl = options.plcDirectoryUrl as string;
const dispatcherHost = options.dispatcherHost as string;
const label = options.label as string;
const computeProviderMode = options.computeProvider as ComputeProviderMode | undefined;
const computeProviderToken = options.computeProviderToken as string | undefined;
const computeProviderBaseUrl = options.computeProviderBaseUrl as string | undefined;
const registryEndpoint = options.registryEndpoint as string | undefined;
const heartbeatIntervalMs = options.heartbeatIntervalMs as number;

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

function log(severity: string, message: string, extra?: Record<string, unknown>): void {
  console.log(JSON.stringify({ label, severity, message, ...(extra ?? {}) }));
}
function logInfo(obj: Record<string, unknown>): void {
  console.log(JSON.stringify({ label, ...obj }));
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function didWebToHttps(didOrUrl: string): string {
  return didOrUrl.startsWith("did:web:") ? "https://" + didOrUrl.slice("did:web:".length) : didOrUrl;
}

function parseAtUri(uri: string): { repo: string; collection: string; rkey: string } {
  const withoutProtocol = uri.replace("at://", "");
  const parts = withoutProtocol.split("/");
  return { repo: parts[0], collection: parts[1], rkey: parts.slice(2).join("/") };
}

// ---------------------------------------------------------------------------
// Keypair + DID
// ---------------------------------------------------------------------------

logInfo({ event: "bidder_starting", label, dispatcherHost });

const keypair = privateKeyHex
  ? await Secp256k1Keypair.import(privateKeyHex)
  : await Secp256k1Keypair.create({ exportable: true });

const privateKeyHexFinal = privateKeyHex ||
  Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

const attestationKp = await loadOrGenerateKeypair(privateKeyHexFinal);

// ---------------------------------------------------------------------------
// PLC registration
// ---------------------------------------------------------------------------

const plc = new PlcClient({ baseUrl: plcDirectoryUrl });
const signingKeyDid = keypair.did();

const { did, op } = await createGenesisOp({
  rotationKeys: [signingKeyDid],
  verificationMethods: {
    atproto: signingKeyDid,
    attestation: attestationKp.did(),
  },
  alsoKnownAs: [
    `at://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
  ],
  services: {
    atproto_pds: {
      type: "AtprotoPersonalDataServer",
      endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    },
    pdr_temp_market: {
      type: "PDRTempMarket",
      endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    },
    pdr_temp_compute_event: {
      type: "PDRTempComputeEvent",
      endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
    },
  },
  sign: (bytes) => keypair.sign(bytes),
});

logInfo({ event: "bidder_did_plc_registering", did });
await plc.submitOp(did, op);
logInfo({ event: "bidder_did_plc_registered", did });

// ---------------------------------------------------------------------------
// Signer
// ---------------------------------------------------------------------------

const signer = {
  did: () => did,
  sign: (bytes: Uint8Array) => keypair.sign(bytes),
};

// ---------------------------------------------------------------------------
// Repo factory
// ---------------------------------------------------------------------------

const { app, api } = createRepoFactory({
  storage: new MemoryStorage(),
  signer,
  baseOrigin: `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`,
  didWebServices: [
    { id: "pdr_temp_market", type: "PDRTempMarket" },
    { id: "pdr_temp_compute_event", type: "PDRTempComputeEvent" },
  ],
});

// ---------------------------------------------------------------------------
// Record helpers for compute provider construction
// ---------------------------------------------------------------------------

async function createRecordHelper(
  collection: string,
  record: Record<string, unknown>,
): Promise<{ $type: "com.atproto.repo.strongRef"; uri: string; cid: string }> {
  const rkey = TID.next().toString();
  await api.applyWrites(did, [{ action: "create", collection, rkey, record }]);
  const rec = await api.getRecord(did, collection, rkey);
  return {
    $type: "com.atproto.repo.strongRef",
    uri: `at://${did}/${collection}/${rkey}`,
    cid: rec?.cid ?? "",
  };
}

async function deleteRecordHelper(collection: string, rkey: string): Promise<void> {
  await api.applyWrites(did, [{ action: "delete", collection, rkey }]);
}

// ---------------------------------------------------------------------------
// Compute provider
// ---------------------------------------------------------------------------

const mode = computeProviderMode;

const computeProvider: ComputeProvider | null = (() => {
  if (mode === "digitalocean") {
    if (!computeProviderToken) {
      logInfo({ event: "bidder_do_incomplete", hint: "digitalocean mode requires token", mode });
      return null;
    }
    return createDigitalOceanComputeProvider({
      getAgentDid: () => did,
      getIssuerUrl: () => computeProviderBaseUrl || "https://droplet-oidc.its1337.com",
      log: (level, msg, fields) => logInfo({ severity: level, message: msg, ...(fields ?? {}) }),
      parseAtUri,
      digitaloceanBaseUrl: computeProviderBaseUrl || "https://droplet-oidc.its1337.com",
      doToken: computeProviderToken,
      acceptPathVm: "/root/secrets/publicdomainrelay.com/market/accept.json",
      createRecord: createRecordHelper,
      deleteRecord: deleteRecordHelper,
    });
  }
  if (mode === "local") {
    return createLocalComputeProvider({
      log: (level, msg, fields) => logInfo({ severity: level, message: msg, ...(fields ?? {}) }),
      parseAtUri,
      getAgentDid: () => did,
      getIssuerUrl: () => didWebToHttps(""), // updated after relay registration
      acceptPathVm: undefined,
      containerMode: "container",
      vmImage: undefined,
      containerImage: undefined,
      cacheDir: undefined,
      createRecord: createRecordHelper,
      deleteRecord: deleteRecordHelper,
    });
  }
  return null;
})();

if (computeProvider?.setup) {
  await computeProvider.setup();
  logInfo({ event: "bidder_compute_provider_setup_done", did, mode });
}

// ---------------------------------------------------------------------------
// Relay subscription
// ---------------------------------------------------------------------------

const dispatcherDid = `did:web:${dispatcherHost}`;
const { handleRequest } = createSubscriberFactory({ app });

async function getServiceAuthToken(lxm: string): Promise<string> {
  return await signServiceAuth(signer, { aud: dispatcherDid, lxm });
}

const relay = { proxyRef: "", subdomain: "" };

logInfo({ event: "bidder_relay_connecting", dispatcherHost });
const handle = await createSubscriber({
  label,
  keypair,
  getServiceAuthToken,
  dispatcherHost,
  handleRequest,
});
relay.subdomain = handle.subdomain;
relay.proxyRef = handle.proxyRef;
logInfo({ event: "bidder_relay_registered", subdomain: handle.subdomain, proxyRef: handle.proxyRef });

// ---------------------------------------------------------------------------
// Routes (slim factory)
// ---------------------------------------------------------------------------

const routes = createBidderRoutes({
  app: app as any,
  repoApi: api,
  signer,
  attestationKp,
  computeProvider,
  idResolver: new IdResolver(),
  relay,
  did,
  dispatcherHost,
  mode,
  log,
});

// ---------------------------------------------------------------------------
// OIDC issuer (local mode only)
// ---------------------------------------------------------------------------

if (mode === "local" && computeProvider) {
  const issuerUrl = didWebToHttps(relay.proxyRef);
  const oidcIssuer = createOidcIssuer({
    getIssuerUrl: () => issuerUrl,
    getDroplet: (id) => computeProvider.getDroplet?.(id),
    serviceUrl: issuerUrl,
    log: (level, msg, extra) => logInfo({ severity: level, message: msg, ...(extra ?? {}) }),
  });
  // deno-lint-ignore no-explicit-any
  app.route("/", oidcIssuer.app as any);
  logInfo({ event: "bidder_oidc_issuer_mounted", issuerUrl });
}

// ---------------------------------------------------------------------------
// Discovery record + heartbeat
// ---------------------------------------------------------------------------

const BIDDER_DISCOVERY_NSID = "com.publicdomainrelay.temp.market.bidderDiscovery";
let discoveryRecordRkey: string | null = null;

async function ensureDiscoveryRecord(): Promise<void> {
  const nowIso = new Date().toISOString();

  if (!discoveryRecordRkey) {
    const existing = await api.listRecords(did, BIDDER_DISCOVERY_NSID, { limit: 1 });
    if (existing?.records?.length) {
      discoveryRecordRkey = existing.records[0].uri.split("/").pop()!;
    }
  }

  if (discoveryRecordRkey) {
    const current = await api.getRecord(did, BIDDER_DISCOVERY_NSID, discoveryRecordRkey);
    const prev = (current?.value ?? {}) as Record<string, unknown>;
    await api.applyWrites(did, [{
      action: "update",
      collection: BIDDER_DISCOVERY_NSID,
      rkey: discoveryRecordRkey,
      record: { ...prev, updatedAt: nowIso },
    }]);
  } else {
    const rkey = TID.next().toString();
    await api.applyWrites(did, [{
      action: "create",
      collection: BIDDER_DISCOVERY_NSID,
      rkey,
      record: {
        $type: BIDDER_DISCOVERY_NSID,
        endpointUrl: relay.proxyRef || `${did}#pdr_temp_market`,
        appliesTo: ["com.publicdomainrelay.temp.compute.vm"],
        updatedAt: nowIso,
        createdAt: nowIso,
      },
    }]);
    discoveryRecordRkey = rkey;
  }
}

let discoveryTimer: ReturnType<typeof setInterval> | null = null;

function startDiscoveryUpdater(): void {
  if (discoveryTimer) return;
  logInfo({ event: "discovery_updater_start", intervalMs: heartbeatIntervalMs });
  discoveryTimer = setInterval(async () => {
    try {
      await ensureDiscoveryRecord();
    } catch (err) {
      logInfo({ event: "discovery_update_error", err: String(err) });
    }
  }, heartbeatIntervalMs);
}

function stopDiscoveryUpdater(): void {
  if (discoveryTimer) {
    clearInterval(discoveryTimer);
    discoveryTimer = null;
  }
}

// ---------------------------------------------------------------------------
// Allowlist + offering + registry
// ---------------------------------------------------------------------------

const ALLOWLIST_NSID = "com.publicdomainrelay.temp.auth.allowlist.rbacDid";

async function ensureOperatorAllowlist(service: string): Promise<void> {
  const existing = await api.listRecords(did, ALLOWLIST_NSID, { limit: 100 });
  for (const rec of existing?.records ?? []) {
    const v = rec.value as Record<string, unknown>;
    const protects = v.protects as Record<string, { service: string; scope?: string }> | undefined;
    for (const p of Object.values(protects ?? {})) {
      if (
        (p.service === service || p.service === "*") &&
        (p.scope === "account.auth" || p.scope === "*" || !p.scope)
      ) {
        logInfo({ event: "bidder_allowlist_exists", uri: rec.uri });
        return;
      }
    }
  }
  const rkey = TID.next().toString();
  await api.applyWrites(did, [{
    action: "create",
    collection: ALLOWLIST_NSID,
    rkey,
    record: {
      $type: ALLOWLIST_NSID,
      protects: { allowSelf: { service, scope: "account.auth" } },
      allowed: { allowSelf: [did] },
      createdAt: new Date().toISOString(),
    },
  }]);
  logInfo({ event: "bidder_allowlist_created", uri: `at://${did}/${ALLOWLIST_NSID}/${rkey}`, service });
}

const OFFERING_NSID = "com.publicdomainrelay.temp.market.offering";

async function ensureOffering(): Promise<void> {
  const existing = await api.listRecords(did, OFFERING_NSID, { limit: 1 });
  if (existing?.records?.length) {
    logInfo({ event: "bidder_offering_exists", uri: existing.records[0].uri });
    return;
  }
  const rkey = TID.next().toString();
  await api.applyWrites(did, [{
    action: "create",
    collection: OFFERING_NSID,
    rkey,
    record: {
      $type: OFFERING_NSID,
      endpointUrl: `${did}#pdr_temp_market`,
      appliesTo: ["com.publicdomainrelay.temp.compute.vm"],
      createdAt: new Date().toISOString(),
    },
  }]);
  logInfo({ event: "bidder_offering_created", uri: `at://${did}/${OFFERING_NSID}/${rkey}` });
}

async function registerWithRegistry(): Promise<void> {
  const REGISTER_BIDDER_NSID = "com.publicdomainrelay.temp.market.registerBidder";
  const endpoints = registryEndpoint ? [registryEndpoint] : [];
  if (endpoints.length === 0) {
    logInfo({ event: "registry_disabled", reason: "no REGISTRY_ENDPOINT configured" });
    return;
  }
  const body = { bidderDid: did, appliesTo: ["com.publicdomainrelay.temp.compute.vm"] };
  for (const endpoint of endpoints) {
    try {
      let targetBase: string;
      let audDid: string;
      if (endpoint.startsWith("http://") || endpoint.startsWith("https://")) {
        targetBase = `${endpoint.replace(/\/+$/, "")}/xrpc`;
        audDid = `did:web:${new URL(endpoint).host}`;
      } else {
        logInfo({ event: "register_with_registry_error", endpoint, error: "unsupported endpoint format" });
        continue;
      }
      const token = await signServiceAuth(signer, { aud: audDid, lxm: REGISTER_BIDDER_NSID });
      const res = await fetch(`${targetBase}/${REGISTER_BIDDER_NSID}`, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
        body: JSON.stringify(body),
      });
      if (res.ok) {
        logInfo({ event: "registered_with_registry", endpoint });
        startDiscoveryUpdater();
      } else {
        const errBody = await res.text();
        logInfo({ event: "register_with_registry_error", endpoint, status: res.status, body: errBody });
      }
    } catch (err) {
      logInfo({ event: "register_with_registry_exception", endpoint, err: String(err) });
    }
  }
}

// ---------------------------------------------------------------------------
// Lifecycle startup
// ---------------------------------------------------------------------------

if (mode !== "local" && mode !== "digitalocean") {
  await ensureOperatorAllowlist(computeProviderBaseUrl ?? "");
}
await ensureOffering();
await ensureDiscoveryRecord();
await registerWithRegistry();

// ---------------------------------------------------------------------------
// Serve
// ---------------------------------------------------------------------------

const url = `https://${relay.subdomain}.${dispatcherHost}`;
logInfo({ event: "bidder_ready", did, subdomain: relay.subdomain, proxyRef: relay.proxyRef, url });

Deno.serve(
  { port, onListen: ({ port: p }) => log("info", "listening", { port: p, did }) },
  app.fetch,
);

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

function stop(): void {
  stopDiscoveryUpdater();
  handle.ws.close();
  routes.stop();
}

Deno.addSignalListener("SIGINT", () => {
  log("info", "shutting_down", { signal: "SIGINT" });
  stop();
  Deno.exit();
});
Deno.addSignalListener("SIGTERM", () => {
  log("info", "shutting_down", { signal: "SIGTERM" });
  stop();
  Deno.exit();
});
