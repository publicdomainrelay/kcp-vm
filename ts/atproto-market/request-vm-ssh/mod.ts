import { Command } from "@publicdomainrelay/cli-args-env";
import { parsePolicyArgs } from "@publicdomainrelay/policy-engine-cli-options";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  applyOAuthAgentToRequesterPDS,
  createRequesterPDS,
  createOAuthRequester,
  runComputeContract,
  createSshSessionProvider,
  ensureDumbpipe,
} from "@publicdomainrelay/requester-xrpc";
import type { OAuthRequesterHandle } from "@publicdomainrelay/requester-xrpc";
import { removedTransportFlagsIn } from "@publicdomainrelay/cloud-init-common";
import { createDelegatingAgent } from "@publicdomainrelay/atproto-helpers";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import { EVENT_NSID, OFFERING_NSID } from "@publicdomainrelay/market-common";
import { REQUESTER_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { parseSecretsFile } from "@publicdomainrelay/secrets-common";
import { createSecretsCapability } from "@publicdomainrelay/guest-capability-secrets";
import type { GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import { Secp256k1Keypair } from "@atproto/crypto";
import { explicitRelayUrls, marketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { callbackRef, cliIdentityMode, cliMarketIdentity, vaultRefs } from "@publicdomainrelay/market-identity-abc";
import type { BrowserOAuthClient, IdentityPrompt, IdentityVault, MarketIdentity, MarketIdentityMode } from "@publicdomainrelay/market-identity-abc";
import { requesterAssociatingRepo, startMarketIdentityReconciler, vaultOAuthSessionStore } from "@publicdomainrelay/market-identity-atproto";
import type { MarketIdentityHandle } from "@publicdomainrelay/market-identity-atproto";
import { createMemoryIdentityVault } from "@publicdomainrelay/market-identity-memory";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import { createTtyIdentityPrompt } from "@publicdomainrelay/identity-prompt-tty";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import { createFileIdentityVault, createSessionFileIdentityVault } from "@publicdomainrelay/market-identity-vault-file";
import cliArgsEnv from "./cli-args-env.ts";

/** Structural policy spec; matches @publicdomainrelay/policy-engine PolicySpec. */
interface PolicySpec {
  name: string;
  description?: string;
  args: Record<string, unknown>;
}

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_REQUEST_VM_SSH", cliArgsEnv, runtimeConfig).resolve();

const removedFlags = removedTransportFlagsIn(options, ["user-data-transport", "fedproxy-host"]);
if (removedFlags.length > 0) {
  for (const err of removedFlags) console.error(err.message);
  Deno.exit(2);
}

const label = (options.label as string) ?? "request-vm-ssh";
const logger = createLogger({ serviceName: label });

const network = marketNetwork({
  plcDirectoryUrl: options.plcDirectoryUrl as string | undefined,
  ingressProxyHost: options.ingressProxyHost as string | undefined,
  relayUrls: options.relayUrl as string | undefined,
  firehoseUrls: options.firehoseUrl as string | undefined,
  qrOrigin: options.qrOrigin as string | undefined,
});
const ingressProxyHost = network.ingressProxyHost;
installMarketNetworkFetch({ network });

const firehoseMode = (options.firehoseMode as string) || "off";
const eventStreams = createDefaultATProtoEventStreamsClient({
  onlyRelays: explicitRelayUrls(network, firehoseMode),
  log: logger,
});
const relayUrls = eventStreams.relays.map((r) => r.url);

const splitDids = (s: string | undefined): string[] =>
  s ? s.split(",").map((d) => d.trim()).filter(Boolean) : [];
const extraBidderDids = splitDids(options.bidderDids as string | undefined);
const denyBidderDids = splitDids(options.denyBidderDids as string | undefined);

const userDataPath = options.userData as string | undefined;
let baseUserData: string | undefined;
if (userDataPath) {
  baseUserData = await Deno.readTextFile(userDataPath);
  logger.info("user_data_loaded", { userDataPath, bytes: baseUserData.length });
}
const rbac = !(options.skipRbac as boolean);

const capabilities: GuestCapability[] = [];
const secretsPath = options.secrets as string | undefined;
if (secretsPath) {
  const secrets = parseSecretsFile(await Deno.readTextFile(secretsPath));
  capabilities.push(createSecretsCapability({ secrets, logger }));
  logger.info("secrets_capability_enabled", {
    secretsPath,
    count: secrets.length,
    paths: secrets.map((s) => s.path),
  });
}

const dumbpipePath = await ensureDumbpipe(logger);
logger.info("requester_starting", {
  label,
  ingressProxyHost,
  relayUrls,
  transport: "iroh",
});

const serve = createServe({
  logger,
  tcp: (options.port != null) ? { addr: (options.serveAddr as string) || "127.0.0.1", port: options.port as number, certFile: options.tlsCertFile as string | undefined, keyFile: options.tlsKeyFile as string | undefined } : undefined,
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  portFile: options.portFile as string | undefined,
});

// Resolve privateKeyHex from --private-key-hex-path if --private-key-hex not set.
const privateKeyHexPath = options.privateKeyHexPath as string | undefined;
let resolvedPrivateKeyHex = options.privateKeyHex as string | undefined;
if (privateKeyHexPath && !resolvedPrivateKeyHex) {
  try {
    const content = await Deno.readTextFile(privateKeyHexPath).then((s) => s.trim());
    if (content) {
      resolvedPrivateKeyHex = content;
      logger.info("private_key_loaded_from_path", { path: privateKeyHexPath });
    }
  } catch { /* file missing -- will generate and save below */ }
}

const identityName = options.marketIdentityName as string;
const identityVaultMount = options.identityVaultMount as string;
const stateDir = options.stateDir as string | undefined;
const AUTO_REFRESH_THRESHOLD_MS = 3_600_000;

function cliKcpIdentityPrompt(): IdentityPrompt | undefined {
  if (!options.marketIdentityKcpServer) return undefined;
  const workspace = options.marketIdentityKcpWorkspace as string | undefined;
  if (!workspace) throw new Error("--market-identity-kcp-server needs --market-identity-kcp-workspace");
  const caFile = options.marketIdentityKcpCaCertFile as string | undefined;
  return createKcpIdentityPrompt({
    server: options.marketIdentityKcpServer as string,
    workspace,
    tokenFile: options.marketIdentityKcpTokenFile as string | undefined,
    caCert: caFile ? Deno.readTextFileSync(caFile) : undefined,
  });
}

function loopbackPort(redirectUri: string | undefined): number {
  const m = (redirectUri ?? "").match(/^https?:\/\/[^/]+:(\d+)/);
  return m ? parseInt(m[1]) : 0;
}

const kcpIdentityPrompt = cliKcpIdentityPrompt();
const cliMode: MarketIdentityMode | undefined = kcpIdentityPrompt ? undefined : cliIdentityMode({
  mode: options.atprotoIdentityMode as string | undefined,
  oauth: options.atprotoOauth as boolean,
  oauthQr: options.atprotoOauthQr as boolean,
  handle: options.atprotoHandle as string | undefined,
});
logger.info("requester_identity_mode", { mode: cliMode ?? "kcp", name: identityName });

let identityVault: IdentityVault = createMemoryIdentityVault();
let identityRefs = vaultRefs(identityName, { mode: cliMode ?? "localRepo" }, identityVaultMount);
const ttyIdentityPrompt = kcpIdentityPrompt ? undefined : createTtyIdentityPrompt({
  identity: cliMarketIdentity(identityName, {
    mode: cliMode!,
    handle: options.atprotoHandle as string | undefined,
    association: options.skipQr ? "Skip" : "RequireApproval",
  }),
  logger,
  callbackPort: loopbackPort(options.oauthRedirectUri as string | undefined),
  deliverCallback: (params) => identityVault.write(callbackRef(identityRefs.session), params),
});
const identityPrompt: IdentityPrompt = kcpIdentityPrompt ?? ttyIdentityPrompt!;

async function identityObject(): Promise<MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> }> {
  let waiting = false;
  while (true) {
    const obj = await identityPrompt.get(identityName).catch((err) => {
      logger.warn("requester_identity_read_failed", { name: identityName, error: String(err) });
      return null;
    });
    if (obj?.spec) return obj as MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> };
    if (!waiting) logger.info("requester_identity_waiting", { name: identityName, hint: "no MarketIdentity object with a spec yet" });
    waiting = true;
    await new Promise((r) => setTimeout(r, 1_000));
  }
}

const identitySpec = (await identityObject()).spec;
const identityMode: MarketIdentityMode = identitySpec.mode;
identityRefs = vaultRefs(identityName, identitySpec, identityVaultMount);

function baseIdentityVault(): IdentityVault {
  if (options.identityVaultAddress) {
    const caFile = options.identityVaultCaCertFile as string | undefined;
    return createOpenBaoIdentityVault({
      address: options.identityVaultAddress as string,
      tokenFile: options.identityVaultTokenFile as string | undefined,
      caCert: caFile ? Deno.readTextFileSync(caFile) : undefined,
      namespace: options.identityVaultNamespace as string | undefined,
    });
  }
  if (options.identityVaultFile) return createFileIdentityVault({ path: options.identityVaultFile as string });
  if (stateDir) return createFileIdentityVault({ path: `${stateDir}/identity-vault.json` });
  logger.warn("requester_identity_store_in_memory", { hint: "pass --state-dir to keep the sign-in across runs" });
  return createMemoryIdentityVault();
}

const baseVault = baseIdentityVault();
identityVault = options.oauthSessionFile
  ? createSessionFileIdentityVault({ base: baseVault, ref: identityRefs.session, path: options.oauthSessionFile as string })
  : baseVault;

async function identityKeyHex(): Promise<string> {
  const generated = async () => Array.from(await (await Secp256k1Keypair.create({ exportable: true })).export()).map((b) => b.toString(16).padStart(2, "0")).join("");
  if (identityMode !== "localRepo" && identityMode !== "oauthQr") return resolvedPrivateKeyHex ?? await generated();
  const existing = await identityVault.read(identityRefs.keys);
  const held = typeof existing?.repoKeyHex === "string" ? existing.repoKeyHex : undefined;
  if (held) {
    if (resolvedPrivateKeyHex && resolvedPrivateKeyHex !== held) logger.warn("requester_identity_key_from_vault", { hint: "the identity vault's key wins over --private-key-hex" });
    return held;
  }
  const hex = resolvedPrivateKeyHex ?? await generated();
  await identityVault.write(identityRefs.keys, { ...(existing ?? {}), repoKeyHex: hex });
  return hex;
}

const privateKeyHex = await identityKeyHex();

function requesterPDS(keyHex: string) {
  return createRequesterPDS({
    logger,
    serve,
    privateKeyHex: keyHex,
    plcDirectoryUrl: network.plcDirectoryUrl,
    ingressProxyHost,
    label,
    storagePath: options.pdsStatePath as string | undefined,
    skipIngress: !!options.noIngressProxy,
  });
}

let localPds: Awaited<ReturnType<typeof createRequesterPDS>> | undefined;
let browserRequester: OAuthRequesterHandle | undefined;

async function openBrowserClient(): Promise<BrowserOAuthClient> {
  const handle = identitySpec.handle ?? options.atprotoHandle as string | undefined;
  if (!handle) throw new Error("mode oauthBrowser needs a handle: --atproto-handle or spec.handle");
  const sessionPath = options.oauthSessionPath as string | undefined;
  const { loadOrGenerateKeypair } = await import("@publicdomainrelay/market-atproto");
  const requester = await createOAuthRequester({
    handle,
    sessionPath,
    sessionStore: sessionPath ? undefined : vaultOAuthSessionStore(identityVault, identityRefs.session),
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: REQUESTER_OAUTH_SCOPE.join(" "),
    plcDirectoryUrl: network.plcDirectoryUrl,
    logger,
    attestationKp: await loadOrGenerateKeypair(privateKeyHex),
    privateKeyHex,
  });
  browserRequester = requester;
  return {
    restore: () => requester.restore(),
    startFlow: () => requester.startFlow(),
    completeFlow: (params) => requester.completeFlow(params),
    agent: { get did() { return requester.pds.did; } },
  };
}

const identityHandle: MarketIdentityHandle = startMarketIdentityReconciler({
  name: identityName,
  prompt: identityPrompt,
  vault: identityVault,
  logger,
  plcDirectoryUrl: network.plcDirectoryUrl,
  qrOrigin: network.qrOrigin,
  vaultMount: identityVaultMount,
  autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
  oauthClientId: options.oauthSessionClientId as string | undefined,
  localRepo: {
    qrParam: "plc",
    open: async (repoKeyHex: string) => {
      localPds = await requesterPDS(repoKeyHex);
      await localPds.beginServe();
      return requesterAssociatingRepo(localPds);
    },
  },
  oauthBrowser: { open: openBrowserClient },
});

async function signedIn() {
  if (kcpIdentityPrompt) return await identityHandle.ready();
  return await Promise.race([
    identityHandle.ready(),
    new Promise<never>((_, reject) => {
      const poll = setInterval(async () => {
        const obj = await identityPrompt.get(identityName);
        if (obj?.status?.phase === "Failed") {
          clearInterval(poll);
          reject(new Error(`sign-in failed: ${obj.status.message ?? "unknown"}`));
        }
      }, 200);
      identityHandle.ready().finally(() => clearInterval(poll));
    }),
  ]);
}

let ready: Awaited<ReturnType<typeof signedIn>>;
try {
  ready = await signedIn();
} catch (err) {
  logger.error("requester_sign_in_failed", { mode: identityMode, error: err instanceof Error ? err.message : String(err) });
  await identityHandle.stop().catch(() => {});
  ttyIdentityPrompt?.stop();
  Deno.exit(1);
}

let pds: RequesterPDS;
const isOAuth = !localPds;
if (localPds) {
  pds = localPds;
} else if (browserRequester) {
  pds = browserRequester.pds;
} else {
  pds = await requesterPDS(privateKeyHex);
  const did = ready.did;
  const userAgent = Object.assign(createDelegatingAgent(did, () => identityHandle.agent()), { sessionData: { userDid: did } });
  applyOAuthAgentToRequesterPDS(pds, userAgent, { log: (event, data) => logger.info(event, data) });
}
logger.info("requester_identity_ready", { did: ready.did, handle: ready.handle, mode: identityMode, localRepo: !!localPds });

if (privateKeyHexPath && (identityMode === "localRepo" || identityMode === "oauthQr")) {
  try {
    await Deno.writeTextFile(privateKeyHexPath, privateKeyHex);
    if (!resolvedPrivateKeyHex) logger.info("private_key_generated_and_saved", { path: privateKeyHexPath });
  } catch (err) {
    logger.warn("private_key_save_failed", { path: privateKeyHexPath, error: String(err) });
  }
}

const offeringDids = new Set<string>();
const offeringWatcher = eventStreams.watch({
  wantedCollections: [OFFERING_NSID],
  onEvent: (e) => { if (e.operation !== "delete") offeringDids.add(e.did); },
  log: logger,
});
logger.info("offering_firehose_watch_started", { relayCount: eventStreams.relays.length, jetstreamCount: eventStreams.jetstreams.length });

// Watch own PDS for compute events (vm.onNetwork, vm.registerIdentity)
const ownEventWatcher = eventStreams.watch({
  wantedCollections: [EVENT_NSID],
  onEvent: (e) => {
    if (e.operation === "delete") return;
    logger.info("event_watcher", { collection: e.collection, did: e.did, rkey: e.rkey });
  },
  log: logger,
});

// OAuth mode: the requester's market identity is the OAuth session user (every
// market record is written as that user), NOT the ephemeral PDS keypair DID.
const marketDid = (pds as unknown as { oauthSession?: { userDid?: string } }).oauthSession?.userDid ??
  pds.did;
logger.info("requester_ready", { did: marketDid, ingressRef: isOAuth ? "(oauth)" : pds.ingressRef, ingressProxyHost });

const origLog = console.log.bind(console);
const origErr = console.error.bind(console);
const origStderrWrite = Deno.stderr.write.bind(Deno.stderr);
const origStdoutWrite = Deno.stdout.write.bind(Deno.stdout);
const buf: Array<Uint8Array> = [];

function pauseConsole(): void {
  const capture = (...args: unknown[]) =>
    buf.push(new TextEncoder().encode(args.map((a) => typeof a === "string" ? a : JSON.stringify(a)).join(" ") + "\n"));
  console.log = capture;
  console.error = capture;
  Deno.stderr.write = (data: Uint8Array) => { buf.push(data); return Promise.resolve(data.length); };
  Deno.stdout.write = (data: Uint8Array) => { buf.push(data); return Promise.resolve(data.length); };
}

async function resumeConsole(): Promise<void> {
  console.log = origLog;
  console.error = origErr;
  Deno.stderr.write = origStderrWrite;
  Deno.stdout.write = origStdoutWrite;
  for (const chunk of buf) await origStderrWrite(chunk);
  buf.length = 0;
}

function shutdown(): void {
  eventStreams.close();
  serve.shutdown();
  ttyIdentityPrompt?.stop();
  identityHandle.stop().finally(() => Deno.exit());
}

// Provisioning-only hold mode: the first SIGTERM/SIGINT releases the held VM
// (runComputeContract then submits vm.delete and returns); only a signal after
// that tears down without teardown. Without --hold the signal kills directly.
const holdMode = Boolean(options.hold);
const holdAbort = new AbortController();
function onSignal(): void {
  if (holdMode) {
    logger.info("vm_hold_signal", {});
    holdAbort.abort();
  } else {
    shutdown();
  }
}
Deno.addSignalListener("SIGINT", onSignal);
Deno.addSignalListener("SIGTERM", onSignal);

if (holdMode && options.skipSsh) {
  console.error("--hold requires SSH readiness (conflicts with --skip-ssh)");
  Deno.exit(2);
}

const policyName = options.policy as string | undefined;
let policy: PolicySpec | undefined;
if (policyName) {
  try {
    policy = { name: policyName, args: parsePolicyArgs(options.policyArgs) };
    const { createPolicyRegistry } = await import("@publicdomainrelay/policy-deno-typescript");
    const { resolvePolicyName } = await import("@publicdomainrelay/policy-engine-evaluator");
    resolvePolicyName(createPolicyRegistry(), policyName, "requester");
  } catch (err) {
    console.error(`invalid --policy: ${err}`);
    Deno.exit(2);
  }
}

const result = await runComputeContract(pds, {
  logger,
  ingressProxyHost,
  plcUrl: network.plcDirectoryUrl,
  guestHostAliases: ((options.guestHostAliases as string) || "")
    .split(",").map((s: string) => s.trim()).filter(Boolean),
  vmName: options.vmName as string | undefined,
  skipSsh: options.skipSsh as boolean,
  execProgram: options.exec as string,
  keepVm: options.keepVm as boolean,
  hold: holdMode,
  holdAbort: holdAbort.signal,
  sshAuthorizedKeys: ((options.sshAuthorizedKey ?? []) as (string | string[])[])
    .flatMap((v: string | string[]) => Array.isArray(v) ? v : v.split(","))
    .map((s: string) => s.trim()).filter(Boolean),
  vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
  extraBidderDids,
  denyBidderDids,
  relayUrls,
  baseUserData,
  rbac,
  capabilities,
  policy,
  policyEngine: options.policyEngine as string | undefined,
  onlyRemotePolicyExec: options.onlyRemotePolicyExec as boolean | undefined,
  allowUntrustedPolicyExec: options.allowUntrustedPolicyExec as boolean | undefined,
  offeringWatcherDids: () => [...offeringDids],
  eventStreams,
  sshProvider: createSshSessionProvider(logger, { dumbpipePath }),
  onSshStart: () => pauseConsole(),
  onSshEnd: () => resumeConsole(),
});

logger.info("result", result as unknown as Record<string, unknown>);
await pds.dispose();
shutdown();
