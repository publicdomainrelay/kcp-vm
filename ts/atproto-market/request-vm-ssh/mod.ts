import { Command } from "@publicdomainrelay/cli-args-env";
import { isValidPolicyMode, type PolicyMode, DYNAMIC } from "@publicdomainrelay/market-policy-abc";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  createRequesterPDS,
  createOAuthRequester,
  runComputeContract,
  createSshSessionProvider,
  ensureWebsocat,
} from "@publicdomainrelay/requester-xrpc";
import { pollForOAuthSession, createOAuthAgentFromSession, tryRestoreOAuthQRSession, saveOAuthQRSession, OAuthSessionExpiredError } from "@publicdomainrelay/atproto-helpers";
import { startLoopbackCallbackServer } from "@publicdomainrelay/atproto-oauth-helpers";
import { createPlcDirectoryClient, createGenesisOp, PlcClient, PlcNotFoundError } from "@publicdomainrelay/did-plc";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import { EVENT_NSID, OFFERING_NSID } from "@publicdomainrelay/market-common";
import { createDefaultATProtoEventStreamsClient } from "@publicdomainrelay/atproto-event-streams-client";
import { qrcode } from "@libs/qrcode";
import { IdResolver } from "@atproto/identity";
import { Secp256k1Keypair } from "@atproto/crypto";
import cliArgsEnv from "./cli-args-env.ts";

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_REQUEST_VM_SSH", cliArgsEnv, runtimeConfig).resolve();

const label = (options.label as string) ?? "request-vm-ssh";
const logger = createLogger({ serviceName: label });

const ingressProxyHost = (options.ingressProxyHost as string) || "xrpc.fedproxy.com";

// Auto-detect local dev: *.localhost isn't in DNS.  Patch fetch so the
// requester can reach the bidder's PDS endpoints (also on *.localhost).
if (ingressProxyHost.includes("localhost") || ingressProxyHost.startsWith("127.")) {
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
    return realFetch(input as string | URL | Request, init);
  }) as typeof fetch;
}
const cliRelayUrl = options.relayUrl as string | undefined;

const eventStreams = createDefaultATProtoEventStreamsClient({
  additionalRelays: cliRelayUrl ? [cliRelayUrl] : [],
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

await ensureWebsocat(logger);
logger.info("requester_starting", { label, ingressProxyHost, relayUrls });

const serve = createServe({
  logger,
  tcp: (options.port != null) ? { addr: (options.serveAddr as string) || "127.0.0.1", port: options.port as number } : undefined,
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
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
  } catch { /* file missing — will generate and save below */ }
}

// Full OAuth scope — single source of truth for registered + loopback clients.
// Requester subset: compute.vm, market.rfp/accept/event, compute.events, badgeBlueKeys,
// fedproxy.rbac, + all four RPC endpoints.
const OAUTH_SCOPE_FULL = [
  "atproto",
  "repo:com.publicdomainrelay.temp.compute.vm?action=create",
  "repo:com.publicdomainrelay.temp.market.rfp?action=create",
  "repo:com.publicdomainrelay.temp.market.accept?action=create",
  "repo:com.publicdomainrelay.temp.market.event?action=create",
  "repo:com.publicdomainrelay.temp.compute.events.vm.delete?action=create",
  "repo:com.publicdomainrelay.temp.compute.events.vm.onNetwork?action=create",
  "repo:com.publicdomainrelay.temp.badgeBlueKeys?action=create",
  "repo:com.fedproxy.rbac?action=create",
  "rpc:com.publicdomainrelay.temp.market.submitRfp?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitAccept?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitBid?aud=*",
  "rpc:com.publicdomainrelay.temp.market.submitEvent?aud=*",
];

let pds: RequesterPDS;
let isOAuth = false;
let _oauthAgentForDispose: { dispose(): void } | null = null;
const AUTO_REFRESH_THRESHOLD_MS = 3_600_000;

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
    setTimeout(() => Deno.exit(1), 500);
  };
}

