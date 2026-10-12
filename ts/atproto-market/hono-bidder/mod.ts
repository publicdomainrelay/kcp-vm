import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { marketNetwork } from "@publicdomainrelay/market-network-common";
import { cliIdentityMode } from "@publicdomainrelay/market-identity-abc";
import { BidderPolicyError, createMarketBidderApp, marketBidderGuestTlsUrl, missingProviderFields } from "@publicdomainrelay/hono-factory-market-bidder-app";
import type { MarketBidderAppSpec, MarketBidderProviderSpec } from "@publicdomainrelay/hono-factory-market-bidder-app";
import cliArgsEnv from "./cli-args-env.ts";

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_HONO_BIDDER", cliArgsEnv, runtimeConfig).resolve();
const o: Record<string, any> = options;

const logger = createLogger({ serviceName: (o.serviceName as string) ?? "bidder" });

const providers: MarketBidderProviderSpec[] = [];
if (o.computeProviderDigitaloceanToken) providers.push({ kind: "digitalocean", token: o.computeProviderDigitaloceanToken, baseUrl: o.computeProviderDigitaloceanBaseUrl });
if (o.computeProviderLocal) providers.push({ kind: "local", containerImage: o.computeProviderLocalContainerImage, caCertPem: o.caCertPem });
if (o.computeProviderFirecracker) {
  providers.push({
    kind: "firecracker",
    nodeimage: o.computeProviderFirecrackerNodeimage,
    nodeboot: o.computeProviderFirecrackerNodeboot,
    config: o.computeProviderFirecrackerConfig,
    repoDir: o.computeProviderFirecrackerRepoDir,
    vmm: o.computeProviderFirecrackerVmm,
    runnerImage: o.computeProviderFirecrackerRunnerImage,
    workRoot: o.computeProviderFirecrackerWorkRoot,
    preinstall: o.computeProviderFirecrackerPreinstall,
    containerLabels: o.computeProviderFirecrackerContainerLabels,
    vmruntimeCommand: o.computeProviderFirecrackerVmruntimeCommand
      ? JSON.parse(o.computeProviderFirecrackerVmruntimeCommand)
      : undefined,
    rangeBase: o.computeProviderFirecrackerRangeBase,
    reuseImage: o.computeProviderFirecrackerReuseImage === true,
  });
}
if (o.computeProviderKcp) {
  providers.push({
    kind: "kcp",
    server: o.computeProviderKcpServer,
    workspace: o.computeProviderKcpWorkspace,
    handle: o.computeProviderKcpHandle,
    tokenFile: o.computeProviderKcpTokenFile,
    caFile: o.computeProviderKcpCaFile,
    guestHostAddress: o.computeProviderKcpGuestHostAddress,
  });
}
if (o.computeProviderDenoWorker) providers.push({ kind: "denoWorker", permissionMode: o.workerPermissionMode });

for (const p of providers) {
  const missing = missingProviderFields(p).map((f) => `--compute-provider-${p.kind}-${f.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)}`);
  if (missing.length > 0) throw new Error(`the ${p.kind} provider is enabled and ${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not set`);
}

const kcpIdentity = o.marketIdentityKcpServer as string | undefined;
if (kcpIdentity && !o.marketIdentityKcpWorkspace) throw new Error("--market-identity-kcp-server needs --market-identity-kcp-workspace");
const vault = o.identityVaultAddress
  ? { kind: "openbao" as const, address: o.identityVaultAddress, tokenFile: o.identityVaultTokenFile, caCertFile: o.identityVaultCaCertFile, namespace: o.identityVaultNamespace }
  : o.identityVaultFile ? { kind: "file" as const, path: o.identityVaultFile } : undefined;
const explicitMode = o.atprotoIdentityMode as string | undefined;
const urlRepo = !kcpIdentity && cliIdentityMode({
  mode: explicitMode, oauth: o.atprotoOauth, oauthQr: o.atprotoOauthQr, handle: o.atprotoHandle, password: !!o.atprotoPassword, vault: !!vault,
}) !== "localRepo";

const spec: MarketBidderAppSpec = {
  network: marketBidderGuestTlsUrl(marketNetwork({
    plcDirectoryUrl: o.plcDirectoryUrl,
    ingressProxyHost: o.ingressProxyHost,
    relayUrls: o.relayUrl,
    firehoseUrls: o.firehoseUrl,
    qrOrigin: o.qrOrigin,
  }), o.guestTlsPort),
  identity: {
    name: o.marketIdentityName,
    vaultMount: o.identityVaultMount,
    prompt: kcpIdentity
      ? { kind: "kcp", server: kcpIdentity, workspace: o.marketIdentityKcpWorkspace, tokenFile: o.marketIdentityKcpTokenFile, caCertFile: o.marketIdentityKcpCaCertFile }
      : { kind: "tty", mode: explicitMode, oauth: o.atprotoOauth, oauthQr: o.atprotoOauthQr, skipAssociation: o.skipQr },
    vault,
    stateDir: o.stateDir,
    handle: o.atprotoHandle,
    pdsUrl: o.atprotoPdsUrl,
    password: o.atprotoPassword,
    privateKeyHex: o.privateKeyHex,
    privateKeyHexPath: o.privateKeyHexPath,
    associateWith: o.associateWith,
    oauth: {
      clientId: o.oauthClientId,
      redirectUri: o.oauthRedirectUri,
      sessionPath: o.oauthSessionPath,
      sessionFile: o.oauthSessionFile,
      sessionClientId: o.oauthSessionClientId,
    },
  },
  repo: urlRepo ? { kind: "url" } : { kind: "inproc", statePath: o.pdsStatePath },
  providers,
  guestEndpoints: "inproc",
  market: {
    serve: { addr: o.serveAddr, port: o.servePort, unix: o.serveUnix, tlsCertFile: o.tlsCertFile, tlsKeyFile: o.tlsKeyFile },
    ingressProxy: !o.noIngressProxy,
    firehoseMode: o.firehoseMode,
    offeringRefreshSec: o.offeringRefreshSec,
    policy: o.policy,
    policyArgs: o.policyArgs,
    onlyRemotePolicyExec: o.onlyRemotePolicyExec,
    allowUntrustedPolicyExec: o.allowUntrustedPolicyExec,
    contractStoreDir: o.contractStoreDir,
    ingressKeyDir: o.ingressKeyDir,
  },
  reports: { portFile: o.portFile, listenersFile: o.listenersFile },
};

const app = createMarketBidderApp(spec, { logger });

const shutdown = () => void app.shutdown().finally(() => Deno.exit());
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

let status;
try {
  status = await app.start();
} catch (err) {
  await app.shutdown().catch(() => {});
  if (err instanceof BidderPolicyError) {
    console.error(err.message);
    Deno.exit(2);
  }
  logger.error("bidder_start_failed", { error: err instanceof Error ? err.message : String(err) });
  Deno.exit(1);
}

const statusFile = o.statusFile as string | undefined;
if (statusFile) {
  await Deno.writeTextFile(`${statusFile}.tmp`, JSON.stringify(status) + "\n");
  await Deno.rename(`${statusFile}.tmp`, statusFile);
  logger.info("bidder_status_written", { path: statusFile, ...status });
}
console.log(JSON.stringify({ event: "bidder_ready", ...status }));

await app.announce();
