import { Command } from "@publicdomainrelay/cli-args-env";
import { parsePolicyArgs } from "@publicdomainrelay/policy-engine-cli-options";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidderProviderRef } from "@publicdomainrelay/market-bidder-abc";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createFsContractStore } from "@publicdomainrelay/market-bidder-contract-store-fs";
import { createComputeProviderDenoWorker, createWorkerProviderHooks } from "@publicdomainrelay/market-bidder-worker";
import { createATProto, createLocalPDSAgent, createDelegatingAgent, registerKeypairPlcDid, createOAuthAgent, createOAuthAgentFromSession, pollForOAuthSession, tryRestoreOAuthQRSession, saveOAuthQRSession, OAuthSessionExpiredError } from "@publicdomainrelay/atproto-helpers";
import { isLocalMarketNetwork, marketNetwork, splitHostPort } from "@publicdomainrelay/market-network-common";
import type { MarketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import type { LocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { cliIdentityMode, cliMarketIdentity, vaultRefs } from "@publicdomainrelay/market-identity-abc";
import type { IdentityVault, MarketIdentityMode, MarketIdentityObjects } from "@publicdomainrelay/market-identity-abc";
import { bidderAssociatingRepo, startMarketIdentityReconciler } from "@publicdomainrelay/market-identity-atproto";
import type { MarketIdentityHandle } from "@publicdomainrelay/market-identity-atproto";
import { createMemoryIdentityVault, createMemoryMarketIdentityObjects } from "@publicdomainrelay/market-identity-memory";
import { createKcpMarketIdentityObjects } from "@publicdomainrelay/market-identity-kcp";
import { createOpenBaoIdentityVault } from "@publicdomainrelay/market-identity-vault-openbao";
import { createFileIdentityVault } from "@publicdomainrelay/market-identity-vault-file";
import { startLoopbackCallbackServer, oauthClientMetadata } from "@publicdomainrelay/atproto-oauth-helpers";
import { createBadgeBlueKeysRecord, loadOrGenerateKeypair } from "@publicdomainrelay/market-atproto";
import { ACCEPT_NSID, EVENT_NSID, OFFERING_NSID, RFP_NSID } from "@publicdomainrelay/market-common";
import { BIDDER_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import { verifyRelayVisibility } from "@publicdomainrelay/requester-xrpc";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { DEFAULT_RELAY_URLS } from "@publicdomainrelay/atproto-event-stream-common";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createFirecrackerComputeProvider } from "@publicdomainrelay/compute-provider-firecracker";
import { createFirecrackerNodeImage } from "@publicdomainrelay/node-image-firecracker";
import { createFirecrackerMicrovm, parseLabels } from "@publicdomainrelay/microvm-firecracker";
import { createDockerBackend } from "@publicdomainrelay/container-backend-docker";
import { createRbacProvisioner } from "@publicdomainrelay/rbac-atproto";
import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { qrcode } from "@libs/qrcode";
import cliArgsEnv from "./cli-args-env.ts";

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_HONO_BIDDER", cliArgsEnv, runtimeConfig).resolve();

const serviceName = (options.serviceName as string) ?? "bidder";
const logger = createLogger({ serviceName });
const BIDDER_ASSOC_SERVICE = "bidder_associate";
// Resolve privateKeyHex: --private-key-hex takes priority, then --private-key-hex-path
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

const keypair = resolvedPrivateKeyHex
  ? await Secp256k1Keypair.import(resolvedPrivateKeyHex, { exportable: true })
  : await Secp256k1Keypair.create({ exportable: true });

const privateKeyHex = resolvedPrivateKeyHex ??
  Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

// Pre-compute attestation keypair from the same private key as the rotation key.
// Must be available before createGenesisOp (OAuth QR path) to publish in PLC DID.
const attestationKp = await loadOrGenerateKeypair(privateKeyHex);

const networkFromOptions = marketNetwork({
  plcDirectoryUrl: options.plcDirectoryUrl as string | undefined,
  ingressProxyHost: options.ingressProxyHost as string | undefined,
  relayUrls: options.relayUrl as string | undefined,
  firehoseUrls: options.firehoseUrl as string | undefined,
});
const guestTlsPort = options.guestTlsPort as number | undefined;
const network: MarketNetwork = guestTlsPort
  ? { ...networkFromOptions, guestTlsUrl: `https://${splitHostPort(networkFromOptions.ingressProxyHost).hostname}:${guestTlsPort}` }
  : networkFromOptions;
const ingressProxyHost = network.ingressProxyHost;
const plcDirectoryUrl = network.plcDirectoryUrl;
let ownPlcDid: string | undefined;

installMarketNetworkFetch({ network });

// --ca-cert-pem / --guest-tls-port are threaded to the compute provider so
// guests trust and reach the TLS dispatcher listener for OIDC (HTTPS-only).
// The bidder's own dispatcher traffic stays on the plain listener.

// Each relay gets its own keypair so subscribers never share a subdomain/FQDN
// on the dispatcher (collision would route everyone to the last connector).
async function cliCreateIngress() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  const bidderDid = ownPlcDid ?? atproto.did;
  const localSigner = {
    did: () => bidderDid,
    sign: async (bytes: Uint8Array) => keypair.sign(bytes),
  };
  return createIngress({ logger, ingressProxyHost, signer: localSigner, keypair: relayKeypair });
}

// deno-lint-ignore no-explicit-any
let atprotoAgent: any;
let pdsHostname: string | undefined;
let isLocal = false;
let isOAuth = false;
const AUTO_REFRESH_THRESHOLD_MS = 3_600_000; // 1 hour -- proactively refresh access token before it expires

function createSessionExpiredHandler(label: string) {
  return (err: OAuthSessionExpiredError) => {
    logger.error("oauth_session_expired_shutting_down", {
      sessionPath: err.sessionPath,
      label,
      hint: "Restart the process to re-authenticate via QR code. The stale session file has been deleted.",
    });
    if (err.sessionPath) {
      try { Deno.removeSync(err.sessionPath); } catch { /* ignore */ }
    }
    // Give the log a moment to flush, then exit so systemd/docker restarts us.
    setTimeout(() => Deno.exit(1), 500);
  };
}
const _deferredRelayUrls: string[] = [];
let _deferredPdsPort = 0;

const identityName = options.marketIdentityName as string;
const identityVaultMount = options.identityVaultMount as string;
const pdsStatePath = options.pdsStatePath as string | undefined;

function cliIdentityVault(): IdentityVault | undefined {
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
  return undefined;
}

function cliKcpIdentityObjects(): MarketIdentityObjects | undefined {
  if (!options.marketIdentityKcpServer) return undefined;
  const workspace = options.marketIdentityKcpWorkspace as string | undefined;
  if (!workspace) throw new Error("--market-identity-kcp-server needs --market-identity-kcp-workspace");
  const caFile = options.marketIdentityKcpCaCertFile as string | undefined;
  return createKcpMarketIdentityObjects({
    server: options.marketIdentityKcpServer as string,
    workspace,
    tokenFile: options.marketIdentityKcpTokenFile as string | undefined,
    caCert: caFile ? Deno.readTextFileSync(caFile) : undefined,
  });
}

const identityVault = cliIdentityVault();
const kcpIdentityObjects = cliKcpIdentityObjects();
const identityMode: MarketIdentityMode | undefined = kcpIdentityObjects ? undefined : cliIdentityMode({
  mode: options.atprotoIdentityMode as string | undefined,
  oauth: options.atprotoOauth as boolean,
  oauthQr: options.atprotoOauthQr as boolean,
  handle: options.atprotoHandle as string | undefined,
  password: !!options.atprotoPassword,
  vault: !!identityVault,
});
logger.info("bidder_identity_mode", { mode: identityMode ?? "kcp", name: identityName });

let identityHandle: MarketIdentityHandle | undefined;
let localRepoAgent: LocalPDSAgent | undefined;

async function openLocalRepoAgent(repoKeypair: Secp256k1Keypair): Promise<LocalPDSAgent> {
  const pdsServe = createServe({ logger });
  const agent = await createLocalPDSAgent({
    logger, keypair: repoKeypair,
    serve: pdsServe,
    plcDirectoryUrl,
    ingressProxyHost,
    storagePath: pdsStatePath,
    associateServiceId: BIDDER_ASSOC_SERVICE,
  });
  await agent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  return agent;
}

async function identityVaultFor(mode: MarketIdentityMode | undefined): Promise<IdentityVault> {
  if (identityVault) {
    if (options.atprotoPassword) logger.warn("atproto_password_ignored", { hint: "the identity vault holds the password" });
    return identityVault;
  }
  if (mode === "pdsAccount" && options.atprotoPassword) {
    logger.warn("atproto_password_from_options", { hint: "development only; production keeps the password in the identity vault" });
    const vault = createMemoryIdentityVault();
    await vault.write(vaultRefs(identityName, { mode }, identityVaultMount).keys, { password: options.atprotoPassword as string });
    return vault;
  }
  throw new Error(`identity mode ${mode ?? "from kcp"} needs an identity vault: --identity-vault-file or --identity-vault-address`);
}

async function signInThroughIdentity(): Promise<void> {
  const vault = await identityVaultFor(identityMode);
  const objects = kcpIdentityObjects ?? createMemoryMarketIdentityObjects([
    cliMarketIdentity(identityName, {
      mode: identityMode!,
      handle: options.atprotoHandle as string | undefined,
      pdsUrl: identityMode === "pdsAccount" ? options.atprotoPdsUrl as string : undefined,
    }),
  ]);
  identityHandle = startMarketIdentityReconciler({
    name: identityName,
    objects,
    vault,
    logger,
    plcDirectoryUrl,
    qrOrigin: network.qrOrigin,
    vaultMount: identityVaultMount,
    autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
    oauthClientId: options.oauthClientId as string | undefined,
    localRepo: {
      qrParam: "bdr",
      open: async (repoKeyHex: string) => {
        localRepoAgent = await openLocalRepoAgent(await Secp256k1Keypair.import(repoKeyHex, { exportable: true }));
        return bidderAssociatingRepo(localRepoAgent, BIDDER_ASSOC_SERVICE);
      },
    },
  });
  const ready = kcpIdentityObjects
    ? await identityHandle.ready()
    : await Promise.race([
      identityHandle.ready(),
      new Promise<never>((_, reject) => {
        const poll = setInterval(async () => {
          const obj = await objects.get(identityName);
          if (obj?.status?.phase === "Failed") {
            clearInterval(poll);
            reject(new Error(`sign-in failed: ${obj.status.message ?? "unknown"}`));
          }
        }, 200);
        identityHandle!.ready().finally(() => clearInterval(poll));
      }),
    ]);
  if (ready.repo && localRepoAgent) {
    atprotoAgent = localRepoAgent;
    isLocal = true;
    pdsHostname = localRepoAgent.relay?.ingressHost || undefined;
  } else {
    atprotoAgent = createDelegatingAgent(ready.did, () => identityHandle!.agent());
    ownPlcDid = await registerKeypairPlcDid({ keypair, attestationDid: attestationKp.did(), plcDirectoryUrl, logger });
    pdsHostname = ready.pdsUrl && identityMode === "pdsAccount" ? new URL(ready.pdsUrl).host : undefined;
  }
  logger.info("bidder_identity_ready", { did: ready.did, handle: ready.handle, pdsUrl: ready.pdsUrl, localRepo: !!ready.repo });
}

if (kcpIdentityObjects || identityMode === "pdsAccount" || identityMode === "oauthDelegated") {
  try {
    await signInThroughIdentity();
  } catch (err) {
    logger.error("bidder_sign_in_failed", { mode: identityMode ?? "kcp", error: err instanceof Error ? err.message : String(err) });
    await identityHandle?.stop().catch(() => {});
    Deno.exit(1);
  }
} else if (identityMode === "oauthBrowser") {
  const handle = options.atprotoHandle as string | undefined;
  if (!handle) {
    logger.error("--atproto-oauth requires --atproto-handle");
    Deno.exit(1);
  }
  // OAuth flow -- use remote PDS via ATProto OAuth
  const sessionPath = options.oauthSessionPath as string;
  const oauthAgent = await createOAuthAgent({
    handle: options.atprotoHandle as string,
    sessionPath,
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: BIDDER_OAUTH_SCOPE.join(" "),
    plcDirectoryUrl: plcDirectoryUrl,
    logger,
  });

  // Restore existing session or start new flow
  const restored = await oauthAgent.restore();
  if (!restored) {
    const authUrl = await oauthAgent.startFlow();
    // Parse redirect URI to find port, then start loopback callback server
    const redirectUri = (options.oauthRedirectUri as string) || "http://127.0.0.1:0/callback";
    const portMatch = redirectUri.match(/:(\d+)/);
    const port = portMatch ? parseInt(portMatch[1]) : 0;

    // Open browser (platform-aware)
    const cmd = Deno.build.os === "darwin" ? "open" : Deno.build.os === "windows" ? "cmd" : "xdg-open";
    if (Deno.build.os === "windows") {
      new Deno.Command(cmd, { args: ["/c", "start", authUrl] }).spawn();
    } else {
      new Deno.Command(cmd, { args: [authUrl] }).spawn();
    }

    logger.info("oauth_browser_opened", { authUrl });

    const { promise, shutdown } = startLoopbackCallbackServer(port);
    const params = await promise;
    shutdown();
    await oauthAgent.completeFlow(params);
  }

  atprotoAgent = oauthAgent;
  isOAuth = true;
  // In OAuth mode, the PDS hostname is the PDS from the session (not used for requestCrawl)
  pdsHostname = undefined;
} else if (identityMode === "oauthQr") {
  const qrSessionPath = options.oauthSessionFile as string | undefined;
  const qrSessionKey = (handle: string | undefined) => ({ sessionPath: qrSessionPath, label: "bidder", handle });
  const plcDid = await registerKeypairPlcDid({ keypair, attestationDid: attestationKp.did(), plcDirectoryUrl, logger });
  ownPlcDid = plcDid;
  const qrSigner = { did: () => plcDid, sign: (bytes: Uint8Array) => keypair.sign(bytes) };

  // Try restoring saved OAuth QR session
  const _restoredAgent = await tryRestoreOAuthQRSession({
    logger, ...qrSessionKey(options.atprotoHandle as string | undefined),
    autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
    // No onSessionExpired here -- restore handles expiry internally
    // (delete file, return null -> falls through to QR auth).
  });
  if (_restoredAgent) {
    atprotoAgent = _restoredAgent;
    isOAuth = true;
    pdsHostname = undefined;
    logger.info("oauth_qr_session_restored", { userDid: _restoredAgent.sessionData?.userDid });
  } else {
    // Generate nonce for defense-in-depth POST auth
    const oauthNonce = Array.from(crypto.getRandomValues(new Uint8Array(16)),
      (b) => b.toString(16).padStart(2, "0")).join("");

    // Show QR
    const qrUrl = `${network.qrOrigin}/#oauth=${encodeURIComponent(plcDid)}&n=${oauthNonce}`;
    process.stdout.write("\n" + "=".repeat(60) + "\n");
    process.stdout.write("  Scan this QR code with your phone to authenticate:\n\n");
    qrcode(qrUrl, { output: "console", ecl: "HIGH" });
    process.stdout.write("\n  Or open this URL:\n  " + qrUrl + "\n");
    process.stdout.write("=".repeat(60) + "\n\n");

    logger.info("oauth_qr_awaiting_session", { did: plcDid });

    // Poll for session
    const session = await pollForOAuthSession({
      cliDid: plcDid,
      signer: qrSigner,
      qrFedforkOrigin: network.qrOrigin,
      logger,
    });

    // Create OAuth agent from transferred session
    const oauthAgent = await createOAuthAgentFromSession(session, {
      logger,
      autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
      onSessionExpired: createSessionExpiredHandler("bidder"),
      saveSession: (s) => saveOAuthQRSession(s, qrSessionKey(s.handle)),
    });
    atprotoAgent = oauthAgent;

    // Persist session for future restarts
    await saveOAuthQRSession(session, qrSessionKey(session.handle));

    isOAuth = true;
    pdsHostname = undefined;
    logger.info("oauth_qr_session_ready", { userDid: session.userDid, handle: session.handle });
  }
} else {
  atprotoAgent = await openLocalRepoAgent(keypair);
  isLocal = true;
  const relayHost: string = (atprotoAgent as { relay?: { ingressHost?: string } }).relay?.ingressHost ?? "";
  if (relayHost) {
    pdsHostname = relayHost;
  }

  // Show association QR so user can link their Bluesky account
  if (!options.skipQr) {
    const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
    let hasAssociation = false;
    let cursor: string | undefined;
    do {
      const result = await (atprotoAgent as LocalPDSAgent).repoApi.listRecords(
        atprotoAgent.did, BADGE_BLUE_KEYS_NSID, { limit: 100, cursor },
      );
      for (const rec of result.records) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === atprotoAgent.did && v.service === BIDDER_ASSOC_SERVICE) {
          hasAssociation = true;
          break;
        }
      }
      cursor = result.cursor;
    } while (cursor && !hasAssociation);

    if (hasAssociation) {
      logger.info("existing_association_found", { did: atprotoAgent.did, hint: "skipping QR -- prior association exists" });
    } else {
      const qrUrl = `${network.qrOrigin}/#bdr=${atprotoAgent.did}`;
      logger.info("qr_url", { url: qrUrl });
      process.stdout.write("\n" + "=".repeat(60) + "\n");
      process.stdout.write("  Scan this QR code with your phone to authenticate:\n\n");
      qrcode(qrUrl, { output: "console", ecl: "HIGH" });
      process.stdout.write("\n  Or open this URL:\n  " + qrUrl + "\n");
      process.stdout.write("=".repeat(60) + "\n\n");

      logger.info("waiting_for_association", {
        hint: "Scan QR code, then confirm on your phone",
        bidderDid: atprotoAgent.did,
      });
      const callerDid = await atprotoAgent.associateCalled;

      let handle = callerDid;
      try {
        const idr = new IdResolver();
        const doc = await idr.did.resolve(callerDid);
        const aka = (doc as Record<string, unknown>)?.alsoKnownAs as string[] | undefined;
        if (aka?.[0]) handle = aka[0].replace("at://", "");
      } catch { /* use DID as fallback */ }

      const answer = prompt(`Associate with ${handle}? [y/N] `);
      if (!answer || !answer.toLowerCase().startsWith("y")) {
        logger.info("association_rejected", { callerDid, handle });
        atprotoAgent.rejectAssociation(new Error("User rejected association"));
        Deno.exit(0);
      }
      atprotoAgent.approveAssociation();
      logger.info("association_confirmed", { callerDid, handle });
    }
  }
}
const atproto = await createATProto({
  logger,
  badgeBlueSigner: attestationKp,
  plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
  agent: atprotoAgent,
});

