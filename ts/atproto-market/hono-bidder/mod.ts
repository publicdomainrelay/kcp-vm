import { Command } from "@publicdomainrelay/cli-args-env";
import { isValidPolicyMode, type PolicyMode } from "@publicdomainrelay/market-policy-abc";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import type { MarketBidderProviderRef } from "@publicdomainrelay/market-bidder-abc";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createComputeProviderDenoWorker, createWorkerProviderHooks } from "@publicdomainrelay/market-bidder-worker";
import { createATProto, createLocalPDSAgent, createRemoteAgent, createOAuthAgent, createOAuthAgentFromSession, pollForOAuthSession, tryRestoreOAuthQRSession, saveOAuthQRSession, OAuthSessionExpiredError } from "@publicdomainrelay/atproto-helpers";
import type { LocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { startLoopbackCallbackServer, oauthClientMetadata } from "@publicdomainrelay/atproto-oauth-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { ACCEPT_NSID, EVENT_NSID, OFFERING_NSID, RFP_NSID } from "@publicdomainrelay/market-common";
import { verifyRelayVisibility } from "@publicdomainrelay/requester-xrpc";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { createPlcDirectoryClient, createGenesisOp, PlcClient, PlcNotFoundError } from "@publicdomainrelay/did-plc";
import { createDigitalOceanComputeProvider } from "@publicdomainrelay/compute-provider-digitalocean";
import { createLocalComputeProvider } from "@publicdomainrelay/compute-provider-local";
import { createOidcProvisioningEnricher } from "@publicdomainrelay/oidc-issuer-hono";
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
  } catch { /* file missing — will generate and save below */ }
}

const keypair = resolvedPrivateKeyHex
  ? await Secp256k1Keypair.import(resolvedPrivateKeyHex, { exportable: true })
  : await Secp256k1Keypair.create({ exportable: true });

const privateKeyHex = resolvedPrivateKeyHex ??
  Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

const ingressProxyHost = (options.ingressProxyHost as string) || "xrpc.fedproxy.com";
// Set in OAuth QR path — bidder's did:plc (owns local keypair).
let oauthPlcDid: string | undefined;

const plcDirectoryUrl = (options.plcDirectoryUrl as string) || "https://plc.directory";

// Auto-detect local dev: *.localhost isn't in DNS.  If the dispatcher is
// reachable at a local host, patch fetch so the bidder can resolve the
// requester's PDS endpoints (also on *.localhost) through the relay.
// Also patches plc.directory → local PLC when plcDirectoryUrl is local.
const isLocalDev = ingressProxyHost.includes("localhost") || ingressProxyHost.startsWith("127.");
const _plcHost = (() => { try { return new URL(plcDirectoryUrl).hostname; } catch { return plcDirectoryUrl; } })();
const isLocalPlc = _plcHost === "localhost" || _plcHost.startsWith("127.") || _plcHost === "0.0.0.0";
if (isLocalDev || isLocalPlc) {
  const patchPort = ingressProxyHost.includes(":") ? ingressProxyHost.split(":").pop()! : "80";
  const realFetch = globalThis.fetch;
  globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
    let url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const m = url.match(/^https:\/\/([^/]+)(\/.*)?$/);
    if (m && m[1].endsWith(".localhost")) {
      let host = m[1];
      if (!host.includes(":")) host = `${host}:${patchPort}`;
      url = `http://${host}${m[2] ?? ""}`;
      return realFetch(url, init);
    }
    if (isLocalPlc && url.startsWith("https://plc.directory/")) {
      url = plcDirectoryUrl + url.slice("https://plc.directory".length);
      return realFetch(url, init);
    }
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;
}

