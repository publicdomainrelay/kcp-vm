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
  createKcpFetch,
  createKcpGatewayListenerUrl,
  createKcpMarketIdentities,
  createKcpNodeClaimStore,
  type KcpCredentials,
  parseKubeconfig,
} from "@publicdomainrelay/socialweb-computer-k8s-nodeclaim-kcp";
import {
  createMarketNodeFulfiller,
  type MarketNodeFulfiller,
  meshdPinOf,
  type DebugNodeHostKeys,
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

const kcpCredentials: KcpCredentials | null = options.kcpKubeconfig
  ? parseKubeconfig(await Deno.readTextFile(options.kcpKubeconfig as string), options.kcpContext as string | undefined)
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
  if (!kcpCredentials) throw new Error("openbao-server needs kcp-kubeconfig to read the OpenBaoServer object");
  const resolve = createOpenBaoServerResolver({
    server: kcpCredentials.server,
    workspace: options.openbaoServerWorkspace as string,
    name: options.openbaoServer as string,
    client: options.openbaoClient as string,
    fetch: createKcpFetch(kcpCredentials),
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
    ? createKcpMarketIdentities({ credentials: kcpCredentials, workspace: options.marketIdentityWorkspace as string })
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
  if (!kcpCredentials) throw new Error("set gateway-url, or kcp-kubeconfig to read the cluster gateway's listener from its Gateway object");
  return createKcpGatewayListenerUrl({
    credentials: kcpCredentials,
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
  const tickets = new Map<string, string>();
  const knownHosts = new Map<string, { alias: string; file: string }>();
  const announce = (claim: string) => {
    const ticket = tickets.get(claim);
    const known = knownHosts.get(claim);
    if (!ticket || !known) return;
    logger.warn("debug_node_ssh", {
      claim,
      privateKeyPath,
      knownHostsFile: known.file,
      ssh: `ssh -i ${privateKeyPath} -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=${known.file} -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias=${known.alias} -o ProxyCommand='dumbpipe connect ${ticket}' root@${known.alias}`,
    });
  };
  return {
    authorizedKey,
    onNetwork(claim: string, address: string) {
      const ticket = address.replace(/^iroh:\/\//, "");
      if (ticket === address) return;
      tickets.set(claim, ticket);
      announce(claim);
    },
    onHostKeys(claim: string, hostKeys: DebugNodeHostKeys) {
      const file = `${dir}/known_hosts.${hostKeys.alias}`;
      const lines = hostKeys.keys.map((k) => `${hostKeys.alias} ${k.type} ${k.publicKey}\n`).join("");
      Deno.writeTextFile(file, lines, { mode: 0o600 }).then(() => {
        knownHosts.set(claim, { alias: hostKeys.alias, file });
        announce(claim);
      }).catch((err) => logger.warn("debug_node_ssh_known_hosts_failed", { claim, file, error: String(err) }));
    },
  };
}

const debugSsh = options.debugNodeSshDir ? await debugNodeSsh(options.debugNodeSshDir as string) : null;

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
const clustersExportUrl = options.kcpClustersExportUrl as string | undefined;
if (!claimKeys.durable && kcpCredentials && clustersExportUrl) {
  logger.warn("claim_keys_in_memory", {
    next: "a restarted door cannot re-serve an in-flight claim's secrets URL, so its Bidding and " +
      "Provisioning claims are marked Failed and released on deletion; set openbao-addr to resume them",
  });
}

let fulfiller: MarketNodeFulfiller | null = null;
if (kcpCredentials && clustersExportUrl) {
  fulfiller = createMarketNodeFulfiller({
    store: createKcpNodeClaimStore({ credentials: kcpCredentials, virtualWorkspace: clustersExportUrl }),
    links: createKcpClusterLinkReader({ credentials: kcpCredentials, virtualWorkspace: clustersExportUrl }),
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
    claimKeys,
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