// Bind the ephemeral attestation key to this bidder's DID so remote verifiers
// accept the inline signatures on records it authors. An OAuth-QR bidder signs
// with an attestation did:key that is NOT in its DID document (the account key
// is), and verifyRecordSignatures' fallback keyBoundByBadgeBlueKey requires a
// badgeBlueKeys record at the deterministic rkey. Local-PDS bidders publish the
// key in their DID doc, so the binding is redundant-but-harmless. The service
// is deliberately not bidder_associate/requester_associate so operator
// resolution (which scans badgeBlueKeys for exactly those services) skips it.
try {
  await createBadgeBlueKeysRecord({
    did: atproto.did,
    keyId: attestationKp.did(),
    service: "bidder_attestation",
    // createRecord (not putRecord): the OAuth session's badgeBlueKeys scope is
    // action=create only, so putRecord/updateRecord 403s with ScopeMissingError.
    writeRecord: (repo, collection, rkey, record) =>
      typeof atprotoAgent.createRecord === "function"
        ? atprotoAgent.createRecord(repo, collection, rkey, record)
        : atproto.updateRecord(collection, rkey, record),
  });
  logger.info("badge_blue_key_bound", { did: atproto.did, keyId: attestationKp.did(), service: "bidder_attestation" });
} catch (err) {
  logger.warn("badge_blue_key_bind_failed", { error: String(err) });
}

