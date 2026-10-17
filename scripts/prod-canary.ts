import { parseArgs } from "jsr:@std/cli@1/parse-args";
import { Secp256k1Keypair } from "@atproto/crypto";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { createOAuthLocalPds, type OAuthLocalPds } from "@publicdomainrelay/atproto-helpers";

const CLUSTER_NSID = "com.publicdomainrelay.temp.cluster.";
const K8S_NSID = "computer.socialweb.k8s.";

const args = parseArgs(Deno.args, {
  string: [
    "gateway",
    "audience",
    "creds",
    "creds-prefix",
    "pds",
    "ca-file",
    "name",
    "log",
    "image",
    "kubectl",
    "bidder-kubeconfig",
    "identity",
    "plc",
    "ingress-proxy-host",
    "web-origin",
  ],
  boolean: ["web-signin", "ingress-tls"],
  default: {
    identity: "ephemeral",
    plc: "https://plc.directory",
    "ingress-proxy-host": "xrpc.fedproxy.com",
    gateway: "https://k8s.socialweb.computer",
    audience: "did:web:k8s.socialweb.computer",
    creds: "~/.aliceoa_bsky_social_source",
    "creds-prefix": "ALICE_ATPROTO",
    pds: "https://bsky.social",
    log: "~/.local/state/swc-prod-canary/results.jsonl",
    image: "docker.io/library/busybox:1.36.1",
    kubectl: "kubectl",
    "ready-timeout": "600",
    "job-timeout": "1200",
    "delete-timeout": "900",
    "vm-timeout": "300",
    "total-timeout": "3600",
    interval: "5",
  },
});

function expand(path: string): string {
  return path.startsWith("~/") ? `${Deno.env.get("HOME")}/${path.slice(2)}` : path;
}

const gateway = args.gateway.replace(/\/+$/, "");
const webOrigin = (args["web-origin"] ?? args.gateway).replace(/\/+$/, "");
const ephemeral = args.identity === "ephemeral";
if (!ephemeral && args.identity !== "creds") throw new Error("--identity is ephemeral or creds");
const logPath = expand(args.log);
const readyTimeout = Number(args["ready-timeout"]) * 1000;
const jobTimeout = Number(args["job-timeout"]) * 1000;
const deleteTimeout = Number(args["delete-timeout"]) * 1000;
const vmTimeout = Number(args["vm-timeout"]) * 1000;
const totalTimeout = Number(args["total-timeout"]) * 1000;
const intervalMs = Number(args.interval) * 1000;

function utcDay(): string {
  return new Date().toISOString().slice(0, 10).replaceAll("-", "");
}

const clusterName = args.name ?? `canary-${utcDay()}`;
const jobName = `canary-${Date.now().toString(36)}`;
const marker = "canary-ok";

interface Step {
  step: string;
  ok: boolean;
  seconds: number;
  detail?: unknown;
}

const steps: Step[] = [];
const started = Date.now();
let cluster: string | null = null;
let failure: string | null = null;
let finished = false;

function note(step: string, since: number, ok: boolean, detail?: unknown): void {
  steps.push({ step, ok, seconds: Math.round((Date.now() - since) / 100) / 10, detail });
}

class CanaryError extends Error {}

const caCerts = args["ca-file"] ? [await Deno.readTextFile(expand(args["ca-file"]))] : undefined;
const client = caCerts ? Deno.createHttpClient({ caCerts }) : undefined;

function http(url: string, init: RequestInit & { timeoutMs?: number } = {}): Promise<Response> {
  const { timeoutMs, ...rest } = init;
  return fetch(url, {
    ...rest,
    signal: AbortSignal.timeout(timeoutMs ?? 60_000),
    ...(client ? { client } : {}),
  } as RequestInit);
}

async function readCreds(): Promise<{ handle: string; password: string }> {
  const text = await Deno.readTextFile(expand(args.creds));
  const vars = new Map<string, string>();
  for (const line of text.split("\n")) {
    const m = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(line);
    if (!m) continue;
    let v = m[2].trim();
    if (v.length >= 2 && (v[0] === "'" || v[0] === '"') && v.endsWith(v[0])) v = v.slice(1, -1);
    vars.set(m[1], v);
  }
  const handle = vars.get(`${args["creds-prefix"]}_HANDLE`);
  const password = vars.get(`${args["creds-prefix"]}_PASSWORD`);
  if (!handle || !password) {
    throw new CanaryError(`the credential file has no ${args["creds-prefix"]}_HANDLE and ${args["creds-prefix"]}_PASSWORD`);
  }
  return { handle, password };
}