// Each relay gets its own keypair so subscribers never share a subdomain/FQDN
// on the dispatcher (collision would route everyone to the last connector).
async function cliCreateIngress() {
  const relayKeypair = await Secp256k1Keypair.create({ exportable: true });
  // Service auth JWTs use the local keypair (owns bidder's did:plc).
  // OAuth QR mode: atproto.did is the user's Bluesky DID — use oauthPlcDid instead.
  const bidderDid = oauthPlcDid ?? atproto.did;
  const localSigner = {
    did: () => bidderDid,
    sign: async (bytes: Uint8Array) => keypair.sign(bytes),
  };
  return createIngress({ logger, ingressProxyHost, signer: localSigner, keypair: relayKeypair });
}

// Full OAuth scope — single source of truth for registered + loopback clients.
// Matches did-key-associator/oauth-client-metadata.json canonical list.
const OAUTH_SCOPE_FULL = [
  "atproto",
  // Collection writes
  "repo:com.publicdomainrelay.temp.market.offering?action=create",
  "repo:com.publicdomainrelay.temp.market.offering?action=update",
  "repo:com.publicdomainrelay.temp.auth.allowlist.rbacDid?action=create",
  "repo:com.publicdomainrelay.temp.market.bids.free?action=create",
  "repo:com.publicdomainrelay.temp.market.bid?action=create",
  "repo:com.publicdomainrelay.temp.market.receipt?action=create",
  "repo:com.publicdomainrelay.temp.market.event?action=create",
  "repo:com.publicdomainrelay.temp.badgeBlueKeys?action=create",
  "repo:com.publicdomainrelay.temp.market.bidderAssociation?action=create",
  "repo:com.publicdomainrelay.temp.compute.config.wif.simple?action=create",
  "repo:com.publicdomainrelay.temp.compute.vm?action=create",
  "repo:com.publicdomainrelay.temp.market.rfp?action=create",
  "repo:com.publicdomainrelay.temp.market.accept?action=create",
  "repo:com.publicdomainrelay.temp.compute.events.vm.delete?action=create",
  "repo:com.publicdomainrelay.temp.compute.events.vm.onNetwork?action=create",
  "repo:com.fedproxy.rbac?action=create",
  // RPC endpoints
  "rpc:com.publicdomainrelay.temp.market.submitRfp?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitAccept?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitBid?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitEvent?aud=*",
];