// Headless operator association: mint a bidder_associate badgeBlueKeys record
// mapping this bidder to the operator DID that owns it. Mirrors exactly what
// the QR associate flow writes (keyId=operator, challenge=self, service
// "bidder_associate"); doing it before createMarketBidder means the boot-time
// trust-cache refresh resolves operatorOf(selfDid)=associateWith, which is what
// a "only-me" policy consults before admitting a bid.
const associateWith = options.associateWith as string | undefined;
if (associateWith) {
  try {
    const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
    // Canonical association shape: {challenge: OPERATOR, keyId: ASSOCIATED}.
    // The operator is the challenge; the associated bidder is the keyId.
    // (The old shape -- challenge=self, keyId=operator -- inverted the fields
    // and made operator-discovery mis-read an operator's acknowledgment as its
    // own operator, e.g. ocnuqjlz -> 5jo53.)
    await atproto.createRecord(BADGE_BLUE_KEYS_NSID, {
      $type: BADGE_BLUE_KEYS_NSID,
      challenge: associateWith,
      keyId: atproto.did,
      service: "bidder_associate",
      createdAt: new Date().toISOString(),
    });
    logger.info("bidder_associated_with_operator", { operatorDid: associateWith, bidderDid: atproto.did });
  } catch (err) {
    logger.warn("bidder_associate_failed", { operatorDid: associateWith, error: String(err) });
  }
}