interface Session {
  accessJwt: string;
  did: string;
  pds: string;
}

let session: Session | null = null;
let pds: OAuthLocalPds | null = null;
let tombstoned = false;
let webToken: string | null = null;

async function openEphemeral(): Promise<void> {
  const t = Date.now();
  const logger = createLogger({ serviceName: "prod-canary" });
  const serve = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
  pds = await createOAuthLocalPds({
    logger,
    keypair: await Secp256k1Keypair.create({ exportable: true }),
    serve,
    plcDirectoryUrl: args.plc.replace(/\/+$/, ""),
    ingressProxyHost: args["ingress-proxy-host"],
    tls: args["ingress-tls"],
  });
  await pds.beginServe();
  note("ephemeralIdentity", t, true, { did: pds.did, pds: pds.issuer });
}

function cookieOf(r: Response, name: string): string | null {
  for (const c of r.headers.getSetCookie()) {
    const [pair] = c.split(";");
    const i = pair.indexOf("=");
    if (pair.slice(0, i).trim() === name) return pair.slice(i + 1).trim();
  }
  return null;
}

async function webSignIn(): Promise<void> {
  const t = Date.now();
  const start = await http(`${webOrigin}/oauth/start`, {
    method: "POST",
    headers: { "content-type": "application/json", origin: webOrigin },
    body: JSON.stringify({ handle: pds!.did }),
  });
  if (!start.ok) throw new CanaryError(`/oauth/start answered ${start.status}: ${(await start.text()).slice(0, 300)}`);
  const state = cookieOf(start, "swc_oauth_state");
  const { authorizationUrl } = await start.json();
  if (!state || !authorizationUrl) throw new CanaryError("/oauth/start set no state cookie or gave no authorizationUrl");
  const callback = await pds!.approve(authorizationUrl);
  if (!callback.startsWith(`${webOrigin}/`)) throw new CanaryError(`the PDS redirected to ${new URL(callback).origin}, not the web app`);
  const back = await http(callback, { headers: { cookie: `swc_oauth_state=${state}` }, redirect: "manual" });
  await back.body?.cancel();
  webToken = cookieOf(back, "swc_session");
  if (!webToken) throw new CanaryError(`the callback answered ${back.status} ${back.headers.get("location") ?? ""} and set no session cookie`);
  const who = await web("GET", "/session");
  if (who.status !== 200 || who.body.did !== pds!.did) throw new CanaryError(`GET /session answered ${who.status} for ${JSON.stringify(who.body)}`);
  note("webSignIn", t, true, { did: who.body.did });
}

