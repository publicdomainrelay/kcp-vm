// Compute requester implementation — PDS creation, contract flow orchestration,
// SSH session management, websocat bootstrapping, relay-based bidder discovery.
// All I/O lives here: fetch, Deno.Command, Deno.makeTempDir, WebSocket, crypto.

import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import { getPdsEndpoint } from "@atproto/common-web";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { Signer } from "@publicdomainrelay/atproto-repo-abc";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { PlcClient, createGenesisOp } from "@publicdomainrelay/did-plc";
import { createSubscriber } from "@publicdomainrelay/did-key-relay-subscriber-xrpc";
import { createSubscriberFactory } from "@publicdomainrelay/hono-factory-did-key-relay-subscriber-xrpc";
import {
  loadOrGenerateKeypair,
  attestationFor,
  toStorableEntry,
  createSubmitBidHandler,
  createRecordResolver,
  verifyRecordSignatures,
  verifyRemoteProof,
} from "@publicdomainrelay/market-atproto";
import { stripResolved, atUriAuthority } from "@publicdomainrelay/market-abc";
import type { InlineAttestation, AttestationKeypair, SubmitBidCallback } from "@publicdomainrelay/market-atproto";
import {
  COMPUTE_VM_NSID,
  RFP_NSID,
  ACCEPT_NSID,
  OFFERING_NSID,
  EVENT_NSID,
  COMPUTE_EVENTS_VM_DELETE_NSID,
  SUBMIT_RFP_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_ACCEPT_NSID,
  SUBMIT_EVENT_NSID,
  SUBMIT_RFP_LXM,
  SUBMIT_ACCEPT_LXM,
  SUBMIT_EVENT_LXM,
  VOUCH_NSID,
} from "@publicdomainrelay/market-common";
import type { StrongRef } from "@publicdomainrelay/market-common";
import { buildDefaultUserData, flattenLabel, type CloudInitContext } from "@publicdomainrelay/cloud-init-common";
import type {
  RequesterPDS,
  PDSOptions,
  CollectedBid,
  ContractFlowOptions,
  ContractFlowResult,
  SshSessionProvider,
} from "@publicdomainrelay/requester-abc";
import type { LoggerInterface } from "@publicdomainrelay/logger";

// ---------------------------------------------------------------------------
// Extended types (impl details beyond the abc contract)
// ---------------------------------------------------------------------------

// deno-lint-ignore no-explicit-any
type HonoApp = any;

export interface RequesterPDSImpl extends RequesterPDS {
  app: HonoApp;
  signer: Signer;
  keypair: Secp256k1Keypair;
  api: RepoApi;
}

// ---------------------------------------------------------------------------
// bidder discovery via relay (replaces discoverBiddersFromRegistries)
// ---------------------------------------------------------------------------

export async function discoverBiddersFromRelay(opts: {
  relayUrl: string;
  collection: string;
  log?: LoggerInterface;
}): Promise<string[]> {
  const { relayUrl, collection, log } = opts;
  try {
    const url = `${relayUrl.replace(/\/+$/, "")}/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}`;
    log?.info("relay_discovery_query", { url, collection });
    const res = await fetch(url);
    if (!res.ok) {
      log?.warn("relay_discovery_http_error", { status: res.status, collection });
      return [];
    }
    const data = await res.json() as { repos?: Array<{ did: string }> };
    const dids = [...new Set((data.repos ?? []).map((r) => r.did).filter(Boolean))];
    log?.info("relay_discovery_result", { collection, count: dids.length });
    return dids;
  } catch (err) {
    log?.warn("relay_discovery_error", { collection, error: String(err) });
    return [];
  }
}

// ---------------------------------------------------------------------------
// createRequesterPDS — adapted from hono-bidder pattern + reference server.ts
// ---------------------------------------------------------------------------