const firehoseMode = (options.firehoseMode as string) || "off";
const additionalRelays: string[] = [
  ...network.relayUrls,
  ...(firehoseMode === "subscriberepos" ? network.firehoseUrls : []),
];

const eventStreams = createDefaultATProtoEventStreamsClient({
  additionalRelays,
  removeRelays: network.firehoseUrls.length > 0 ? DEFAULT_RELAY_URLS : undefined,
  log: logger,
});

// Collect relay URLs for PDS registration.
const relayUrls = eventStreams.relays.map((r) => r.url);

// Only http(s) relay ROOT URLs are valid requestCrawl targets. Firehose URLs
// (wss://host/xrpc/.../subscribeRepos) added for event streaming are consumed
// by the event-streams client, not by requestCrawl -- passing them here makes
// fetch throw "Url scheme 'wss' not supported" and leaves the bidder with no
// capable relay for PDS registration/visibility.
const crawlRelayUrls = relayUrls.filter((u) => {
  try {
    const url = new URL(u);
    return (url.protocol === "http:" || url.protocol === "https:") &&
      (url.pathname === "/" || url.pathname === "");
  } catch {
    return false;
  }
});

// Collect local relay URLs for deferred registration after serve starts.
for (const url of crawlRelayUrls) {
  if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
    _deferredRelayUrls.push(url);
  }
}

