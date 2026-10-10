// Live instrument S1: one NodeClaim fulfilled end to end by a real firecracker
// guest, joining a k3s control plane that listens ONLY on this host's loopback.
//
// The guest reaches the cluster only through the links its user_data installs:
// the OpenBao link named by SOCIALWEB_K8S_LIVE_OPENBAO_TICKET (CA at
// SOCIALWEB_K8S_LIVE_OPENBAO_CA) and the apiserver link whose ticket it reads
// from OpenBao. The door holds no connection to the guest.
//
// Skipped unless SOCIALWEB_K8S_LIVE=1, and skipped with a printed reason when
// the firecracker harness's own environment is missing.
//
// No policy is attached: the local gha-lite policy engine runs inside a Worker
// and so needs --unstable-worker-options, which is not part of this file's
// invocation. The contract's policy is optional and nothing this instrument
// measures -- the guest, its tunnel, the reverse forward, the join -- is behind
// it. The bid window it leaves is the contract's default, 30s.
//
//   SOCIALWEB_FIRECRACKER_NODEIMAGE=/path/to/socialweb-nodeimage \
//   SOCIALWEB_FIRECRACKER_CONFIG=/path/to/nodeimage-market.json \
//   SOCIALWEB_FIRECRACKER_REPO_DIR=/path/to/socialweb-computer-kcp \
//   SOCIALWEB_FIRECRACKER_RUNNER_IMAGE=localhost/socialweb-firecracker-runner:local \
//   SOCIALWEB_FIRECRACKER_WORK_ROOT=/var/lib/socialweb/guests \
//   SOCIALWEB_FIRECRACKER_REUSE_IMAGE=1 \
//   SOCIALWEB_K8S_LIVE=1 deno test -A test/live_market_node_test.ts

import "../hono-socialweb-computer-k8s/undici-preload.ts";
import { assert, assertEquals } from "@std/assert";
import { Secp256k1Keypair } from "@atproto/crypto";
import { Hono } from "@hono/hono";
import { createLogger } from "@publicdomainrelay/logger";
import { createServe } from "@publicdomainrelay/serve";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import { createATProto, createLocalPDSAgent } from "@publicdomainrelay/atproto-helpers";
import { createBadgeBlueSigner } from "@publicdomainrelay/market-atproto";
import { createPlcDirectoryClient } from "@publicdomainrelay/did-plc";
import { createMarketBidder } from "@publicdomainrelay/market-bidder";
import { createComputeProviderHooks } from "@publicdomainrelay/market-bidder-compute";
import { createFirecrackerComputeProvider } from "@publicdomainrelay/compute-provider-firecracker";
import { createFirecrackerNodeImage } from "@publicdomainrelay/node-image-firecracker";
import { createFirecrackerMicrovm } from "@publicdomainrelay/microvm-firecracker";
import { createDockerBackend } from "@publicdomainrelay/container-backend-docker";
import type { ComputeAtproto } from "@publicdomainrelay/compute-provider-abc";
import { createRelayFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-xrpc";
import { createRequesterPDS } from "@publicdomainrelay/requester-xrpc";
import { requestCompute } from "@publicdomainrelay/compute-request-xrpc";
import { createMarketNodeFulfiller } from "@publicdomainrelay/socialweb-computer-k8s-node-market";
import type { NodeClaim, NodeClaimStore } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { FINALIZER_NODE_CLAIM, type NodeClaimStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { installFetchInterceptor } from "../../../atproto-market/test/fetch-interceptor.ts";

const ENV = {
  nodeimage: "SOCIALWEB_FIRECRACKER_NODEIMAGE",
  config: "SOCIALWEB_FIRECRACKER_CONFIG",
  repoDir: "SOCIALWEB_FIRECRACKER_REPO_DIR",
  runnerImage: "SOCIALWEB_FIRECRACKER_RUNNER_IMAGE",
  workRoot: "SOCIALWEB_FIRECRACKER_WORK_ROOT",
  preinstall: "SOCIALWEB_FIRECRACKER_PREINSTALL",
  rangeBase: "SOCIALWEB_FIRECRACKER_RANGE_BASE",
  reuseImage: "SOCIALWEB_FIRECRACKER_REUSE_IMAGE",
  advertise: "SOCIALWEB_K8S_ADVERTISE",
  openbaoTicket: "SOCIALWEB_K8S_LIVE_OPENBAO_TICKET",
  openbaoCA: "SOCIALWEB_K8S_LIVE_OPENBAO_CA",
} as const;

const NODE_NAME = "live-node1";
const CENTRAL_DEFAULT_BIDDER = "did:plc:centraldefaultbidder000000";
const DEFAULT_ADVERTISE = "100.127.255.254";

function onPath(binary: string): boolean {
  try {
    return new Deno.Command("sh", { args: ["-c", `command -v ${binary}`], stdout: "null", stderr: "null" })
      .outputSync().code === 0;
  } catch {
    return false;
  }
}

function skipReasonOf(): string | null {
  if (Deno.env.get("SOCIALWEB_K8S_LIVE") !== "1") {
    return "SOCIALWEB_K8S_LIVE is not 1";
  }
  const absent = (["nodeimage", "config", "repoDir", "runnerImage", "workRoot", "openbaoTicket", "openbaoCA"] as const)
    .filter((k) => !Deno.env.get(ENV[k]));
  if (absent.length > 0) {
    return `${absent.map((k) => ENV[k]).join(", ")} unset`;
  }
  for (const binary of ["k3s", "kine", "kubectl", "docker"]) {
    if (!onPath(binary)) return `${binary} is not on PATH`;
  }
  return null;
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function token(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return `k10${hex(bytes)}`;
}

function didWebToHttps(s: string): string {
  return s.startsWith("did:web:") ? "https://" + s.slice("did:web:".length) : s;
}

function hostAddress(): string {
  const override = Deno.env.get("SOCIALWEB_K8S_RELAY_HOST_IP");
  if (override) return override;
  const candidates = Deno.networkInterfaces().filter((i) =>
    i.family === "IPv4" && i.address !== "127.0.0.1" && !i.address.startsWith("169.254.")
  );
  const preferred = candidates.find((i) => i.name !== "docker0" && !i.address.startsWith("172.17.")) ??
    candidates[0];
  if (!preferred) {
    throw new Error("no non-loopback IPv4 address on this host, so no guest could dial the dispatcher");
  }
  return preferred.address;
}

interface Ran {
  code: number;
  stdout: string;
  stderr: string;
}

async function runCommand(
  command: string,
  args: string[],
  timeoutMs = 120_000,
  env?: Record<string, string>,
): Promise<Ran> {
  const child = new Deno.Command(command, {
    args,
    stdout: "piped",
    stderr: "piped",
    ...(env ? { env } : {}),
  }).spawn();
  const collected = (async () => {
    const [stdout, stderr] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
    ]);
    const status = await child.status;
    return { code: status.code, stdout, stderr };
  })();
  const timedOut = Symbol("timeout");
  const timer = setTimeout(() => {
    try {
      child.kill("SIGKILL");
    } catch { /* already gone */ }
  }, timeoutMs);
  const raced = await Promise.race([
    collected,
    new Promise<typeof timedOut>((resolve) => setTimeout(() => resolve(timedOut), timeoutMs + 5_000)),
  ]);
  clearTimeout(timer);
  if (raced === timedOut) {
    throw new Error(`${command} ${args.join(" ")} did not finish within ${timeoutMs}ms`);
  }
  return raced;
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, "'\\''")}'`;
}

