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
import { createXrpcRelay } from "@publicdomainrelay/xrpc-relay";
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
  RELAYS_NSID,
} from "@publicdomainrelay/market-common";
import type { StrongRef } from "@publicdomainrelay/market-common";
import { buildDefaultUserData, patchDefaultUserData, flattenLabel, type CloudInitContext } from "@publicdomainrelay/cloud-init-common";
import {
  FEDPROXY_RBAC_NSID,
  buildSshKeyRbacRecord,
} from "@publicdomainrelay/fedproxy-rbac-common";
import type {
  RequesterPDS,
  PDSOptions,
  CollectedBid,
  ContractFlowOptions,
  ContractFlowResult,
  SshSessionProvider,
} from "@publicdomainrelay/requester-abc";
import type { LoggerInterface, StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { ASSOCIATE_CONFIRM_NSID } from "@publicdomainrelay/market-lexicons";
import { verifyServiceAuth } from "@publicdomainrelay/market-atproto";

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
  timeoutMs?: number;
}): Promise<string[]> {
  const { relayUrl, collection, log, timeoutMs } = opts;
  try {
    const url = `${relayUrl.replace(/\/+$/, "")}/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}`;
    log?.info("relay_discovery_query", { url, collection });
    const res = await fetch(url, { signal: timeoutMs ? AbortSignal.timeout(timeoutMs) : undefined });
    if (!res.ok) {
      log?.warn("relay_discovery_http_error", { relayUrl, status: res.status, collection });
      return [];
    }
    const data = await res.json() as { repos?: Array<{ did: string }> };
    const dids = [...new Set((data.repos ?? []).map((r) => r.did).filter(Boolean))];
    log?.info("relay_discovery_result", { relayUrl, collection, count: dids.length });
    return dids;
  } catch (err) {
    log?.warn("relay_discovery_error", { relayUrl, collection, error: String(err) });
    return [];
  }
}

/**
 * Query multiple atproto relays for bidders. Each relay failure is logged but
 * non-blocking — results from all successful relays are unioned.
 */
export async function discoverBiddersFromRelays(opts: {
  relayUrls: string[];
  collection: string;
  log?: LoggerInterface;
  timeoutMs?: number;
}): Promise<string[]> {
  const { relayUrls, collection, log, timeoutMs } = opts;
  if (relayUrls.length === 0) return [];
  const results = await Promise.all(
    relayUrls.map((url) => discoverBiddersFromRelay({ relayUrl: url, collection, log, timeoutMs })),
  );
  const all = results.flat();
  return [...new Set(all)];
}

/**
 * Auto-discover relay URLs from $ATPROTO_DID's repo records. Reads
 * com.publicdomainrelay.temp.market.relays records, extracts the `relays`
 * string arrays, deduplicates them, and returns the union.
 * Falls back to an empty array if $ATPROTO_DID is unset, the DID can't be
 * resolved, or no relay records exist.
 */
export async function autoDiscoverRelayUrls(opts: {
  atprotoDid?: string;
  log?: LoggerInterface;
}): Promise<string[]> {
  const did = opts.atprotoDid ?? Deno.env.get("ATPROTO_DID");
  if (!did) return [];
  const log = opts.log;
  log?.info("relay_autodiscover_lookup", { did });
  try {
    const resolver = new IdResolver();
    const doc = await resolver.did.resolve(did);
    if (!doc) {
      log?.warn("relay_autodiscover_did_unresolvable", { did });
      return [];
    }
    const pdsUrl = getPdsEndpoint(doc);
    if (!pdsUrl) {
      log?.warn("relay_autodiscover_no_pds", { did });
      return [];
    }
    const records = await listRecordsAll(pdsUrl, did, RELAYS_NSID);
    const urls: string[] = [];
    for (const r of records) {
      const relays = (r.value as Record<string, unknown>).relays;
      if (Array.isArray(relays)) {
        for (const item of relays) {
          if (typeof item === "string" && item.trim() && (item.startsWith("https://") || item.startsWith("http://"))) urls.push(item.trim());
        }
      }
    }
    const deduped = [...new Set(urls)];
    log?.info("relay_autodiscover_result", { did, sources: records.length, urls: deduped.length });
    return deduped;
  } catch (err) {
    log?.warn("relay_autodiscover_error", { did, error: String(err) });
    return [];
  }
}

