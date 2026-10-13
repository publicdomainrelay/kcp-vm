import { Command } from "@publicdomainrelay/cli-args-env";
import { parsePolicyArgs } from "@publicdomainrelay/policy-engine-cli-options";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { applyOAuthAgentToRequesterPDS, createOAuthRequester, createRequesterPDS } from "@publicdomainrelay/requester-xrpc";
import type { OAuthRequesterHandle } from "@publicdomainrelay/requester-xrpc";
import { awaitComputeHostKeys, releaseCompute, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestHandle } from "@publicdomainrelay/compute-request-xrpc";
import type { ComputeRequestHandlers, ContractState } from "@publicdomainrelay/compute-request-abc";
import { createDelegatingAgent } from "@publicdomainrelay/atproto-helpers";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import { OFFERING_NSID } from "@publicdomainrelay/market-common";
import { REQUESTER_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { parseSecretsFile } from "@publicdomainrelay/secrets-common";
import { createSecretsCapability } from "@publicdomainrelay/guest-capability-secrets";
import type { GuestCapability } from "@publicdomainrelay/guest-capability-abc";
import type { UserDataPatch } from "@publicdomainrelay/cloud-init-common";
import { Secp256k1Keypair } from "@atproto/crypto";
import { explicitRelayUrls, marketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { vaultRefs } from "@publicdomainrelay/market-identity-abc";
import type { BrowserOAuthClient, IdentityVault, MarketIdentity, MarketIdentityMode, VaultRef } from "@publicdomainrelay/market-identity-abc";
import { requesterAssociatingRepo, startMarketIdentityReconciler, vaultOAuthSessionStore } from "@publicdomainrelay/market-identity-atproto";
import type { MarketIdentityHandle } from "@publicdomainrelay/market-identity-atproto";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import { createOpenBaoServerResolver } from "@publicdomainrelay/openbao-server-kcp";
import { createComputeContractKcpStore } from "@publicdomainrelay/compute-contract-kcp";
import type { ComputeContract, ComputeContractPhase, ComputeContractStatus, MarketLoopStatus, MarketRequester, NodeClaimContract } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { MARKET_REQUESTER_RESOURCE } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { ExecutionState, type KcpCondition } from "@publicdomainrelay/kcp-client-abc";
import { createCoalescedStatusWriter, createKcpRestClient, statusFieldWrite } from "@publicdomainrelay/kcp-client-rest";
import { MarketLoopTally, type MarketLoopView } from "@publicdomainrelay/market-abc";
import cliArgsEnv from "./cli-args-env.ts";

interface PolicySpec {
  name: string;
  description?: string;
  args: Record<string, unknown>;
}

let runtimeConfig: Record<string, unknown> | null = null;
try {
  runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default;
} catch {
  runtimeConfig = null;
}
const { options } = await new Command("CONFIG_PATH_REQUEST_VM_SSH", cliArgsEnv, runtimeConfig).resolve();
const o: Record<string, any> = options;

if (!o.statusFile) {
  console.error("request-vm-ssh runs only as the requester DenoPod of a MarketRequester application, in that application's own kcp: start a requester with `socialweb-computer run request`");
  Deno.exit(2);
}
const kcpBase = typeof o.kcpServer === "string" ? o.kcpServer.split("/clusters/")[0] : undefined;
o.marketIdentityKcpServer ||= kcpBase;
o.computeContractKcpServer ||= kcpBase;
o.identityVaultKcpServer ||= kcpBase;
const missing = [
  ["market-identity-kcp-server", o.marketIdentityKcpServer],
  ["market-identity-kcp-workspace", o.marketIdentityKcpWorkspace],
  ["compute-contract-kcp-server", o.computeContractKcpServer],
  ["identity-vault-openbao-server", o.identityVaultOpenbaoServer],
  ["identity-vault-kcp-server", o.identityVaultKcpServer],
].filter(([, v]) => !v).map(([n]) => `--${n}`);
if (missing.length > 0) {
  console.error(`request-vm-ssh: ${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} unset; the MarketRequester's profile renders them`);
  Deno.exit(2);
}

const label = o.label as string;
const logger = createLogger({ serviceName: label });
const readCa = (path: string | undefined): string | undefined => path ? Deno.readTextFileSync(path) : undefined;

const network = marketNetwork({
  plcDirectoryUrl: o.plcDirectoryUrl,
  ingressProxyHost: o.ingressProxyHost,
  relayUrls: o.relayUrl,
  firehoseUrls: o.firehoseUrl,
  qrOrigin: o.qrOrigin,
  hosts: o.networkHosts,
});
installMarketNetworkFetch({ network });

const contractKcp = createKcpRestClient({
  server: o.computeContractKcpServer,
  tokenFile: o.computeContractKcpTokenFile,
  caCert: readCa(o.computeContractKcpCaCertFile),
});

async function marketLoop(): Promise<MarketLoopTally | undefined> {
  const root = o.marketRootName as string | undefined;
  if (!root) return undefined;
  const target = { resource: MARKET_REQUESTER_RESOURCE, workspace: o.computeContractKcpWorkspace };
  const prior = await contractKcp.get(target, root).then(
    (obj) => (obj as MarketRequester | null)?.status?.market,
    (err: unknown) => {
      logger.warn("requester_market_status_read_failed", { root, error: String(err) });
      return undefined;
    },
  );
  const write = statusFieldWrite(contractKcp, target, root, "market");
  const writer = createCoalescedStatusWriter<null>({
    write: () => {
      const market: MarketLoopStatus = tally.view(new Date());
      return write(market);
    },
    onError: (err) => logger.warn("requester_market_status_failed", { root, error: String(err) }),
  });
  const tally = new MarketLoopTally(() => writer.set(null), prior as Partial<MarketLoopView> | undefined);
  return tally;
}
const loop = await marketLoop();

const firehoseMode = (o.firehoseMode as string) || "off";
const eventStreams = createDefaultATProtoEventStreamsClient({
  onlyRelays: explicitRelayUrls(network, firehoseMode),
  log: logger,
  cursorStore: loop
    ? {
      load: (source) => loop.cursorOf(source),
      save: (source, p) => loop.streamProgress(source, p.cursor, p.eventTime, new Date()),
    }
    : undefined,
});
const relayUrls = eventStreams.relays.map((r) => r.url);

const serve = createServe({
  logger,
  tcp: { addr: o.serveAddr, port: o.port ?? 0, certFile: o.tlsCertFile, keyFile: o.tlsKeyFile },
  portFile: o.portFile,
  listenersFile: o.listenersFile,
});
let healthDid = "";
serve.app.get("/xrpc/_health", (c) => c.json({ ok: healthDid !== "", did: healthDid }, healthDid ? 200 : 503));

const identityName = o.marketIdentityName as string;
const vaultMount = o.identityVaultMount as string;
const identityPrompt = createKcpIdentityPrompt({
  server: o.marketIdentityKcpServer,
  workspace: o.marketIdentityKcpWorkspace,
  tokenFile: o.marketIdentityKcpTokenFile,
  caCert: readCa(o.marketIdentityKcpCaCertFile),
});
const contracts = createComputeContractKcpStore({
  kcp: contractKcp,
  server: o.computeContractKcpServer,
  workspace: o.computeContractKcpWorkspace,
  tokenFile: o.computeContractKcpTokenFile,
  caCert: readCa(o.computeContractKcpCaCertFile),
  name: o.computeContractName,
});
const resolveVault = createOpenBaoServerResolver({
  server: o.identityVaultKcpServer,
  workspace: o.identityVaultKcpWorkspace,
  tokenFile: o.identityVaultKcpTokenFile,
  caCert: readCa(o.identityVaultKcpCaCertFile),
  name: o.identityVaultOpenbaoServer,
  client: o.identityVaultOpenbaoClient,
  clientDir: o.identityVaultOpenbaoClientDir,
  onWaiting: (reason) => logger.info("requester_vault_waiting", { openbaoserver: o.identityVaultOpenbaoServer, reason }),
});
const vault: IdentityVault = createOpenBaoIdentityVault({
  endpoint: async () => {
    const r = await resolveVault();
    if (r.kvMount !== vaultMount) throw new Error(`openbaoserver client ${r.name} has kv mount ${r.kvMount}, not --identity-vault-mount ${vaultMount}`);
    return { address: r.address, caCert: r.caCert, clientCert: r.clientCert, clientKey: r.clientKey, certLogin: r.certLogin };
  },
});

async function identityObject(): Promise<MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> }> {
  let waiting = false;
  for (;;) {
    const obj = await identityPrompt.get(identityName).catch((err) => {
      logger.warn("requester_identity_read_failed", { name: identityName, error: String(err) });
      return null;
    });
    if (obj?.spec) return obj as MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> };
    if (!waiting) logger.info("requester_identity_waiting", { name: identityName });
    waiting = true;
    await new Promise((r) => setTimeout(r, 1_000));
  }
}