function spawnToLog(command: string, args: string[], logPath: string): Deno.ChildProcess {
  const line = `exec ${[command, ...args].map(shellQuote).join(" ")} >> ${shellQuote(logPath)} 2>&1`;
  return new Deno.Command("sh", { args: ["-c", line] }).spawn();
}

async function kubectlApply(kubeconfig: string, manifest: string): Promise<Ran> {
  const child = new Deno.Command("kubectl", {
    args: ["--kubeconfig", kubeconfig, "apply", "-f", "-"],
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  const writer = child.stdin.getWriter();
  await writer.write(new TextEncoder().encode(manifest));
  await writer.close();
  const collected = Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
  ]).then(async ([stdout, stderr]) => ({ code: (await child.status).code, stdout, stderr }));
  const timer = setTimeout(() => {
    try {
      child.kill("SIGKILL");
    } catch { /* already gone */ }
  }, 60_000);
  const ran = await collected;
  clearTimeout(timer);
  return ran;
}

class FailNow extends Error {}

// The guest's console is the only place its own reason for stopping appears.
//
// The node image carries an agent that expects a node identity disk. A guest
// rented for a cluster through the market has none, so that agent exits at boot
// and its fatal is EXPECTED here: a cloud-init guest carries on without it, and
// the join module gives the guest the resolver the agent would otherwise have
// written. So the agent's words are a verdict only when the guest in fact never
// got going - no tunnel subscriber started and no tunnel address reported.
const PROGRESS = /Started tunnel-subscriber\.service|guest-onnetwork\.service|Deno was installed successfully/;

// A guest needs minutes, not seconds: cloud-init installs deno, starts the tunnel
// subscriber and reports its address before anything about the join can be said.
const GUEST_GRACE_MS = 150_000;

