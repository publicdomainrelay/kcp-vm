import { assert, assertEquals } from "@std/assert";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createKcpIdentityPrompt } from "@publicdomainrelay/identity-prompt-kcp";
import { findSecretMaterial } from "@publicdomainrelay/identity-prompt-abc";
import type { MarketIdentity } from "@publicdomainrelay/market-identity-abc";
import { createFileIdentityVault } from "@publicdomainrelay/market-identity-vault-file";
import {
  completeOAuthQrWithHonoPds,
  createAssociationCaller,
  createFakeKcp,
  serveOnPort0,
  startFakeQrRelay,
  startOAuthPds,
  startPlcDirectory,
  waitFor,
} from "./helpers/market_identity_fixtures.ts";

const MARKET_ROOT = new URL("../", import.meta.url).pathname;
const BIDDER_ENTRY = `${MARKET_ROOT}hono-bidder/mod.ts`;
const CLIS = ["hono-bidder", "request-vm-ssh"];

const OPTION_ENV = /^(ATP_|OAUTH_|IDENTITY_VAULT_|MARKET_IDENTITY_|REPO_PRIVATE_KEY|PDS_STATE_PATH|ASSOCIATE_WITH|SKIP_QR|RELAY_URL|FIREHOSE_|INGRESS_PROXY_HOST|PLC_DIRECTORY_URL|SERVE_|PORT_FILE|LISTENERS_FILE|STATUS_FILE|CONTRACT_STORE_DIR|COMPUTE_PROVIDER_|CA_CERT_PEM|GUEST_TLS_PORT|CONFIG_PATH_|STATE_DIR|QR_ORIGIN|HOME$)/;

interface Proc {
  lines: Array<Record<string, unknown>>;
  stdout(): string;
  ready: Promise<Record<string, unknown>>;
  exited: Promise<Deno.CommandStatus>;
  exitedEarly(): boolean;
  stop(): Promise<void>;
}

function spawnBidder(args: string[], env: Record<string, string>): Proc {
  const child = new Deno.Command(Deno.execPath(), {
    args: ["run", "-A", "--node-modules-dir=none", "--unstable-kv", "--unstable-worker-options", BIDDER_ENTRY, ...args],
    cwd: MARKET_ROOT,
    stdin: "null",
    stdout: "piped",
    stderr: "piped",
    clearEnv: true,
    env: { ...Object.fromEntries(Object.entries(Deno.env.toObject()).filter(([k]) => !OPTION_ENV.test(k))), ...env },
  }).spawn();
  const lines: Array<Record<string, unknown>> = [];
  const raw: string[] = [];
  const ready = Promise.withResolvers<Record<string, unknown>>();
  ready.promise.catch(() => {});
  let done = false;
  const dec = new TextDecoder();
  const pump = async (stream: ReadableStream<Uint8Array>, label: string) => {
    let buf = "";
    for await (const chunk of stream) {
      const text = dec.decode(chunk, { stream: true });
      if (label === "out") raw.push(text);
      buf += text;
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        console.error(`[bidder-${label}] ${line}`);
        try {
          const o = JSON.parse(line) as Record<string, unknown>;
          lines.push(o);
          if (o.event === "bidder_ready") ready.resolve(o);
        } catch { /* terminal text */ }
      }
    }
  };
  const pumps = Promise.all([pump(child.stdout, "out"), pump(child.stderr, "err")]);
  const exited = child.status.then(async (s) => {
    done = true;
    await pumps.catch(() => {});
    ready.reject(new Error(`bidder exited with ${s.code} before bidder_ready`));
    return s;
  });
  return {
    lines,
    stdout: () => raw.join(""),
    ready: ready.promise,
    exited,
    exitedEarly: () => done,
    async stop() {
      try {
        child.kill("SIGTERM");
      } catch { /* gone */ }
      await exited;
    },
  };
}

function withTimeout<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    p.finally(() => clearTimeout(timer)),
    new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error(`${what} within ${ms}ms`)), ms);
    }),
  ]);
}

async function filesUnder(dir: string): Promise<string[]> {
  const out: string[] = [];
  try {
    for await (const e of Deno.readDir(dir)) {
      const p = `${dir}/${e.name}`;
      if (e.isDirectory) out.push(...await filesUnder(p));
      else out.push(p);
    }
  } catch { /* absent */ }
  return out;
}

