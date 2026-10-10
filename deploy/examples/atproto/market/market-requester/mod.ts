import { Command } from "@publicdomainrelay/cli-args-env";
import { createLogger, type StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  createRequesterPDS,
  createSshSessionProvider,
  runComputeContract,
} from "@publicdomainrelay/requester-xrpc";
import cliArgsEnv from "./cli-args-env.json" with { type: "json" };

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
  vmDeleteStatus?: number;
  vmDeleteOk?: boolean;
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

const localPort = ingressProxyHost!.includes(":") ? ingressProxyHost!.split(":").pop()! : "80";
const realFetch = globalThis.fetch;
globalThis.fetch = ((input: string | URL | Request, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  const m = url.match(/^https:\/\/([^/]+)(\/.*)?$/);
  if (m && (m[1].endsWith(".localhost") || m[1].includes(".localhost:"))) {
    const host = m[1].includes(":") ? m[1] : `${m[1]}:${localPort}`;
    return realFetch(`http://${host}${m[2] ?? ""}`, input instanceof Request ? input : init);
  }
  if (url.startsWith("https://plc.directory/")) {
    return realFetch(plcDirectoryUrl + url.slice("https://plc.directory".length), init);
  }
  return realFetch(input, init);
}) as typeof fetch;

const base = createLogger({ serviceName: "market-requester" });
const logger: StructuredLoggerInterface = {
  debug: (m, d) => base.debug(m, d),
  warn: (m, d) => base.warn(m, d),
  error: (m, d) => base.error(m, d),
  info: (m, d) => {
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
  plcDirectoryUrl: plcDirectoryUrl!,
  ingressProxyHost: ingressProxyHost!,
  label: "market-requester",
});
await pds.beginServe();
verdict.requesterDid = pds.did;

const ssh = createSshSessionProvider(logger);
const noSsh = {
  generateKeypair: (vmName: string) => ssh.generateKeypair(vmName),
  pollReady: (_key: string, target: string) => {
    verdict.onNetwork = target;
    verdict.onNetworkMs = Date.now() - started;
    logger.info("market_requester_onnetwork", { target, ms: verdict.onNetworkMs });
    return Promise.resolve(false);
  },
  runSession: () => Promise.reject(new Error("the test-bed requester never opens a session")),
};

try {
  const result = await runComputeContract(pds, {
    logger,
    ingressProxyHost: ingressProxyHost!,
    extraBidderDids: bidderDids,
    guestHostAliases,
    relayUrls: [],
    vmReadyTimeoutSec,
    sshProvider: noSsh,
    userData: { base: "#cloud-config\n" + JSON.stringify(guestBase) },
  });
  verdict.winnerDid = result.winnerDid;
  verdict.bids = result.bids;
  verdict.receiptOk = result.receiptOk;
  verdict.receiptUri = result.receiptUri;
  if (result.error) verdict.reason = `contract error: ${result.error}`;
  else if (!result.receiptOk) verdict.reason = "no valid receipt";
  else if (!verdict.onNetwork) verdict.reason = `the guest did not report onNetwork within ${vmReadyTimeoutSec}s`;
  else if (verdict.vmDeleteOk !== true) verdict.reason = `vm.delete was not accepted (status ${verdict.vmDeleteStatus})`;
  else {
    verdict.ok = true;
    verdict.reason = "guest reported onNetwork and the contract was released";
  }
} catch (err) {
  verdict.reason = `contract threw: ${err instanceof Error ? err.message : String(err)}`;
}

await Promise.resolve(pds.dispose?.()).catch(() => undefined);
await Promise.resolve(serve.shutdown()).catch(() => undefined);
await finish(verdict.ok ? 0 : 1);