// PDS registration is deliberately deferred until AFTER the offering exists
// (see the requestCrawl re-announce below the market bidder setup): a relay
// crawl that connects before the offering commit misses it in the live-stream
// gap, leaving the bidder invisible until the next offering refresh. Announcing
// here (pre-offering) would trigger exactly that useless early crawl.

// Persist generated key to path for future runs.
if (privateKeyHexPath) {
  try {
    const parent = privateKeyHexPath.includes("/") ? privateKeyHexPath.slice(0, privateKeyHexPath.lastIndexOf("/")) : ".";
    await Deno.mkdir(parent, { recursive: true });
    await Deno.writeTextFile(privateKeyHexPath, privateKeyHex);
    if (resolvedPrivateKeyHex) {
      logger.info("private_key_rewritten", { path: privateKeyHexPath });
    } else {
      logger.info("private_key_generated_and_saved", { path: privateKeyHexPath });
    }
  } catch (err) {
    logger.warn("private_key_save_failed", { path: privateKeyHexPath, error: String(err) });
  }
}

const providers: MarketBidderProviderRef[] = [];
const serves: ReturnType<typeof createServe>[] = [];
let localProviderEnsureImage: (() => Promise<void>) | undefined;
// Shared accept->receipt map -- created early so provider guest routes can use it.
const acceptToContract = new Map<string, { receiptKey: string; receiptUri: string; receiptCid: string; submitEventUrl?: string }>();