export async function createRequesterPDS(
  opts: PDSOptions,
): Promise<RequesterPDSImpl> {
  const port = opts.port ?? 8080;
  const privateKeyHex = opts.privateKeyHex ?? "";
  const plcDirectoryUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const dispatcherHost = opts.dispatcherHost ?? "xrpc.fedproxy.com";
  const label = opts.label ?? "requester";
  const baseOrigin = `http://localhost:${port}`;

  // ── keypair ──────────────────────────────────────────────────────────

  const keypair = privateKeyHex
    ? await Secp256k1Keypair.import(privateKeyHex)
    : await Secp256k1Keypair.create({ exportable: true });

  const privateKeyHexFinal = privateKeyHex ||
    Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

  // ── attestation keypair ───────────────────────────────────────────────

  const attestationKp = await loadOrGenerateKeypair(privateKeyHexFinal);

  // ── did:plc registration ─────────────────────────────────────────────

  const plc = new PlcClient({ baseUrl: plcDirectoryUrl });
  const signingKeyDid = keypair.did();
  const epHost = dispatcherHost.replace(/:\d+$/, "");

  const { did, op } = await createGenesisOp({
    rotationKeys: [signingKeyDid],
    verificationMethods: {
      atproto: signingKeyDid,
      attestation: attestationKp.did(),
    },
    alsoKnownAs: [
      `at://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
    ],
    services: {
      atproto_pds: {
        type: "AtprotoPersonalDataServer",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
      pdr_temp_market: {
        type: "PDRTempMarket",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
      pdr_temp_compute_event: {
        type: "PDRTempComputeEvent",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  console.log(JSON.stringify({ event: "did_plc_registering", did, label }));
  await plc.submitOp(did, op);
  console.log(JSON.stringify({ event: "did_plc_registered", did, label }));

  // ── signer ───────────────────────────────────────────────────────────

  const signer: Signer = {
    did: () => did,
    sign: (bytes) => keypair.sign(bytes),
  };

  // ── relay ready promise ──────────────────────────────────────────────

  const relayBox: { resolve?: (info: { subdomain: string; proxyRef: string }) => void } = {};
  const relayReady = new Promise<{ subdomain: string; proxyRef: string }>((resolve) => {
    relayBox.resolve = resolve;
  });
  let relaySubdomain = "";
  let relayProxyRef = "";

  // ── pending bids ─────────────────────────────────────────────────────

  const pendingBids: Map<string, CollectedBid[]> = new Map();

  // ── repo factory ─────────────────────────────────────────────────────

  const { app, api } = createRepoFactory({
    storage: new MemoryStorage(),
    signer,
    baseOrigin,
    didWebServices: [
      { id: "pdr_temp_market", type: "PDRTempMarket" },
      { id: "pdr_temp_compute_event", type: "PDRTempComputeEvent" },
    ],
  });

  // ── request/response logging middleware ──────────────────────────────

  app.use("*", async (c: { req: { method: string; url: string }; res: { status: number; clone(): { text(): Promise<string> } } }, next: () => Promise<void>) => {
    const method = c.req.method;
    const path = new URL(c.req.url).pathname;
    const start = Date.now();
    await next();
    const status = c.res.status;
    const durationMs = Date.now() - start;
    const event = status >= 400 ? "response_error" : "response";
    console.log(JSON.stringify({ event, method, path, status, durationMs, label }));
  });

  // ── submitBid handler ────────────────────────────────────────────────

  const idResolver = new IdResolver();

  const onBid: SubmitBidCallback = ({ uri, cid, record, issuerDid }) => {
    const rfpUri = (record.rfp as StrongRef | undefined)?.uri;
    if (!rfpUri) return;
    const queue = pendingBids.get(rfpUri) ?? [];
    queue.push({ did: issuerDid ?? "unknown", uri, cid, record: record as unknown as Record<string, unknown> });
    pendingBids.set(rfpUri, queue);
    console.log(JSON.stringify({ event: "submitBid_queued", callerDid: issuerDid, uri, rfpUri, label }));
  };

  const bidHandler = createSubmitBidHandler({
    deps: {
      hostname: (req: Request) => {
        const host = req.headers.get("host") ?? req.headers.get("x-forwarded-host");
        return host ? host.split(":")[0] : relaySubdomain
          ? `${relaySubdomain}.${dispatcherHost}`
          : dispatcherHost;
      },
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
    },
    serviceIds: ["pdr_temp_market"],
    onBid,
  });
  app.post(`/xrpc/${SUBMIT_BID_NSID}`, (c: { req: { raw: Request } }) => bidHandler(c.req.raw));

  // ── HTTP server ──────────────────────────────────────────────────────

  const serverController = new AbortController();
  Deno.serve({ port, signal: serverController.signal }, app.fetch);

  console.log(JSON.stringify({ event: "listening", port, did, baseOrigin, label }));

  // ── relay subscriber ─────────────────────────────────────────────────

  const dispatcherDid = `did:web:${dispatcherHost.replace(/:\d+$/, "")}`;
  const { handleRequest } = createSubscriberFactory({ app });

  async function getServiceAuthToken(lxm: string): Promise<string> {
    return await signServiceAuth(signer, { aud: dispatcherDid, lxm });
  }

  console.log(JSON.stringify({ event: "relay_connecting", dispatcherHost, label }));

  const handle = await createSubscriber({
    label,
    keypair,
    getServiceAuthToken,
    dispatcherHost,
    handleRequest,
  });

  relaySubdomain = handle.subdomain;
  relayProxyRef = handle.proxyRef;
  console.log(JSON.stringify({
    event: "relay_registered",
    subdomain: handle.subdomain,
    proxyRef: handle.proxyRef,
    label,
  }));
  relayBox.resolve?.({ subdomain: handle.subdomain, proxyRef: handle.proxyRef });

  // ── helpers ──────────────────────────────────────────────────────────

  async function createRepoRecord(
    collection: string,
    record: Record<string, unknown>,
  ): Promise<{ uri: string; cid: string }> {
    const rkey = TID.next().toString();
    await api.applyWrites(did, [{ action: "create", collection, rkey, record }]);
    const rec = await api.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function createSignedRepoRecord(
    collection: string,
    record: Record<string, unknown>,
    aKp: { did(): string; privateKey: { bytes: Uint8Array; toBytes?(): Uint8Array } },
    issuer?: string,
  ): Promise<{ uri: string; cid: string }> {
    const rkey = TID.next().toString();
    const att = attestationFor(aKp as unknown as AttestationKeypair, issuer);
    const entry = await att.sign({ record, repository: did }) as InlineAttestation;
    const signed = { ...record, signatures: [toStorableEntry(entry)] };
    await api.applyWrites(did, [{ action: "create", collection, rkey, record: signed }]);
    const rec = await api.getRecord(did, collection, rkey);
    return { uri: `at://${did}/${collection}/${rkey}`, cid: rec?.cid ?? "" };
  }

  async function resolveBidderEndpoint(
    endpointUrl: string,
  ): Promise<{ targetUrl: string; audDid: string } | null> {
    if (endpointUrl.startsWith("http://") || endpointUrl.startsWith("https://")) {
      return {
        targetUrl: `${endpointUrl.replace(/\/+$/, "")}/xrpc`,
        audDid: `did:web:${new URL(endpointUrl).host}`,
      };
    }
    if (endpointUrl.startsWith("did:")) {
      const didPart = endpointUrl.split("#")[0];
      const svcDoc = await idResolver.did.resolve(didPart);
      const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : "pdr_temp_market";
      const svc = (svcDoc?.service ?? []).find((s: { id: string }) => s.id === `#${svcId}`);
      const svcEndpoint = (svc as { serviceEndpoint?: string } | undefined)?.serviceEndpoint;
      if (!svcEndpoint) return null;
      const svcHost = new URL(svcEndpoint).host;
      return {
        targetUrl: `${svcEndpoint.replace(/\/+$/, "")}/xrpc`,
        audDid: `did:web:${svcHost}`,
      };
    }
    return null;
  }

  async function callBidder(
    targetBase: string,
    nsid: string,
    lxm: string,
    audDid: string,
    body: Record<string, unknown>,
  ): Promise<{ status: number; ok: boolean; body: unknown }> {
    const token = await signServiceAuth(signer, { aud: audDid, lxm });
    const res = await fetch(`${targetBase}/${nsid}`, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    });
    let resBody: unknown;
    try { resBody = await res.json(); } catch { resBody = await res.text(); }
    return { status: res.status, ok: res.ok, body: resBody };
  }

  return {
    did,
    app,
    signer,
    keypair,
    api,
    proxyRef: relayProxyRef,
    relaySubdomain,
    relayReady,
    pendingBids,
    stop: () => {
      handle.ws.close();
      serverController.abort();
    },
    createRepoRecord,
    createSignedRepoRecord,
    resolveBidderEndpoint,
    callBidder,
    attestationKp,
    privateKeyHex: privateKeyHexFinal,
  };
}

// ---------------------------------------------------------------------------
// SSH session provider
// ---------------------------------------------------------------------------

function sshTunnelArgs(privateKeyPath: string, fqdn: string): string[] {
  return [
    "-o", `ProxyCommand=websocat --binary wss://${fqdn}`,
    "-o", `IdentityFile=${privateKeyPath}`,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "LogLevel=ERROR",
  ];
}

export function createSshSessionProvider(): SshSessionProvider {
  async function generateKeypair(
    vmName: string,
  ): Promise<{ publicKey: string; privateKeyPath: string }> {
    const dir = await Deno.makeTempDir({ prefix: `ssh-${vmName}-` });
    const privateKeyPath = `${dir}/id_ed25519`;
    const cmd = new Deno.Command("ssh-keygen", {
      args: ["-t", "ed25519", "-N", "", "-C", `root@${vmName}`, "-f", privateKeyPath],
      stdout: "null",
      stderr: "piped",
    });
    const { code, stderr } = await cmd.output();
    if (code !== 0) {
      throw new Error(`ssh-keygen failed: ${new TextDecoder().decode(stderr)}`);
    }
    const publicKey = (await Deno.readTextFile(`${privateKeyPath}.pub`)).trim();
    return { publicKey, privateKeyPath };
  }

  async function pollReady(
    privateKeyPath: string,
    fqdn: string,
    timeoutMs: number,
  ): Promise<boolean> {
    const deadline = Date.now() + timeoutMs;
    let attempt = 0;
    while (Date.now() < deadline) {
      attempt++;
      const cmd = new Deno.Command("ssh", {
        args: [
          ...sshTunnelArgs(privateKeyPath, fqdn),
          "-o", "BatchMode=yes",
          "-o", "ConnectTimeout=10",
          `root@${fqdn}`,
          "true",
        ],
        stdout: "null",
        stderr: "piped",
      });
      const { code, stderr } = await cmd.output();
      if (code === 0) {
        console.log(JSON.stringify({ event: "vm_ssh_ready", fqdn, attempt }));
        return true;
      }
      console.log(JSON.stringify({ event: "vm_ssh_poll", fqdn, attempt, code, error: new TextDecoder().decode(stderr).trim().slice(0, 200) }));
      await new Promise((r) => setTimeout(r, 5000));
    }
    console.log(JSON.stringify({ event: "vm_ssh_timeout", fqdn, timeoutMs }));
    return false;
  }

  async function runSession(
    privateKeyPath: string,
    fqdn: string,
    program: string,
  ): Promise<number> {
    const interactive = Deno.stdin.isTerminal();
    const args = [...sshTunnelArgs(privateKeyPath, fqdn)];
    if (interactive) {
      args.push("-tt", `root@${fqdn}`);
    } else {
      args.push(`root@${fqdn}`, program);
    }
    const cmd = new Deno.Command("ssh", { args, stdin: "inherit", stdout: "inherit", stderr: "inherit" });
    const child = cmd.spawn();
    const { code } = await child.status;
    return code;
  }

  return { generateKeypair, pollReady, runSession };
}

// ---------------------------------------------------------------------------
// websocat bootstrap
// ---------------------------------------------------------------------------

export async function ensureWebsocat(): Promise<void> {
  const which = new Deno.Command("which", { args: ["websocat"], stdout: "null", stderr: "null" });
  if ((await which.output()).code === 0) {
    console.log(JSON.stringify({ event: "websocat_found", source: "system" }));
    return;
  }

  const plat = Deno.build.os;
  const arch = Deno.build.arch;
  const triple: Record<string, Record<string, string>> = {
    linux: { x86_64: "x86_64-unknown-linux-musl", aarch64: "aarch64-unknown-linux-musl" },
    darwin: { x86_64: "x86_64-apple-darwin", aarch64: "aarch64-apple-darwin" },
  };
  const target = triple[plat]?.[arch];
  if (!target) {
    console.log(JSON.stringify({ event: "websocat_unsupported", plat, arch }));
    return;
  }

  const version = "v1.14.0";
  const url = `https://github.com/vi/websocat/releases/download/${version}/websocat.${target}`;

  const dir = await Deno.makeTempDir({ prefix: "websocat-" });
  const binPath = `${dir}/websocat`;
  console.log(JSON.stringify({ event: "websocat_downloading", url }));

  const resp = await fetch(url);
  if (!resp.ok || !resp.body) {
    console.log(JSON.stringify({ event: "websocat_download_failed", status: resp.status }));
    return;
  }

  const file = await Deno.open(binPath, { write: true, create: true, mode: 0o755 });
  await resp.body.pipeTo(file.writable);
  console.log(JSON.stringify({ event: "websocat_downloaded", path: binPath }));

  Deno.env.set("PATH", `${dir}:${Deno.env.get("PATH") ?? ""}`);
  console.log(JSON.stringify({ event: "websocat_path_updated", dir }));
}

// ---------------------------------------------------------------------------
// runComputeContract — adapted from reference server.ts
// ---------------------------------------------------------------------------

export async function runComputeContract(
  pds: RequesterPDS,
  opts: ContractFlowOptions & {
    sshProvider?: SshSessionProvider;
    relayUrl?: string;
    signer?: Signer;
  } = {},
): Promise<ContractFlowResult> {
  const vmName = opts.vmName ?? `compute-${randomHex8()}`;
  const bidWindowSec = opts.bidWindowSec ?? 30;
  const skipSsh = opts.skipSsh ?? false;
  const execProgram = opts.execProgram ?? "bash";
  const noDelete = opts.noDelete ?? false;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 300;
  const extraBidderDids = opts.extraBidderDids ?? [];
  const denyBidderDids = opts.denyBidderDids ?? [];
  const sshProvider = opts.sshProvider ?? createSshSessionProvider();
  const relayUrl = opts.relayUrl;
  const signer = opts.signer;

  const { proxyRef, subdomain: relaySubdomain } = await pds.relayReady;
  (pds as { proxyRef: string; relaySubdomain: string }).proxyRef = proxyRef;
  (pds as { proxyRef: string; relaySubdomain: string }).relaySubdomain = relaySubdomain;

  const log = (event: string, extra: Record<string, unknown> = {}) =>
    console.log(JSON.stringify({ event, ...extra }));

  log("relay_ready_for_rfp", { proxyRef });

  let cloudInit = "";
  let privateKeyPath = "";
  let vmFqdn = "";

  if (!skipSsh) {
    const dispatcherHost = relaySubdomain.includes(".")
      ? relaySubdomain.substring(relaySubdomain.indexOf(".") + 1)
      : "xrpc.fedproxy.com";
    vmFqdn = `${flattenLabel(vmName)}--${flattenLabel(pds.did)}.${dispatcherHost}`;
    const ssh = await sshProvider.generateKeypair(vmName);
    privateKeyPath = ssh.privateKeyPath;
    log("ssh_keypair_generated", {
      privateKeyPath,
      publicKey: ssh.publicKey,
      vmFqdn,
      hint: `ssh -i ${privateKeyPath} -o ProxyCommand='websocat --binary wss://${vmFqdn}' root@${vmFqdn}`,
    });

    const didPlcKey = pds.did.startsWith("did:plc:")
      ? pds.did.slice("did:plc:".length)
      : pds.did;
    const ctx: CloudInitContext = {
      vmName,
      didPlc: pds.did,
      didPlcKey,
      relayHost: dispatcherHost,
      xrpcRelaySubdomain: relaySubdomain,
      sshAuthorizedKey: ssh.publicKey,
    };
    cloudInit = buildDefaultUserData(ctx);
  } else {
    cloudInit = `#cloud-config
packages:
  - curl
runcmd:
  - echo "test VM (no sshd) ready" | tee /tmp/ready
`;
  }

  // 1. Create compute.vm record.
  const { uri: vmUri, cid: vmCid } = await pds.createRepoRecord(COMPUTE_VM_NSID, {
    $type: COMPUTE_VM_NSID,
    role: vmName.trim() || "compute",
    user_data: cloudInit,
    createdAt: new Date().toISOString(),
  });
  log("vm_record_created", { uri: vmUri, cid: vmCid });

  // 2. Create signed market.rfp.
  const { uri: rfpUri, cid: rfpCid } = await pds.createSignedRepoRecord(RFP_NSID, {
    $type: RFP_NSID,
    domain: "compute",
    payload: { $type: "com.atproto.repo.strongRef", uri: vmUri, cid: vmCid },
    submitBid: `${pds.did}#pdr_temp_market`,
    createdAt: new Date().toISOString(),
  }, pds.attestationKp, pds.did);
  log("rfp_created", { uri: rfpUri, cid: rfpCid });

  // 3. Discover bidder DIDs.
  const idResolver = new IdResolver();

  // 3a. Vouch-based discovery.
  let vouchedDids: string[] = [];
  try {
    const vouchRecords = await (pds as RequesterPDSImpl).api.listRecords(pds.did, VOUCH_NSID);
    vouchedDids = Array.from(new Set(
      (vouchRecords?.records ?? [])
        .filter((r) => (r.value as Record<string, unknown>).kind !== "denounce")
        .map((r) => r.uri.split("/").pop() ?? "")
        .filter((rkey) => rkey.startsWith("did:"))
    ));
    log("vouch_discovery", { count: vouchedDids.length });
  } catch (err) {
    log("vouch_discovery_error", { error: String(err) });
  }

  // 3b. Relay-based discovery (PRIMARY — relay IS the registry).
  let relayDids: string[] = [];
  if (relayUrl) {
    relayDids = await discoverBiddersFromRelay({ relayUrl, collection: OFFERING_NSID });
    if (relayDids.length > 0) log("relay_discovery", { count: relayDids.length });
  }

  const bidderDids = Array.from(new Set([...relayDids, ...vouchedDids, ...extraBidderDids]));
  const deniedSet = new Set(denyBidderDids);
  const filteredBidderDids = bidderDids.filter((d) => !deniedSet.has(d));
  log("bidder_discovery", { total: filteredBidderDids.length, relay: relayDids.length, vouched: vouchedDids.length, extra: extraBidderDids.length, denied: bidderDids.length - filteredBidderDids.length });

  // 4. Submit RFP to each bidder.
  for (const bidderDid of filteredBidderDids) {
    try {
      const doc = await idResolver.did.resolve(bidderDid);
      if (!doc) continue;
      const pdsUrl = getPdsEndpoint(doc);
      if (!pdsUrl) continue;

      // Fetch offering records from bidder's PDS.
      const offerings = await listRecordsAll(pdsUrl, bidderDid, OFFERING_NSID);
      for (const offering of offerings) {
        const appliesTo = offering.value.appliesTo as string[] | undefined;
        const endpointUrl = offering.value.endpointUrl as string | undefined;
        if (!endpointUrl || !Array.isArray(appliesTo) || !appliesTo.includes(COMPUTE_VM_NSID)) continue;

        const target = await pds.resolveBidderEndpoint(endpointUrl);
        if (!target) {
          log("bidder_unknown_endpoint", { endpointUrl });
          continue;
        }
        log("submitting_rfp", { bidderDid, endpointUrl });
        const r = await pds.callBidder(target.targetUrl, SUBMIT_RFP_NSID, SUBMIT_RFP_LXM, target.audDid, {
          rfpUri, rfpCid,
        });
        log("submitRfp_result", { bidderDid, status: r.status, ok: r.ok });
      }
    } catch (err) {
      log("bidder_error", { bidderDid, error: String(err) });
    }
  }

  // 5. Wait for bids.
  log("waiting_for_bids", { bidWindowSec });
  await new Promise<void>((resolve) => setTimeout(resolve, bidWindowSec * 1000));

  const bids = pds.pendingBids.get(rfpUri) ?? [];
  pds.pendingBids.delete(rfpUri);
  log("bids_collected", { count: bids.length });

  if (bids.length === 0) {
    const result: ContractFlowResult = { event: "no_bids", error: `no bids received within ${bidWindowSec}s` };
    log("no_bids", result as unknown as Record<string, unknown>);
    return result;
  }

  // 6. Pick lowest-cost winner.
  const winner = bids.reduce((best, b) => {
    const cost = (n: CollectedBid) => Number((n.record.payload as Record<string, unknown> | undefined)?.cost ?? Infinity);
    return cost(b) < cost(best) ? b : best;
  }, bids[0]);
  log("winner", { uri: winner.uri, did: winner.did });

  // 7. Create signed market.accept.
  const { uri: acceptUri, cid: acceptCid } = await pds.createSignedRepoRecord(ACCEPT_NSID, {
    $type: ACCEPT_NSID,
    rfp: { $type: "com.atproto.repo.strongRef", uri: rfpUri, cid: rfpCid },
    bid: { $type: "com.atproto.repo.strongRef", uri: winner.uri, cid: winner.cid },
    submitEvent: `${pds.did}#pdr_temp_compute_event`,
    createdAt: new Date().toISOString(),
  }, pds.attestationKp, pds.did);
  log("accept_created", { uri: acceptUri, cid: acceptCid });

  // 8. Submit accept to winning bidder.
  const submitAcceptTarget = winner.record.submitAccept as string | undefined;
  let receiptUri: string | undefined;
  let receiptCid: string | undefined;
  let submitEventRef: string | undefined;

  if (submitAcceptTarget) {
    const target = await pds.resolveBidderEndpoint(submitAcceptTarget);
    if (target) {
      log("submitting_accept", { target: submitAcceptTarget });
      const r = await pds.callBidder(target.targetUrl, SUBMIT_ACCEPT_NSID, SUBMIT_ACCEPT_LXM, target.audDid, {
        acceptUri, acceptCid,
      });
      const body = r.body as { id?: string; uri?: string; cid?: string; submitEvent?: string };
      receiptUri = body.uri;
      receiptCid = body.cid;
      submitEventRef = body.submitEvent;
      log("submitAccept_result", { status: r.status, receiptUri, receiptCid, submitEventRef });
    } else {
      log("accept_target_unresolvable", { submitAcceptTarget });
    }
  }

  // 9. Verify receipt.
  let receiptOk = false;
  if (receiptUri && receiptCid) {
    try {
      const resolver = createRecordResolver(new IdResolver());
      const receipt = await resolver.resolve({ uri: receiptUri, cid: receiptCid });
      const accept = await resolver.resolve({ uri: acceptUri, cid: acceptCid });
      const receiptBare = stripResolved(receipt) as Record<string, unknown>;
      const sigOk = await verifyRecordSignatures({
        record: receiptBare,
        repositoryDid: atUriAuthority(receiptUri),
      });
      const bindOk = verifyRemoteProof({
        subjectRecord: stripResolved(accept) as Record<string, unknown>,
        subjectRepositoryDid: pds.did,
        proofRecord: receiptBare,
      });
      receiptOk = sigOk && bindOk;
      log("receipt_verified", { receiptUri, sigOk, bindOk, ok: receiptOk });
    } catch (err) {
      log("receipt_verify_error", { receiptUri, error: String(err) });
    }
  } else {
    log("receipt_missing", { receiptUri, receiptCid });
  }

  const result: ContractFlowResult = {
    event: "compute_request_complete",
    vmUri, vmCid,
    rfpUri, rfpCid,
    acceptUri, acceptCid,
    bidUri: winner.uri, bidCid: winner.cid, winnerDid: winner.did,
    receiptUri, receiptCid, submitEventRef,
    receiptOk,
    bids: bids.length,
  };
  log("compute_request_complete", result as unknown as Record<string, unknown>);

  // 10. SSH (gated on valid receipt).
  if (skipSsh) {
    // tests / headless: skip SSH.
  } else if (!receiptOk) {
    log("vm_poll_bailed", { reason: "no valid receipt", receiptUri, receiptCid });
  } else {
    log("vm_ssh_waiting", { vmFqdn, timeoutSec: vmReadyTimeoutSec });
    const ready = await sshProvider.pollReady(privateKeyPath, vmFqdn, vmReadyTimeoutSec * 1000);
    if (!ready) {
      log("vm_ssh_unavailable", { vmFqdn });
    } else {
      opts.onSshStart?.();
      const code = await sshProvider.runSession(privateKeyPath, vmFqdn, execProgram);
      await opts.onSshEnd?.();
      log("vm_ssh_session_exit", { vmFqdn, code });
    }
  }

  // 11. Tear down VM via compute.events.vm.delete (unless --no-delete).
  if (noDelete) {
    log("vm_delete_skipped", { reason: "--no-delete" });
  } else if (!receiptUri || !receiptCid || !submitEventRef) {
    log("vm_delete_skipped", { reason: "missing receipt refs", receiptUri, receiptCid, submitEventRef });
  } else {
    try {
      const nowIso = new Date().toISOString();
      const { uri: delUri, cid: delCid } = await pds.createSignedRepoRecord(
        COMPUTE_EVENTS_VM_DELETE_NSID,
        { $type: COMPUTE_EVENTS_VM_DELETE_NSID, reason: "session_ended", createdAt: nowIso },
        pds.attestationKp, pds.did,
      );
      const eventRecord = {
        $type: EVENT_NSID,
        receipt: { $type: "com.atproto.repo.strongRef", uri: receiptUri, cid: receiptCid },
        payload: { $type: "com.atproto.repo.strongRef", uri: delUri, cid: delCid },
        createdAt: nowIso,
      };
      const { uri: eventUri, cid: eventCid } = await pds.createSignedRepoRecord(
        EVENT_NSID, eventRecord, pds.attestationKp, pds.did,
      );
      const target = await pds.resolveBidderEndpoint(submitEventRef);
      if (!target) {
        log("vm_delete_target_unresolvable", { submitEventRef });
      } else {
        log("submitting_vm_delete", { submitEventRef, eventUri });
        const r = await pds.callBidder(target.targetUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_LXM, target.audDid, {
          uri: eventUri,
          cid: eventCid,
          record: eventRecord,
        });
        log("vm_delete_result", { status: r.status, ok: r.ok });
      }
    } catch (err) {
      log("vm_delete_error", { error: String(err) });
    }
  }

  return result;
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

function randomHex8(): string {
  const b = new Uint8Array(4);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

async function listRecordsAll(
  pdsUrl: string,
  repo: string,
  collection: string,
  opts?: { limit?: number; timeoutMs?: number },
): Promise<Array<{ uri: string; cid: string; value: Record<string, unknown> }>> {
  // Use the market-atproto listRecordsAll helper — import it above
  const { listRecordsAll: lra } = await import("@publicdomainrelay/market-atproto");
  return lra(pdsUrl, repo, collection, opts);
}
