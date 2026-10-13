import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { marketNetwork } from "@publicdomainrelay/market-network-common";
import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger, createStructuredLogger, getMinLogLevelFromEnv } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { EventBus } from "@publicdomainrelay/event-bus";
import { createStaticFilesApp, type StaticFileEvent } from "@publicdomainrelay/hono-factory-static-files-fs";
import { K8S_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { createAtprotoSessionVerifier } from "@publicdomainrelay/socialweb-computer-oauth-atproto";
import { createFileSessionStore } from "@publicdomainrelay/socialweb-computer-oauth-session-fs";
import { createVaultSessionStore } from "@publicdomainrelay/socialweb-computer-oauth-session-vault";
import { loadOrCreateKeyHex } from "@publicdomainrelay/key-file-fs";
import { loadOrCreateVaultKeyHex } from "@publicdomainrelay/key-vault";
import { createOpenBaoServerResolver } from "@publicdomainrelay/openbao-server-kcp";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import type { SessionStore } from "@publicdomainrelay/socialweb-computer-abc";
import { createAccountRequesters } from "@publicdomainrelay/socialweb-computer-requester-inproc";
import { createPasswordSessionAgent, parsePasswordSessions } from "@publicdomainrelay/socialweb-computer-session-password";
import { createWebFactory } from "@publicdomainrelay/hono-factory-socialweb-computer-oauth";
import { createRequestProofVerifier } from "@publicdomainrelay/socialweb-computer-k8s-request-proof";
import { createXrpcClusterGateway } from "@publicdomainrelay/socialweb-computer-k8s-gateway-xrpc";
import {
  createClusterApiFactory,
  createPinnedDownloadsFactory,
  type CredentialHelper,
} from "@publicdomainrelay/hono-factory-socialweb-computer-k8s";
import { loadPinnedDownloads } from "@publicdomainrelay/socialweb-computer-k8s-downloads-fs";
import {
  createKcpClusterLinkReader,
  createKcpGatewayListenerUrl,
  createKcpMarketIdentities,
  createKcpNodeClaimStore,
} from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import type { KcpClient } from "@publicdomainrelay/kcp-client-abc";
import { parseKubeconfig } from "@publicdomainrelay/kcp-client-common";
import { createKcpRestClient } from "@publicdomainrelay/kcp-client-rest";
import {
  createMarketNodeFulfiller,
  publishedOpenBaoCAFile,
  type MarketNodeFulfiller,
  MESHD_DOWNLOAD_PREFIX,
  meshdPinOf,
} from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import { createOpenBaoClaimKeyStore } from "@publicdomainrelay/socialweb-computer-k8s-claim-keys-openbao";
import { type ClaimKeyStore, createMemoryClaimKeyStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { createVaultClaimKeyStore } from "@publicdomainrelay/socialweb-computer-k8s-claim-keys-vault";
import { createScopeCache } from "@publicdomainrelay/policy-engine-scope-cache";
import { createScopeRefresher } from "@publicdomainrelay/scope-refresh-timers";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try {
  runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default;
} catch { /* optional */ }

const { options } = await new Command("CONFIG_PATH_HONO_SOCIALWEB_COMPUTER_K8S", cliArgsEnv, runtimeConfig).resolve();

const logger = createLogger({ serviceName: options.label as string });
const log = (event: string, data: Record<string, unknown> = {}) => logger.info(event, data);

const stateDir = options.stateDir as string;
await Deno.mkdir(stateDir, { recursive: true, mode: 0o700 });

const webDir = options.webDir as string;
const publicOrigin = (options.publicOrigin as string | undefined)?.replace(/\/+$/, "");


const testOwnerSessions = options.testOwnerSessionsFile
  ? parsePasswordSessions(JSON.parse(await Deno.readTextFile(options.testOwnerSessionsFile as string)))
  : null;
const testNodeGuestHostAliases = ((options.testNodeGuestHostAliases as string | undefined) ?? "").split(",").map((a) => a.trim())
  .filter(Boolean);
const testNodeUserDataBase = options.testNodeUserDataBaseFile
  ? await Deno.readTextFile(options.testNodeUserDataBaseFile as string)
  : undefined;
if (testOwnerSessions || testNodeGuestHostAliases.length > 0 || testNodeUserDataBase !== undefined) {
  logger.warn("test_seams_enabled", {
    ownerSessions: testOwnerSessions ? [...testOwnerSessions.keys()] : [],
    nodeGuestHostAliases: testNodeGuestHostAliases,
    nodeUserDataBase: testNodeUserDataBase !== undefined,
  });
}

const kcp: KcpClient | null = options.kcpKubeconfig
  ? createKcpRestClient(
    parseKubeconfig(await Deno.readTextFile(options.kcpKubeconfig as string), options.kcpContext as string | undefined),
  )
  : options.kcpServer && options.kcpTokenFile
  ? createKcpRestClient({
    server: new URL(options.kcpServer as string).origin,
    tokenFile: options.kcpTokenFile as string,
    caFile: options.kcpCaFile as string | undefined,
  })
  : null;

async function accountState(): Promise<
  { sessionStore: SessionStore; requesterKeyHex: () => Promise<string>; vaultClaimKeys?: ClaimKeyStore }
> {
  if (!options.openbaoServer) {
    logger.warn("account_state_in_files", { stateDir, next: "set openbao-server to keep sessions and the requester key in the vault" });
    return {
      sessionStore: createFileSessionStore(`${stateDir}/oauth-sessions.json`, {
        onCorrupt: ({ path, quarantine }) => logger.error("session_store_corrupt", { path, quarantine }),
      }),
      requesterKeyHex: () => loadOrCreateKeyHex(`${stateDir}/requester-key.hex`),
    };
  }
  if (!kcp) throw new Error("openbao-server needs kcp-kubeconfig to read the OpenBaoServer object");
  const resolve = createOpenBaoServerResolver({
    kcp,
    server: kcp.server,
    workspace: options.openbaoServerWorkspace as string,
    name: options.openbaoServer as string,
    client: options.openbaoClient as string,
    clientDir: options.openbaoClientDir as string | undefined,
    onWaiting: (reason) => log("vault_waiting", { openbaoserver: options.openbaoServer, reason }),
  });
  const resolved = await resolve();
  log("vault_resolved", { openbaoserver: resolved.name, address: resolved.address, kvMount: resolved.kvMount, role: resolved.certLogin.role });
  const vault = createOpenBaoIdentityVault({
    endpoint: async () => {
      const r = await resolve();
      return { address: r.address, caCert: r.caCert, clientCert: r.clientCert, clientKey: r.clientKey, certLogin: r.certLogin };
    },
  });
  const identities = options.marketIdentityWorkspace
    ? createKcpMarketIdentities({ kcp, workspace: options.marketIdentityWorkspace as string })
    : null;
  return {
    sessionStore: createVaultSessionStore({
      vault,
      mount: resolved.kvMount,
      onSet: async (did, session, ref) => {
        const name = await identities?.signedIn({ did, handle: session.handle, pdsUrl: session.pds, sessionRef: ref })
          .catch((err) => logger.warn("market_identity_write_failed", { did, error: String(err) }));
        if (name) log("market_identity_ready", { did, marketIdentity: name });
      },
      onDel: async (did, ref) => {
        await identities?.signedOut(did, ref).catch((err) => logger.warn("market_identity_write_failed", { did, error: String(err) }));
      },
    }),
    requesterKeyHex: () => loadOrCreateVaultKeyHex(vault, { mount: resolved.kvMount, path: "requester/key" }),
    vaultClaimKeys: createVaultClaimKeyStore({ vault, mount: resolved.kvMount }),
  };
}

const { sessionStore, requesterKeyHex, vaultClaimKeys } = await accountState();

const accounts = createAccountRequesters({
  sessionStore,
  requesterKeyHex,
  plcDirectoryUrl: options.plcDirectoryUrl as string,
  ingressProxyHost: options.ingressProxyHost as string,
  oauthClientId: options.oauthClientId as string | undefined,
  agentWithoutOAuthFor: (did) => {
    const session = testOwnerSessions?.get(did);
    return session ? createPasswordSessionAgent(session) : null;
  },
  log,
});

function gatewayUrl(): string | (() => Promise<string>) {
  if (options.gatewayUrl) return options.gatewayUrl as string;
  if (!kcp) throw new Error("set gateway-url, or kcp-kubeconfig to read the cluster gateway's listener from its Gateway object");
  return createKcpGatewayListenerUrl({
    kcp,
    workspace: options.gatewayKcpWorkspace as string,
    name: options.gatewayKcpName as string,
    listener: options.gatewayKcpListener as string,
  });
}

const gateway = createXrpcClusterGateway({
  gatewayUrl: gatewayUrl(),
  audience: options.gatewayAudience as string,
  serviceAuth: {
    async serviceAuth(did, aud, lxm) {
      const agent = await accounts.agentFor(did);
      if (!agent.getServiceAuth) throw new Error("the account's agent cannot mint service auth");
      return await agent.getServiceAuth(aud, lxm);
    },
  },
});

const downloadsDir = (options.downloadsDir as string | undefined) ??
  `${webDir}/${(options.downloadsSubdir as string).replace(/^\/+|\/+$/g, "")}`;
const pinned = await loadPinnedDownloads(downloadsDir);
const helpers: CredentialHelper[] = pinned.list().flatMap((d) => {
  const match = /^socialweb-credential-([a-z0-9]+)-([a-z0-9_]+)$/.exec(d.name);
  return match ? [{ os: match[1], arch: match[2], path: `/downloads/${d.sha256}/${d.name}` }] : [];
}).sort((a, b) => `${a.os}-${a.arch}`.localeCompare(`${b.os}-${b.arch}`));

const testNodeDownloadHost = options.testNodeDownloadHost as string | undefined;
const requiredDownloads = ((options.requiredDownloads as string | undefined) ?? "").split(",").map((n) => n.trim())
  .filter(Boolean);
const missingDownloads = requiredDownloads.filter((name) => !pinned.list().some((d) => d.name === name));
if (missingDownloads.length > 0) {
  logger.error("required_downloads_missing", { downloadsDir, missing: missingDownloads, have: pinned.list().map((d) => d.name) });
  throw new Error(
    `required pinned downloads are missing from ${downloadsDir}: ${missingDownloads.join(", ")}. Each needs <name> and a ` +
      `sha256sum-format <name>.sha256 there; the socialweb-computer binary passes its socialweb-computer-k8s-downloads ` +
      `asset as --downloads-dir`,
  );
}
if (requiredDownloads.some((n) => n.startsWith(MESHD_DOWNLOAD_PREFIX)) && !publicOrigin && !testNodeDownloadHost) {
  logger.error("node_download_origin_missing", { requiredDownloads });
  throw new Error(
    "socialweb-meshd is a required download, but node guests have no origin to fetch it from: set public-origin " +
      "(or test-node-download-host in a market network)",
  );
}

const serve = createServe({
  logger,
  tcp: {
    addr: options.serveAddr as string,
    port: options.httpPort as number,
    certFile: options.tlsCertFile as string | undefined,
    keyFile: options.tlsKeyFile as string | undefined,
  },
  portFile: options.portFile as string | undefined,
});

const web = createWebFactory({
  sessionStore,
  verifier: createAtprotoSessionVerifier({ log }),
  scope: K8S_OAUTH_SCOPE.join(" "),
  publicOrigin,
  clientName: "socialweb-computer-k8s",
  log,
});

const api = createClusterApiFactory({
  gateway,
  proof: createRequestProofVerifier({ sessions: sessionStore, publicOrigin, log }),
  maxKubeconfigTtlSec: options.kubeconfigMaxTtlSec as number,
  kcpPublicHost: options.kcpPublicHost as string | undefined,
  credentialHelpers: helpers,
  log,
});

const bus = new EventBus<StaticFileEvent>();
bus.subscribe((event) => {
  if (event.type === "file-not-found") logger.warn("web_404", { path: event.path });
});
const staticApp = createStaticFilesApp(webDir, createStructuredLogger("web", getMinLogLevelFromEnv()), bus);

serve.app.route("/", web.createApp());
serve.app.route("/", api.createApp());
serve.app.route("/", createPinnedDownloadsFactory({ downloads: pinned, log }).createApp());
serve.app.route("/", staticApp as never);

const relayUrls = ((options.relayUrl as string | undefined) ?? "").split(",").map((u) => u.trim()).filter(Boolean);
installMarketNetworkFetch({
  network: marketNetwork({
    plcDirectoryUrl: options.plcDirectoryUrl as string,
    ingressProxyHost: options.ingressProxyHost as string,
    relayUrls,
    hosts: options.networkHosts as string | undefined,
  }),
});

const scopeCache = createScopeCache();
const scopeRefresher = createScopeRefresher({ scopeCache });

const openBaoClient = options.openbaoCaFile
  ? Deno.createHttpClient({ caCerts: [await Deno.readTextFile(options.openbaoCaFile as string)] })
  : null;
const claimKeys = options.openbaoAddr
  ? createOpenBaoClaimKeyStore({
    address: options.openbaoAddr as string,
    mount: options.openbaoKvMount as string,
    token: async () => (await Deno.readTextFile(options.openbaoTokenFile as string)).trim(),
    ...(openBaoClient ? { fetch: (input, init) => fetch(input, { ...init, client: openBaoClient } as unknown as RequestInit) } : {}),
  })
  : vaultClaimKeys ?? createMemoryClaimKeyStore();
const kcpWorkspace = options.kcpWorkspace as string | undefined;
if (!claimKeys.durable && kcp && kcpWorkspace) {
  logger.warn("claim_keys_in_memory", {
    next: "a restarted door cannot re-serve an in-flight claim's secrets URL, so its Bidding and " +
      "Provisioning claims are marked Failed and released on deletion; set openbao-addr to resume them",
  });
}

const nodeGuestCaPem = options.nodeGuestCaFile ? (await Deno.readTextFile(options.nodeGuestCaFile as string)).trim() : "";

await serve.beginServe();
const nodeDownloadOrigin = testNodeDownloadHost
  ? `${options.tlsCertFile ? "https" : "http"}://${testNodeDownloadHost}:${serve.tcpPort}`
  : publicOrigin;
const meshd = meshdPinOf(pinned.list(), nodeDownloadOrigin);

let fulfiller: MarketNodeFulfiller | null = null;
if (kcp && kcpWorkspace) {
  fulfiller = createMarketNodeFulfiller({
    store: createKcpNodeClaimStore({ kcp, homesWorkspace: kcpWorkspace }),
    links: createKcpClusterLinkReader({ kcp }),
    requesterFor: (did) => accounts.requesterFor(did),
    owners: () => Promise.resolve(testOwnerSessions ? [...testOwnerSessions.keys()] : []),
    ...(options.clusterCaDir ? { openBaoCAFileOf: publishedOpenBaoCAFile(options.clusterCaDir as string) } : {}),
    plcUrl: options.plcDirectoryUrl as string,
    ingressProxyHost: options.ingressProxyHost as string,
    relayUrls,
    vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
    extraBidderDids: ((options.nodeExtraBidderDids as string | undefined) ?? "").split(",").map((d) => d.trim()).filter(Boolean),
    guestHostAliases: testNodeGuestHostAliases,
    userDataBase: testNodeUserDataBase,
    ...(nodeGuestCaPem ? { guestCaPem: nodeGuestCaPem } : {}),
    ...(options.nodeGuestCurlHome ? { guestCurlHome: options.nodeGuestCurlHome as string } : {}),
    ...(options.nodePolicy ? { policy: { name: options.nodePolicy as string, args: JSON.parse(options.nodePolicyArgs as string) } } : {}),
    pollIntervalMs: options.nodePollIntervalMs as number,
    ...(meshd ? { meshd } : {}),
    scopeCache,
    scopeRefresher,
    claimKeys,
    log,
  });
}

fulfiller?.start();
log("ready", {
  httpPort: options.httpPort,
  webDir,
  publicOrigin: publicOrigin ?? null,
  credentialHelpers: helpers.length,
  pinnedDownloads: pinned.list(),
  meshd: meshd ?? null,
  downloadsDir,
  nodeDownloadOrigin: nodeDownloadOrigin ?? null,
  fulfilsNodes: fulfiller !== null,
});
if (fulfiller && !meshd) {
  logger.warn("meshd_not_offered", {
    downloadsDir,
    nodeDownloadOrigin: nodeDownloadOrigin ?? null,
    meaning: "market nodes join with the bridge CNI and per-node DNS but no pod mesh: pods on different nodes cannot reach each other",
    needs: "socialweb-meshd-x86_64 and socialweb-meshd-aarch64 with their .sha256 sidecars in the downloads dir, and public-origin (or test-node-download-host) set",
  });
}

async function shutdown(): Promise<void> {
  await fulfiller?.stop();
  await accounts.shutdown();
  serve.shutdown();
  Deno.exit(0);
}

Deno.addSignalListener("SIGINT", () => void shutdown());
Deno.addSignalListener("SIGTERM", () => void shutdown());