if (options.computeProviderDigitaloceanToken) {
  const relay = await cliCreateIngress();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  providers.push(createComputeProviderHooks({
    provider: createDigitalOceanComputeProvider({
      logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
      getIssuerUrl: () => relay.ingressUrl,
      digitaloceanBaseUrl: (options.computeProviderDigitaloceanBaseUrl as string) || "https://api.digitalocean.com",
      doToken: options.computeProviderDigitaloceanToken as string,
      oidcProvisioning: true,
      rbacProvisioner: createRbacProvisioner(),
      acceptToContract,
      createSignedRepoRecord: (collection: string, record: Record<string, unknown>) =>
        atproto.createSignedRepoRecord(collection, record).then(({ uri, cid }: { uri: string; cid: string }) => ({
          $type: "com.atproto.repo.strongRef" as const,
          uri,
          cid,
        })),
      callService: (url: string, body: Record<string, unknown>) =>
        atproto.callService(url, "", "", body).then((res: { status: number; body: unknown }) =>
          new Response(JSON.stringify(res.body), { status: res.status }),
        ),
    }),
  }));
  await serve.beginServe();
}

if (options.computeProviderLocal) {
  const relay = await cliCreateIngress();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  const localProvider = createLocalComputeProvider({
    logger, atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto, serve,
    getIssuerUrl: () => relay.ingressUrl,
    oidcProvisioning: true,
    rbacProvisioner: createRbacProvisioner(),
    containerMode: options.computeProviderLocalMode as "vm" | "container" | undefined,
    vmImage: options.computeProviderLocalVmImage as string | undefined,
    containerImage: options.computeProviderLocalContainerImage as string | undefined,
    cacheDir: options.computeProviderLocalCacheDir as string | undefined,
    ingressProxyHost: options.ingressProxyHost as string,
    caCertPem: options.caCertPem as string | undefined,
    guestTlsPort: options.guestTlsPort as number | undefined,
    debugLogContainerOutput: isLocalMarketNetwork(network),
    createSignedRepoRecord: atproto.createSignedRepoRecord.bind(atproto),
    callService: atproto.callService.bind(atproto),
    acceptToContract,
  });
  localProviderEnsureImage = () => (localProvider as { ensureImage?(): Promise<void> }).ensureImage?.() ?? Promise.resolve();
  providers.push(createComputeProviderHooks({
    provider: localProvider,
  }));
  await serve.beginServe();
}

if (options.computeProviderFirecracker) {
  const required: Array<[string, unknown]> = [
    ["--compute-provider-firecracker-nodeimage", options.computeProviderFirecrackerNodeimage],
    ["--compute-provider-firecracker-nodeboot", options.computeProviderFirecrackerNodeboot],
    ["--compute-provider-firecracker-config", options.computeProviderFirecrackerConfig],
    ["--compute-provider-firecracker-repo-dir", options.computeProviderFirecrackerRepoDir],
    ["--compute-provider-firecracker-vmm", options.computeProviderFirecrackerVmm],
    ["--compute-provider-firecracker-runner-image", options.computeProviderFirecrackerRunnerImage],
    ["--compute-provider-firecracker-work-root", options.computeProviderFirecrackerWorkRoot],
  ];
  const missing = required.filter(([, value]) => !value).map(([flag]) => flag);
  if (missing.length > 0) {
    throw new Error(
      `the Firecracker provider is enabled and ${missing.join(", ")} ${missing.length === 1 ? "is" : "are"} ` +
        `not set. Every one of them is a path on this host -- the two binaries, the image builder's ` +
        `configuration, the checkout the guest agent is built from, the VMM, and where guests work ` +
        `-- and none has a default that would be right on another machine. A provider started ` +
        `without one of them would fail at the first RFP instead of at startup.`,
    );
  }
  const relay = await cliCreateIngress();
  const serve = createServe({ logger, relays: [relay] });
  serves.push(serve);
  const firecrackerProvider = createFirecrackerComputeProvider({
    logger,
    atproto: atproto as import("@publicdomainrelay/compute-provider-abc").ComputeAtproto,
    serve,
    getIssuerUrl: () => relay.ingressUrl,
    image: createFirecrackerNodeImage({
      binary: options.computeProviderFirecrackerNodeimage as string,
      configPath: options.computeProviderFirecrackerConfig as string,
      repoDir: options.computeProviderFirecrackerRepoDir as string,
      preinstallPath: options.computeProviderFirecrackerPreinstall as string | undefined,
      logger,
    }),
    microvm: createFirecrackerMicrovm({
      backend: createDockerBackend(),
      runnerImage: options.computeProviderFirecrackerRunnerImage as string,
      labels: parseLabels((options.computeProviderFirecrackerContainerLabels as string | undefined) ?? ""),
      logger,
    }),
    workRoot: options.computeProviderFirecrackerWorkRoot as string,
    rangeBase: options.computeProviderFirecrackerRangeBase as string | undefined,
    reuseStale: options.computeProviderFirecrackerReuseImage === true,
    oidcProvisioning: true,
    rbacProvisioner: createRbacProvisioner(),
    guestTlsPort: options.guestTlsPort as number | undefined,
    createSignedRepoRecord: atproto.createSignedRepoRecord.bind(atproto),
    callService: atproto.callService.bind(atproto),
    acceptToContract,
  });
  providers.push(createComputeProviderHooks({
    provider: firecrackerProvider.provider,
  }));
  await serve.beginServe();
}