const identitySpec = (await identityObject()).spec;
const identityMode: MarketIdentityMode = identitySpec.mode;
const identityRefs = vaultRefs(identityName, identitySpec, vaultMount);
logger.info("requester_identity_mode", { mode: identityMode, name: identityName });

async function heldKeyHex(ref: VaultRef, field: string): Promise<string> {
  const existing = await vault.read(ref);
  const held = typeof existing?.[field] === "string" ? existing[field] as string : undefined;
  if (held) return held;
  const kp = await Secp256k1Keypair.create({ exportable: true });
  const hex = Array.from(await kp.export()).map((b) => b.toString(16).padStart(2, "0")).join("");
  await vault.write(ref, { ...(existing ?? {}), [field]: hex });
  return hex;
}

const privateKeyHex = await heldKeyHex(identityRefs.keys, "repoKeyHex");

function requesterPDS(keyHex: string) {
  return createRequesterPDS({
    logger,
    serve,
    privateKeyHex: keyHex,
    plcDirectoryUrl: network.plcDirectoryUrl,
    ingressProxyHost: network.ingressProxyHost,
    label,
    storagePath: o.pdsStatePath,
    skipIngress: !!o.noIngressProxy,
  });
}

let localPds: Awaited<ReturnType<typeof createRequesterPDS>> | undefined;
let browserRequester: OAuthRequesterHandle | undefined;

