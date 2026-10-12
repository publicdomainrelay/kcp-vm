import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger, type StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  createRequesterPDS,
  createSshSessionProvider,
  ensureDumbpipe,
  runComputeContract,
} from "@publicdomainrelay/requester-xrpc";
import type { GuestHostKeys } from "@publicdomainrelay/requester-abc";
import { marketNetwork } from "@publicdomainrelay/market-network-common";
import { installMarketNetworkFetch } from "@publicdomainrelay/market-network-fetch";
import { releaseCompute, requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

interface Termination {
  reason: string;
  exitCode?: number;
  observedAt?: string;
  receivedAt: string;
}

interface Verdict {
  ok: boolean;
  reason: string;
  requesterDid?: string;
  winnerDid?: string;
  bids?: number;
  receiptOk?: boolean;
  receiptUri?: string;
  onNetwork?: string;
  onNetworkMs?: number;
  hostKeyFingerprints?: string[];
  hostKeyAlias?: string;
  sshCommand?: string;
  sshVerified?: boolean;
  sshVerifiedAt?: string;
  mismatchRefused?: boolean;
  mismatchRefusedAt?: string;
  vmDeleteStatus?: number;
  vmDeleteOk?: boolean;
  terminated?: Termination;
  lateDelete?: boolean;
  totalMs: number;
  startedAt: string;
  finishedAt?: string;
}

const { options } = await new Command("CONFIG_PATH_MARKET_REQUESTER", cliArgsEnv, null).resolve();

const splitList = (v: unknown): string[] =>
  String(v ?? "").split(",").map((s) => s.trim()).filter(Boolean);

const plcDirectoryUrl = options.plcDirectoryUrl as string | undefined;
const ingressProxyHost = options.ingressProxyHost as string | undefined;
const bidderDids = splitList(options.bidderDids);
const guestHostAliases = splitList(options.guestHostAliases);
const resultFile = options.resultFile as string;
const controlDir = options.controlDir as string | undefined;
const vmReadyTimeoutSec = options.vmReadyTimeoutSec as number;
const guestCaFile = options.guestCaFile as string | undefined;
const guestCaPem = guestCaFile ? (await Deno.readTextFile(guestCaFile)).trim() : "";
const guestJournalToConsole = options.guestJournalToConsole === true;
const guestCurlHome = options.guestCurlHome as string | undefined;
const guestBase: Record<string, unknown> = {};
const guestBootcmd: string[][] = [];
if (guestCaPem) guestBase.ca_certs = { trusted: [guestCaPem] };
if (guestCurlHome) {
  guestBootcmd.push(["systemctl", "set-environment", `CURL_HOME=${guestCurlHome}`]);
}
if (guestJournalToConsole) {
  guestBootcmd.push([
    "sh",
    "-c",
    "mkdir -p /etc/systemd/journald.conf.d && printf '[Journal]\\nForwardToConsole=yes\\nMaxLevelConsole=info\\nTTYPath=/dev/ttyS0\\n' > /etc/systemd/journald.conf.d/console.conf && systemctl restart systemd-journald",
  ]);
}
if (guestBootcmd.length > 0) guestBase.bootcmd = guestBootcmd;
const guestDiagnoseUrl = options.guestDiagnoseUrl as string | undefined;
if (guestDiagnoseUrl) {
  guestBase.runcmd = [[
    "sh",
    "-c",
    `systemctl show-environment; cat /root/.curlrc; curl -v --max-time 10 ${guestDiagnoseUrl} 2>&1 | tail -n 25`,
  ]];
}

const started = Date.now();
const verdict: Verdict = { ok: false, reason: "not finished", totalMs: 0, startedAt: new Date(started).toISOString() };

async function finish(code: number): Promise<never> {
  verdict.totalMs = Date.now() - started;
  verdict.finishedAt = new Date().toISOString();
  await Deno.writeTextFile(resultFile, JSON.stringify(verdict, null, 2) + "\n");
  console.log(JSON.stringify({ event: "market_requester_verdict", ...verdict }));
  Deno.exit(code);
}

if (!plcDirectoryUrl || !ingressProxyHost || bidderDids.length === 0) {
  verdict.reason = "plc-directory-url, ingress-proxy-host and bidder-dids are required";
  await finish(2);
}

const network = marketNetwork({ plcDirectoryUrl, ingressProxyHost, caBundleRef: guestCaFile });
installMarketNetworkFetch({ network });

const base = createLogger({ serviceName: "market-requester" });
let hostKeyMismatchSeen = false;
const logger: StructuredLoggerInterface = {
  debug: (m, d) => base.debug(m, d),
  warn: (m, d) => base.warn(m, d),
  error: (m, d) => base.error(m, d),
  info: (m, d) => {
    if (m === "vm_ssh_host_key_mismatch") hostKeyMismatchSeen = true;
    if (m === "vm_ssh_waiting" && d) verdict.sshCommand = d.sshCommand as string | undefined;
    if (m === "vm_delete_result" && d) {
      verdict.vmDeleteStatus = d.status as number;
      verdict.vmDeleteOk = d.ok as boolean;
    }
    base.info(m, d);
  },
};

const serve = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
const pds = await createRequesterPDS({
  logger,
  serve,
  plcDirectoryUrl: network.plcDirectoryUrl,
  ingressProxyHost: network.ingressProxyHost,
  label: "market-requester",
});
await pds.beginServe();
verdict.requesterDid = pds.did;

const ssh = createSshSessionProvider(logger, { dumbpipePath: await ensureDumbpipe(logger) });

async function forgedHostKeys(alias: string): Promise<GuestHostKeys> {
  const forged = await ssh.generateKeypair("forged-host-key");
  const [type, publicKey] = forged.publicKey.split(/\s+/);
  return { alias, keys: [{ type, publicKey }] };
}

const verifyingSsh = {
  generateKeypair: (vmName: string) => ssh.generateKeypair(vmName),
  pollReady: async (key: string, target: string, timeoutMs: number, signal?: AbortSignal, hostKeys?: GuestHostKeys) => {
    verdict.onNetwork = target;
    verdict.onNetworkMs = Date.now() - started;
    verdict.hostKeyFingerprints = hostKeys?.keys.map((k) => `${k.type} ${k.fingerprint}`);
    verdict.hostKeyAlias = hostKeys?.alias;
    logger.info("market_requester_onnetwork", { target, ms: verdict.onNetworkMs, hostKeys: verdict.hostKeyFingerprints });
    if (!hostKeys) return false;
    verdict.sshVerified = await ssh.pollReady(key, target, timeoutMs, signal, hostKeys);
    verdict.sshVerifiedAt = new Date().toISOString();
    logger.info("market_requester_ssh_verified", { ok: verdict.sshVerified, alias: hostKeys.alias });
    if (!verdict.sshVerified) return false;
    hostKeyMismatchSeen = false;
    const refusedReady = await ssh.pollReady(key, target, 60_000, signal, await forgedHostKeys(hostKeys.alias));
    verdict.mismatchRefused = !refusedReady && hostKeyMismatchSeen;
    verdict.mismatchRefusedAt = new Date().toISOString();
    logger.info("market_requester_mismatch_probe", { refused: verdict.mismatchRefused, ready: refusedReady });
    return false;
  },
  runSession: () => Promise.reject(new Error("the test-bed requester never opens a session")),
};

const userData = { base: "#cloud-config\n" + JSON.stringify(guestBase) };

async function writeControl(name: string, value: unknown): Promise<void> {
  const path = `${controlDir}/${name}`;
  await Deno.writeTextFile(`${path}.tmp`, JSON.stringify(value, null, 2) + "\n");
  await Deno.rename(`${path}.tmp`, path);
}

async function released(): Promise<void> {
  while (true) {
    try {
      await Deno.stat(`${controlDir}/release`);
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
}

async function runHeld(): Promise<void> {
  const vmName = `market-requester-${Date.now().toString(36)}`;
  const { publicKey } = await ssh.generateKeypair(vmName);
  const contract = await requestCompute(pds, {
    logger,
    ingressProxyHost: network.ingressProxyHost,
    extraBidderDids: bidderDids,
    guestHostAliases,
    relayUrls: [],
    vmReadyTimeoutSec,
    vmName,
    sshPublicKey: publicKey,
    userData,
  });
  let state = contract.state;
  verdict.winnerDid = state.winner?.did;
  verdict.bids = state.bids;
  verdict.receiptOk = state.receiptOk;
  verdict.receiptUri = state.receipt?.uri;
  if (!state.receiptOk || !state.vmAddress) {
    verdict.reason = state.receiptOk
      ? `the guest did not report onNetwork within ${vmReadyTimeoutSec}s`
      : `no valid receipt (${state.outcome ?? state.error ?? state.phase})`;
    if (state.receiptOk) await releaseCompute(pds, state, { logger, capabilities: contract.capabilities });
    await contract.dispose();
    return;
  }
  verdict.onNetwork = state.vmAddress;
  verdict.onNetworkMs = Date.now() - started;
  logger.info("market_requester_onnetwork", { target: state.vmAddress, ms: verdict.onNetworkMs });
  await writeControl("onnetwork.json", {
    vmAddress: state.vmAddress,
    receiptUri: verdict.receiptUri,
    winnerDid: verdict.winnerDid,
    bids: verdict.bids,
    onNetworkMs: verdict.onNetworkMs,
    at: new Date().toISOString(),
  });
  void contract.terminated?.then(async (ended) => {
    state = ended;
    verdict.terminated = {
      reason: ended.termination?.reason ?? "unknown",
      ...(ended.termination?.exitCode !== undefined ? { exitCode: ended.termination.exitCode } : {}),
      observedAt: ended.termination?.observedAt,
      receivedAt: new Date().toISOString(),
    };
    logger.info("market_requester_terminated", { ...verdict.terminated });
    await writeControl("terminated.json", verdict.terminated);
  });
  await released();
  verdict.lateDelete = state.termination !== undefined;
  await releaseCompute(pds, { ...state, termination: undefined }, { logger, capabilities: contract.capabilities });
  await contract.dispose();
  if (verdict.vmDeleteOk !== true) {
    verdict.reason = `vm.delete was not accepted (status ${verdict.vmDeleteStatus})`;
    return;
  }
  verdict.ok = true;
  verdict.reason = verdict.lateDelete
    ? `vm.delete after the bidder's vm.terminated (${verdict.terminated?.reason}) was accepted`
    : "the held contract was released";
}

if (controlDir) {
  try {
    await runHeld();
  } catch (err) {
    verdict.reason = `contract threw: ${err instanceof Error ? err.message : String(err)}`;
  }
  await Promise.resolve(pds.dispose?.()).catch(() => undefined);
  await Promise.resolve(serve.shutdown()).catch(() => undefined);
  await finish(verdict.ok ? 0 : 1);
}

try {
  const result = await runComputeContract(pds, {
    logger,
    ingressProxyHost: network.ingressProxyHost,
    extraBidderDids: bidderDids,
    guestHostAliases,
    relayUrls: [],
    vmReadyTimeoutSec,
    sshProvider: verifyingSsh,
    userData,
  });
  verdict.winnerDid = result.winnerDid;
  verdict.bids = result.bids;
  verdict.receiptOk = result.receiptOk;
  verdict.receiptUri = result.receiptUri;
  if (result.error) verdict.reason = `contract error: ${result.error}`;
  else if (!result.receiptOk) verdict.reason = "no valid receipt";
  else if (!verdict.onNetwork) verdict.reason = `the guest did not report onNetwork within ${vmReadyTimeoutSec}s`;
  else if (!verdict.hostKeyFingerprints) verdict.reason = "the guest's vm.hostKeys never arrived";
  else if (!verdict.sshVerified) verdict.reason = "ssh with StrictHostKeyChecking=yes against the reported key failed";
  else if (!verdict.mismatchRefused) verdict.reason = "ssh with a different known host key was not refused";
  else if (verdict.vmDeleteOk !== true) verdict.reason = `vm.delete was not accepted (status ${verdict.vmDeleteStatus})`;
  else {
    verdict.ok = true;
    verdict.reason = "guest reported onNetwork and host keys, ssh verified them, a mismatch was refused, and the contract was released";
  }
} catch (err) {
  verdict.reason = `contract threw: ${err instanceof Error ? err.message : String(err)}`;
}

await Promise.resolve(pds.dispose?.()).catch(() => undefined);
await Promise.resolve(serve.shutdown()).catch(() => undefined);
await finish(verdict.ok ? 0 : 1);