async function guestVerdict(consolePath: string): Promise<string | null> {
  let text: string;
  try {
    text = await Deno.readTextFile(consolePath);
  } catch {
    return null;
  }
  if (!/KCP_AGENT fatal|nodeagent: [^"]*fatal/.test(text)) return null;
  if (PROGRESS.test(text)) return null;
  return `the guest's agent stopped the guest before it could join: ${
    text.split("\n").filter((l) => /KCP_AGENT fatal|nodeagent:/.test(l)).slice(-2).join(" | ")
  }\nlast 1200 bytes of ${consolePath}:\n${text.slice(-1200)}`;
}

async function waitFor(
  what: string,
  deadlineMs: number,
  probe: () => Promise<boolean> | boolean,
  intervalMs = 2_000,
): Promise<number> {
  const started = Date.now();
  const deadline = started + deadlineMs;
  let last = "";
  while (Date.now() < deadline) {
    try {
      if (await probe()) return Date.now() - started;
    } catch (err) {
      // A probe that knows the wait is already lost says so with FailNow: only
      // a probe that could recover on the next tick is swallowed here.
      if (err instanceof FailNow) throw err;
      last = String(err);
    }
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`${what} did not happen within ${deadlineMs}ms${last ? `: ${last}` : ""}`);
}

interface ControlPlane {
  port: number;
  advertiseAddress: string;
  dataDir: string;
  tokenFile: string;
  serverCAFile: string;
  kubeconfig: string;
  logPath: string;
  stop(): Promise<void>;
}

function pickPort(): number {
  const bytes = new Uint8Array(2);
  crypto.getRandomValues(bytes);
  return 20_000 + ((bytes[0] << 8 | bytes[1]) % 40_000);
}

async function portIsFree(port: number): Promise<boolean> {
  try {
    const listener = Deno.listen({ hostname: "127.0.0.1", port });
    listener.close();
    return true;
  } catch {
    return false;
  }
}

async function waitForTcp(port: number, deadlineMs: number): Promise<boolean> {
  const deadline = Date.now() + deadlineMs;
  while (Date.now() < deadline) {
    try {
      const conn = await Deno.connect({ hostname: "127.0.0.1", port });
      conn.close();
      return true;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }
  return false;
}

async function startControlPlane(advertiseAddress: string): Promise<ControlPlane> {
  const root = await Deno.makeTempDir({ prefix: "live-k3s-" });
  const dataDir = `${root}/k3s`;
  const tokenFile = `${root}/token`;
  const kubeconfig = `${dataDir}/server/cred/admin.kubeconfig`;
  const serverCAFile = `${dataDir}/server/tls/server-ca.crt`;
  const logPath = `${root}/k3s.log`;
  await Deno.writeTextFile(tokenFile, `${token()}\n`);

  let kineChild: Deno.ChildProcess | null = null;
  let k3sChild: Deno.ChildProcess | null = null;
  let port = 0;

  const stop = async () => {
    for (const child of [k3sChild, kineChild]) {
      if (!child) continue;
      try {
        child.kill("SIGTERM");
      } catch { /* already gone */ }
    }
    await new Promise((r) => setTimeout(r, 1_000));
    for (const child of [k3sChild, kineChild]) {
      if (!child) continue;
      try {
        child.kill("SIGKILL");
      } catch { /* already gone */ }
    }
    await runCommand("pkill", ["-f", root], 20_000).catch(() => undefined);
    await Deno.remove(root, { recursive: true }).catch(() => undefined);
  };

  try {
    for (let attempt = 0; attempt < 5; attempt++) {
      port = pickPort();
      const kinePort = pickPort();
      // k3s binds the apiserver one above its supervisor's --https-listen-port,
      // so neither the store's port nor anything else may sit in that pair. The
      // controller-manager, scheduler and cloud-controller-manager ports default
      // to one fixed set PER HOST, so a second cluster on this host fails at bind
      // unless each is moved, exactly as the door does it.
      const componentPorts = [pickPort(), pickPort(), pickPort()];
      const wanted = [port, port + 1, kinePort, ...componentPorts];
      if (new Set(wanted).size !== wanted.length) continue;
      const free = await Promise.all(wanted.map(portIsFree));
      if (!free.every((f) => f)) continue;

      kineChild = spawnToLog("kine", [
        "--endpoint",
        `sqlite://${root}/kine.db`,
        "--listen-address",
        `127.0.0.1:${kinePort}`,
        "--metrics-bind-address=0",
      ], `${root}/kine.log`);
      if (!await waitForTcp(kinePort, 20_000)) {
        throw new Error(`kine never listened on 127.0.0.1:${kinePort}`);
      }

      k3sChild = spawnToLog("k3s", [
        "server",
        "--disable-agent",
        "--disable=traefik",
        "--data-dir",
        dataDir,
        "--datastore-endpoint",
        `http://127.0.0.1:${kinePort}`,
        "--egress-selector-mode",
        "cluster",
        "--advertise-address",
        advertiseAddress,
        "--advertise-port",
        String(port),
        "--bind-address",
        "127.0.0.1",
        "--https-listen-port",
        String(port),
        "--tls-san",
        advertiseAddress,
        "--tls-san",
        "127.0.0.1",
        "--token-file",
        tokenFile,
        "--write-kubeconfig",
        kubeconfig,
        "--kube-controller-manager-arg",
        `secure-port=${componentPorts[0]}`,
        "--kube-scheduler-arg",
        `secure-port=${componentPorts[1]}`,
        "--kube-cloud-controller-manager-arg",
        `secure-port=${componentPorts[2]}`,
      ], logPath);

      // k3s disables anonymous auth, so /readyz answers 401 to anything without
      // the admin credential: readiness has to be asked with the cluster's own.
      const ready = await waitFor(
        "the k3s apiserver's /readyz",
        120_000,
        async () => {
          if (!await exists(kubeconfig)) return false;
          const out = await runCommand(
            "kubectl",
            ["--kubeconfig", kubeconfig, "get", "--raw", "/readyz"],
            20_000,
          );
          return out.code === 0 && out.stdout.includes("ok");
        },
        3_000,
      ).then(() => true).catch(async (err) => {
        const tail = await Deno.readTextFile(logPath).catch(() => "");
        console.log(`[live] k3s did not become ready on port ${port}: ${String(err)}\n${tail.slice(-4000)}`);
        return false;
      });

      if (ready) {
        const text = await Deno.readTextFile(kubeconfig);
        console.log(`[live] control plane ready on 127.0.0.1:${port}`);
        console.log(`[live] admin kubeconfig:\n${text}`);
        return {
          port,
          advertiseAddress,
          dataDir,
          tokenFile,
          serverCAFile,
          kubeconfig,
          logPath,
          stop,
        };
      }

      try {
        k3sChild.kill("SIGKILL");
      } catch { /* already gone */ }
      try {
        kineChild.kill("SIGKILL");
      } catch { /* already gone */ }
      k3sChild = null;
      kineChild = null;
      await runCommand("pkill", ["-f", root], 20_000).catch(() => undefined);
      await new Promise((r) => setTimeout(r, 2_000));
    }
    throw new Error("no control plane came up on any candidate port");
  } catch (err) {
    await stop();
    throw err;
  }
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch {
    return false;
  }
}

class MemoryNodeClaimStore implements NodeClaimStore {
  readonly claims = new Map<string, NodeClaim>();
  readonly statuses: NodeClaimStatus[] = [];

  put(claim: NodeClaim): void {
    this.claims.set(claim.name, structuredClone(claim));
  }

  list(): Promise<NodeClaim[]> {
    return Promise.resolve([...this.claims.values()].map((c) => structuredClone(c)));
  }

  get(claim: Pick<NodeClaim, "workspace" | "name">): Promise<NodeClaim | null> {
    const current = this.claims.get(claim.name);
    return Promise.resolve(current ? structuredClone(current) : null);
  }

  replaceStatus(claim: NodeClaim, status: NodeClaimStatus): Promise<void> {
    const current = this.claims.get(claim.name);
    if (current) current.status = structuredClone(status);
    this.statuses.push(structuredClone(status));
    return Promise.resolve();
  }

  addFinalizer(claim: NodeClaim): Promise<void> {
    const current = this.claims.get(claim.name);
    if (current && !current.finalizers.includes(FINALIZER_NODE_CLAIM)) {
      current.finalizers.push(FINALIZER_NODE_CLAIM);
    }
    return Promise.resolve();
  }

  removeFinalizer(claim: NodeClaim): Promise<void> {
    const current = this.claims.get(claim.name);
    if (current) current.finalizers = current.finalizers.filter((f) => f !== FINALIZER_NODE_CLAIM);
    return Promise.resolve();
  }

  phase(): string | undefined {
    return this.claims.get(NODE_NAME)?.status.phase;
  }

  status(): NodeClaimStatus | undefined {
    return this.claims.get(NODE_NAME)?.status;
  }
}

function createFakePlc() {
  const ops = new Map<string, Record<string, unknown>>();
  const app = new Hono();

  function didFromPath(path: string): string {
    return decodeURIComponent(path.startsWith("/") ? path.slice(1) : path);
  }

  app.post("/*", async (c) => {
    const did = didFromPath(new URL(c.req.url).pathname);
    ops.set(did, await c.req.json().catch(() => ({})) as Record<string, unknown>);
    return c.json({ ok: true });
  });

  app.get("/*", (c) => {
    const did = didFromPath(new URL(c.req.url).pathname);
    const op = ops.get(did);
    if (!op) return c.json({ message: `DID not found: ${did}` }, 404);
    const vms = (op.verificationMethods ?? {}) as Record<string, string>;
    const svcs = (op.services ?? {}) as Record<string, { type: string; endpoint: string }>;
    return c.json({
      "@context": ["https://www.w3.org/ns/did/v1", "https://w3id.org/security/multikey/v1"],
      id: did,
      alsoKnownAs: (op.alsoKnownAs ?? []) as string[],
      verificationMethod: Object.entries(vms).map(([name, didKey]) => ({
        id: `${did}#${name}`,
        type: "Multikey",
        controller: did,
        publicKeyMultibase: String(didKey).replace(/^did:key:/, ""),
      })),
      service: Object.entries(svcs).map(([name, s]) => ({
        id: `#${name}`,
        type: s.type,
        serviceEndpoint: s.endpoint,
      })),
    });
  });

  return { app };
}

const skipReason = skipReasonOf();
if (skipReason !== null) {
  console.log(`[test] skipping the live market node test: ${skipReason}`);
}

Deno.test({
  name: "[live] a market guest joins a loopback k3s through the door's reverse forward",
  ignore: skipReason !== null,
  sanitizeOps: false,
  sanitizeResources: false,
}, async () => {
  const logger = createLogger({ serviceName: "it-live-market-node" });
  const cleanups: Array<() => void | Promise<void>> = [];
  const advertiseAddress = Deno.env.get(ENV.advertise) ?? DEFAULT_ADVERTISE;
  const relayHostIp = hostAddress();
  console.log(`[live] guests will reach the dispatcher at ${relayHostIp}`);

  const control = await startControlPlane(advertiseAddress);
  cleanups.push(() => control.stop());

  const plc = createFakePlc();
  const plcCtl = new AbortController();
  const plcPortReady = Promise.withResolvers<number>();
  Deno.serve(
    {
      port: 0,
      hostname: "127.0.0.1",
      signal: plcCtl.signal,
      onListen: (addr) => plcPortReady.resolve((addr as Deno.NetAddr).port),
    },
    plc.app.fetch,
  );
  const plcPort = await plcPortReady.promise;
  cleanups.push(() => plcCtl.abort());
  const plcLocalUrl = `http://localhost:${plcPort}`;
  // Everything that builds a resolver is told https://plc.directory, the name
  // @atproto/identity's SSRF-guarded fetch accepts; the interceptor below is
  // what turns that name into the directory this test is actually running. A
  // plain http:// URL handed straight to a resolver is refused outright.
  const plcDirectoryUrl = "https://plc.directory";

  // Installed twice on purpose: the relay factory builds its resolver from
  // whatever fetch is global at construction, so the directory redirect must be
  // in place before it is built; the subdomain redirect needs the dispatcher's
  // port, which only exists once it is serving.
  const restoreDirectoryFetch = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl: plcLocalUrl,
    dispPort: 0,
  });
  cleanups.push(restoreDirectoryFetch);

  const dispatcherApp = createRelayFactory({
    hostname: "relay.localhost",
    additionalHosts: [relayHostIp],
    plcDirectoryUrl,
  }).createApp();
  const dispatcherCtl = new AbortController();
  const dispPortReady = Promise.withResolvers<number>();
  Deno.serve(
    {
      port: 0,
      hostname: "0.0.0.0",
      signal: dispatcherCtl.signal,
      onListen: (addr) => dispPortReady.resolve((addr as Deno.NetAddr).port),
    },
    dispatcherApp.fetch,
  );
  const dispPort = await dispPortReady.promise;
  cleanups.push(() => dispatcherCtl.abort());
  const ingressProxyHost = `relay.localhost:${dispPort}`;

  const restoreFetch = installFetchInterceptor({
    realFetch: globalThis.fetch,
    plcDirectoryUrl: plcLocalUrl,
    dispPort,
  });
  cleanups.push(restoreFetch);

  const fetchFailures: Array<{ url: string; cause: string }> = [];
  {
    const patched = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      try {
        return await patched(input, init);
      } catch (err) {
        const cause = (err as { cause?: unknown }).cause;
        fetchFailures.push({ url, cause: cause === undefined ? String(err) : String(cause) });
        throw err;
      }
    }) as typeof fetch;
    cleanups.push(() => {
      globalThis.fetch = patched;
    });
  }

  let providerId: string | number | undefined;
  let firecracker: ReturnType<typeof createFirecrackerComputeProvider> | undefined;

  try {
    const bidderKeypair = await Secp256k1Keypair.create({ exportable: true });
    const bidderPrivHex = hex(await bidderKeypair.export());

    const pdsAgent = await createLocalPDSAgent({
      logger,
      keypair: bidderKeypair,
      serve: createServe({ logger }),
      plcDirectoryUrl,
      ingressProxyHost,
    });
    await pdsAgent.beginServe();

    const atproto = await createATProto({
      logger,
      badgeBlueSigner: await createBadgeBlueSigner({ privateKeyHex: bidderPrivHex }),
      plcDirectory: createPlcDirectoryClient({ plcDirectoryUrl }),
      agent: pdsAgent,
    });

    const makeRelay = async () => {
      const kp = await Secp256k1Keypair.create({ exportable: true });
      return createIngress({ logger, ingressProxyHost, signer: atproto.signer, keypair: kp });
    };

    const providerRelay = await makeRelay();
    const providerServe = createServe({ logger, relays: [providerRelay] });
    cleanups.push(() => providerServe.shutdown());

    const inner = createFirecrackerComputeProvider({
      logger,
      atproto: atproto as unknown as ComputeAtproto,
      serve: providerServe,
      getIssuerUrl: () => didWebToHttps(providerRelay.ingressRef),
      image: createFirecrackerNodeImage({
        binary: Deno.env.get(ENV.nodeimage)!,
        configPath: Deno.env.get(ENV.config)!,
        repoDir: Deno.env.get(ENV.repoDir)!,
        preinstallPath: Deno.env.get(ENV.preinstall),
        logger,
      }),
      microvm: createFirecrackerMicrovm({
        backend: createDockerBackend(),
        runnerImage: Deno.env.get(ENV.runnerImage)!,
        logger,
      }),
      workRoot: Deno.env.get(ENV.workRoot)!,
      rangeBase: Deno.env.get(ENV.rangeBase),
      reuseStale: Deno.env.get(ENV.reuseImage) === "1",
    });
    firecracker = inner;

    const provisions: Array<{ providerId: string | number; ip: unknown; console: unknown }> = [];
    const provisionFailures: unknown[] = [];
    const destroyed: Array<string | number> = [];
    const provider = createComputeProviderHooks({
      provider: {
        ...inner.provider,
        async provision(vm, requesterDid, spec) {
          try {
            const result = await inner.provider.provision(vm, requesterDid, spec);
            const metadata = result.metadata as Record<string, unknown>;
            provisions.push({ providerId: result.providerId, ip: metadata?.ip, console: metadata?.console });
            return result;
          } catch (err) {
            provisionFailures.push(err);
            throw err;
          }
        },
        async destroy(id) {
          destroyed.push(id);
          return await inner.provider.destroy(id);
        },
      },
    });

    await inner.ensureImage();
    await providerServe.beginServe();

    const bidderRelay = await makeRelay();
    const bidder = await createMarketBidder({
      logger,
      atproto,
      providers: [provider],
      relay: bidderRelay,
      serve: createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 }, relays: [bidderRelay] }),
    });
    await bidder.beginServe();
    cleanups.push(() => bidder.shutdown());

    const requesterServe = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
    const requester = await createRequesterPDS({
      logger,
      serve: requesterServe,
      plcDirectoryUrl,
      ingressProxyHost,
      label: "requester",
    });
    cleanups.push(() => requesterServe.shutdown());
    await requester.beginServe();

    const store = new MemoryNodeClaimStore();
    const claimDir = await Deno.makeTempDir({ prefix: "live-nodeclaim-" });
    cleanups.push(() => Deno.remove(claimDir, { recursive: true }).catch(() => undefined));
    const nodePasswordFile = `${claimDir}/node-password`;
    await Deno.writeTextFile(nodePasswordFile, `${token()}\n`);

    const claim: NodeClaim = {
      workspace: "live",
      name: NODE_NAME,
      finalizers: [],
      spec: {
        cluster: "live",
        nodeName: NODE_NAME,
        ownerDid: requester.did,
        apiserverPort: control.port,
        advertiseAddress,
        tokenFile: control.tokenFile,
        nodePasswordFile,
        serverCAFile: control.serverCAFile,
        denyBidderDids: [CENTRAL_DEFAULT_BIDDER],
      },
      status: {},
    };
    store.put(claim);

    const started = Date.now();
    const fulfilLog: Array<{ at: number; event: string }> = [];
    let joinAt = 0;

    // The canonical market harness makes a locally-started bidder findable by
    // naming it, because nothing local can be discovered through a relay index
    // or a vouch graph, and it points the guest at the dispatcher through an
    // /etc/hosts alias, because the host's loopback is not something a guest
    // shares. A NodeClaim carries neither, so both are added here, around the
    // same requestCompute the fulfiller would call: the RFP flow, cloud-init
    // composition and the guest are untouched.
    const computeRequester: typeof requestCompute = (pds, opts, handlers) =>
      requestCompute(pds, {
        ...(opts ?? {}),
        extraBidderDids: [atproto.did],
        guestHostAliases: [`${relayHostIp} relay.localhost`],
      }, handlers);

    const fulfiller = createMarketNodeFulfiller({
      store,
      links: {
        get: (workspace, name) =>
          Promise.resolve({ workspace, name, status: { phase: "Listening", ticket: Deno.env.get(ENV.openbaoTicket)! } }),
      },
      openBaoCAFileOf: () => Deno.env.get(ENV.openbaoCA)!,
      requesterFor: () => Promise.resolve(requester),
      ingressProxyHost,
      relayUrls: [],
      // The wait for the guest to register its tunnel. It also bounds teardown:
      // requestCompute's network wait is not abort-aware, so an abort during it
      // only lands when this elapses.
      vmReadyTimeoutSec: 300,
      requestCompute: computeRequester,
      log: (event, data) => {
        fulfilLog.push({ at: Date.now(), event });
        console.log(JSON.stringify({ at: new Date().toISOString(), src: "node-fulfiller", event, ...data }));
      },
    });

    fulfiller.start();
    cleanups.push(() => fulfiller.stop());

    let lastWaitingLine = 0;

    const phaseProbe = async () => {
      const status = store.status();
      if (status?.phase === "Joining" && joinAt === 0) joinAt = Date.now();
      if (status?.phase === "Failed") {
        throw new FailNow(`the node claim failed: ${status.message ?? "no reason given"}`);
      }
      const consolePath = provisions[0]?.console;
      if (Date.now() - lastWaitingLine > 30_000) {
        lastWaitingLine = Date.now();
        console.log(
          `[live] waiting: phase=${status?.phase ?? "none"} guests=${provisions.length} console=${
            typeof consolePath === "string" ? consolePath : "none"
          }`,
        );
      }
      if (typeof consolePath === "string" && Date.now() - started > GUEST_GRACE_MS) {
        const verdict = await guestVerdict(consolePath);
        if (verdict) throw new FailNow(verdict);
      }
      return status?.phase === "Joining";
    };

    const joinMs = await waitFor("the claim to reach Joining", 15 * 60_000, phaseProbe, 1_000);
    console.log(`[live] READING claim-reached-Joining ms=${Date.now() - started} (wait ${joinMs}ms)`);
    assertEquals(store.phase(), "Joining");
    assert(provisions.length > 0, "the claim reached Joining without the provider provisioning a guest");
    providerId = provisions[0].providerId;
    console.log(`[live] provider placed ${providerId} at ${provisions[0].ip}`);

    const kubectl = async (args: string[], timeoutMs = 60_000): Promise<Ran> =>
      await runCommand("kubectl", ["--kubeconfig", control.kubeconfig, ...args], timeoutMs);

    const nodeReadyMs = await waitFor("node live-node1 to report Ready", 8 * 60_000, async () => {
      const out = await kubectl(["get", "node", NODE_NAME, "-o", "json"], 30_000);
      if (out.code !== 0) return false;
      const node = JSON.parse(out.stdout) as {
        status?: { conditions?: Array<{ type: string; status: string }> };
      };
      const ready = node.status?.conditions?.find((c) => c.type === "Ready");
      return ready?.status === "True";
    }, 5_000);
    console.log(`[live] READING node-Ready ms=${Date.now() - started} (wait ${nodeReadyMs}ms)`);

    const WORKLOADS = ["coredns", "metrics-server", "local-path-provisioner"];
    const readyReplicas = async (name: string): Promise<number | null> => {
      const out = await kubectl(["-n", "kube-system", "get", "deployment", name, "-o", "json"], 30_000);
      if (out.code !== 0) return null;
      const deployment = JSON.parse(out.stdout) as { status?: { readyReplicas?: number; replicas?: number } };
      return deployment.status?.readyReplicas ?? 0;
    };

    const workloadsMs = await waitFor(
      "kube-system coredns, metrics-server and local-path-provisioner to reach 1/1",
      8 * 60_000,
      async () => {
        const counts = await Promise.all(WORKLOADS.map(readyReplicas));
        return counts.every((c) => c === 1);
      },
      5_000,
    );
    console.log(`[live] READING workloads-1/1 ms=${Date.now() - started} (wait ${workloadsMs}ms)`);

    const holdStarted = Date.now();
    while (Date.now() - holdStarted < 60_000) {
      await new Promise((r) => setTimeout(r, 10_000));
      const counts = await Promise.all(WORKLOADS.map(readyReplicas));
      assertEquals(
        counts,
        [1, 1, 1],
        `the kube-system workloads stopped being 1/1 after ${Date.now() - holdStarted}ms of holding`,
      );
    }
    console.log(`[live] READING workloads-held-60s ok`);

    const configText = await Deno.readTextFile(Deno.env.get(ENV.config)!);
    const config = JSON.parse(configText) as { images?: string[] };
    const busybox = (config.images ?? []).find((i) => i.includes("busybox"));
    assert(busybox !== undefined, `the node image config names no busybox image: ${configText.slice(0, 400)}`);

    const podManifest = JSON.stringify({
      apiVersion: "v1",
      kind: "Pod",
      metadata: { name: "live-busybox", namespace: "default" },
      spec: {
        nodeName: NODE_NAME,
        restartPolicy: "Never",
        containers: [{
          name: "busybox",
          image: busybox,
          command: ["sh", "-c", "echo live-busybox-hello; sleep 3600"],
        }],
      },
    });
    const applied = await kubectlApply(control.kubeconfig, podManifest);
    assertEquals(applied.code, 0, `kubectl apply failed: ${applied.stderr}`);

    const podRunning = await waitFor("the busybox pod to run", 4 * 60_000, async () => {
      const out = await kubectl(["-n", "default", "get", "pod", "live-busybox", "-o", "json"], 30_000);
      if (out.code !== 0) return false;
      const pod = JSON.parse(out.stdout) as { status?: { phase?: string } };
      return pod.status?.phase === "Running";
    }, 5_000);
    console.log(`[live] READING busybox-Running ms=${Date.now() - started} (wait ${podRunning}ms)`);

    const logs = await waitFor("kubectl logs to return the pod's output", 2 * 60_000, async () => {
      const out = await kubectl(["-n", "default", "logs", "live-busybox"], 45_000);
      return out.code === 0 && out.stdout.includes("live-busybox-hello");
    }, 5_000);
    console.log(`[live] READING kubectl-logs ms=${Date.now() - started} (wait ${logs}ms)`);

    const exec = await waitFor("kubectl exec to run inside the pod", 2 * 60_000, async () => {
      const out = await kubectl(["-n", "default", "exec", "live-busybox", "--", "echo", "exec-ok"], 45_000);
      return out.code === 0 && out.stdout.includes("exec-ok");
    }, 5_000);
    console.log(`[live] READING kubectl-exec ms=${Date.now() - started} (wait ${exec}ms)`);

    const beforeDelete = fulfilLog.length;
    store.claims.get(NODE_NAME)!.deletionTimestamp = new Date().toISOString();
    const releasedMs = await waitFor("the deleted claim to be Released", 6 * 60_000, () => {
      const current = store.claims.get(NODE_NAME)!;
      return current.status.phase === "Released" && current.finalizers.length === 0;
    }, 1_000);
    console.log(`[live] READING released ms=${Date.now() - started} (wait ${releasedMs}ms)`);

    const finalClaim = store.claims.get(NODE_NAME)!;
    assertEquals(finalClaim.status.phase, "Released");
    assertEquals(finalClaim.finalizers, []);
    assertEquals(store.status()?.contract?.phase, "released");
    assertEquals(store.status()?.contract?.release?.ok, true);
    assert(store.status()?.contract?.receipt?.uri, "the released claim keeps the receipt it released");

    const deleteEvents = fulfilLog.slice(beforeDelete).map((e) => e.event);
    assert(
      deleteEvents.includes("vm_delete_result") || deleteEvents.includes("vm_delete_submitted"),
      `the contract did not submit vm.delete after the claim was deleted; events were ${
        JSON.stringify([...new Set(deleteEvents)])
      }`,
    );

    await waitFor("the provider to destroy the guest", 2 * 60_000, async () => {
      const gone = (inner.provider as unknown as {
        getDroplet(id: string | number): unknown;
      }).getDroplet(providerId!);
      return destroyed.length > 0 && gone === undefined;
    }, 2_000);
    console.log(`[live] provider destroyed ${JSON.stringify(destroyed)} and reports no droplet for ${providerId}`);
    console.log(`[live] READINGS join=${joinAt - started}ms nodeReady=${nodeReadyMs}ms workloads=${workloadsMs}ms`);
    if (fetchFailures.length > 0) {
      console.log(`[live] rejected fetches during the run: ${JSON.stringify(fetchFailures.slice(0, 8))}`);
    }
  } finally {
    for (const cleanup of cleanups.reverse()) {
      try {
        await cleanup();
      } catch { /* best effort */ }
    }
    if (firecracker && providerId !== undefined) {
      await firecracker.provider.destroy(providerId).catch(() => undefined);
    }
    await new Promise((r) => setTimeout(r, 200));
  }
});