// deno-lint-ignore no-explicit-any
let atprotoAgent: any;
let pdsHostname: string | undefined;
let isLocal = false;
let isOAuth = false;
const AUTO_REFRESH_THRESHOLD_MS = 3_600_000; // 1 hour — proactively refresh access token before it expires

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
if ((options.atprotoOauth as boolean)) {
  const handle = options.atprotoHandle as string | undefined;
  if (!handle) {
    logger.error("--atproto-oauth requires --atproto-handle");
    Deno.exit(1);
  }
  // OAuth flow — use remote PDS via ATProto OAuth
  const sessionPath = options.oauthSessionPath as string;
  const oauthAgent = await createOAuthAgent({
    handle: options.atprotoHandle as string,
    sessionPath,
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: OAUTH_SCOPE_FULL.join(" "),
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
} else if ((options.atprotoOauthQr as boolean)) {
  // QR-based OAuth — scan with phone, session transferred via qr.fedfork.com
  // Register DID on PLC (needed for service auth JWT verification)
  const plcClient = createPlcDirectoryClient({ plcDirectoryUrl });
  const genesisOp = await createGenesisOp({
    rotationKeys: [keypair.did()],
    verificationMethods: { atproto: keypair.did() },
    sign: (bytes: Uint8Array) => keypair.sign(bytes),
  });
  const plcDid = genesisOp.did;
  oauthPlcDid = plcDid;
  const qrSigner = { did: () => plcDid, sign: (bytes: Uint8Array) => keypair.sign(bytes) };
  try {
    await plcClient.resolve(plcDid);
    logger.info("oauth_qr_did_resolved", { did: plcDid });
  } catch (err) {
    if (err instanceof PlcNotFoundError) {
      await plcClient.submitOp(plcDid, genesisOp.op);
      logger.info("oauth_qr_did_registered", { did: plcDid });
    } else {
      throw err;
    }
  }

  // Try restoring saved OAuth QR session
  const _restoredAgent = await tryRestoreOAuthQRSession({
    logger, label: "bidder", handle: options.atprotoHandle as string | undefined,
    autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
    // No onSessionExpired here — restore handles expiry internally
    // (delete file, return null → falls through to QR auth).
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
    const qrUrl = `https://qr.fedfork.com/#oauth=${encodeURIComponent(plcDid)}&n=${oauthNonce}`;
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
      qrFedforkOrigin: "https://qr.fedfork.com",
      logger,
    });

    // Create OAuth agent from transferred session
    const oauthAgent = await createOAuthAgentFromSession(session, {
      logger,
      autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
      onSessionExpired: createSessionExpiredHandler("bidder"),
      saveSession: (s) => saveOAuthQRSession(s, { label: "bidder", handle: s.handle }),
    });
    atprotoAgent = oauthAgent;

    // Persist session for future restarts
    await saveOAuthQRSession(session, { label: "bidder", handle: session.handle });

    isOAuth = true;
    pdsHostname = undefined;
    logger.info("oauth_qr_session_ready", { userDid: session.userDid, handle: session.handle });
  }
} else if ((options.atprotoHandle as string | undefined) && (options.atprotoPassword as string | undefined)) {
  const pdsStatePath = options.pdsStatePath as string | undefined;
  // Relay-only: no TCP listener. associateConfirm arrives via relay →
  // app.fetch programmatic. subscribeRepos firehose is wired via
  // directSubscriptionHandler (in-process callback, no loopback WS).
  const pdsServe = createServe({ logger });
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: pdsServe,
    plcDirectoryUrl,
    ingressProxyHost,
    storagePath: pdsStatePath,
    associateServiceId: BIDDER_ASSOC_SERVICE,
  });
  await atprotoAgent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  isLocal = true;
  const relayHost: string = (atprotoAgent as { relay?: { ingressHost?: string } }).relay?.ingressHost ?? "";
  if (relayHost) {
    pdsHostname = relayHost;
  }
} else {
  // Default: ephemeral PDS + association QR. Bidder owns its PLC DID;
  // user links their Bluesky account by scanning the QR code.
  const pdsStatePath = options.pdsStatePath as string | undefined;
  const pdsServe = createServe({ logger });
  atprotoAgent = await createLocalPDSAgent({
    logger, keypair,
    serve: pdsServe,
    plcDirectoryUrl,
    ingressProxyHost,
    storagePath: pdsStatePath,
    associateServiceId: BIDDER_ASSOC_SERVICE,
  });
  await atprotoAgent.beginServe();
  _deferredPdsPort = pdsServe.tcpPort;
  isLocal = true;
  const relayHost: string = (atprotoAgent as { relay?: { ingressHost?: string } }).relay?.ingressHost ?? "";
  if (relayHost) {
    pdsHostname = relayHost;
  }

  // Show association QR so user can link their Bluesky account
  if (!options.noQr) {
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
      logger.info("existing_association_found", { did: atprotoAgent.did, hint: "skipping QR — prior association exists" });
    } else {
      const qrUrl = `https://qr.fedfork.com/#plc=${atprotoAgent.did}`;
      logger.info("qr_url", { url: qrUrl });
      const qr = qrcode(qrUrl, { output: "console", ecl: "HIGH" });
      console.log(qr);

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
  badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex }),
  plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
  agent: atprotoAgent,
});

const cliRelayUrl = (options.relayUrl as string) || "";

const eventStreams = createDefaultATProtoEventStreamsClient({
  additionalRelays: cliRelayUrl ? [cliRelayUrl] : [],
  log: logger,
});

// Collect relay URLs for PDS registration.
const relayUrls = eventStreams.relays.map((r) => r.url);