async function web(method: "GET" | "POST" | "DELETE", path: string, body?: unknown): Promise<Answer> {
  const r = await http(`${webOrigin}${path}`, {
    method,
    headers: {
      origin: webOrigin,
      cookie: `swc_session=${webToken}`,
      ...(body === undefined ? {} : { "content-type": "application/json" }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  try {
    return { status: r.status, body: JSON.parse(text) };
  } catch {
    return { status: r.status, body: text.slice(0, 300) };
  }
}

async function webListsCluster(): Promise<void> {
  const t = Date.now();
  const a = await web("POST", "/api/clusters/list", {});
  if (a.status !== 200) throw new CanaryError(`POST /api/clusters/list answered ${a.status}: ${JSON.stringify(a.body).slice(0, 300)}`);
  if (!(a.body.clusters ?? []).some((c: { name?: string }) => c.name === clusterName)) {
    throw new CanaryError(`POST /api/clusters/list through the web session does not list ${clusterName}`);
  }
  note("webClusterList", t, true, { clusters: a.body.clusters.length });
}

async function webSignOut(): Promise<string | null> {
  if (!webToken) return null;
  const t = Date.now();
  try {
    const a = await web("DELETE", "/session");
    webToken = null;
    if (a.status !== 200) return `DELETE /session answered ${a.status}`;
    note("webSignOut", t, true, a.body);
    return null;
  } catch (e) {
    return `DELETE /session failed: ${(e as Error).message}`;
  }
}

async function tombstone(): Promise<string | null> {
  if (!pds || tombstoned) return null;
  const t = Date.now();
  try {
    await pds.tombstone();
    tombstoned = true;
    const r = await http(`${args.plc.replace(/\/+$/, "")}/${pds.did}`);
    await r.body?.cancel();
    if (r.status !== 410) return `after the tombstone the PLC directory answered ${r.status} for ${pds.did}`;
    note("tombstone", t, true, { did: pds.did, plcStatus: r.status });
    return null;
  } catch (e) {
    return `tombstone of ${pds.did} failed: ${(e as Error).message}`;
  } finally {
    pds.stop();
  }
}

async function openSession(): Promise<Session> {
  const { handle, password } = await readCreds();
  const pds = args.pds.replace(/\/+$/, "");
  const r = await http(`${pds}/xrpc/com.atproto.server.createSession`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ identifier: handle, password }),
  });
  if (!r.ok) throw new CanaryError(`createSession answered ${r.status}`);
  const out = await r.json();
  let home = pds;
  const svc = (out.didDoc?.service ?? []).find((s: { id?: string }) => s.id?.endsWith("#atproto_pds"));
  if (svc?.serviceEndpoint && !args["ca-file"]) home = String(svc.serviceEndpoint).replace(/\/+$/, "");
  return { accessJwt: out.accessJwt, did: out.did, pds: home };
}

async function token(lxm: string): Promise<string> {
  if (pds) return await signServiceAuth(pds.signer as never, { aud: args.audience, lxm, expiresInSec: 120 });
  session ??= await openSession();
  const q = new URLSearchParams({ aud: args.audience, lxm });
  const r = await http(`${session.pds}/xrpc/com.atproto.server.getServiceAuth?${q}`, {
    headers: { authorization: `Bearer ${session.accessJwt}` },
  });
  if (!r.ok) {
    session = null;
    throw new CanaryError(`getServiceAuth for ${lxm} answered ${r.status}`);
  }
  return (await r.json()).token;
}

interface Answer {
  status: number;
  body: any;
}

async function xrpc(
  method: "GET" | "POST",
  nsid: string,
  opts: { params?: Record<string, string>; body?: unknown } = {},
): Promise<Answer> {
  const q = opts.params ? `?${new URLSearchParams(opts.params)}` : "";
  const r = await http(`${gateway}/xrpc/${nsid}${q}`, {
    method,
    headers: {
      authorization: `Bearer ${await token(nsid)}`,
      ...(opts.body === undefined ? {} : { "content-type": "application/json" }),
    },
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  });
  const text = await r.text();
  let body: any = text;
  try {
    body = JSON.parse(text);
  } catch {
    body = text.slice(0, 300);
  }
  return { status: r.status, body };
}

const transient = new Set([408, 425, 429, 500, 502, 503, 504]);

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

async function listClusters(): Promise<Array<Record<string, any>>> {
  const a = await xrpc("GET", `${CLUSTER_NSID}listClusters`);
  if (a.status !== 200) {
    throw new CanaryError(`${transient.has(a.status) ? "transient: " : ""}listClusters answered ${a.status}: ${JSON.stringify(a.body).slice(0, 300)}`);
  }
  return a.body.clusters ?? [];
}

async function waitFor<T>(
  what: string,
  timeoutMs: number,
  probe: () => Promise<T | null>,
): Promise<T> {
  const until = Date.now() + timeoutMs;
  let lastError = "";
  for (;;) {
    try {
      const v = await probe();
      if (v !== null) return v;
      lastError = "";
    } catch (e) {
      if (e instanceof CanaryError && !e.message.startsWith("transient:")) throw e;
      lastError = String((e as Error).message ?? e);
    }
    if (Date.now() > until) {
      throw new CanaryError(`timed out after ${Math.round(timeoutMs / 1000)}s waiting for ${what}${lastError ? `: ${lastError}` : ""}`);
    }
    await sleep(intervalMs);
  }
}

async function waitUnlisted(name: string): Promise<void> {
  await waitFor(`cluster ${name} to be unlisted`, deleteTimeout, async () => {
    const hit = (await listClusters()).find((c) => c.name === name);
    if (!hit) return true;
    if (hit.teardownBlocked) throw new CanaryError(`cluster ${name} teardown is blocked`);
    return null;
  });
}

function kubectlWords(): string[] {
  return args.kubectl.split(/\s+/).filter(Boolean);
}

async function computeVMs(): Promise<Set<string>> {
  const [bin, ...pre] = kubectlWords();
  const out = await new Deno.Command(bin, {
    args: [...pre, "--kubeconfig", expand(args["bidder-kubeconfig"]!), "get", "computevms", "-A", "-o", "json"],
    stdout: "piped",
    stderr: "piped",
  }).output();
  if (!out.success) {
    throw new CanaryError(`kubectl get computevms exited ${out.code}: ${new TextDecoder().decode(out.stderr).slice(0, 200)}`);
  }
  const items = JSON.parse(new TextDecoder().decode(out.stdout)).items ?? [];
  return new Set(items.map((i: any) => `${i.metadata?.namespace ?? ""}/${i.metadata?.name}`));
}

function jobManifest() {
  return {
    apiVersion: "batch/v1",
    kind: "Job",
    metadata: { name: jobName, namespace: "default" },
    spec: {
      backoffLimit: 0,
      template: {
        spec: {
          restartPolicy: "Never",
          containers: [{
            name: "run",
            image: args.image,
            command: ["sh", "-c", `echo ${marker} $(uname -m)`],
          }],
        },
      },
    },
  };
}

async function preflight(): Promise<string[]> {
  const t = Date.now();
  const leftovers: string[] = [];
  for (const c of await listClusters()) {
    if (!String(c.name).startsWith("canary-")) continue;
    leftovers.push(c.name);
    if (c.phase !== "Draining") {
      const a = await xrpc("POST", `${CLUSTER_NSID}deleteCluster`, { body: { name: c.name } });
      if (a.status !== 200 && a.status !== 202 && a.status !== 404) {
        throw new CanaryError(`deleteCluster of leftover ${c.name} answered ${a.status}`);
      }
    }
  }
  for (const name of leftovers) await waitUnlisted(name);
  note("preflight", t, true, { leftovers });
  return leftovers;
}

async function createAndWaitReady(): Promise<void> {
  let t = Date.now();
  const a = await xrpc("POST", `${CLUSTER_NSID}createCluster`, { body: { name: clusterName } });
  if (a.status !== 200) throw new CanaryError(`createCluster answered ${a.status}: ${JSON.stringify(a.body)}`);
  cluster = clusterName;
  note("createCluster", t, true, { phase: a.body.phase });
  t = Date.now();
  await waitFor(`cluster ${clusterName} to be Ready`, readyTimeout, async () => {
    const hit = (await listClusters()).find((c) => c.name === clusterName);
    if (!hit) return null;
    if (hit.phase === "Draining") throw new CanaryError(`cluster ${clusterName} went Draining while coming up`);
    return hit.phase === "Ready" ? hit : null;
  });
  note("clusterReady", t, true);
}

async function runJob(): Promise<unknown> {
  let t = Date.now();
  await waitFor("the Job to be accepted", jobTimeout, async () => {
    const a = await xrpc("POST", `${K8S_NSID}kubectlApplyServerSide`, {
      body: { cluster: clusterName, manifest: jobManifest() },
    });
    if (a.status >= 200 && a.status < 300) return true;
    if (transient.has(a.status) || a.status === 409) return null;
    throw new CanaryError(`kubectlApplyServerSide answered ${a.status}: ${JSON.stringify(a.body).slice(0, 300)}`);
  });
  note("applyJob", t, true, { job: jobName });
  t = Date.now();
  const job = await waitFor(`Job ${jobName} to finish`, jobTimeout, async () => {
    const a = await xrpc("GET", `${K8S_NSID}getJob`, {
      params: { cluster: clusterName, namespace: "default", name: jobName, tailLines: "20" },
    });
    if (a.status === 200) {
      if (a.body.phase === "Failed") throw new CanaryError(`Job ${jobName} Failed: ${JSON.stringify(a.body.conditions)}`);
      return a.body.phase === "Succeeded" ? a.body : null;
    }
    if (transient.has(a.status) || a.status === 404 || a.status === 409) return null;
    throw new CanaryError(`getJob answered ${a.status}: ${JSON.stringify(a.body).slice(0, 300)}`);
  });
  const logs = (job.pods ?? []).flatMap((p: any) => (p.containers ?? []).map((c: any) => String(c.log ?? "")));
  if (!logs.some((l: string) => l.includes(marker))) {
    throw new CanaryError(`Job ${jobName} Succeeded but no container log holds ${marker}`);
  }
  note("jobSucceeded", t, true, { logs: logs.map((l: string) => l.trim().slice(0, 120)) });
  return job;
}

async function deleteAndVerify(): Promise<void> {
  let t = Date.now();
  const a = await xrpc("POST", `${CLUSTER_NSID}deleteCluster`, { body: { name: clusterName } });
  if (a.status !== 200 && a.status !== 202) {
    throw new CanaryError(`deleteCluster answered ${a.status}: ${JSON.stringify(a.body)}`);
  }
  note("deleteCluster", t, true, { phase: a.body.phase });
  t = Date.now();
  await waitUnlisted(clusterName);
  cluster = null;
  note("clusterUnlisted", t, true);
}

async function main(): Promise<void> {
  const checkVMs = Boolean(args["bidder-kubeconfig"]);
  if (ephemeral) await openEphemeral();
  if (args["web-signin"]) await webSignIn();
  await preflight();
  const before = checkVMs ? await computeVMs() : new Set<string>();
  await createAndWaitReady();
  if (args["web-signin"]) await webListsCluster();
  await runJob();
  let mine: string[] = [];
  if (checkVMs) {
    const t = Date.now();
    mine = [...await computeVMs()].filter((v) => !before.has(v));
    if (mine.length === 0) throw new CanaryError("the Job passed and no new ComputeVM appeared on the bidder");
    note("computeVMsHeld", t, true, { vms: mine });
  }
  await deleteAndVerify();
  if (checkVMs) {
    const t = Date.now();
    await waitFor("the canary ComputeVMs to be gone", vmTimeout, async () => {
      const now = await computeVMs();
      return mine.some((v) => now.has(v)) ? null : true;
    });
    note("computeVMsGone", t, true, { vms: mine });
  } else {
    steps.push({ step: "computeVMsGone", ok: true, seconds: 0, detail: "skipped: no --bidder-kubeconfig" });
  }
}

async function bestEffortDelete(): Promise<string | null> {
  if (!cluster) return null;
  try {
    const a = await xrpc("POST", `${CLUSTER_NSID}deleteCluster`, { body: { name: cluster } });
    if (a.status === 200 || a.status === 202 || a.status === 404) return null;
    return `cleanup deleteCluster answered ${a.status}`;
  } catch (e) {
    return `cleanup deleteCluster failed: ${(e as Error).message}`;
  }
}

async function finish(): Promise<never> {
  if (finished) Deno.exit(1);
  finished = true;
  const leaked = cluster;
  const cleanupError = failure ? await bestEffortDelete() : null;
  const signOutError = await webSignOut();
  const tombstoneError = await tombstone();
  if (!failure && (signOutError || tombstoneError)) failure = signOutError ?? tombstoneError;
  const result = {
    ts: new Date(started).toISOString(),
    ok: failure === null,
    gateway,
    identity: pds ? pds.did : "creds",
    cluster: clusterName,
    job: jobName,
    seconds: Math.round((Date.now() - started) / 100) / 10,
    error: failure,
    leaked: failure && leaked ? leaked : undefined,
    cleanupError: cleanupError ?? undefined,
    signOutError: signOutError ?? undefined,
    tombstoneError: tombstoneError ?? undefined,
    steps,
  };
  const line = JSON.stringify(result);
  console.log(line);
  try {
    const dir = logPath.slice(0, logPath.lastIndexOf("/"));
    if (dir) await Deno.mkdir(dir, { recursive: true });
    await Deno.writeTextFile(logPath, line + "\n", { append: true, mode: 0o600 });
  } catch (e) {
    console.error(`could not append to ${logPath}: ${(e as Error).message}`);
    Deno.exit(1);
  }
  Deno.exit(failure === null ? 0 : 1);
}

const watchdog = setTimeout(() => {
  failure ??= `total timeout of ${Math.round(totalTimeout / 1000)}s reached`;
  finish();
}, totalTimeout);

try {
  await main();
} catch (e) {
  const cause = (e as { cause?: unknown }).cause;
  failure = `${(e as Error).message ?? String(e)}${cause ? `: ${String(cause)}` : ""}`;
}
clearTimeout(watchdog);
await finish();