// ---------------------------------------------------------------------------
// createRequesterPDS — adapted from hono-bidder pattern + reference server.ts
// ---------------------------------------------------------------------------

export async function createRequesterPDS(
  opts: PDSOptions,
): Promise<RequesterPDSImpl> {
  const logger: StructuredLoggerInterface = opts.logger;
  const serve = opts.serve;
  const privateKeyHex = opts.privateKeyHex ?? "";
  const plcDirectoryUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const dispatcherHost = opts.dispatcherHost ?? "xrpc.fedproxy.com";
  const label = opts.label ?? "requester";

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
      requester_associate: {
        type: "PDRRequesterAssociate",
        endpoint: `https://${signingKeyDid.replace(/:/g, "-").toLowerCase()}.${epHost}`,
      },
    },
    sign: (bytes) => keypair.sign(bytes),
  });

  logger.info("did_plc_registering", { did, label });
  await plc.submitOp(did, op);
  logger.info("did_plc_registered", { did, label });

  // ── signer ───────────────────────────────────────────────────────────

  const signer: Signer = {
    did: () => did,
    sign: (bytes) => keypair.sign(bytes),
  };

  // ── pending bids ─────────────────────────────────────────────────────

  const pendingBids: Map<string, CollectedBid[]> = new Map();

  // ── association confirmation (webapp calls this before RFP) ─────────
  let resolveAssociateCalled: ((callerDid: string) => void) | null = null;
  const associateCalled = new Promise<string>((r) => { resolveAssociateCalled = r; });
  let resolveAssociationApproved: (() => void) | null = null;
  let rejectAssociationApproved: ((err: Error) => void) | null = null;
  const associationApproved = new Promise<void>((resolve, reject) => {
    resolveAssociationApproved = resolve;
    rejectAssociationApproved = reject;
  });

  // ── repo factory ─────────────────────────────────────────────────────

  const baseOrigin = `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${dispatcherHost}`;

  const { app, api } = createRepoFactory({
    storage: new MemoryStorage(),
    signer,
    baseOrigin,
    didWebServices: [
      { id: "pdr_temp_market", type: "PDRTempMarket" },
      { id: "pdr_temp_compute_event", type: "PDRTempComputeEvent" },
      { id: "requester_associate", type: "PDRRequesterAssociate" },
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
    logger.info(event, { method, path, status, durationMs, label });
  });

  // ── relay (WS connect deferred to serve.beginServe -> relay.onServe) ──

  const relay = createXrpcRelay({ logger, dispatcherHost, signer, keypair, label });
  const relayFqdn = (): string => {
    const ref = relay.proxyRef;
    return ref.startsWith("did:web:") ? ref.slice("did:web:".length) : ref;
  };

  // ── submitBid handler ────────────────────────────────────────────────

  const idResolver = new IdResolver();

  const onBid: SubmitBidCallback = ({ uri, cid, record, issuerDid }) => {
    const rfpUri = (record.rfp as StrongRef | undefined)?.uri;
    if (!rfpUri) return;
    const queue = pendingBids.get(rfpUri) ?? [];
    queue.push({ did: issuerDid ?? "unknown", uri, cid, record: record as unknown as Record<string, unknown> });
    pendingBids.set(rfpUri, queue);
    logger.info("submitBid_queued", { callerDid: issuerDid, uri, rfpUri, label });
  };

  const bidHandler = createSubmitBidHandler({
    deps: {
      hostname: (req: Request) => {
        const host = req.headers.get("host") ?? req.headers.get("x-forwarded-host");
        return host ? host.split(":")[0] : (relayFqdn() || dispatcherHost);
      },
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
    },
    serviceIds: ["pdr_temp_market"],
    onBid,
  });
  app.post(`/xrpc/${SUBMIT_BID_NSID}`, (c: { req: { raw: Request } }) => bidHandler(c.req.raw));

  // ── associateConfirm (webapp calls to confirm requester association) ──
  app.post(`/xrpc/${ASSOCIATE_CONFIRM_NSID}`, async (c) => {
    const authHeader = c.req.header("Authorization");
    if (!authHeader) return c.json({ error: "Unauthorized" }, 401);
    try {
      const auth = await verifyServiceAuth({
        authHeader,
        hostname: relayFqdn() || dispatcherHost,
        lxm: ASSOCIATE_CONFIRM_NSID,
        serviceIds: ["requester_associate"],
        extraAudienceDids: [did],
        idResolver,
      });
      resolveAssociateCalled?.(auth.issuerDid);
      // Wait for CLI user to approve/reject before responding to webapp
      await associationApproved;
      return c.json({ ok: true, requesterDid: did });
    } catch (err) {
      return c.json({ error: String(err) }, 401);
    }
  });

  // ── mount the repo app + relay on the shared serve handle ────────────

  serve.app.route("/", app as never);
  serve.addRelay(relay);

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
    const url = `${targetBase}/${nsid}`;
    const fetchBody = JSON.stringify(body);
    console.log(JSON.stringify({ event: "callBidder_pre", url, bodyType: typeof fetchBody, bodyLen: fetchBody.length, tokenLen: token.length }));
    const res = await fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
      body: fetchBody,
    });
    const resText = await res.text();
    let resBody: unknown;
    try { resBody = JSON.parse(resText); } catch { resBody = resText; }
    return { status: res.status, ok: res.ok, body: resBody };
  }

  return {
    did,
    app,
    signer,
    keypair,
    api,
    serve,
    relay,
    get proxyRef(): string { return relay.proxyRef; },
    get relaySubdomain(): string { return relayFqdn(); },
    beginServe: () => serve.beginServe(),
    pendingBids,
    createRepoRecord,
    createSignedRepoRecord,
    resolveBidderEndpoint,
    callBidder,
    attestationKp,
    privateKeyHex: privateKeyHexFinal,
    associateCalled,
    approveAssociation: () => { resolveAssociationApproved?.(); },
    rejectAssociation: (err: Error) => { rejectAssociationApproved?.(err); },
  };
}