if (options.computeProviderDenoWorker) {
  const workerPermMode = (options.workerPermissionMode as string) || "deny-all";
  let permissionPolicyHandler: import("@publicdomainrelay/compute-deno-abc").PermissionPolicyHandler | undefined;
  if (workerPermMode === "allow-net") {
    const { createAllowNetOnlyPolicyHandler } = await import("@publicdomainrelay/compute-deno-atproto");
    permissionPolicyHandler = createAllowNetOnlyPolicyHandler();
  }
  providers.push(createWorkerProviderHooks({
    provider: await createComputeProviderDenoWorker({ logger, atproto }),
    permissionPolicyHandler,
  }));
}

const offeringRefreshSec = (options.offeringRefreshSec as number) ?? 300;

// Market factory gets its own relay/serve (own keypair -> own subdomain/FQDN).
// Ingress only disabled when explicitly requested via --no-ingress-proxy.
const bidderIngress = options.noIngressProxy ? undefined : await cliCreateIngress();
const bidderServe = createServe({
  logger,
  tcp: {
    addr: (options.serveAddr as string) || "0.0.0.0",
    port: (options.servePort as number) ?? 0,
    certFile: options.tlsCertFile as string | undefined,
    keyFile: options.tlsKeyFile as string | undefined,
  },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  portFile: options.portFile as string | undefined,
  relays: bidderIngress ? [bidderIngress] : [],
});

// OAuth client metadata endpoint -- serves registered-client metadata.
bidderServe.app.get("/oauth-client-metadata.json", (_c: { json(obj: Record<string, unknown>): Response }) => {
  return _c.json(oauthClientMetadata({
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: BIDDER_OAUTH_SCOPE.join(" "),
    clientName: "Compute Provider (hono-bidder)",
  }));
});

const policy = options.policy as string | undefined;
let policyArgs: Record<string, unknown> = {};
try {
  policyArgs = parsePolicyArgs(options.policyArgs);
  if (policy) {
    const { createPolicyRegistry } = await import("@publicdomainrelay/policy-deno-typescript");
    const { resolvePolicyName } = await import("@publicdomainrelay/policy-deno-typescript-shared");
    resolvePolicyName(createPolicyRegistry(), policy, "bidder"); // throws on unknown / wrong side
  }
} catch (err) {
  console.error(`invalid --policy: ${err}`);
  Deno.exit(2);
}

const contractStoreDir = (options.contractStoreDir as string | undefined) ||
  (options.computeProviderFirecracker ? `${options.computeProviderFirecrackerWorkRoot}/contracts` : undefined);
const contractStore = contractStoreDir ? createFsContractStore({ dir: contractStoreDir }) : undefined;
if (contractStoreDir) logger.info("bidder_contract_store", { dir: contractStoreDir });

const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderIngress,
  contractStore,
  eventStreams,
  offeringRefreshMs: offeringRefreshSec > 0 ? offeringRefreshSec * 1000 : undefined,
  serve: bidderServe,
  policy,
  policyArgs,
  policyExec: {
    onlyRemote: options.onlyRemotePolicyExec as boolean | undefined,
    allowUntrusted: options.allowUntrustedPolicyExec as boolean | undefined,
  },
  onSessionExpired: isOAuth ? createSessionExpiredHandler("bidder") : undefined,
  acceptToContract,
});

