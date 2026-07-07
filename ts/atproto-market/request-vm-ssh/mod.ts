import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  createRequesterPDS,
  runComputeContract,
  createSshSessionProvider,
  ensureWebsocat,
} from "@publicdomainrelay/requester-xrpc";
import { OFFERING_NSID } from "@publicdomainrelay/market-common";
import { createFirehoseWatcher as createSubscribeReposWatcher } from "@publicdomainrelay/firehose-watcher-subscriberepos";
import { createFirehoseWatcher as createJetstreamWatcher } from "@publicdomainrelay/firehose-watcher-jetstream";
import { qrcode } from "@libs/qrcode";
import { IdResolver } from "@atproto/identity";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_REQUEST_VM_SSH", cliArgsEnv, runtimeConfig).resolve();

const label = (options.label as string) ?? "request-vm-ssh";
const logger = createLogger({ serviceName: label });

const dispatcherHost = (options.dispatcherHost as string) || "xrpc.fedproxy.com";

// Auto-detect local dev: *.localhost isn't in DNS.  Patch fetch so the
// requester can reach the bidder's PDS endpoints (also on *.localhost).
if (dispatcherHost.includes("localhost") || dispatcherHost.startsWith("127.")) {
  const patchPort = dispatcherHost.includes(":") ? dispatcherHost.split(":").pop()! : "80";
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
const relayUrl = options.relayUrl as string | undefined;

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
logger.info("requester_starting", { label, dispatcherHost, relayUrl: relayUrl ?? "(none)" });

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

const pds = await createRequesterPDS({
  logger,
  serve,
  privateKeyHex: resolvedPrivateKeyHex,
  plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory",
  dispatcherHost,
  label,
  storagePath: options.pdsStatePath as string | undefined,
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

const offeringFirehoseMode = (options.offeringFirehoseMode as string) || "off";
const offeringFirehoseUrl = options.offeringFirehoseUrl as string | undefined;
const offeringDids = new Set<string>();
let offeringWatcher: { close(): void } | undefined;
if (offeringFirehoseMode !== "off" && offeringFirehoseUrl) {
  const make = offeringFirehoseMode === "jetstream" ? createJetstreamWatcher : createSubscribeReposWatcher;
  offeringWatcher = make({
    url: offeringFirehoseUrl,
    wantedCollections: [OFFERING_NSID],
    onRecord: (e) => { if (e.operation !== "delete") offeringDids.add(e.did); },
    log: logger,
  });
  logger.info("offering_firehose_watch_started", { mode: offeringFirehoseMode });
}

await pds.beginServe();
logger.info("requester_ready", { did: pds.did, proxyRef: pds.proxyRef, dispatcherHost });

const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
let hasAssociation = false;
if (!options.noQr) {
  let cursor: string | undefined;
  do {
    const result = await pds.api.listRecords(pds.did, BADGE_BLUE_KEYS_NSID, { limit: 100, cursor });
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

if (hasAssociation) {
  logger.info("existing_association_found", { did: pds.did, hint: "skipping QR — prior association exists" });
}

if (!options.noQr && !hasAssociation) {
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
  offeringWatcher?.close();
  serve.shutdown();
  Deno.exit();
}
Deno.addSignalListener("SIGINT", shutdown);
Deno.addSignalListener("SIGTERM", shutdown);

const policyModeRaw = options.policyMode as string | undefined;
const policyMode = (policyModeRaw === "only_me" || policyModeRaw === "direct_network" || policyModeRaw === "policy_based")
  ? policyModeRaw : undefined;
const policyEngineEndpoint = (policyMode === "policy_based") ? options.policyEngineEndpoint as string | undefined : undefined;

const result = await runComputeContract(pds, {
  logger,
  dispatcherHost,
  fedproxyHost: options.fedproxyHost as string | undefined,
  vmName: options.vmName as string | undefined,
  bidWindowSec: options.bidWindowSec as number,
  skipSsh: options.skipSsh as boolean,
  execProgram: options.exec as string,
  keepVm: options.keepVm as boolean,
  vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
  extraBidderDids,
  denyBidderDids,
  relayUrl,
  baseUserData,
  rbac,
  policyMode,
  policyEngineEndpoint,
  offeringWatcherDids: () => [...offeringDids],
  sshProvider: createSshSessionProvider(logger),
  onSshStart: () => pauseConsole(),
  onSshEnd: () => resumeConsole(),
});

logger.info("result", result as unknown as Record<string, unknown>);
await pds.dispose();
shutdown();
