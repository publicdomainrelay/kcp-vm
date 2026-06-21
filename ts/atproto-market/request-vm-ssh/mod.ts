// @publicdomainrelay/request-vm-ssh
// Compute market requester: create RFPs, collect bids, provision VMs, SSH in.
//
// Library exports (when imported):
//   createRequesterPDS, runComputeContract, createSshSessionProvider,
//   ensureWebsocat, discoverBiddersFromRelay, createRequesterFactory
//
// CLI (when run directly via `deno run mod.ts` or compiled binary):
//   Resolves config, creates PDS, subscribes to relay, runs contract flow.

// ── library re-exports ──────────────────────────────────────────────────

import type { RequesterPDSImpl } from "@publicdomainrelay/requester-xrpc";

export {
  createRequesterPDS,
  runComputeContract,
  createSshSessionProvider,
  ensureWebsocat,
  discoverBiddersFromRelay,
} from "@publicdomainrelay/requester-xrpc";
export type { RequesterPDSImpl } from "@publicdomainrelay/requester-xrpc";

export { createRequesterFactory } from "@publicdomainrelay/hono-factory-requester-xrpc";
export type { RequesterFactoryOptions } from "@publicdomainrelay/hono-factory-requester-xrpc";

export type {
  RequesterPDS,
  ContractFlowOptions,
  ContractFlowResult,
  CollectedBid,
  SshSessionProvider,
  ConsoleBuffer,
  PDSOptions,
} from "@publicdomainrelay/requester-abc";

// ── CLI ─────────────────────────────────────────────────────────────────