// ---------------------------------------------------------------------------
// SSH session provider
// ---------------------------------------------------------------------------

function sshTunnelArgs(
  privateKeyPath: string,
  fqdn: string,
  proxyCmdOverride?: string,
): string[] {
  return [
    "-o", `ProxyCommand=${proxyCmdOverride ?? `websocat --binary wss://${fqdn}`}`,
    "-o", `IdentityFile=${privateKeyPath}`,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "LogLevel=ERROR",
  ];
}

export function createSshSessionProvider(
  logger?: StructuredLoggerInterface,
  opts?: { proxyCommandFn?: (fqdn: string) => string },
): SshSessionProvider {
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
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
    const proxyCmd = opts?.proxyCommandFn?.(fqdn);
    const deadline = Date.now() + timeoutMs;
    let attempt = 0;
    while (Date.now() < deadline) {
      attempt++;
      const cmd = new Deno.Command("ssh", {
        args: [
          ...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd),
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
        log("vm_ssh_ready", { fqdn, attempt });
        return true;
      }
      log("vm_ssh_poll", { fqdn, attempt, code, error: new TextDecoder().decode(stderr).trim().slice(0, 200) });
      await new Promise((r) => setTimeout(r, 5000));
    }
    log("vm_ssh_timeout", { fqdn, timeoutMs });
    return false;
  }

  async function runSession(
    privateKeyPath: string,
    fqdn: string,
    program: string,
  ): Promise<number> {
    const proxyCmd = opts?.proxyCommandFn?.(fqdn);
    const interactive = Deno.stdin.isTerminal();
    const args = [...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd)];
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

export async function ensureWebsocat(logger?: StructuredLoggerInterface): Promise<void> {
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
  const which = new Deno.Command("which", { args: ["websocat"], stdout: "null", stderr: "null" });
  if ((await which.output()).code === 0) {
    log("websocat_found", { source: "system" });
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
    log("websocat_unsupported", { plat, arch });
    return;
  }

  const version = "v1.14.0";
  const url = `https://github.com/vi/websocat/releases/download/${version}/websocat.${target}`;

  const dir = await Deno.makeTempDir({ prefix: "websocat-" });
  const binPath = `${dir}/websocat`;
  log("websocat_downloading", { url });

  const resp = await fetch(url);
  if (!resp.ok || !resp.body) {
    log("websocat_download_failed", { status: resp.status });
    return;
  }

  const file = await Deno.open(binPath, { write: true, create: true, mode: 0o755 });
  await resp.body.pipeTo(file.writable);
  log("websocat_downloaded", { path: binPath });

  Deno.env.set("PATH", `${dir}:${Deno.env.get("PATH") ?? ""}`);
  log("websocat_path_updated", { dir });
}

// ---------------------------------------------------------------------------
// runComputeContract — adapted from reference server.ts
// ---------------------------------------------------------------------------

export async function runComputeContract(
  pds: RequesterPDS,
  opts: ContractFlowOptions & {
    sshProvider?: SshSessionProvider;
    relayUrls?: string[];
    relayUrl?: string; // deprecated: use relayUrls
    signer?: Signer;
    offeringWatcherDids?: () => string[];
    logger?: StructuredLoggerInterface;
    policyMode?: "only_me" | "direct_network" | "policy_based";
  } = {},
): Promise<ContractFlowResult> {
  const vmName = opts.vmName ?? `compute-${randomHex8()}`;
  const bidWindowSec = opts.bidWindowSec ?? 30;
  const skipSsh = opts.skipSsh ?? false;
  const execProgram = opts.execProgram ?? "bash";
  const keepVm = opts.keepVm ?? false;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? 300;
  const extraBidderDids = opts.extraBidderDids ?? [];
  const denyBidderDids = opts.denyBidderDids ?? [];
  const policyMode = opts.policyMode;
  const sshProvider = opts.sshProvider ?? createSshSessionProvider(
    opts.logger,
    { proxyCommandFn: opts.sshProxyCommandFn },
  );
  const relayUrl = opts.relayUrl;
  const relayUrls = opts.relayUrls ?? (relayUrl ? [relayUrl] : []);
  const signer = opts.signer;

  const logger = opts.logger;
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));

  // Relay WS connect happens in serve.beginServe(); proxyRef is set by then.
  const proxyRef = pds.relay.proxyRef;
  const dispatcherHost = opts.dispatcherHost ??
    (pds.relaySubdomain.includes(".")
      ? pds.relaySubdomain.substring(pds.relaySubdomain.indexOf(".") + 1)
      : "xrpc.fedproxy.com");
  const relaySubdomain = pds.relaySubdomain.endsWith("." + dispatcherHost)
    ? pds.relaySubdomain.slice(0, pds.relaySubdomain.length - dispatcherHost.length - 1)
    : pds.relaySubdomain.split(".")[0];
  const fedproxyHost = opts.fedproxyHost ?? "fedproxy.com";

  log("relay_ready_for_rfp", { proxyRef });

  let cloudInit = "";
  let privateKeyPath = "";
  let vmFqdn = "";

  if (!skipSsh) {
    vmFqdn = `${flattenLabel(vmName)}--${flattenLabel(pds.did)}.${fedproxyHost}`;
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
    cloudInit = opts.baseUserData
      ? patchDefaultUserData(opts.baseUserData, ctx)
      : buildDefaultUserData(ctx);
    if (opts.userDataFactory) {
      // userDataFactory replaces the default cloud-init — caller builds the
      // guest transport (e.g. did-key-relay tunnel-subscriber replacing fedproxy).
      cloudInit = opts.userDataFactory(ssh.publicKey);
    }
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
  const rfpRecord: Record<string, unknown> = {
    $type: RFP_NSID,
    domain: "compute",
    payload: { $type: "com.atproto.repo.strongRef", uri: vmUri, cid: vmCid },
    submitBid: `${pds.did}#pdr_temp_market`,
    createdAt: new Date().toISOString(),
  };

  // Attach fulfillment policy if a policyMode is set.
  let policyRef: { uri: string; cid: string } | undefined;
  if (policyMode) {
    try {
      const { createPolicy } = await import("@publicdomainrelay/market-policy");
      const policy = createPolicy(policyMode);
      if (policy) {
        policyRef = await pds.createRepoRecord(policy.policyNsid, {
          $type: policy.policyNsid,
          requesterDid: pds.did,
          vouchNsid: policyMode === "direct_network" ? "sh.tangled.graph.vouch" : undefined,
          maxDepth: policyMode === "direct_network" ? 1 : undefined,
          createdAt: new Date().toISOString(),
        });
        rfpRecord.policy = { $type: "com.atproto.repo.strongRef", uri: policyRef.uri, cid: policyRef.cid };
        log("policy_attached", { policyMode, policyUri: policyRef.uri });
      }
    } catch (err) {
      log("policy_create_error", { error: String(err) });
    }
  }

  const { uri: rfpUri, cid: rfpCid } = await pds.createSignedRepoRecord(RFP_NSID, rfpRecord, pds.attestationKp, pds.did);
  log("rfp_created", { uri: rfpUri, cid: rfpCid, hasPolicy: !!policyRef });

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
  // Merge configured relayUrls + auto-discovered from $ATPROTO_DID.
  const autoRelayUrls = await autoDiscoverRelayUrls({ log: logger });
  const allRelayUrls = [...new Set([...relayUrls, ...autoRelayUrls])];
  let relayDids: string[] = [];
  if (allRelayUrls.length > 0) {
    relayDids = await discoverBiddersFromRelays({ relayUrls: allRelayUrls, collection: OFFERING_NSID, log: logger, timeoutMs: 15_000 });
    if (relayDids.length > 0) log("relay_discovery", { count: relayDids.length, relays: allRelayUrls.length, configured: relayUrls.length, autodiscovered: autoRelayUrls.length });
    else log("relay_discovery_empty", { relays: allRelayUrls.length, configured: relayUrls.length, autodiscovered: autoRelayUrls.length });
  }

  // 3c. Live firehose offering watch (complements the relay index; catches
  // offerings the relay lagged or missed during this run).
  const watcherDids = opts.offeringWatcherDids?.() ?? [];
  if (watcherDids.length > 0) log("offering_watch_discovery", { count: watcherDids.length });

  const bidderDids = Array.from(new Set([...relayDids, ...watcherDids, ...vouchedDids, ...extraBidderDids]));
  const deniedSet = new Set(denyBidderDids);
  const filteredBidderDids = bidderDids.filter((d) => !deniedSet.has(d));
  log("bidder_discovery", { total: filteredBidderDids.length, relay: relayDids.length, watch: watcherDids.length, vouched: vouchedDids.length, extra: extraBidderDids.length, denied: bidderDids.length - filteredBidderDids.length });

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

  // 6b. Authorize the VM to register its SSH host key. Resolve the winner's
  // bidConfig (wif.simple), then write a com.fedproxy.rbac record into our own
  // repo granting the VM (by wif subject) createRecord on com.fedproxy.sshPublicKey
  // for this VM's service name. The local PDS is served over the xrpc relay, so
  // the booting VM reaches it through the relay and publishes its host key —
  // exactly the reference compute-spa flow. Off unless opts.rbac (CLI default-on).
  if (opts.rbac && !skipSsh) {
    const bidConfigRef = (winner.record.config ?? winner.record.bidConfig) as { uri?: string; cid?: string } | undefined;
    if (bidConfigRef?.uri && bidConfigRef?.cid) {
      try {
        const resolver = createRecordResolver(idResolver);
        const cfg = await resolver.resolve({ uri: bidConfigRef.uri, cid: bidConfigRef.cid }) as Record<string, unknown>;
        const issuerUri = cfg.issuer_uri as string | undefined;
        const actx = cfg.actx as string | undefined;
        const subjectTemplate = cfg.subject as string | undefined;
        if (issuerUri && actx) {
          const serviceName = vmName.trim() || "compute";
          const rbacRecord = buildSshKeyRbacRecord({
            serviceName,
            issuerUri,
            actx,
            requesterDid: pds.did,
            subjectTemplate,
          });
          const { uri: rbacUri } = await pds.createRepoRecord(FEDPROXY_RBAC_NSID, rbacRecord);
          log("rbac_created", { uri: rbacUri, serviceName, issuerUri });
        } else {
          log("rbac_skipped", { reason: "bidConfig missing issuer_uri/actx", bidConfigUri: bidConfigRef.uri });
        }
      } catch (err) {
        log("rbac_skipped", { reason: String(err), bidConfigUri: bidConfigRef.uri });
      }
    } else {
      log("rbac_skipped", { reason: "winner bid has no bidConfig ref" });
    }
  }

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
    result.sshReady = ready;
    if (!ready) {
      log("vm_ssh_unavailable", { vmFqdn });
    } else {
      opts.onSshStart?.();
      const code = await sshProvider.runSession(privateKeyPath, vmFqdn, execProgram);
      await opts.onSshEnd?.();
      result.sshExitCode = code;
      log("vm_ssh_session_exit", { vmFqdn, code });
    }
  }

  // 11. Tear down VM via compute.events.vm.delete (unless --keep-vm).
  if (keepVm) {
    log("vm_delete_skipped", { reason: "--keep-vm" });
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
