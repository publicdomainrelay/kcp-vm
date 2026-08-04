// Production fedproxy.com only-me policy integration test.
// Validates: requester posts RFP with policy=only-me, a bidder whose operator
// IS the requester receives the RFP, evaluates only-me to allow, bids, and the
// RFP/bid/accept/receipt cycle completes against production fedproxy.com.
//
// The requester-side plumbing under test: buildPolicyRecord mints a
// policies.builtin record, attaches its strongRef to the RFP's `policies`
// field, and the bidder's trust cache resolves operatorOf(bidder)=requester (via a
// bidder_associate badgeBlueKeys record minted with --associate-with), so the
// attached only-me policy admits the bid. --policy open on the bidder only
// opens the bidder's OWN engagement gate -- the RFP's attached only-me policy is
// still enforced bidder-side in onRfp and is the thing under test.
//
// Spawns hono-bidder CLI subprocess. Uses real plc.directory + xrpc.fedproxy.com
// so the relay can resolve all DIDs. Auto-skips when prod infra unreachable.
//
// Runs on macOS (container), Linux (docker), WSL2 (docker), Windows (wsl docker).
//
//   deno test --allow-all test/bidder_policy_only_me_integration_test.ts
//
// Set DENO_TEST_PROD=1 to force-run even if probe reachability fails.

import { assert } from "@std/assert";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import {
  createRequesterPDS, ensureWebsocat, runComputeContract,
} from "@publicdomainrelay/requester-xrpc";

// ===========================================================================
// Helpers
// ===========================================================================

const encoder = new TextEncoder();

async function hasCommand(cmd: string): Promise<boolean> {
  try {
    const { code } = await new Deno.Command("which", {
      args: [cmd], stdout: "null", stderr: "null",
    }).output();
    return code === 0;
  } catch { return false; }
}

async function probeRtt(url: string, timeoutMs = 5000): Promise<number | null> {
  try {
    const ac = new AbortController();
    const t = setTimeout(() => ac.abort(), timeoutMs);
    const start = Date.now();
    const r = await fetch(url, { signal: ac.signal });
    clearTimeout(t);
    await r.body?.cancel();
    // Any response (including 404) proves the host is reachable.
    return Date.now() - start;
  } catch { return null; }
}

// ===========================================================================
// Subprocess bidder spawner
// ===========================================================================

interface BidderProcess {
  did: string;
  cleanup: () => void;
}

async function spawnBidder(opts: {
  modPath: string;
  args: string[];
  label: string;
}): Promise<BidderProcess> {
  const decoder = new TextDecoder();
  const cmd = new Deno.Command("deno", {
    args: ["run", "-A", opts.modPath, ...opts.args],
    stdout: "piped",
    stderr: "piped",
  });
  const child = cmd.spawn();
  let killed = false;
  const cleanup = () => {
    killed = true;
    try { child.kill("SIGTERM"); } catch { /* already exited */ }
  };

  const { promise, resolve, reject } = Promise.withResolvers<string>();

  (async () => {
    const reader = child.stderr.getReader();
    let buf = "";
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const chunk = decoder.decode(value, { stream: true });
        buf += chunk;
        while (true) {
          const nl = buf.indexOf("\n");
          if (nl < 0) break;
          Deno.stderr.writeSync(encoder.encode(`[${opts.label}] ${buf.slice(0, nl)}\n`));
          buf = buf.slice(nl + 1);
        }
      }
    } catch { /* stream closed */ }
  })();

  (async () => {
    const reader = child.stdout.getReader();
    let buf = "";
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const chunk = decoder.decode(value, { stream: true });
        buf += chunk;
        while (true) {
          const nl = buf.indexOf("\n");
          if (nl < 0) break;
          const line = buf.slice(0, nl).trim();
          buf = buf.slice(nl + 1);
          if (!line) continue;
          try {
            const parsed = JSON.parse(line);
            if (parsed.event === "bidder_ready" && parsed.did) {
              resolve(parsed.did);
              return;
            }
          } catch { /* not JSON */ }
        }
      }
    } catch { /* stream closed */ }
    if (!killed) reject(new Error(`[${opts.label}] process exited without bidder_ready`));
  })();

  const timeout = setTimeout(() => {
    if (!killed) reject(new Error(`[${opts.label}] bidder_ready timeout after 60s`));
  }, 60_000);

  try {
    const did = await promise;
    clearTimeout(timeout);
    return { did, cleanup };
  } catch (e) {
    clearTimeout(timeout);
    cleanup();
    throw e;
  }
}

// ===========================================================================
// Test
// ===========================================================================

const ORG = new URL("../../", import.meta.url).pathname.replace(/\/$/, "");
const HONO_BIDDER = `${ORG}/atproto-market/hono-bidder/mod.ts`;

const PROD_PLC = "https://plc.directory";
const PROD_DISPATCHER = "xrpc.fedproxy.com";
const PROD_FEDPROXY = "fedproxy.com";

