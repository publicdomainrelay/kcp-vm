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
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

let runtimeConfig: Record<string, unknown> | null = null;
try { runtimeConfig = (await import("./config.json", { with: { type: "json" } })).default; } catch { /* optional */ }
const { options } = await new Command("CONFIG_PATH_REQUEST_VM_SSH", cliArgsEnv, runtimeConfig).resolve();

const label = (options.label as string) ?? "request-vm-ssh";
const logger = createLogger({ serviceName: label });

const dispatcherHost = (options.dispatcherHost as string) || "xrpc.fedproxy.com";
const relayUrl = options.relayUrl as string | undefined;

const splitDids = (s: string | undefined): string[] =>
  s ? s.split(",").map((d) => d.trim()).filter(Boolean) : [];
const extraBidderDids = splitDids(options.bidderDids as string | undefined);
const denyBidderDids = splitDids(options.denyBidderDids as string | undefined);

await ensureWebsocat(logger);
logger.info("requester_starting", { label, dispatcherHost, relayUrl: relayUrl ?? "(none)" });

const serve = createServe({
  logger,
  tcp: { addr: (options.serveAddr as string) || "0.0.0.0", port: (options.port as number) ?? 0 },
  unix: (options.serveUnix as string | undefined) ? { socketPath: options.serveUnix as string } : undefined,
});

const pds = await createRequesterPDS({
  logger,
  serve,
  privateKeyHex: options.privateKeyHex as string | undefined,
  plcDirectoryUrl: (options.plcDirectoryUrl as string) || "https://plc.directory",
  dispatcherHost,
  label,
});

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

const result = await runComputeContract(pds, {
  logger,
  dispatcherHost,
  vmName: options.vmName as string | undefined,
  bidWindowSec: options.bidWindowSec as number,
  skipSsh: options.skipSsh as boolean,
  execProgram: options.exec as string,
  noDelete: options.noDelete as boolean,
  vmReadyTimeoutSec: options.vmReadyTimeoutSec as number,
  extraBidderDids,
  denyBidderDids,
  relayUrl,
  offeringWatcherDids: () => [...offeringDids],
  sshProvider: createSshSessionProvider(logger),
  onSshStart: () => pauseConsole(),
  onSshEnd: () => resumeConsole(),
});

logger.info("result", result as unknown as Record<string, unknown>);