function shutdown() {
  bidder.shutdown();
  if (isOAuth && atprotoAgent && typeof (atprotoAgent as Record<string, unknown>).dispose === "function") {
    ((atprotoAgent as Record<string, unknown>).dispose as () => void)();
  }
  for (const s of serves) s.shutdown();
  if (identityHandle) {
    identityHandle.stop().finally(() => Deno.exit());
    return;
  }
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

await bidder.beginServe();

// Machine-readable line for test harness subprocess spawn
console.log(JSON.stringify({
  event: "bidder_ready",
  did: atproto.did,
  ingressRef: bidderIngress?.ingressRef,
  servePort: bidderServe.tcpPort,
}));

const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
let hasAssociation = false;
if (!options.skipQr) {
  let cursor: string | undefined;
  do {
    const result = await atproto.listRecords(atproto.did, BADGE_BLUE_KEYS_NSID, { limit: 100 });
    for (const rec of result.records) {
      const v = rec.value as Record<string, unknown>;
      if (v.challenge === atproto.did && v.service === BIDDER_ASSOC_SERVICE) {
        hasAssociation = true;
        break;
      }
    }
    cursor = (result as { cursor?: string }).cursor;
  } while (cursor && !hasAssociation);
}

if (hasAssociation) {
  logger.info("existing_association_found", { did: atproto.did, hint: "skipping QR -- prior association exists" });
}

if (!options.skipQr && !hasAssociation) {
  if (!isLocal) {
    logger.warn("qr_association_requires_local_pds", {
      hint: "QR association only works with a local PDS (no --atproto-handle). Use --skip-qr to skip.",
    });
  } else {
    const qrUrl = `${network.qrOrigin}/#bdr=${atproto.did}`;
    logger.info("qr_url", { url: qrUrl });
    process.stdout.write("\n" + "=".repeat(60) + "\n");
    process.stdout.write("  Scan this QR code with your phone to authenticate:\n\n");
    qrcode(qrUrl, { output: "console", ecl: "HIGH" });
    process.stdout.write("\n  Or open this URL:\n  " + qrUrl + "\n");
    process.stdout.write("=".repeat(60) + "\n\n");

    logger.info("waiting_for_association", {
      hint: "Scan QR code, then confirm on your phone",
      bidderDid: atproto.did,
    });
    const localAgent = atprotoAgent as LocalPDSAgent;
    const callerDid = await localAgent.associateCalled;

    const assocIdResolver = new IdResolver({ plcUrl: plcDirectoryUrl });
    let handle = callerDid;
    try {
      const doc = await assocIdResolver.did.resolve(callerDid);
      const aka = (doc as Record<string, unknown>)?.alsoKnownAs as string[] | undefined;
      if (aka?.[0]) handle = aka[0].replace("at://", "");
    } catch { /* use DID as fallback */ }

    const answer = prompt(`Associate with ${handle}? [y/N] `);
    if (!answer || !answer.toLowerCase().startsWith("y")) {
      logger.info("association_rejected", { callerDid, handle });
      localAgent.rejectAssociation(new Error("User rejected association"));
      Deno.exit(0);
    }
    localAgent.approveAssociation();
    logger.info("association_confirmed", { callerDid, handle });
  }
}

// Pre-build container image after association so first provision is fast.
if (localProviderEnsureImage) {
  logger.info("ensuring container image", {});
  await localProviderEnsureImage();
  logger.info("container image ready", {});
}

const _localServePort = _deferredPdsPort || bidderServe.tcpPort;
if (_deferredRelayUrls.length > 0 && _localServePort > 0) {
  const localHostname = `127.0.0.1:${_localServePort}`;
  for (const url of _deferredRelayUrls) {
    try {
      await atproto.requestCrawl(url, localHostname);
      logger.info("relay_local_reregistered", { registry: url, hostname: localHostname });
    } catch (err) {
      logger.warn("relay_local_reregister_failed", { registry: url, error: String(err) });
    }
  }
}

// Re-commit offering so relays index it immediately
// rather than waiting for the periodic offering refresh.
try {
  await bidder.refreshOffering();
  logger.info("relay_offering_refreshed_after_local_registration", {});
} catch (err) {
  logger.warn("relay_offering_refresh_failed", { error: String(err) });
}

// Verify the offering is discoverable through at least one relay
// that supports listReposByCollection. Probes each relay, then polls
// capable ones until the bidder's DID appears or the poll budget expires.
// Non-blocking on failure -- bidder still boots, just warns.
const _allRelayUrls = [...crawlRelayUrls, ..._deferredRelayUrls];
const _visibilityHostname = pdsHostname ?? (_localServePort > 0 ? `127.0.0.1:${_localServePort}` : undefined);

// Register the PDS with ALL relays now that the offering record is committed.
// This is the first (and only) requestCrawl announce — the relay crawls a repo
// that already contains the offering, so its backfill indexes it immediately
// (no live-stream gap). The relay treats re-requestCrawl as a full re-crawl, so
// this announce doubles as the "see the fresh offering commit" ping.
if (_visibilityHostname) {
  for (const url of crawlRelayUrls) {
    try {
      await atproto.requestCrawl(url, _visibilityHostname);
      logger.info("relay_reregistered_after_offering_refresh", { url, hostname: _visibilityHostname });
    } catch (err) {
      logger.warn("relay_reregister_failed", { url, error: String(err) });
    }
  }
}

if (_allRelayUrls.length > 0 && _visibilityHostname) {
  const relayResult = await verifyRelayVisibility({
    relayUrls: _allRelayUrls,
    bidderDid: atproto.did,
    collection: OFFERING_NSID,
    log: logger,
  });
  if (relayResult.ok) {
    logger.info("relay_visibility_confirmed", {
      indexedBy: relayResult.indexedBy,
      capableRelays: relayResult.capableRelays,
    });
  } else {
    logger.warn("relay_visibility_failed", {
      capableRelays: relayResult.capableRelays,
      failures: relayResult.failures,
      hint: "Requester-side discovery via listReposByCollection will not find this bidder. The periodic offering refresh may eventually fix this.",
    });
  }
}