// Declaration-time reachability probe (top-level await -- runs once at module
// load). This is a live-prod integration test: it needs the real PLC directory,
// the fedproxy ingress relay, and fedproxy.com up, and it registers a fresh
// did:plc per run. When any hard dependency is unreachable the test is
// `ignore: true` (auto-skip for the RIGHT reason -- a probe fired, not a hard
// env constant). Set DENO_TEST_PROD=1 to force-run regardless of reachability;
// the runtime flow then fails loudly with the real error.
const FORCE_PROD = Deno.env.get("DENO_TEST_PROD") === "1";
const PROD_PROBE = await Promise.all([
  probeRtt(`${PROD_PLC}/`),
  probeRtt(`https://${PROD_DISPATCHER}/.well-known/did.json`),
  probeRtt(`https://${PROD_FEDPROXY}/`),
]);
const PROD_REACHABLE = PROD_PROBE.every((r) => r !== null);
if (!FORCE_PROD && !PROD_REACHABLE) {
  const missing = [PROD_PLC, PROD_DISPATCHER, PROD_FEDPROXY]
    .filter((_, i) => PROD_PROBE[i] === null);
  console.log(`[SKIP] prod infrastructure unreachable: ${missing.join(", ")}`);
}

Deno.test({
  name: "prod fedproxy.com -- policy=only-me RFP flow",
  sanitizeOps: false,
  sanitizeResources: false,
  ignore: !FORCE_PROD && !PROD_REACHABLE,
}, async (t) => {
  console.log(
    `[RTT] plc=${PROD_PROBE[0] ?? "N/A"}ms disp=${PROD_PROBE[1] ?? "N/A"}ms fedproxy=${PROD_PROBE[2] ?? "N/A"}ms`,
  );

  const logger = createLogger({ serviceName: "prod-only-me" });
  const cleanups: Array<() => void> = [];

  await ensureWebsocat(logger).catch(() => {});
  if (!(await hasCommand("websocat"))) {
    console.log("[SKIP] websocat not installed");
    return;
  }

  await t.step("[bidder-prod-only-me] policy=only-me RFP flow", async () => {
    // -- Create requester PDS first -- its DID becomes the only-me operator --
    const requesterServe = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
    const requester = await createRequesterPDS({
      logger, serve: requesterServe,
      plcDirectoryUrl: PROD_PLC,
      ingressProxyHost: PROD_DISPATCHER,
      label: "requester-prod-only-me",
    });
    cleanups.push(() => requesterServe.shutdown());
    await requester.beginServe();

    // -- Spawn hono-bidder subprocess ----------------------------------
    // Fresh identity per run: unique temp key + PDS state, so the bidder never
    // collides with the persisted ~/.cache/pdr-market/bidder-private-key (a
    // live production bidder DID -- reusing it cross-contaminates the run).
    // --associate-with mints the bidder_associate badgeBlueKeys record that
    // makes the boot-time trust-cache refresh resolve operatorOf(bidder)=
    // requester, which is what the RFP's attached only-me policy checks before
    // admitting a bid. --policy open keeps the bidder's own engagement gate
    // from rejecting the fresh requester before the attached policy runs.
    // No --firehose-* flags: the requester PUSHES the RFP to this bidder, and
    // passing a wss firehose URL into the relay-registration path breaks PDS
    // visibility (requestCrawl cannot handle a wss scheme).
    const tmp = await Deno.makeTempDir({ prefix: "pdr-onlyme-" });
    const proc = await spawnBidder({
      modPath: HONO_BIDDER,
      args: [
        "--ingress-proxy-host", PROD_DISPATCHER,
        "--plc-directory-url", PROD_PLC,
        "--compute-provider-local",
        "--compute-provider-local-mode", "container",
        "--serve-port", "0",
        "--skip-qr",
        "--policy", "open",
        "--associate-with", requester.did,
        "--private-key-hex-path", `${tmp}/bidder-key`,
        "--pds-state-path", `${tmp}/bidder-pds`,
      ],
      label: "hono-bidder-prod-only-me",
    });
    cleanups.push(proc.cleanup);

    // -- Run compute contract with policy=only-me ------------------
    //
    // This exercises the requester-side policy plumbing inside
    // runComputeContract: when a policy is set, buildPolicyRecord mints a
    // policies.builtin record naming the policy and carrying its args, then
    // attaches its strongRef to the RFP's `policies` field before signing.
    //
    // skipSsh=true avoids provisioning a guest -- we only need to verify
    // the RFP/bid/accept cycle succeeds.
    const result = await runComputeContract(requester, {
      logger,
      ingressProxyHost: PROD_DISPATCHER,
      fedingressHost: PROD_FEDPROXY,
      rbac: true,
      skipSsh: true,
      keepVm: true,
      extraBidderDids: [proc.did],
      denyBidderDids: ["did:plc:centraldefaultbidder000000"],
      policy: { name: "only-me", args: { bidWindowSec: 15 } },
    });

    // -- Assertions ----------------------------------------------------
    assert(result.event === "compute_request_complete",
      `[prod-only-me] expected compute_request_complete, got ${result.event}: ${result.error ?? ""}`);
    assert(typeof result.bids === "number" && result.bids > 0,
      `[prod-only-me] expected >0 bids, got ${result.bids}`);
    // Under enforced only-me, no OTHER production bidder can win: their
    // operator is not the requester, so the attached policy denies them. The
    // winner must be OUR spawned bidder -- the only bidder whose operator IS the
    // requester (via the bidder_associate record minted by --associate-with).
    assert(result.winnerDid === proc.did,
      `[prod-only-me] expected winner to be the associated bidder ${proc.did}, got ${result.winnerDid}`);
    assert(result.receiptOk === true,
      `[prod-only-me] expected receipt verification to pass`);
  });

  // =======================================================================
  // Cleanup
  // =======================================================================
  for (const c of cleanups.reverse()) {
    try { await c(); } catch { /* best effort */ }
  }
  await new Promise((r) => setTimeout(r, 200));
});
