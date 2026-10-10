import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger, createStructuredLogger, getMinLogLevelFromEnv } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { EventBus } from "@publicdomainrelay/event-bus";
import { createStaticFilesApp, type StaticFileEvent } from "@publicdomainrelay/hono-factory-static-files-fs";
import { K8S_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { createAtprotoSessionVerifier } from "@publicdomainrelay/socialweb-computer-oauth-atproto";
import { createFileSessionStore } from "@publicdomainrelay/socialweb-computer-oauth-session-fs";
import { createAccountRequesters } from "@publicdomainrelay/socialweb-computer-requester-inproc";
import { createWebFactory } from "@publicdomainrelay/hono-factory-socialweb-computer-oauth";
import { createRequestProofVerifier } from "@publicdomainrelay/socialweb-computer-k8s-request-proof";
import { createXrpcClusterGateway } from "@publicdomainrelay/socialweb-computer-k8s-gateway-xrpc";
import { createClusterApiFactory, type CredentialHelper } from "@publicdomainrelay/hono-factory-socialweb-computer-k8s";
import { createKcpNodeClaimStore, parseKubeconfig } from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import { createMarketNodeFulfiller, type MarketNodeFulfiller } from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import { ensureDumbpipe } from "@publicdomainrelay/requester-xrpc";
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

const accounts = createAccountRequesters({
  sessionStore,
  requesterKeyPath: `${stateDir}/requester-key.hex`,
  plcDirectoryUrl: options.plcDirectoryUrl as string,
  ingressProxyHost: options.ingressProxyHost as string,
  oauthClientId: options.oauthClientId as string | undefined,
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

async function credentialHelpers(): Promise<CredentialHelper[]> {
  const subdir = (options.downloadsSubdir as string).replace(/^\/+|\/+$/g, "");
  const out: CredentialHelper[] = [];
  try {
    for await (const entry of Deno.readDir(`${webDir}/${subdir}`)) {
      const match = /^socialweb-credential-([a-z0-9]+)-([a-z0-9_]+)$/.exec(entry.name);
      if (entry.isFile && match) out.push({ os: match[1], arch: match[2], path: `/${subdir}/${entry.name}` });
    }
  } catch (err) {
    if (!(err instanceof Deno.errors.NotFound)) throw err;
  }
  return out.sort((a, b) => a.path.localeCompare(b.path));
}

const helpers = await credentialHelpers();

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
serve.app.route("/", staticApp as never);

const relayUrls = ((options.relayUrl as string | undefined) ?? "").split(",").map((u) => u.trim()).filter(Boolean);

let fulfiller: MarketNodeFulfiller | null = null;
if (options.kcpKubeconfig) {
  const credentials = parseKubeconfig(await Deno.readTextFile(options.kcpKubeconfig as string), options.kcpContext as string | undefined);
  // The guests this door rents are reached over iroh, which means this process
  // runs `dumbpipe connect <ticket>` to open each session. A dumbpipe the
  // operator put on PATH is used as it is; otherwise ensureDumbpipe fetches the
  // same pinned release the guests install, so both ends speak one dumbpipe.
  const dumbpipePath = await ensureDumbpipe(logger);
  fulfiller = createMarketNodeFulfiller({
    dumbpipePath,
    store: createKcpNodeClaimStore({ credentials }),
    requesterFor: (did) => accounts.requesterFor(did),
    apiserverHost: options.nodeApiserverHost as string,
    plcUrl: options.plcDirectoryUrl as string,
    ingressProxyHost: options.ingressProxyHost as string,
    relayUrls,
    vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
    extraBidderDids: ((options.nodeExtraBidderDids as string | undefined) ?? "").split(",").map((d) => d.trim()).filter(Boolean),
    ...(options.nodePolicy ? { policy: { name: options.nodePolicy as string, args: JSON.parse(options.nodePolicyArgs as string) } } : {}),
    pollIntervalMs: options.nodePollIntervalMs as number,
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
  fulfilsNodes: fulfiller !== null,
});

async function shutdown(): Promise<void> {
  await fulfiller?.stop();
  await accounts.shutdown();
  serve.shutdown();
  Deno.exit(0);
}

Deno.addSignalListener("SIGINT", () => void shutdown());
Deno.addSignalListener("SIGTERM", () => void shutdown());