if ((options.atprotoOauth as boolean) && (options.atprotoHandle as string | undefined)) {
  // OAuth requester — no local PDS, firehose-based discovery
  const oauthHandle = await createOAuthRequester({
    handle: options.atprotoHandle as string,
    sessionPath: (options.oauthSessionPath as string) ||
      `${Deno.env.get("HOME") ?? "/tmp"}/.cache/pdr-market/requester-oauth-session.json`,
    clientId: options.oauthClientId as string | undefined,
    redirectUri: options.oauthRedirectUri as string | undefined,
    scope: OAUTH_SCOPE_FULL.join(" "),
    plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory",
    logger,
    attestationKp: await (async () => {
      const kp = resolvedPrivateKeyHex
        ? await (await import("@atproto/crypto")).Secp256k1Keypair.import(resolvedPrivateKeyHex, { exportable: true })
        : await (await import("@atproto/crypto")).Secp256k1Keypair.create({ exportable: true });
      const { loadOrGenerateKeypair } = await import("@publicdomainrelay/market-atproto");
      const hex = Array.from(await kp.export()).map((b: number) => b.toString(16).padStart(2, "0")).join("");
      return loadOrGenerateKeypair(hex);
    })(),
    privateKeyHex: resolvedPrivateKeyHex ?? "",
  });

  const restored = await oauthHandle.restore();
  if (!restored) {
    const authUrl = await oauthHandle.startFlow();
    const redirectUri = (options.oauthRedirectUri as string) || "http://127.0.0.1:0/callback";
    const portMatch = redirectUri.match(/:(\d+)/);
    const port = portMatch ? parseInt(portMatch[1]) : 0;
    const cmd = Deno.build.os === "darwin" ? "open" : "xdg-open";
    new Deno.Command(cmd, { args: [authUrl] }).spawn();
    logger.info("oauth_browser_opened", { authUrl });
    const { promise, shutdown } = startLoopbackCallbackServer(port);
    const params = await promise;
    shutdown();
    await oauthHandle.completeFlow(params);
  }
  pds = oauthHandle.pds;
  isOAuth = true;
} else if ((options.atprotoOauthQr as boolean)) {
  // QR-based OAuth — scan with phone, session transferred via qr.fedfork.com
  const kp = resolvedPrivateKeyHex
    ? await Secp256k1Keypair.import(resolvedPrivateKeyHex, { exportable: true })
    : await Secp256k1Keypair.create({ exportable: true });
  // Register DID on PLC (needed for service auth JWT verification)
  const plcClient = createPlcDirectoryClient({ plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory" });
  const genesisOp = await createGenesisOp({
    rotationKeys: [kp.did()],
    verificationMethods: { atproto: kp.did() },
    sign: (bytes: Uint8Array) => kp.sign(bytes),
  });
  const plcDid = genesisOp.did;
  const pollSigner = { did: () => plcDid, sign: (bytes: Uint8Array) => kp.sign(bytes) };
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

  // Persist key for future runs
  if (privateKeyHexPath) {
    const hex = Array.from(await kp.export()).map((b: number) => b.toString(16).padStart(2, "0")).join("");
    await Deno.writeTextFile(privateKeyHexPath, hex);
  }

  // Try restoring saved OAuth QR session
  let _restoredOAuthAgent: any = null;
  let _session: any = null;
  const _restoredAgent = await tryRestoreOAuthQRSession({
    logger, label: "requester", handle: options.atprotoHandle as string | undefined,
    autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
    // No onSessionExpired here — restore handles expiry internally
    // (delete file, return null → falls through to QR auth).
  });
  if (_restoredAgent) {
    _restoredOAuthAgent = _restoredAgent;
    _oauthAgentForDispose = _restoredAgent;
    isOAuth = true;
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
    _session = await pollForOAuthSession({
      cliDid: plcDid,
      signer: pollSigner,
      qrFedforkOrigin: "https://qr.fedfork.com",
      logger,
    });
  }

  // Create requester PDS for market handler infrastructure, but use OAuth agent
  pds = await createRequesterPDS({
    logger,
    serve,
    privateKeyHex: resolvedPrivateKeyHex,
    plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory",
    ingressProxyHost,
    label,
    storagePath: options.pdsStatePath as string | undefined,
    skipIngress: !!options.noIngressProxy,
  });

  // Set OAuth agent on PDS
  if (_restoredOAuthAgent) {
    (pds as unknown as Record<string, unknown>).oauthAgent = _restoredOAuthAgent;
    (pds as unknown as Record<string, unknown>).oauthSession = _restoredOAuthAgent.sessionData;
    logger.info("oauth_qr_session_restored", { userDid: _restoredOAuthAgent.sessionData?.userDid });
  } else {
    const oauthAgent = await createOAuthAgentFromSession(_session, {
      logger,
      autoRefreshThresholdMs: AUTO_REFRESH_THRESHOLD_MS,
      onSessionExpired: createSessionExpiredHandler("requester"),
      saveSession: (s) => saveOAuthQRSession(s, { label: "requester", handle: s.handle }),
    });
    _oauthAgentForDispose = oauthAgent;
    (pds as unknown as Record<string, unknown>).oauthAgent = oauthAgent;
    (pds as unknown as Record<string, unknown>).oauthSession = _session;
    await saveOAuthQRSession(_session, { label: "requester", handle: _session.handle });
    logger.info("oauth_qr_session_ready", { userDid: _session.userDid, handle: _session.handle });
  }

  // Override record creation to use user's Bluesky PDS (firehose-visible).
  const _userAgent = (pds as unknown as Record<string, unknown>).oauthAgent as import("@publicdomainrelay/atproto-helpers").AtprotoAgentLike & { sessionData: { userDid: string } } | undefined;
  if (_userAgent) {
    pds.createRepoRecord = async (collection: string, record: Record<string, unknown>) => {
      const rkey = (await import("@atproto/common-web")).TID.next().toString();
      const { uri, cid } = await _userAgent.createRecord!(_userAgent.sessionData.userDid, collection, rkey, record);
      return { uri, cid };
    };
    pds.createSignedRepoRecord = async (collection: string, record: Record<string, unknown>, aKp?: { did(): string; privateKey: { bytes: Uint8Array } }, issuer?: string) => {
      const { attestationFor, toStorableEntry } = await import("@publicdomainrelay/market-atproto");
      const rkey = (await import("@atproto/common-web")).TID.next().toString();
      const att = attestationFor(aKp as import("@publicdomainrelay/market-atproto").AttestationKeypair, issuer);
      const entry = await att.sign({ record: record as Record<string, unknown>, repository: _userAgent.sessionData.userDid }) as import("@publicdomainrelay/market-atproto").InlineAttestation;
      const signed = { ...record, signatures: [toStorableEntry(entry)] };
      const { uri, cid } = await _userAgent.createRecord!(_userAgent.sessionData.userDid, collection, rkey, signed);
      return { uri, cid };
    };
  }

  isOAuth = true;
} else {
  pds = await createRequesterPDS({
    logger,
    serve,
    privateKeyHex: resolvedPrivateKeyHex,
    plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory",
    ingressProxyHost,
    label,
    storagePath: options.pdsStatePath as string | undefined,
    skipIngress: !!options.noIngressProxy,
  });
  // Persist generated key to path for future runs.
  if (privateKeyHexPath) {
    try {
      await Deno.writeTextFile(privateKeyHexPath, pds.privateKeyHex);
      if (resolvedPrivateKeyHex) {
        // Already had it — rewrite same value (idempotent).
      } else {
        logger.info("private_key_generated_and_saved", { path: privateKeyHexPath });
      }
    } catch (err) {
      logger.warn("private_key_save_failed", { path: privateKeyHexPath, error: String(err) });
    }
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

if (!isOAuth) {
  await pds.beginServe();
}
logger.info("requester_ready", { did: pds.did, ingressRef: isOAuth ? "(oauth)" : pds.ingressRef, ingressProxyHost });

const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
let hasAssociation = false;
if (!options.skipQr) {
  if (isOAuth) {
    // OAuth mode: check badgeBlueKeys on user's PDS via OAuth agent.
    const oa = (pds as unknown as { oauthAgent?: { listRecords(did: string, coll: string, opts?: { limit?: number }): Promise<{ records: Array<{ value: Record<string, unknown> }> }> } }).oauthAgent;
    const userDid = (pds as unknown as { oauthSession?: { userDid: string } }).oauthSession?.userDid;
    if (oa && userDid) {
      const result = await oa.listRecords(userDid, BADGE_BLUE_KEYS_NSID, { limit: 100 });
      for (const rec of result.records) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === userDid && v.service === "requester_associate" && v.keyId === pds.did) {
          hasAssociation = true;
          break;
        }
      }
    }
  } else {
    let cursor: string | undefined;
    do {
      const result = await (pds as unknown as { api: { listRecords(did: string, coll: string, opts: { limit: number; cursor?: string }): Promise<{ records: Array<{ value: Record<string, unknown> }>; cursor?: string }> } }).api.listRecords(pds.did, BADGE_BLUE_KEYS_NSID, { limit: 100, cursor });
      for (const rec of result.records) {
        const v = rec.value as Record<string, unknown>;
        if (v.challenge === pds.did && v.service === "requester_associate") {
          hasAssociation = true;
          break;
        }
      }
      cursor = result.cursor;
    } while (cursor && !hasAssociation);
  }
}

// OAuth QR: user logged in as the operator account. The OAuth session
// IS the association proof — no second QR or badgeBlueKeys record needed.
if (isOAuth) {
  hasAssociation = true;
}

if (hasAssociation) {
  logger.info("existing_association_found", { did: pds.did, hint: "skipping QR — prior association exists" });
}

if (!options.skipQr && !hasAssociation) {
  const qrUrl = `https://qr.fedfork.com/#plc=${pds.did}`;
  logger.info("qr_url", { url: qrUrl });
  const qr = qrcode(qrUrl, { output: "console", ecl: "HIGH" });
  console.log(qr);

  logger.info("waiting_for_association", {
    hint: "Scan QR code, then confirm on your phone",
    requesterDid: pds.did,
  });
  const callerDid = await pds.associateCalled;

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
    pds.rejectAssociation(new Error("User rejected association"));
    Deno.exit(0);
  }
  pds.approveAssociation();
  logger.info("association_confirmed", { callerDid, handle });
}

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
  _oauthAgentForDispose?.dispose();
  serve.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

const policyModeRaw = options.policyMode as string | undefined;
const policyMode = isValidPolicyMode(policyModeRaw) ? policyModeRaw : undefined;
const policyEngineEndpoint = (policyMode === DYNAMIC) ? options.policyEngineEndpoint as string | undefined : undefined;

const result = await runComputeContract(pds, {
  logger,
  ingressProxyHost,
  fedingressHost: options.fedingressHost as string | undefined,
  vmName: options.vmName as string | undefined,
  bidWindowSec: options.bidWindowSec as number,
  skipSsh: options.skipSsh as boolean,
  execProgram: options.exec as string,
  keepVm: options.keepVm as boolean,
  vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
  extraBidderDids,
  denyBidderDids,
  relayUrls,
  baseUserData,
  rbac,
  policyMode,
  policyEngineEndpoint,
  offeringWatcherDids: () => [...offeringDids],
  eventStreams,
  sshProvider: createSshSessionProvider(logger),
  onSshStart: () => pauseConsole(),
  onSshEnd: () => resumeConsole(),
});

logger.info("result", result as unknown as Record<string, unknown>);
await pds.dispose();
shutdown();
