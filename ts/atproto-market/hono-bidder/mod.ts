// Thin CLI entrypoint for market bidder.
// Owns: config resolution, compute provider / sandbox construction, OIDC mounting.
// Delegates lifecycle to createMarketBidder (lib/market-bidder).

import { Command } from "@publicdomainrelay/cli-args-env";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { CallbackSet } from "@publicdomainrelay/market-bidder";
import { createVmBidderCallbacks } from "@publicdomainrelay/market-bidder-compute";
import { createWorkerBidderCallbacks } from "@publicdomainrelay/market-bidder-worker";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import type { ComputeProvider, ComputeProviderMode } from "@publicdomainrelay/compute-provider-abc";
import { parseAtUri } from "@publicdomainrelay/compute-provider-abc";
import { createDenoComputeManifestStore, createDenoComputeInstanceStore, createDenoComputeInstanceRunner } from "@publicdomainrelay/compute-deno-atproto";
import { createDenoBundler, createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";
import { createOidcIssuer } from "@publicdomainrelay/oidc-issuer-hono";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

// ── config ────────────────────────────────────────────────────────────────

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
const mode = options.computeProvider as ComputeProviderMode | undefined;
const computeProviderToken = options.computeProviderToken as string | undefined;
const computeProviderBaseUrl = options.computeProviderBaseUrl as string | undefined;

// ── logger ────────────────────────────────────────────────────────────────

function log(severity: string, message: string, extra?: Record<string, unknown>): void {
  console.log(JSON.stringify({ label, severity, message, ...(extra ?? {}) }));
}

// ── helpers ───────────────────────────────────────────────────────────────

function didWebToHttps(didOrUrl: string): string {
  return didOrUrl.startsWith("did:web:") ? "https://" + didOrUrl.slice("did:web:".length) : didOrUrl;
}

// ── create + start bidder ─────────────────────────────────────────────────

const bidder = await createMarketBidder({
  serveOpts: { addr: "0.0.0.0", port },
  privateKeyHex,
  plcDirectoryUrl,
  label,
  relay: { host: dispatcherHost },
  inProcessPds: {},
  log,
  callbackFactory: async (deps) => {
    const callbacks: CallbackSet = {};

    // ── compute provider ─────────────────────────────────────────────

    let computeProvider: ComputeProvider | null = null;

    if (mode === "digitalocean") {
      if (!computeProviderToken) {
        log("info", "bidder do incomplete", { hint: "digitalocean mode requires token", mode });
      } else {
        computeProvider = createDigitalOceanComputeProvider({
          getAgentDid: () => deps.did,
          getIssuerUrl: () => computeProviderBaseUrl || "https://droplet-oidc.its1337.com",
          log: deps.log as any,
          parseAtUri,
          digitaloceanBaseUrl: computeProviderBaseUrl || "https://droplet-oidc.its1337.com",
          doToken: computeProviderToken,
          acceptPathVm: "/root/secrets/publicdomainrelay.com/market/accept.json",
          createRecord: deps.createRecord as any,
          deleteRecord: deps.deleteRecord,
        });
      }
    } else if (mode === "local") {
      computeProvider = createLocalComputeProvider({
        log: deps.log as any,
        parseAtUri,
        getAgentDid: () => deps.did,
        getIssuerUrl: () => didWebToHttps(deps.relay.proxyRef),
        acceptPathVm: undefined,
        containerMode: "container",
        vmImage: undefined,
        containerImage: undefined,
        cacheDir: undefined,
        createRecord: deps.createRecord as any,
        deleteRecord: deps.deleteRecord,
      });
    }

    if (computeProvider?.setup) {
      await computeProvider.setup();
      log("info", "bidder compute provider setup done", { did: deps.did, mode });
    }

    // ── VM callbacks ─────────────────────────────────────────────────

    if (computeProvider) {
      const vm = createVmBidderCallbacks({
        did: deps.did,
        attestationKp: deps.attestationKp,
        signer: deps.signer,
        idResolver: deps.idResolver,
        relay: { proxyRef: deps.relay.proxyRef },
        computeProvider,
        log: deps.log,
        activeContracts: deps.activeContracts,
        createRepoRecord: deps.createRepoRecord,
        createSignedRepoRecord: deps.createSignedRepoRecord,
        callService: deps.callService,
        resolve: deps.resolve,
      });
      callbacks.rfpCallbacks = vm.rfp;
      callbacks.onAccept = vm.accept;
      callbacks.eventCallbacks = vm.event;
    }

    // ── sandbox + worker callbacks ────────────────────────────────────

    if (computeProvider) {
      const sandboxBundler = createDenoBundler();
      const workerManifestStore = createDenoComputeManifestStore(
        { createRecord: deps.createRecord as any, getRecord: deps.repoApi.getRecord } as any,
        deps.did,
      );
      const workerInstanceStore = createDenoComputeInstanceStore(
        { createRecord: deps.createRecord as any, getRecord: deps.repoApi.getRecord } as any,
        deps.did,
      );
      const workerRunner = createDenoComputeInstanceRunner({
        manifestStore: workerManifestStore,
        instanceStore: workerInstanceStore,
        bundler: sandboxBundler,
        createWorker: createPersistentDenoWorker,
      });
      log("info", "bidder sandbox ready");

      const w = createWorkerBidderCallbacks({
        did: deps.did,
        attestationKp: deps.attestationKp,
        signer: deps.signer,
        idResolver: deps.idResolver,
        relay: { proxyRef: deps.relay.proxyRef },
        workerManifestStore,
        workerRunner,
        log: deps.log,
        activeContracts: deps.activeContracts,
        createRepoRecord: deps.createRepoRecord,
        createSignedRepoRecord: deps.createSignedRepoRecord,
        callService: deps.callService,
        resolve: deps.resolve,
      });

      callbacks.rfpCallbacks = {
        pdr_temp_market: {
          ...(callbacks.rfpCallbacks?.["pdr_temp_market"] ?? {}),
          ...(w.rfp["pdr_temp_market"] ?? {}),
        },
      };
    }

    return callbacks;
  },
});

// ── OIDC issuer (local mode only) ────────────────────────────────────────

if (mode === "local") {
  const issuerUrl = didWebToHttps(bidder.relay.proxyRef);
  const oidcIssuer = createOidcIssuer({
    getIssuerUrl: () => issuerUrl,
    getDroplet: (_id: string) => undefined,
    serviceUrl: issuerUrl,
    log: (level: string, msg: string, extra?: Record<string, unknown>) =>
      log(level, msg, extra),
  });
  bidder.app.route("/", oidcIssuer.app as any);
  log("info", "bidder oidc issuer mounted", { issuerUrl });
}
