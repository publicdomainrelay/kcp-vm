import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger, createStructuredLogger, getMinLogLevelFromEnv } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { EventBus } from "@publicdomainrelay/event-bus";
import { createStaticFilesApp, type StaticFileEvent } from "@publicdomainrelay/hono-factory-static-files-fs";
import { K8S_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { createAtprotoSessionVerifier } from "@publicdomainrelay/socialweb-computer-oauth-atproto";
import { createFileSessionStore } from "@publicdomainrelay/socialweb-computer-oauth-session-fs";
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
  createKcpNodeClaimStore,
  parseKubeconfig,
} from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import {
  createMarketNodeFulfiller,
  type MarketNodeFulfiller,
  meshdPinOf,
} from "@publicdomainrelay/socialweb-computer-k8s-node-market";
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

const sessionStore = createFileSessionStore(`${stateDir}/oauth-sessions.json`, {
  onCorrupt: ({ path, quarantine }) => logger.error("session_store_corrupt", { path, quarantine }),
});

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

const accounts = createAccountRequesters({
  sessionStore,
  requesterKeyPath: `${stateDir}/requester-key.hex`,
  plcDirectoryUrl: options.plcDirectoryUrl as string,
  ingressProxyHost: options.ingressProxyHost as string,
  oauthClientId: options.oauthClientId as string | undefined,
  agentWithoutOAuthFor: (did) => {
    const session = testOwnerSessions?.get(did);
    return session ? createPasswordSessionAgent(session) : null;
  },
  log,
});

const gateway = createXrpcClusterGateway({
  gatewayUrl: options.gatewayUrl as string,
  audience: options.gatewayAudience as string,
  serviceAuth: {
    async serviceAuth(did, aud, lxm) {
      const agent = await accounts.agentFor(did);
      if (!agent.getServiceAuth) throw new Error("the account's agent cannot mint service auth");
      return await agent.getServiceAuth(aud, lxm);
    },
  },
});

const downloadsDir = `${webDir}/${(options.downloadsSubdir as string).replace(/^\/+|\/+$/g, "")}`;

async function credentialHelpers(): Promise<CredentialHelper[]> {
  const subdir = (options.downloadsSubdir as string).replace(/^\/+|\/+$/g, "");
  const out: CredentialHelper[] = [];
  try {
    for await (const entry of Deno.readDir(downloadsDir)) {
      const match = /^socialweb-credential-([a-z0-9]+)-([a-z0-9_]+)$/.exec(entry.name);
      if (entry.isFile && match) out.push({ os: match[1], arch: match[2], path: `/${subdir}/${entry.name}` });
    }
  } catch (err) {
    if (!(err instanceof Deno.errors.NotFound)) throw err;
  }
  return out.sort((a, b) => a.path.localeCompare(b.path));
}

const helpers = await credentialHelpers();
const pinned = await loadPinnedDownloads(downloadsDir);
const meshd = meshdPinOf(pinned.list(), publicOrigin);

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

async function debugNodeSsh(dir: string) {
  const privateKeyPath = `${dir}/id_ed25519`;
  await Deno.mkdir(dir, { recursive: true, mode: 0o700 });
  const exists = await Deno.stat(privateKeyPath).then(() => true, () => false);
  if (!exists) {
    const out = await new Deno.Command("ssh-keygen", {
      args: ["-q", "-t", "ed25519", "-N", "", "-C", "socialweb-debug-node-ssh", "-f", privateKeyPath],
    }).output();
    if (!out.success) throw new Error(`ssh-keygen failed: ${new TextDecoder().decode(out.stderr)}`);
  }
  const authorizedKey = (await Deno.readTextFile(`${privateKeyPath}.pub`)).trim();
  logger.warn("debug_node_ssh_enabled", { privateKeyPath, authorizedKey });
  return {
    authorizedKey,
    onNetwork(claim: string, address: string) {
      const ticket = address.replace(/^iroh:\/\//, "");
      if (ticket === address) return;
      logger.warn("debug_node_ssh", {
        claim,
        privateKeyPath,
        ssh: `ssh -i ${privateKeyPath} -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ProxyCommand='dumbpipe connect ${ticket}' root@${claim}`,
      });
    },
  };
}

const debugSsh = options.debugNodeSshDir ? await debugNodeSsh(options.debugNodeSshDir as string) : null;

const scopeCache = createScopeCache();
const scopeRefresher = createScopeRefresher({ scopeCache });

let fulfiller: MarketNodeFulfiller | null = null;
if (options.kcpKubeconfig) {
  const credentials = parseKubeconfig(await Deno.readTextFile(options.kcpKubeconfig as string), options.kcpContext as string | undefined);
  fulfiller = createMarketNodeFulfiller({
    store: createKcpNodeClaimStore({ credentials }),
    links: createKcpClusterLinkReader({ credentials }),
    requesterFor: (did) => accounts.requesterFor(did),
    plcUrl: options.plcDirectoryUrl as string,
    ingressProxyHost: options.ingressProxyHost as string,
    relayUrls,
    vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
    extraBidderDids: ((options.nodeExtraBidderDids as string | undefined) ?? "").split(",").map((d) => d.trim()).filter(Boolean),
    guestHostAliases: testNodeGuestHostAliases,
    userDataBase: testNodeUserDataBase,
    ...(options.nodePolicy ? { policy: { name: options.nodePolicy as string, args: JSON.parse(options.nodePolicyArgs as string) } } : {}),
    pollIntervalMs: options.nodePollIntervalMs as number,
    ...(meshd ? { meshd } : {}),
    ...(debugSsh ? { debugSsh } : {}),
    scopeCache,
    scopeRefresher,
    log,
  });
}

await serve.beginServe();
fulfiller?.start();
log("ready", {
  httpPort: options.httpPort,
  webDir,
  publicOrigin: publicOrigin ?? null,
  credentialHelpers: helpers.length,
  pinnedDownloads: pinned.list(),
  meshd: meshd ?? null,
  fulfilsNodes: fulfiller !== null,
});
if (fulfiller && !meshd) {
  logger.warn("meshd_not_offered", {
    downloadsDir,
    publicOrigin: publicOrigin ?? null,
    meaning: "market nodes join with the bridge CNI and per-node DNS but no pod mesh: pods on different nodes cannot reach each other",
    needs: "socialweb-meshd-x86_64 and socialweb-meshd-aarch64 with their .sha256 sidecars in the downloads dir, and public-origin set",
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