// CLI only runs when invoked directly. Library consumers get exports only.
if (import.meta.main) {
  const { Command } = await import("@publicdomainrelay/cli-args-env");
  const { createStructuredLogger } = await import("@publicdomainrelay/logger");
  const {
    createRequesterPDS: _createRequesterPDS,
    runComputeContract: _runComputeContract,
    createSshSessionProvider: _createSshSessionProvider,
    ensureWebsocat: _ensureWebsocat,
    discoverBiddersFromRelay: _discoverBiddersFromRelay,
  } = await import("@publicdomainrelay/requester-xrpc");
  const { createRequesterFactory } = await import("@publicdomainrelay/hono-factory-requester-xrpc");
  const { IdResolver } = await import("@atproto/identity");
  const cliArgsEnvMod = await import("./cli-args-env.json", { with: { type: "json" } });
  const cliArgsEnv = cliArgsEnvMod.default;

  // ── config ──────────────────────────────────────────────────────────

  let runtimeConfig: Record<string, unknown> | null = null;
  try {
    const mod = await import("./config.json", { with: { type: "json" } });
    runtimeConfig = mod.default;
  } catch { /* optional */ }

  const { options } = await new Command(
    "CONFIG_PATH_REQUEST_VM_SSH",
    cliArgsEnv,
    runtimeConfig,
  ).resolve();

  const port = options.port as number;
  const privateKeyHex = options.privateKeyHex as string | undefined;
  const plcDirectoryUrl = options.plcDirectoryUrl as string;
  const dispatcherHost = options.dispatcherHost as string;
  const label = options.label as string;
  const vmName = options.vmName as string | undefined;
  const bidWindowSec = options.bidWindowSec as number;
  const execProgram = options.exec as string;
  const vmReadyTimeoutSec = options.vmReadyTimeoutSec as number;
  const noDelete = options.noDelete as boolean;
  const skipSsh = options.skipSsh as boolean;
  const bidderDidsStr = options.bidderDids as string | undefined;
  const denyBidderDidsStr = options.denyBidderDids as string | undefined;
  const relayUrl = options.relayUrl as string | undefined;

  const extraBidderDids = bidderDidsStr
    ? bidderDidsStr.split(",").map((s) => s.trim()).filter(Boolean)
    : [];
  const denyBidderDids = denyBidderDidsStr
    ? denyBidderDidsStr.split(",").map((s) => s.trim()).filter(Boolean)
    : [];

  // ── logger ───────────────────────────────────────────────────────────

  const log = createStructuredLogger(label);

  // ── websocat ─────────────────────────────────────────────────────────

  await _ensureWebsocat();
  log.info("websocat_ready");

  // ── PDS + relay ──────────────────────────────────────────────────────

  log.info("requester_starting", { label, dispatcherHost, relayUrl: relayUrl ?? "(none)" });

  const pds = await _createRequesterPDS({
    port,
    privateKeyHex,
    plcDirectoryUrl,
    dispatcherHost,
    label,
  });

  // ── submitBid handler ────────────────────────────────────────────────

  await createRequesterFactory({
    app: (pds as RequesterPDSImpl).app,
    idResolver: new IdResolver(),
    did: pds.did,
    serviceIds: ["pdr_temp_market"],
    onBid: ({ uri, cid, record, issuerDid }) => {
      const rfpUri = (
        record.rfp as { uri?: string } | undefined
      )?.uri;
      if (!rfpUri) return;
      const queue = pds.pendingBids.get(rfpUri) ?? [];
      queue.push({ did: issuerDid ?? "unknown", uri, cid, record: record as Record<string, unknown> });
      pds.pendingBids.set(rfpUri, queue);
      log.info("bid_queued", { callerDid: issuerDid, uri, rfpUri });
    },
    log,
  });

  // ── relay ready ──────────────────────────────────────────────────────

  const { proxyRef, subdomain } = await pds.relayReady;
  (pds as RequesterPDSImpl).proxyRef = proxyRef;
  (pds as RequesterPDSImpl).relaySubdomain = subdomain;

  // ── console buffer (for interactive SSH) ─────────────────────────────

  const _origLog = console.log.bind(console);
  const _origErr = console.error.bind(console);
  const _origStderrWrite = Deno.stderr.write.bind(Deno.stderr);
  const _origStdoutWrite = Deno.stdout.write.bind(Deno.stdout);
  const _buf: Array<Uint8Array> = [];

  function pauseConsole(): void {
    console.log = (...args: unknown[]) => {
      _buf.push(new TextEncoder().encode(args.map((a) => typeof a === "string" ? a : JSON.stringify(a)).join(" ") + "\n"));
    };
    console.error = (...args: unknown[]) => {
      _buf.push(new TextEncoder().encode(args.map((a) => typeof a === "string" ? a : JSON.stringify(a)).join(" ") + "\n"));
    };
    Deno.stderr.write = (data: Uint8Array) => { _buf.push(data); return Promise.resolve(data.length); };
    Deno.stdout.write = (data: Uint8Array) => { _buf.push(data); return Promise.resolve(data.length); };
  }

  async function resumeConsole(): Promise<void> {
    console.log = _origLog;
    console.error = _origErr;
    Deno.stderr.write = _origStderrWrite;
    Deno.stdout.write = _origStdoutWrite;
    for (const chunk of _buf) {
      await _origStderrWrite(chunk);
    }
    _buf.length = 0;
  }

  // ── contract flow ────────────────────────────────────────────────────

  const sshProvider = _createSshSessionProvider();

  const result = await _runComputeContract(pds, {
    vmName,
    bidWindowSec,
    skipSsh,
    execProgram,
    noDelete,
    vmReadyTimeoutSec,
    extraBidderDids,
    denyBidderDids,
    relayUrl,
    sshProvider,
    onSshStart: () => pauseConsole(),
    onSshEnd: () => resumeConsole(),
  });

  log.info("result", result as unknown as Record<string, unknown>);

  // ── lifecycle ────────────────────────────────────────────────────────

  const url = `https://${subdomain}.${dispatcherHost}`;
  log.info("requester_ready", { did: pds.did, subdomain, proxyRef, url });

  Deno.serve(
    { port, onListen: ({ port: p }) => log.info("listening", { port: p, did: pds.did }) },
    (pds as RequesterPDSImpl).app.fetch,
  );

  function stop(): void {
    pds.stop();
    log.info("shutting_down");
    Deno.exit();
  }

  Deno.addSignalListener("SIGINT", () => {
    log.info("signal", { signal: "SIGINT" });
    stop();
  });
  Deno.addSignalListener("SIGTERM", () => {
    log.info("signal", { signal: "SIGTERM" });
    stop();
  });
}