Deno.test("hono-bidder and request-vm-ssh reach every interactive step through the IdentityPrompt port", async () => {
  for (const cli of CLIS) {
    const src = await Deno.readTextFile(`${MARKET_ROOT}${cli}/mod.ts`);
    const options = await Deno.readTextFile(`${MARKET_ROOT}${cli}/cli-args-env.ts`);
    for (const [what, pattern] of [
      ["a stdin prompt", /\bprompt\(/],
      ["a QR drawn by the CLI", /\bqrcode\(/],
      ["a browser spawned by the CLI", /xdg-open|"open"|"cmd"/],
      ["a loopback listener in the CLI", /startLoopbackCallbackServer/],
      ["an exit on an expired session", /oauth_session_expired_shutting_down|OAuthSessionExpiredError/],
      ["the legacy QR session file helpers", /tryRestoreOAuthQRSession|saveOAuthQRSession|pollForOAuthSession/],
    ] as const) {
      assert(!pattern.test(src), `${cli}/mod.ts still has ${what}`);
    }
    assert(src.includes("@publicdomainrelay/identity-prompt-tty"), `${cli} renders through the tty prompt`);
    assert(src.includes("@publicdomainrelay/identity-prompt-kcp"), `${cli} can follow a kcp MarketIdentity`);
    assert(src.includes("startMarketIdentityReconciler"), `${cli} signs in through the MarketIdentity reconciler`);
    for (const option of ["oauth-session-path", "oauth-session-file"]) {
      const block = options.slice(options.indexOf(`"${option}"`), options.indexOf("}", options.indexOf(`"${option}"`)));
      assert(!/HOME|USERPROFILE/.test(block), `${cli} --${option} has no default under $HOME`);
    }
    assert(options.includes(`"state-dir"`), `${cli} has a --state-dir for its session store`);
    assert(options.includes(`"market-identity-kcp-server"`), `${cli} can follow a kcp MarketIdentity`);
  }
});

Deno.test({
  name: "hono-bidder with stdin closed and no --skip-qr: the association QR is in its kcp MarketIdentity and an approval patched there completes it",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const plc = await startPlcDirectory();
  const kcp = createFakeKcp("root:bob");
  const kcpServed = await kcp.serve();
  const dispatcher = createRelayFactory({ hostname: "relay.localhost" }).createApp();
  const disp = await serveOnPort0(dispatcher.fetch);
  const dispPort = new URL(disp.url).port;
  const state = await Deno.makeTempDir({ prefix: "identity-cli-" });
  const home = await Deno.makeTempDir({ prefix: "identity-cli-home-" });
  const identity: MarketIdentity = {
    apiVersion: "socialweb.computer/v1alpha1",
    kind: "MarketIdentity",
    metadata: { name: "bidder" },
    spec: { mode: "localRepo", association: "RequireApproval" },
  };
  kcp.put(identity);
  const bidder = spawnBidder([
    "--market-identity-kcp-server", kcpServed.url,
    "--market-identity-kcp-workspace", "root:bob",
    "--state-dir", state,
    "--private-key-hex-path", `${state}/repo-private-key.hex`,
    "--pds-state-path", `${state}/pds-state`,
    "--plc-directory-url", plc.url,
    "--ingress-proxy-host", `relay.localhost:${dispPort}`,
    "--qr-origin", "https://qr.example",
    "--serve-port", "0", "--no-ingress-proxy", "--firehose-mode", "off", "--offering-refresh-sec", "0", "--policy", "open",
  ], { HOME: home });
  try {
    const scan = await waitFor("the association QR in status", () => {
      const s = kcp.read("bidder")?.status;
      return s?.phase === "AwaitingScan" && s.pendingAction?.kind === "scanQr" ? s : null;
    }, 60_000);
    const did = scan.did!;
    assertEquals(scan.pendingAction!.url, `https://qr.example/#bdr=${did}`);
    const doc = await (await fetch(`${plc.url}/${did}`)).json() as { service: Array<{ id: string; serviceEndpoint: string }> };
    const endpoint = new URL(doc.service.find((s) => s.id === "#atproto_pds")!.serviceEndpoint);
    const caller = await createAssociationCaller(plc.url, "carol.test");
    const confirmed = caller.confirm({
      did,
      fetch: (r) => {
        const u = new URL(r.url);
        return dispatcher.fetch(new Request(`http://${endpoint.hostname}:${dispPort}${u.pathname}${u.search}`, r));
      },
    });
    const ask = await waitFor("approveAssociation in status", () => {
      const s = kcp.read("bidder")?.status;
      return s?.phase === "AwaitingApproval" && s.pendingAction?.callerDid === caller.did ? s : null;
    }, 30_000);
    assertEquals(ask.pendingAction!.handle, "carol.test");
    assert(!bidder.exitedEarly(), "a closed stdin is not an answer; the bidder waits on its object");
    await createKcpIdentityPrompt({ server: kcpServed.url, workspace: "root:bob" }).answerAssociation("bidder", caller.did, "approve");
    const res = await withTimeout(confirmed, 30_000, "the associate call answered");
    assertEquals(res.status, 200, await res.clone().text());
    await res.body?.cancel();
    const ready = await withTimeout(bidder.ready, 60_000, "bidder_ready");
    assertEquals(ready.did, did);
    const status = kcp.read("bidder")!.status!;
    assertEquals(status.phase, "Ready");
    assertEquals(status.associations, [caller.did]);
    for (const w of kcp.writes) assertEquals(findSecretMaterial(w.body ? JSON.parse(w.body) : null), [], `${w.method} ${w.path}`);
    assertEquals(await filesUnder(home), [], "nothing under $HOME");
    assert(await createFileIdentityVault({ path: `${state}/identity-vault.json` }).read({ mount: "bidder", path: "identity/bidder/keys" }));
  } finally {
    await bidder.stop();
    await Promise.all([plc.close(), kcpServed.close(), disp.close()]);
  }
});

Deno.test({
  name: "hono-bidder --atproto-oauth-qr: the QR is drawn on the terminal, the scan completes through hono-pds session minting, and the session lives under --state-dir, never $HOME",
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const plc = await startPlcDirectory();
  const qr = await startFakeQrRelay();
  const pds = await startOAuthPds();
  const state = await Deno.makeTempDir({ prefix: "identity-cli-qr-" });
  const home = await Deno.makeTempDir({ prefix: "identity-cli-qr-home-" });
  const args = [
    "--atproto-oauth-qr", "--atproto-handle", "alice.test",
    "--state-dir", state,
    "--private-key-hex-path", `${state}/repo-private-key.hex`,
    "--pds-state-path", `${state}/pds-state`,
    "--plc-directory-url", plc.url,
    "--qr-origin", qr.url,
    "--serve-port", "0", "--no-ingress-proxy", "--firehose-mode", "off", "--offering-refresh-sec", "0", "--policy", "open",
  ];
  try {
    const alice = await pds.createAccount("alice.test");
    const first = spawnBidder(args, { HOME: home });
    try {
      const pending = await waitFor("the QR pending action", () =>
        first.lines.find((l) => l.message === "market_identity_pending_action" && l.kind === "scanQr"), 60_000);
      const url = String(pending.url);
      assert(url.startsWith(`${qr.url}/#oauth=`), url);
      await waitFor("the QR url on the terminal", () => first.stdout().includes(url));
      await completeOAuthQrWithHonoPds({ action: { kind: "scanQr", url }, pds, account: alice });
      const ready = await withTimeout(first.ready, 60_000, "bidder_ready");
      assertEquals(ready.did, alice.did);
    } finally {
      await first.stop();
    }
    const vault = createFileIdentityVault({ path: `${state}/identity-vault.json` });
    const session = await vault.read({ mount: "bidder", path: "identity/bidder/session" });
    assertEquals(session?.userDid, alice.did, "the session is in the state dir's store");
    assertEquals(await filesUnder(home), [], "nothing under $HOME");

    const second = spawnBidder(args, { HOME: home });
    try {
      const ready = await withTimeout(second.ready, 60_000, "bidder_ready after a restart");
      assertEquals(ready.did, alice.did);
      assert(!second.lines.some((l) => l.message === "market_identity_pending_action"), "a restart restores the session with no scan");
      assert(second.lines.some((l) => l.message === "oauth_qr_session_restored"));
    } finally {
      await second.stop();
    }
  } finally {
    await Promise.all([plc.close(), qr.close(), pds.close()]);
  }
});