async function openBrowserClient(): Promise<BrowserOAuthClient> {
  const handle = identitySpec.handle;
  if (!handle) throw new Error("mode oauthBrowser needs spec.identity.handle on the MarketRequester");
  const { loadOrGenerateKeypair } = await import("@publicdomainrelay/market-atproto");
  const requester = await createOAuthRequester({
    handle,
    sessionStore: vaultOAuthSessionStore(vault, identityRefs.session),
    clientId: o.oauthClientId,
    redirectUri: o.oauthRedirectUri,
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
  vault,
  logger,
  plcDirectoryUrl: network.plcDirectoryUrl,
  qrOrigin: network.qrOrigin,
  vaultMount,
  autoRefreshThresholdMs: 3_600_000,
  oauthClientId: o.oauthSessionClientId,
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

function shutdown(): void {
  eventStreams.close();
  serve.shutdown();
  identityHandle.stop().finally(() => Deno.exit());
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

const ready = await identityHandle.ready();

let pds: RequesterPDS;
if (localPds) {
  pds = localPds;
} else if (browserRequester) {
  pds = browserRequester.pds;
} else {
  pds = await requesterPDS(privateKeyHex);
  const userAgent = Object.assign(createDelegatingAgent(ready.did, () => identityHandle.agent()), { sessionData: { userDid: ready.did } });
  applyOAuthAgentToRequesterPDS(pds, userAgent, { log: (event, data) => logger.info(event, data) });
}
const marketDid = (pds as unknown as { oauthSession?: { userDid?: string } }).oauthSession?.userDid ?? pds.did;
healthDid = marketDid;
await serve.beginServe();
logger.info("requester_identity_ready", { did: ready.did, handle: ready.handle, mode: identityMode, localRepo: !!localPds });
await Deno.writeTextFile(`${o.statusFile}.tmp`, JSON.stringify({ did: marketDid, repoDid: pds.did, port: serve.tcpPort }) + "\n");
await Deno.rename(`${o.statusFile}.tmp`, o.statusFile);

const offeringDids = new Set<string>();
eventStreams.watch({
  wantedCollections: [OFFERING_NSID],
  onEvent: (e) => {
    if (e.operation !== "delete") offeringDids.add(e.did);
  },
  log: logger,
});

const contractObject: ComputeContract = await contracts.waitForSpec((reason) =>
  logger.info("requester_contract_waiting", { name: contracts.name, reason })
);
const spec = contractObject.spec!;
const generation = contractObject.metadata?.generation;
let state: ContractState | undefined = contractObject.status?.contract?.phase
  ? contractObject.status.contract as unknown as ContractState
  : undefined;
let secretsUrl = contractObject.status?.secretsUrl;
let secretsFetchedAt = contractObject.status?.secretsFetchedAt;
let phaseNow: ComputeContractPhase = contractObject.status?.phase ?? "Pending";
let messageNow = contractObject.status?.message ?? "";
const execution = new ExecutionState({
  phase: contractObject.status?.phase,
  message: contractObject.status?.message,
  conditions: contractObject.status?.conditions as KcpCondition[] | undefined,
  execution: contractObject.status?.execution ?? {},
});
execution.observe(generation);

function contractStatus(): ComputeContractStatus {
  const view = execution.view();
  return {
    ...view,
    phase: phaseNow,
    message: messageNow,
    ...(secretsUrl ? { secretsUrl } : {}),
    ...(secretsFetchedAt ? { secretsFetchedAt } : {}),
    ...(state ? { contract: state as unknown as NodeClaimContract } : {}),
  } as ComputeContractStatus;
}

const STATUS_WRITE_ATTEMPTS = 3;
let statusWriteError: unknown;
const statusWriter = createCoalescedStatusWriter<null>({
  write: async () => {
    for (let attempt = 1;; attempt++) {
      try {
        await contracts.writeStatus(contractStatus());
        statusWriteError = undefined;
        return;
      } catch (err) {
        if (attempt >= STATUS_WRITE_ATTEMPTS) throw err;
        logger.warn("requester_contract_status_retry", { attempt, error: String(err) });
        await new Promise((r) => setTimeout(r, 500));
      }
    }
  },
  onError: (err) => {
    statusWriteError = err;
    logger.warn("requester_contract_status_failed", { error: String(err) });
  },
});

function stepTo(step: string): void {
  if (execution.step(step, new Date())) logger.info("requester_contract_step", { step });
  statusWriter.set(null);
}

function failedStep(err: unknown): void {
  execution.error(err, new Date());
  statusWriter.set(null);
}

async function writeStatus(phase: ComputeContractPhase, message: string): Promise<void> {
  phaseNow = phase;
  messageNow = message;
  execution.setPhase(phase, message, new Date());
  statusWriter.set(null, true);
  await statusWriter.flush();
  if (statusWriteError) throw statusWriteError;
  logger.info("requester_contract_status", { phase, message, contractPhase: state?.phase, step: execution.view().execution.step });
}

const idle = (): Promise<never> => new Promise<never>(() => {});

if (state?.phase === "released" || contractObject.status?.phase === "Released") {
  await writeStatus("Released", "the contract was released before this start");
  await idle();
}
if (contractObject.status?.phase === "Failed" && !state?.receipt) {
  logger.warn("requester_contract_failed_before", { message: contractObject.status?.message });
  await idle();
}

stepTo("preparing");
const capabilities: GuestCapability[] = [];
if (spec.secretsRef) {
  stepTo("readingSecrets");
  const bundle = await vault.read(spec.secretsRef);
  const text = typeof bundle?.bundle === "string" ? bundle.bundle : undefined;
  if (!text) {
    await writeStatus("Failed", `the secrets bundle ${spec.secretsRef.mount}/${spec.secretsRef.path} is not in the vault`);
    await idle();
  }
  const secrets = parseSecretsFile(text!);
  const keyHex = await heldKeyHex({ mount: spec.secretsRef.mount, path: `contracts/${contracts.name}/secrets-key` }, "privateKeyHex");
  const capability = createSecretsCapability({ secrets, logger, privateKeyHex: keyHex });
  const prepare = capability.prepare!.bind(capability);
  capability.prepare = async (ctx) => {
    const prepared = await prepare(ctx);
    secretsUrl = (prepared.ctx as { secretsUrl?: string } | undefined)?.secretsUrl;
    return prepared;
  };
  capabilities.push(capability);
  logger.info("secrets_capability_enabled", { count: secrets.length, paths: secrets.map((s) => s.path) });
}

let policy: PolicySpec | undefined;
if (spec.policy) policy = { name: spec.policy, args: parsePolicyArgs(spec.policyArgs) };

const guestCaPem = o.guestCaFile ? (await Deno.readTextFile(o.guestCaFile)).trim() : "";
const guestNetwork: UserDataPatch = {
  ...(guestCaPem ? { ca_certs: { trusted: [guestCaPem] } } : {}),
  ...(o.guestCurlHome ? { bootcmd: [["systemctl", "set-environment", `CURL_HOME=${o.guestCurlHome}`]] } : {}),
};

const splitList = (s: string | undefined): string[] => s ? s.split(",").map((x) => x.trim()).filter(Boolean) : [];
const vmReadyTimeoutSec = (spec.vmReadyTimeoutSec ?? o.vmReadyTimeoutSec) as number;

const track = (phase: ComputeContractPhase, what: string) => async (_event: unknown, s: ContractState) => {
  state = s;
  if (s.vmAddress && s.hostKeys) await writeStatus(phaseNow, messageNow);
  else await writeStatus(phase, what);
  return "continue" as const;
};
const handlers: ComputeRequestHandlers = {
  onRfpSubmitted: (event, s) => {
    stepTo("collectingBids");
    return track("Requesting", "the RFP is submitted")(event, s);
  },
  onBid: () => {
    loop?.bid();
    execution.event(new Date());
    statusWriter.set(null);
    return "accept" as const;
  },
  onAccepting: () => {
    stepTo("accepting");
    return "continue" as const;
  },
  onAccepted: (event, s) => {
    loop?.win();
    stepTo("awaitingReceipt");
    return track("Requesting", "a bid is accepted")(event, s);
  },
  onReceipt: (event, s) => {
    stepTo("awaitingGuestOnNetwork");
    return track("Requesting", "the winner's receipt is in")(event, s);
  },
  onNetwork: async (event, s) => {
    stepTo("awaitingHostKeys");
    state = s;
    if (s.hostKeys) await writeStatus(phaseNow, messageNow);
    else await writeStatus("Requesting", `the guest reported onNetwork at ${event.address}; waiting for its host keys`);
    return "continue" as const;
  },
  onHostKeys: async (_event, s) => {
    if (s.vmAddress) stepTo("held");
    state = s;
    await writeStatus(state.vmAddress ? "Held" : "Requesting", "the guest reported its ssh host keys");
  },
  onTerminated: async (event, s) => {
    stepTo("terminated");
    loop?.contractsActive(0);
    state = s;
    await writeStatus("Released", `the bidder ended the contract: ${event.reason}`);
  },
  onSecretsFetched: async (event) => {
    execution.event(new Date());
    logger.info("requester_secrets_fetched", { ...event });
    secretsFetchedAt = new Date().toISOString();
    await writeStatus(phaseNow, messageNow);
    return "continue" as const;
  },
};

logger.info("requester_contract_start", { name: contracts.name, resume: state?.phase ?? null, vmName: spec.vmName });
const releaseEarly = new AbortController();
const flowDone = new AbortController();
void contracts.waitFor((c) => c.spec?.release === true, flowDone.signal).then((asked) => {
  if (asked) releaseEarly.abort();
});
let handle: ComputeRequestHandle;
stepTo(state?.phase ? "resuming" : "postingRfp");
try {
  handle = await requestCompute(pds, {
    logger,
    ingressProxyHost: network.ingressProxyHost,
    plcUrl: network.plcDirectoryUrl,
    guestHostAliases: splitList(o.guestHostAliases),
    vmName: spec.vmName,
    sshAuthorizedKeys: spec.sshAuthorizedKeys ?? [],
    vmReadyTimeoutSec,
    extraBidderDids: [...splitList(o.bidderDids), ...(spec.bidderDids ?? [])],
    denyBidderDids: spec.denyBidderDids ?? [],
    relayUrls,
    userData: { base: spec.userData, modules: [() => guestNetwork] },
    rbac: !o.skipRbac,
    capabilities,
    policy,
    policyEngine: o.policyEngine,
    onlyRemotePolicyExec: o.onlyRemotePolicyExec,
    allowUntrustedPolicyExec: o.allowUntrustedPolicyExec,
    offeringWatcherDids: () => [...offeringDids],
    eventStreams,
    resume: state,
    returnSignal: releaseEarly.signal,
    acceptCopies: o.acceptCopies,
  }, handlers);
  flowDone.abort();
} catch (err) {
  failedStep(err);
  await writeStatus("Failed", `the contract flow threw: ${err instanceof Error ? err.message : String(err)}`);
  await idle();
  throw err;
}
state = handle.state;

if (!state.receipt) {
  failedStep(state.outcome ?? state.error ?? state.phase);
  stepTo("noContract");
  await writeStatus("Failed", `no contract: ${state.outcome ?? state.error ?? state.phase}`);
  await handle.dispose();
  await idle();
}

if (state.vmAddress && !state.hostKeys) stepTo("awaitingHostKeys");
if (state.vmAddress && !state.hostKeys && handle.hostKeys) {
  const hostKeys = await Promise.race([
    handle.hostKeys,
    new Promise<null>((r) => setTimeout(() => r(null), vmReadyTimeoutSec * 1000)),
  ]);
  if (hostKeys) state = { ...state, hostKeys };
}
if (state.vmAddress && !state.hostKeys) {
  state = await awaitComputeHostKeys(pds, state, { logger, eventStreams, plcUrl: network.plcDirectoryUrl, timeoutMs: 120_000 });
}

const ended = new AbortController();
void handle.terminated?.then((s) => {
  state = s;
  ended.abort();
});

if (state.termination) {
  stepTo("terminated");
  await writeStatus("Released", `the bidder ended the contract: ${state.termination.reason}`);
} else {
  const held = state.vmAddress && state.hostKeys;
  stepTo(held ? "held" : "awaitingRelease");
  loop?.contractsActive(held ? 1 : 0);
  await writeStatus(
    held ? "Held" : "Failed",
    held
      ? `held: the guest is on the network at ${state.vmAddress}; set spec.release to release it`
      : `the guest did not report ${state.vmAddress ? "its ssh host keys" : "onNetwork"} (${state.outcome ?? state.error ?? state.phase}); set spec.release to release the contract`,
  );
  const releaseAsked = await contracts.waitFor((c) => c.spec?.release === true, ended.signal);
  if (releaseAsked && !(state as ContractState).termination) {
    stepTo("releasing");
    await writeStatus("Releasing", "submitting vm.delete to the winner");
    state = await releaseCompute(pds, state, { logger, capabilities: handle.capabilities });
    await handle.dispose();
    const ok = state.release?.ok === true;
    if (!ok) failedStep(state.error ?? `vm.delete status ${state.release?.status}`);
    stepTo(ok ? "released" : "releaseFailed");
    loop?.contractsActive(0);
    await writeStatus(
      ok ? "Released" : "Failed",
      ok ? `vm.delete accepted (${state.release?.status})` : `vm.delete failed: ${state.error ?? state.release?.status}`,
    );
  } else {
    stepTo("terminated");
    loop?.contractsActive(0);
    await releaseCompute(pds, state, { logger, capabilities: handle.capabilities });
    await handle.dispose();
    await writeStatus("Released", `the bidder ended the contract: ${(state as ContractState).termination?.reason ?? "unknown"}`);
  }
}
await idle();
