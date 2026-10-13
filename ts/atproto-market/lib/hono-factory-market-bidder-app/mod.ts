import { parsePolicyArgs } from "@publicdomainrelay/policy-engine-cli-options";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import type { IngressRef, ServeHandle } from "@publicdomainrelay/serve";
import { createIngress, ingressUrlOf } from "@publicdomainrelay/did-key-ingress-proxy";
import { generatedKeyHex, vaultGuestKv, vaultOidcJwkStore, vaultRelayKeypair } from "@publicdomainrelay/market-bidder-vault-keys";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidderProviderRef } from "@publicdomainrelay/market-bidder-abc";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createFsContractStore } from "@publicdomainrelay/market-bidder-contract-store-fs";
import { createKcpContractStore } from "@publicdomainrelay/market-bidder-contract-store-kcp";
import { createKcpBidLedger } from "@publicdomainrelay/market-bidder-bids-kcp";
import type { BidLedger } from "@publicdomainrelay/market-bidder-abc";
import type { KcpComputeVMClient } from "@publicdomainrelay/compute-provider-kcp";
import type { ComputeVMAccepts } from "@publicdomainrelay/compute-provider-kcp-abc";
import { createComputeProviderDenoWorker, createWorkerProviderHooks } from "@publicdomainrelay/market-bidder-worker";
import {
  createATProto,
  createDelegatingAgent,
  createLocalPDSAgent,
  createOAuthAgent,
  registerKeypairPlcDid,
} from "@publicdomainrelay/atproto-helpers";
import type { LocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { explicitRelayUrls, isLocalMarketNetwork, splitHostPort } from "@publicdomainrelay/market-network-common";
import type { MarketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { vaultRefs } from "@publicdomainrelay/market-identity-abc";
import type {
  BrowserOAuthClient,
  IdentityPrompt,
  IdentityVault,
  MarketIdentity,
  MarketIdentityMode,
} from "@publicdomainrelay/market-identity-abc";
import {
  bidderAssociatingRepo,
  startMarketIdentityReconciler,
  vaultOAuthSessionStore,
} from "@publicdomainrelay/market-identity-atproto";
import type { MarketIdentityHandle } from "@publicdomainrelay/market-identity-atproto";
import { createMemoryIdentityVault } from "@publicdomainrelay/market-identity-memory";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import { createOpenBaoServerResolver } from "@publicdomainrelay/openbao-server-kcp";
import type { ResolvedOpenBaoServer } from "@publicdomainrelay/openbao-server-kcp";
import type { GuestKv, JwkStore } from "@publicdomainrelay/oidc-issuer-abc";
import { createFileIdentityVault, createSessionFileIdentityVault } from "@publicdomainrelay/market-identity-vault-file";
import { oauthClientMetadata } from "@publicdomainrelay/atproto-oauth-helpers";
import { createBadgeBlueKeysRecord, loadOrGenerateKeypair } from "@publicdomainrelay/market-atproto";
import { OFFERING_NSID } from "@publicdomainrelay/market-common";
import { BIDDER_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { verifyRelayVisibility } from "@publicdomainrelay/relay-visibility-xrpc";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { MarketLoopTally } from "@publicdomainrelay/market-abc";
import { createCoalescedStatusWriter, createKcpRestClient, statusFieldWrite } from "@publicdomainrelay/kcp-client-rest";
import { MARKET_BIDDER_RESOURCE, type MarketLoopStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createFirecrackerComputeProvider } from "@publicdomainrelay/compute-provider-firecracker";
import { createComputeProviderKcpFactory } from "@publicdomainrelay/hono-factory-compute-provider-kcp";
import { createVmruntimeMicrovm } from "@publicdomainrelay/microvm-vmruntime";
import { createRbacProvisioner } from "@publicdomainrelay/rbac-atproto";
import { Secp256k1Keypair } from "@atproto/crypto";
import { loadOrCreateAttestationKeyHex } from "@publicdomainrelay/utils-attestation-key";

export type MarketBidderIdentityPromptSpec = { kind: "kcp"; server: string; workspace: string; tokenFile?: string; caCertFile?: string };

export const BIDDER_SIGN_IN_ONLY_THROUGH_KCP =
  "the bidder signs in only through its MarketIdentity object in its own kcp; start it with socialweb-computer run bidder";

export interface MarketBidderOpenBaoServerRef {
  name: string;
  client: string;
  clientDir?: string;
  kcp: { server: string; workspace?: string; token?: string; tokenFile?: string; caCertFile?: string };
}

export type MarketBidderIdentityVaultSpec =
  | { kind: "openbao"; address?: string; tokenFile?: string; caCertFile?: string; namespace?: string; server?: MarketBidderOpenBaoServerRef }
  | { kind: "file"; path: string };

export interface MarketBidderIdentitySpec {
  name: string;
  vaultMount: string;
  prompt: MarketBidderIdentityPromptSpec;
  vault?: MarketBidderIdentityVaultSpec;
  stateDir?: string;
  handle?: string;
  pdsUrl?: string;
  password?: string;
  privateKeyHex?: string;
  privateKeyHexPath?: string;
  associateWith?: string;
  oauth?: {
    clientId?: string;
    redirectUri?: string;
    sessionPath?: string;
    sessionFile?: string;
    sessionClientId?: string;
  };
}

export type MarketBidderRepoSpec = { kind: "inproc"; statePath?: string } | { kind: "url" };

export interface FirecrackerProviderSpec {
  kind: "firecracker";
  vmruntimeCommand: string[];
  workRoot: string;
}

export interface KcpProviderSpec {
  kind: "kcp";
  server: string;
  workspace: string;
  handle: string;
  tokenFile?: string;
  caFile?: string;
  guestHostAddress?: string;
  guestLoginMount?: string;
}

export type MarketBidderProviderSpec =
  | { kind: "digitalocean"; token: string; baseUrl?: string }
  | { kind: "local"; containerImage?: string; caCertPem?: string }
  | FirecrackerProviderSpec
  | KcpProviderSpec
  | { kind: "denoWorker"; permissionMode?: string };

export interface MarketBidderMarketSpec {
  serve: { addr?: string; port?: number; unix?: string; tlsCertFile?: string; tlsKeyFile?: string };
  ingressProxy: boolean;
  firehoseMode?: string;
  offeringRefreshSec?: number;
  bidWindowSec?: number;
  policy?: string;
  policyArgs?: unknown;
  onlyRemotePolicyExec?: boolean;
  allowUntrustedPolicyExec?: boolean;
  contractStoreDir?: string;
  ingressKeyDir?: string;
  root?: { name: string };
}

export interface MarketBidderAppSpec {
  network: MarketNetwork;
  identity: MarketBidderIdentitySpec;
  repo: MarketBidderRepoSpec;
  providers: MarketBidderProviderSpec[];
  market: MarketBidderMarketSpec;
  reports?: { portFile?: string; listenersFile?: string };
}

export interface MarketBidderAppPorts {
  logger: StructuredLoggerInterface;
  identityPrompt?: IdentityPrompt;
  identityVault?: IdentityVault;
}

export interface MarketBidderAppStatus {
  did: string;
  ingressRef?: string;
  servePort: number;
}

export interface MarketBidderApp {
  start(): Promise<MarketBidderAppStatus>;
  announce(): Promise<void>;
  shutdown(): Promise<void>;
  readonly serves: readonly ServeHandle[];
  readonly relays: ReadonlyMap<string, IngressRef>;
}

export class BidderPolicyError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BidderPolicyError";
  }
}

export const GUEST_DENOPOD_KCP = "guest-kcp";

const BIDDER_ASSOC_SERVICE = "bidder_associate";
const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
const AUTO_REFRESH_THRESHOLD_MS = 3_600_000;
const DEFAULT_BID_WINDOW_SEC = 300;

type GuestContractEntry = { receiptKey: string; receiptUri: string; receiptCid: string; submitEventUrl?: string };
type Atproto = Awaited<ReturnType<typeof createATProto>>;

function guestTlsPortOf(network: MarketNetwork): number | undefined {
  if (!network.guestTlsUrl) return undefined;
  const port = new URL(network.guestTlsUrl).port;
  return port ? parseInt(port) : undefined;
}

function isCrawlRoot(u: string): boolean {
  try {
    const url = new URL(u);
    return (url.protocol === "http:" || url.protocol === "https:") && (url.pathname === "/" || url.pathname === "");
  } catch {
    return false;
  }
}

const REQUIRED_PROVIDER_FIELDS = {
  firecracker: ["vmruntimeCommand", "workRoot"],
  kcp: ["server", "workspace", "handle"],
} as const;

export function missingProviderFields(p: MarketBidderProviderSpec): string[] {
  const required: readonly string[] = p.kind === "firecracker" || p.kind === "kcp"
    ? REQUIRED_PROVIDER_FIELDS[p.kind]
    : [];
  const values = p as unknown as Record<string, unknown>;
  return required.filter((k) => !values[k]);
}

export function marketBidderGuestTlsUrl(network: MarketNetwork, guestTlsPort: number | undefined): MarketNetwork {
  if (!guestTlsPort) return network;
  return { ...network, guestTlsUrl: `https://${splitHostPort(network.ingressProxyHost).hostname}:${guestTlsPort}` };
}

export async function resolveBidderPolicy(market: MarketBidderMarketSpec): Promise<Record<string, unknown>> {
  try {
    const policyArgs = parsePolicyArgs(market.policyArgs) as Record<string, unknown>;
    if (market.policy) {
      const { createPolicyRegistry } = await import("@publicdomainrelay/policy-deno-typescript");
      const { resolvePolicyName } = await import("@publicdomainrelay/policy-deno-typescript-shared");
      resolvePolicyName(createPolicyRegistry(), market.policy, "bidder");
    }
    return policyArgs;
  } catch (err) {
    throw new BidderPolicyError(`invalid --policy: ${err}`);
  }
}

export function createMarketBidderApp(spec: MarketBidderAppSpec, ports: MarketBidderAppPorts): MarketBidderApp {
  const { logger, network } = { logger: ports.logger, network: spec.network };
  const id = spec.identity;
  const listenersFile = spec.reports?.listenersFile;
  const guestTlsPort = guestTlsPortOf(network);
  const serves: ServeHandle[] = [];
  const relays = new Map<string, IngressRef>();
  const acceptToContract = new Map<string, GuestContractEntry>();
  const ensureImages: Array<() => Promise<void>> = [];
  const firecracker = spec.providers.find((p): p is FirecrackerProviderSpec => p.kind === "firecracker");
  const contractStoreDir = spec.market.contractStoreDir || (firecracker ? `${firecracker.workRoot}/contracts` : undefined);
  const ingressKeyDir = spec.market.ingressKeyDir || (contractStoreDir ? `${contractStoreDir}/ingress` : undefined);

  let identityHandle: MarketIdentityHandle | undefined;
  let bidder: MarketBidder | undefined;
  let atproto: Atproto | undefined;
  let keypair: Secp256k1Keypair | undefined;
  let ownPlcDid: string | undefined;
  let pdsHostname: string | undefined;
  let localPdsPort = 0;
  let bidderServe: ServeHandle | undefined;
  let crawlRelayUrls: string[] = [];
  let localRelayUrls: string[] = [];
  let kcpContracts: { client: KcpComputeVMClient; accepts: ComputeVMAccepts; handle: string } | undefined;
  let bidLedger: BidLedger | undefined;
  let vaultMount = id.vaultMount;
  let keyVault: IdentityVault | undefined;
  const keysOutOfFiles = id.vault?.kind === "openbao";
  const vaultServerRef = id.vault?.kind === "openbao" ? id.vault.server : undefined;
  const resolveVaultServer: (() => Promise<ResolvedOpenBaoServer>) | undefined = vaultServerRef
    ? (() => {
      const resolve = createOpenBaoServerResolver({
        server: vaultServerRef.kcp.server,
        workspace: vaultServerRef.kcp.workspace,
        tokenFile: vaultServerRef.kcp.tokenFile,
        token: vaultServerRef.kcp.token,
        caCert: vaultServerRef.kcp.caCertFile ? Deno.readTextFileSync(vaultServerRef.kcp.caCertFile) : undefined,
        name: vaultServerRef.name,
        client: vaultServerRef.client,
        clientDir: vaultServerRef.clientDir,
        onWaiting: (reason) => logger.info("bidder_vault_waiting", { openbaoserver: vaultServerRef.name, reason }),
      });
      return async () => {
        const r = await resolve();
        logger.info("bidder_vault_resolved", { openbaoserver: r.name, address: r.address, kvMount: r.kvMount, role: r.certLogin.role });
        return r;
      };
    })()
    : undefined;

  function marketLoop(): MarketLoopTally | undefined {
    const root = spec.market.root;
    if (!root || id.prompt?.kind !== "kcp" || !id.prompt.server) return undefined;
    const kcp = createKcpRestClient({ server: id.prompt.server, tokenFile: id.prompt.tokenFile, caFile: id.prompt.caCertFile });
    const write = statusFieldWrite(kcp, { resource: MARKET_BIDDER_RESOURCE, workspace: id.prompt.workspace }, root.name, "market");
    const writer = createCoalescedStatusWriter<null>({
      write: () => {
        const market: MarketLoopStatus = tally.view(new Date());
        return write(market);
      },
      onError: (err) => logger.warn("bidder_market_status_failed", { root: root.name, error: String(err) }),
    });
    const tally = new MarketLoopTally(() => writer.set(null));
    logger.info("bidder_market_status_root", { root: root.name, workspace: id.prompt.workspace });
    return tally;
  }

  function kcpPrompt(): IdentityPrompt {
    if (id.prompt?.kind !== "kcp" || !id.prompt.server) throw new Error(BIDDER_SIGN_IN_ONLY_THROUGH_KCP);
    return createKcpIdentityPrompt({
      server: id.prompt.server,
      workspace: id.prompt.workspace,
      tokenFile: id.prompt.tokenFile,
      caCert: id.prompt.caCertFile ? Deno.readTextFileSync(id.prompt.caCertFile) : undefined,
    });
  }

  async function identityObject(prompt: IdentityPrompt): Promise<MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> }> {
    let waiting = false;
    while (true) {
      const obj = await prompt.get(id.name).catch((err) => {
        logger.warn("bidder_identity_read_failed", { name: id.name, error: String(err) });
        return null;
      });
      if (obj?.spec) return obj as MarketIdentity & { spec: NonNullable<MarketIdentity["spec"]> };
      if (!waiting) logger.info("bidder_identity_waiting", { name: id.name, hint: "no MarketIdentity object with a spec yet" });
      waiting = true;
      await new Promise((r) => setTimeout(r, 1_000));
    }
  }

  function baseIdentityVault(mode: MarketIdentityMode): IdentityVault {
    if (ports.identityVault) return ports.identityVault;
    if (id.vault && id.password) logger.warn("atproto_password_ignored", { hint: "the identity vault holds the password" });
    if (id.vault?.kind === "openbao") {
      const resolve = resolveVaultServer;
      return createOpenBaoIdentityVault({
        address: id.vault.address,
        tokenFile: id.vault.tokenFile,
        caCert: id.vault.caCertFile ? Deno.readTextFileSync(id.vault.caCertFile) : undefined,
        namespace: id.vault.namespace,
        endpoint: resolve
          ? async () => {
            const r = await resolve();
            return { address: r.address, caCert: r.caCert, clientCert: r.clientCert, clientKey: r.clientKey, certLogin: r.certLogin };
          }
          : undefined,
      });
    }
    if (id.vault?.kind === "file") return createFileIdentityVault({ path: id.vault.path });
    if (mode === "pdsAccount" && id.password) return createMemoryIdentityVault();
    if (id.stateDir) return createFileIdentityVault({ path: `${id.stateDir}/identity-vault.json` });
    logger.warn("bidder_identity_store_in_memory", { hint: "pass --state-dir to keep the sign-in across restarts" });
    return createMemoryIdentityVault();
  }

  async function readPrivateKeyHex(): Promise<string | undefined> {
    if (id.privateKeyHex || !id.privateKeyHexPath || keysOutOfFiles) return id.privateKeyHex;
    try {
      const content = (await Deno.readTextFile(id.privateKeyHexPath)).trim();
      if (!content) return undefined;
      logger.info("private_key_loaded_from_path", { path: id.privateKeyHexPath });
      return content;
    } catch {
      return undefined;
    }
  }

  async function persistPrivateKeyHex(hex: string, given: string | undefined): Promise<void> {
    const path = id.privateKeyHexPath;
    if (!path || keysOutOfFiles) return;
    try {
      await Deno.mkdir(path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : ".", { recursive: true });
      await Deno.writeTextFile(path, hex);
      logger.info(given ? "private_key_rewritten" : "private_key_generated_and_saved", { path });
    } catch (err) {
      logger.warn("private_key_save_failed", { path, error: String(err) });
    }
  }

  async function ingressKeypair(role: string): Promise<Secp256k1Keypair> {
    if (keyVault) return await vaultRelayKeypair(keyVault, vaultMount, role, logger);
    if (!ingressKeyDir) return await Secp256k1Keypair.create({ exportable: true });
    await Deno.mkdir(ingressKeyDir, { recursive: true, mode: 0o700 });
    const path = `${ingressKeyDir}/${role}.jwk`;
    const kp = await Secp256k1Keypair.import(await loadOrCreateAttestationKeyHex(path), { exportable: true });
    logger.info("bidder_ingress_key", { role, path, did: kp.did() });
    return kp;
  }

  function oidcJwkStore(role: string): JwkStore | undefined {
    return keyVault ? vaultOidcJwkStore(keyVault, vaultMount, role, logger) : undefined;
  }

  function guestVault(): { kv: GuestKv; server: string; mount: string } {
    const vault = keyVault;
    if (!vault || !vaultServerRef) {
      throw new Error("the kcp provider keeps each guest's user data and provisioning record in the bidder's OpenBao; start the bidder with --identity-vault-openbao-server");
    }
    return { kv: vaultGuestKv(vault, vaultMount), server: vaultServerRef.name, mount: vaultMount };
  }

  async function openIngress(role: string): Promise<IngressRef> {
    const relayKeypair = await ingressKeypair(role);
    const bidderDid = ownPlcDid ?? atproto!.did;
    const signer = { did: () => bidderDid, sign: async (bytes: Uint8Array) => keypair!.sign(bytes) };
    const relay = createIngress({ logger, ingressProxyHost: network.ingressProxyHost, signer, keypair: relayKeypair });
    relays.set(role, relay);
    return relay;
  }

  async function openProviderServe(role: string): Promise<{ relay: IngressRef; serve: ServeHandle }> {
    const relay = await openIngress(role);
    const serve = createServe({ logger, relays: [relay], listenersFile });
    serves.push(serve);
    return { relay, serve };
  }

  async function signIn(): Promise<{ agent: unknown; attestationKp: Awaited<ReturnType<typeof loadOrGenerateKeypair>> }> {
    const prompt: IdentityPrompt = ports.identityPrompt ?? kcpPrompt();
    logger.info("bidder_identity_mode", { mode: "kcp", name: id.name });
    if (resolveVaultServer) {
      const r = await resolveVaultServer();
      if (r.kvMount !== vaultMount) logger.info("bidder_vault_mount", { option: vaultMount, openbaoserver: r.kvMount });
      vaultMount = r.kvMount;
    }

    const identitySpec = (await identityObject(prompt)).spec;
    const mode: MarketIdentityMode = identitySpec.mode;
    const refs = vaultRefs(id.name, identitySpec, vaultMount);

    const baseVault = baseIdentityVault(mode);
    if (mode === "pdsAccount" && id.password && !id.vault && !ports.identityVault) {
      logger.warn("atproto_password_from_options", { hint: "development only; production keeps the password in the identity vault" });
      await baseVault.write(refs.keys, { password: id.password });
    }
    if (keysOutOfFiles && (id.oauth?.sessionFile || id.oauth?.sessionPath || id.privateKeyHexPath)) {
      logger.warn("bidder_key_files_ignored", { hint: "with an OpenBao vault the repo key and the OAuth session live only in the vault" });
    }
    const identityVault: IdentityVault = id.oauth?.sessionFile && !keysOutOfFiles
      ? createSessionFileIdentityVault({ base: baseVault, ref: refs.session, path: id.oauth.sessionFile })
      : baseVault;
    if (id.vault || ports.identityVault) keyVault = baseVault;

    const given = await readPrivateKeyHex();
    let privateKeyHex: string;
    if (!keyVault && mode !== "localRepo" && mode !== "oauthQr") {
      privateKeyHex = given ?? await generatedKeyHex();
    } else {
      const existing = await identityVault.read(refs.keys);
      const held = typeof existing?.repoKeyHex === "string" ? existing.repoKeyHex : undefined;
      if (held && given && given !== held) logger.warn("bidder_identity_key_from_vault", { hint: "the identity vault's key wins over --private-key-hex" });
      privateKeyHex = held ?? given ?? await generatedKeyHex();
      if (!held) await identityVault.write(refs.keys, { ...(existing ?? {}), repoKeyHex: privateKeyHex });
    }
    keypair = await Secp256k1Keypair.import(privateKeyHex, { exportable: true });
    const attestationKp = await loadOrGenerateKeypair(privateKeyHex);

    let localRepoAgent: LocalPDSAgent | undefined;
    const repo = spec.repo;
    identityHandle = startMarketIdentityReconciler({
      name: id.name,
      prompt,
      vault: identityVault,
      logger,
      plcDirectoryUrl: network.plcDirectoryUrl,
      qrOrigin: network.qrOrigin,
      vaultMount,
      autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
      oauthClientId: id.oauth?.sessionClientId,
      localRepo: repo.kind === "inproc"
        ? {
          qrParam: "bdr",
          open: async (repoKeyHex: string) => {
            const pdsServe = createServe({ logger, listenersFile });
            localRepoAgent = await createLocalPDSAgent({
              logger,
              keypair: await Secp256k1Keypair.import(repoKeyHex, { exportable: true }),
              serve: pdsServe,
              plcDirectoryUrl: network.plcDirectoryUrl,
              ingressProxyHost: network.ingressProxyHost,
              storagePath: repo.statePath,
              associateServiceId: BIDDER_ASSOC_SERVICE,
            });
            await localRepoAgent.beginServe();
            localPdsPort = pdsServe.tcpPort;
            return bidderAssociatingRepo(localRepoAgent, BIDDER_ASSOC_SERVICE);
          },
        }
        : undefined,
      oauthBrowser: {
        open: async (): Promise<BrowserOAuthClient> => {
          const handle = identitySpec.handle ?? id.handle;
          if (!handle) throw new Error("mode oauthBrowser needs a handle: --atproto-handle or spec.handle");
          const sessionPath = keysOutOfFiles ? undefined : id.oauth?.sessionPath;
          const agent = await createOAuthAgent({
            handle,
            sessionPath,
            sessionStore: sessionPath ? undefined : vaultOAuthSessionStore(identityVault, refs.session) as never,
            clientId: id.oauth?.clientId,
            redirectUri: id.oauth?.redirectUri,
            scope: BIDDER_OAUTH_SCOPE.join(" "),
            plcDirectoryUrl: network.plcDirectoryUrl,
            logger,
          });
          return {
            restore: () => agent.restore(),
            startFlow: () => agent.startFlow(),
            completeFlow: (params) => agent.completeFlow(params),
            agent,
          };
        },
      },
    });

    const handle = identityHandle;
    const ready = await handle.ready();

    let agent: unknown;
    if (ready.repo && localRepoAgent) {
      agent = localRepoAgent;
      pdsHostname = localRepoAgent.relay?.ingressHost || undefined;
    } else {
      agent = createDelegatingAgent(ready.did, () => handle.agent());
      ownPlcDid = await registerKeypairPlcDid({ keypair, attestationDid: attestationKp.did(), plcDirectoryUrl: network.plcDirectoryUrl, logger });
      pdsHostname = ready.pdsUrl && mode === "pdsAccount" ? new URL(ready.pdsUrl).host : undefined;
    }
    logger.info("bidder_identity_ready", { did: ready.did, handle: ready.handle, pdsUrl: ready.pdsUrl, localRepo: !!ready.repo });
    await persistPrivateKeyHex(privateKeyHex, given);
    return { agent, attestationKp };
  }

  async function bindKeys(agent: unknown, attestationKp: { did(): string }): Promise<void> {
    const at = atproto!;
    const writer = agent as { createRecord?: (repo: string, collection: string, rkey: string, record: Record<string, unknown>) => Promise<unknown> };
    try {
      await createBadgeBlueKeysRecord({
        did: at.did,
        keyId: attestationKp.did(),
        service: "bidder_attestation",
        writeRecord: (repo, collection, rkey, record) =>
          typeof writer.createRecord === "function"
            ? writer.createRecord(repo, collection, rkey, record)
            : at.updateRecord(collection, rkey, record),
      } as Parameters<typeof createBadgeBlueKeysRecord>[0]);
      logger.info("badge_blue_key_bound", { did: at.did, keyId: attestationKp.did(), service: "bidder_attestation" });
    } catch (err) {
      logger.warn("badge_blue_key_bind_failed", { error: String(err) });
    }
    if (!id.associateWith) return;
    try {
      await at.createRecord(BADGE_BLUE_KEYS_NSID, {
        $type: BADGE_BLUE_KEYS_NSID,
        challenge: id.associateWith,
        keyId: at.did,
        service: "bidder_associate",
        createdAt: new Date().toISOString(),
      });
      logger.info("bidder_associated_with_operator", { operatorDid: id.associateWith, bidderDid: at.did });
    } catch (err) {
      logger.warn("bidder_associate_failed", { operatorDid: id.associateWith, error: String(err) });
    }
  }

  async function openProvider(p: MarketBidderProviderSpec): Promise<MarketBidderProviderRef> {
    const at = atproto!;
    const computeAtproto = at as unknown as ComputeAtproto;
    const signed = at.createSignedRepoRecord.bind(at);
    const callService = at.callService.bind(at);
    switch (p.kind) {
      case "digitalocean": {
        const { relay, serve } = await openProviderServe("digitalocean");
        const ref = createComputeProviderHooks({
          provider: createDigitalOceanComputeProvider({
            logger, atproto: computeAtproto, serve,
            getIssuerUrl: () => relay.ingressUrl,
            digitaloceanBaseUrl: p.baseUrl || "https://api.digitalocean.com",
            doToken: p.token,
            oidcJwkStore: oidcJwkStore("digitalocean"),
            oidcProvisioning: true,
            rbacProvisioner: createRbacProvisioner(),
            acceptToContract,
            createSignedRepoRecord: (collection: string, record: Record<string, unknown>) =>
              signed(collection, record).then(({ uri, cid }: { uri: string; cid: string }) => ({
                $type: "com.atproto.repo.strongRef" as const,
                uri,
                cid,
              })),
            callService: (url: string, body: Record<string, unknown>) =>
              callService(url, "", "", body).then((res: { status: number; body: unknown }) =>
                new Response(JSON.stringify(res.body), { status: res.status })
              ),
          }),
        });
        await serve.beginServe();
        return ref;
      }
      case "local": {
        const { relay, serve } = await openProviderServe("local");
        const local = createLocalComputeProvider({
          logger, atproto: computeAtproto, serve,
          getIssuerUrl: () => relay.ingressUrl,
          oidcProvisioning: true,
          rbacProvisioner: createRbacProvisioner(),
          containerImage: p.containerImage,
          oidcJwkStore: oidcJwkStore("local"),
          ingressProxyHost: network.ingressProxyHost,
          caCertPem: p.caCertPem,
          guestTlsPort,
          debugLogContainerOutput: isLocalMarketNetwork(network),
          createSignedRepoRecord: signed,
          callService,
          acceptToContract,
        });
        ensureImages.push(() => (local as { ensureImage?(): Promise<void> }).ensureImage?.() ?? Promise.resolve());
        await serve.beginServe();
        return createComputeProviderHooks({ provider: local });
      }
      case "firecracker": {
        const { relay, serve } = await openProviderServe("firecracker");
        const fc = createFirecrackerComputeProvider({
          logger,
          atproto: computeAtproto,
          serve,
          getIssuerUrl: () => relay.ingressUrl,
          microvm: createVmruntimeMicrovm({ command: p.vmruntimeCommand, handle: id.handle ?? at.did, logger }),
          workRoot: p.workRoot,
          oidcJwkStore: oidcJwkStore("firecracker"),
          oidcProvisioning: true,
          rbacProvisioner: createRbacProvisioner(),
          guestTlsPort,
          createSignedRepoRecord: signed,
          callService,
          acceptToContract,
        });
        await serve.beginServe();
        return createComputeProviderHooks({ provider: fc.provider });
      }
      case "kcp": {
        const guest = guestVault();
        const issuerUrl = ingressUrlOf((await ingressKeypair("kcp")).did(), network.ingressProxyHost);
        logger.info("bidder_guest_endpoints", { provider: "kcp", denopod: GUEST_DENOPOD_KCP, issuerUrl });
        const kcp = createComputeProviderKcpFactory({
          logger,
          atproto: computeAtproto,
          credentials: { server: p.server, workspace: p.workspace, tokenFile: p.tokenFile, caFile: p.caFile },
          handle: p.handle,
          guestVault: guest,
          guestHostAddress: p.guestHostAddress,
          guestLoginMount: p.guestLoginMount,
          oidcJwkStore: oidcJwkStore("kcp"),
          getIssuerUrl: () => issuerUrl,
          guestTlsPort,
          oidcProvisioning: true,
          rbacProvisioner: createRbacProvisioner(),
          createSignedRepoRecord: signed,
          callService,
          contractStoreReleases: true,
        });
        kcpContracts = { client: kcp.client, accepts: kcp.accepts, handle: p.handle };
        bidLedger = createKcpBidLedger({
          kcp: createKcpRestClient({ server: p.server, tokenFile: p.tokenFile, caFile: p.caFile }),
          workspace: p.workspace,
          windowMs: (spec.market.bidWindowSec ?? DEFAULT_BID_WINDOW_SEC) * 1_000,
        });
        return createComputeProviderHooks({ provider: kcp.provider });
      }
      case "denoWorker": {
        let permissionPolicyHandler: import("@publicdomainrelay/compute-deno-abc").PermissionPolicyHandler | undefined;
        if ((p.permissionMode || "deny-all") === "allow-net") {
          const { createAllowNetOnlyPolicyHandler } = await import("@publicdomainrelay/compute-deno-atproto");
          permissionPolicyHandler = createAllowNetOnlyPolicyHandler();
        }
        return createWorkerProviderHooks({
          provider: await createComputeProviderDenoWorker({ logger, atproto: at }),
          permissionPolicyHandler,
        });
      }
    }
  }

  function openMarketServe(relay: IngressRef | undefined): ServeHandle {
    const s = spec.market.serve;
    const serve = createServe({
      logger,
      tcp: { addr: s.addr || "0.0.0.0", port: s.port ?? 0, certFile: s.tlsCertFile, keyFile: s.tlsKeyFile },
      unix: s.unix ? { socketPath: s.unix } : undefined,
      portFile: spec.reports?.portFile,
      listenersFile,
      relays: relay ? [relay] : [],
    });
    serve.app.get("/oauth-client-metadata.json", (c) =>
      c.json(oauthClientMetadata({
        clientId: id.oauth?.clientId,
        redirectUri: id.oauth?.redirectUri,
        scope: BIDDER_OAUTH_SCOPE.join(" "),
        clientName: "Compute Provider (hono-bidder)",
      })));
    return serve;
  }

  async function start(): Promise<MarketBidderAppStatus> {
    for (const p of spec.providers) {
      const missing = missingProviderFields(p);
      if (missing.length > 0) {
        throw new Error(`the ${p.kind} provider is enabled and ${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} not set; it would fail at the first RFP instead of at startup`);
      }
    }
    const policyArgs = await resolveBidderPolicy(spec.market);
    installMarketNetworkFetch({ network });

    let signedIn: Awaited<ReturnType<typeof signIn>>;
    try {
      signedIn = await signIn();
    } catch (err) {
      logger.error("bidder_sign_in_failed", { error: err instanceof Error ? err.message : String(err) });
      throw err;
    }
    atproto = await createATProto({
      logger,
      badgeBlueSigner: signedIn.attestationKp,
      plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl: network.plcDirectoryUrl }),
      agent: signedIn.agent as never,
    });
    await bindKeys(signedIn.agent, signedIn.attestationKp);

    const firehoseMode = spec.market.firehoseMode || "off";
    const loop = marketLoop();
    const eventStreams = createDefaultATProtoEventStreamsClient({
      onlyRelays: explicitRelayUrls(network, firehoseMode),
      log: logger,
      onProgress: (source, p) => loop?.streamProgress(source, p.cursor, p.eventTime, new Date()),
    });
    crawlRelayUrls = eventStreams.relays.map((r) => r.url).filter(isCrawlRoot);
    localRelayUrls = crawlRelayUrls.filter((u) => u.startsWith("http://127.0.0.1:") || u.startsWith("http://localhost:"));

    const providers: MarketBidderProviderRef[] = [];
    for (const p of spec.providers) providers.push(await openProvider(p));

    const marketRelay = spec.market.ingressProxy ? await openIngress("market") : undefined;
    bidderServe = openMarketServe(marketRelay);
    const fsStore = contractStoreDir ? createFsContractStore({ dir: contractStoreDir }) : undefined;
    const contractStore = kcpContracts
      ? createKcpContractStore({ ...kcpContracts, tombstones: fsStore ?? { load: () => Promise.resolve({ contracts: [], tombstones: [] }), saveTombstone: () => Promise.resolve() }, logger })
      : fsStore;
    if (contractStoreDir) logger.info("bidder_contract_store", { dir: contractStoreDir, contracts: kcpContracts ? "ComputeVM spec.contract" : "files", tombstones: "files" });
    const refreshSec = spec.market.offeringRefreshSec ?? 300;

    bidder = await createMarketBidder({
      logger,
      atproto,
      providers,
      relay: marketRelay,
      contractStore,
      eventStreams,
      offeringRefreshMs: refreshSec > 0 ? refreshSec * 1000 : undefined,
      serve: bidderServe,
      policy: spec.market.policy,
      policyArgs: policyArgs as never,
      policyExec: { onlyRemote: spec.market.onlyRemotePolicyExec, allowUntrusted: spec.market.allowUntrustedPolicyExec },
      onSessionExpired: (err) =>
        logger.warn("bidder_session_expired", {
          name: id.name,
          error: err.message,
          hint: "the MarketIdentity shows SessionExpired with a sign-in-again action; the bidder stays up",
        }),
      acceptToContract,
      loop,
      bids: bidLedger,
    });
    await bidder.beginServe();
    return { did: atproto.did, ingressRef: marketRelay?.ingressRef, servePort: bidderServe.tcpPort };
  }

  async function announce(): Promise<void> {
    const at = atproto!;
    for (const ensure of ensureImages) {
      logger.info("ensuring container image", {});
      await ensure();
      logger.info("container image ready", {});
    }
    const localPort = localPdsPort || bidderServe!.tcpPort;
    if (localRelayUrls.length > 0 && localPort > 0) {
      const hostname = `127.0.0.1:${localPort}`;
      for (const url of localRelayUrls) {
        await at.requestCrawl(url, hostname).then(
          () => logger.info("relay_local_reregistered", { registry: url, hostname }),
          (err: unknown) => logger.warn("relay_local_reregister_failed", { registry: url, error: String(err) }),
        );
      }
    }
    await bidder!.refreshOffering().then(
      () => logger.info("relay_offering_refreshed_after_local_registration", {}),
      (err: unknown) => logger.warn("relay_offering_refresh_failed", { error: String(err) }),
    );
    const allRelayUrls = [...crawlRelayUrls, ...localRelayUrls];
    const visibilityHostname = pdsHostname ?? (localPort > 0 ? `127.0.0.1:${localPort}` : undefined);
    if (!visibilityHostname) return;
    for (const url of crawlRelayUrls) {
      await at.requestCrawl(url, visibilityHostname).then(
        () => logger.info("relay_reregistered_after_offering_refresh", { url, hostname: visibilityHostname }),
        (err: unknown) => logger.warn("relay_reregister_failed", { url, error: String(err) }),
      );
    }
    if (allRelayUrls.length === 0) return;
    const result = await verifyRelayVisibility({ relayUrls: allRelayUrls, bidderDid: at.did, collection: OFFERING_NSID, log: logger });
    if (result.ok) {
      logger.info("relay_visibility_confirmed", { indexedBy: result.indexedBy, capableRelays: result.capableRelays });
    } else {
      logger.warn("relay_visibility_failed", {
        capableRelays: result.capableRelays,
        failures: result.failures,
        hint: "Requester-side discovery via listReposByCollection will not find this bidder. The periodic offering refresh may eventually fix this.",
      });
    }
  }

  async function shutdown(): Promise<void> {
    bidder?.shutdown();
    for (const s of serves) s.shutdown();
    await identityHandle?.stop().catch(() => {});
  }

  return { start, announce, shutdown, serves, relays };
}