// Collect local relay URLs for deferred registration after serve starts.
for (const url of relayUrls) {
  if (url.startsWith("http://127.0.0.1:") || url.startsWith("http://localhost:")) {
    _deferredRelayUrls.push(url);
  }
}

if (pdsHostname) {
  for (const url of relayUrls) {
    await Promise.race([
      atproto.requestCrawl(url, pdsHostname),
      new Promise<void>((r) => setTimeout(r, 5_000)),
    ]);
  }
} else if (cliRelayUrl) {
  logger.warn("relay_no_hostname_for_registration", {
    reason: "could not determine PDS hostname for relay registration",
  });
}

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
// Shared accept→receipt map — created early so provider guest routes can use it.
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
    oidcProvisioner: createOidcProvisioningEnricher(() => relay.ingressUrl),
    rbacProvisioner: createRbacProvisioner(),
    containerMode: options.computeProviderLocalMode as "vm" | "container" | undefined,
    vmImage: options.computeProviderLocalVmImage as string | undefined,
    containerImage: options.computeProviderLocalContainerImage as string | undefined,
    cacheDir: options.computeProviderLocalCacheDir as string | undefined,
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
// Skip ingress in OAuth mode — PDS is remote, no relay subscriber needed.
const bidderIngress = (options.noIngressProxy || isOAuth) ? undefined : await cliCreateIngress();
const bidderServe = createServe({
  logger,
  tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.servePort as number) ?? 0 },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
  relays: bidderIngress ? [bidderIngress] : [],
});

// OAuth client metadata endpoint — serves registered-client metadata.
bidderServe.app.get("/oauth-client-metadata.json", (_c: { json(obj: Record<string, unknown>): Response }) => {
  return _c.json(oauthClientMetadata({
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: OAUTH_SCOPE_FULL.join(" "),
    clientName: "Compute Provider (hono-bidder)",
  }));
});

const policyModeRaw = options.policyMode as string | undefined;
const policyMode = isValidPolicyMode(policyModeRaw) ? policyModeRaw : undefined;

const bidder = await createMarketBidder({
  logger, atproto, providers, relay: bidderIngress,
  eventStreams,
  offeringRefreshMs: offeringRefreshSec > 0 ? offeringRefreshSec * 1000 : undefined,
  serve: bidderServe,
  policyMode,
  onSessionExpired: isOAuth ? createSessionExpiredHandler("bidder") : undefined,
  acceptToContract,
});

function shutdown() {
  bidder.shutdown();
  if (isOAuth && atprotoAgent && typeof (atprotoAgent as Record<string, unknown>).dispose === "function") {
    ((atprotoAgent as Record<string, unknown>).dispose as () => void)();
  }
  for (const s of serves) s.shutdown();
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
if (!options.noQr) {
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
  logger.info("existing_association_found", { did: atproto.did, hint: "skipping QR — prior association exists" });
}

if (!options.noQr && !hasAssociation) {
  if (!isLocal) {
    logger.warn("qr_association_requires_local_pds", {
      hint: "QR association only works with a local PDS (no --atproto-handle). Use --no-qr to skip.",
    });
  } else {
    const qrUrl = `https://qr.fedfork.com/#bdr=${atproto.did}`;
    logger.info("qr_url", { url: qrUrl });
    const qr = qrcode(qrUrl, { output: "console", ecl: "HIGH" });
    console.log(qr);

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
// Non-blocking on failure — bidder still boots, just warns.
const _allRelayUrls = [...relayUrls, ..._deferredRelayUrls.map((u) => `http://${u}`)];
const _visibilityHostname = pdsHostname ?? (_localServePort > 0 ? `127.0.0.1:${_localServePort}` : undefined);

// Re-request crawl on ALL relays now that the offering record is committed.
// The initial requestCrawl (during atprotoAgent.beginServe) ran before the
// offering was created/corrected. Production relays need a second ping so they
// re-subscribe and see the fresh offering commit.
if (_visibilityHostname) {
  for (const url of relayUrls) {
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
