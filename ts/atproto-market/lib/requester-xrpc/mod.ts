import { Secp256k1Keypair } from "@atproto/crypto";
import { IdResolver } from "@atproto/identity";
import { TID } from "@atproto/common";
import { createRepoFactory } from "@publicdomainrelay/hono-factory-atproto-repo-deno";
import { MemoryStorage, DenoKvStorage, signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import type { Signer } from "@publicdomainrelay/atproto-repo-abc";
import type { RepoApi } from "@publicdomainrelay/atproto-repo-abc";
import { PlcClient, createGenesisOp, PlcNotFoundError } from "@publicdomainrelay/did-plc";
import { createIngress } from "@publicdomainrelay/did-key-ingress-proxy";
import {
  loadOrGenerateKeypair,
  attestationFor,
  toStorableEntry,
  createSubmitBidHandler,
  createSubmitEventHandler,
  createRecordResolver,
} from "@publicdomainrelay/market-atproto";
import { OAuthClient } from "@atproto/oauth-client";
import { webCryptoRuntime, memoryStateStore, jsonSessionStore } from "@publicdomainrelay/atproto-oauth-helpers";
import type { AtprotoAgentLike } from "@publicdomainrelay/atproto-helpers";
import type { InlineAttestation, AttestationKeypair, SubmitBidCallback } from "@publicdomainrelay/market-atproto";
import { SUBMIT_BID_NSID, SUBMIT_EVENT_NSID } from "@publicdomainrelay/market-common";
import type { StrongRef } from "@publicdomainrelay/market-common";
import { DUMBPIPE_VERSION_DEFAULT } from "@publicdomainrelay/cloud-init-common";
import type {
  RequesterPDS,
  PDSOptions,
  CollectedBid,
  ContractFlowOptions,
  ContractFlowResult,
  SshSessionProvider,
} from "@publicdomainrelay/requester-abc";
import type { LoggerInterface, StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { ServeHandle, IngressRef } from "@publicdomainrelay/serve";
import { ASSOCIATE_CONFIRM_NSID, BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";
import { verifyServiceAuth } from "@publicdomainrelay/market-atproto";
import { IROH_SCHEME, NO_CONTRACT_OUTCOMES } from "@publicdomainrelay/compute-request-abc";
import type { ContractState } from "@publicdomainrelay/compute-request-abc";
import {
  DEFAULT_TRANSPORT_MODULE,
  DEFAULT_VM_READY_TIMEOUT_SEC,
  defaultVmName,
  flowViewOf,
  releaseCompute,
  requestCompute,
} from "@publicdomainrelay/compute-request-xrpc";

export { IROH_SCHEME } from "@publicdomainrelay/compute-request-abc";
export {
  autoDiscoverRelayUrls,
  DEFAULT_TRANSPORT_MODULE,
  discoverBiddersFromRelay,
  discoverBiddersFromRelays,
  withDeadline,
} from "@publicdomainrelay/compute-request-xrpc";

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

/**
 * Relay visibility result -- whether at least one relay that supports
 * listReposByCollection has indexed the bidder's offering.
 */
export interface RelayVisibilityResult {
  ok: boolean;
  /** Relays that support listReposByCollection (returned 200, not 404). */
  capableRelays: string[];
  /** Relays that returned the bidder's DID in the collection index. */
  indexedBy: string[];
  /** Relays that failed probing or don't support the endpoint. */
  failures: Array<{ url: string; reason: string }>;
}

/**
 * Verify the bidder's offering is discoverable through at least one relay
 * that supports listReposByCollection. Probes each relay to detect capability
 * (200 = supported, 404 = skip), then polls capable relays until the bidder's
 * DID appears in the collection index or the poll budget expires.
 *
 * Does NOT call requestCrawl -- registration is handled by the caller
 * (hono-bidder's registerPdsWithRelay). This function runs after beginServe()
 * when the offering record exists in the PDS repo.
 */
export async function verifyRelayVisibility(opts: {
  relayUrls: string[];
  bidderDid: string;
  collection: string;
  log?: LoggerInterface;
  pollTimeoutMs?: number;
  pollIntervalMs?: number;
}): Promise<RelayVisibilityResult> {
  const { relayUrls, bidderDid, collection, log, pollTimeoutMs = 15_000, pollIntervalMs = 2_000 } = opts;
  const failures: RelayVisibilityResult["failures"] = [];

  // Phase 1: Probe which relays support listReposByCollection.
  // limit=1000 so a repo whose DID sorts past the default page size (50) is
  // still returned — a lexicographically-late bidder DID otherwise falls on a
  // later page and the check never sees it.
  const collectionPath = `/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}&limit=1000`;
  const capableRelays: string[] = [];
  for (const url of relayUrls) {
    try {
      const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
      if (res.ok) {
        capableRelays.push(url);
        log?.info("relay_capable", { url });
      } else if (res.status === 404) {
        log?.info("relay_no_collection_support", { url });
        failures.push({ url, reason: "listReposByCollection not supported (404)" });
      } else {
        failures.push({ url, reason: `HTTP ${res.status}` });
      }
    } catch (err) {
      failures.push({ url, reason: String(err) });
    }
  }

  if (capableRelays.length === 0) {
    log?.warn("relay_no_capable_relays", { total: relayUrls.length, failures });
    return { ok: false, capableRelays: [], indexedBy: [], failures };
  }
  log?.info("relay_capable_relays", { count: capableRelays.length, relays: capableRelays });

  // Phase 2: Poll capable relays until bidder's DID appears
  const deadline = Date.now() + pollTimeoutMs;
  const indexedBy: string[] = [];
  while (Date.now() < deadline && indexedBy.length === 0) {
    for (const url of capableRelays) {
      try {
        const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
        if (!res.ok) continue;
        const body = await res.json() as { repos?: Array<{ did: string }> };
        if (body.repos?.some((r) => r.did === bidderDid)) {
          indexedBy.push(url);
        }
      } catch { /* probe failed, try next relay */ }
    }
    if (indexedBy.length === 0) {
      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }
  }

  const ok = indexedBy.length > 0;
  if (!ok) {
    log?.warn("relay_visibility_timeout", { capableRelays, pollTimeoutMs, failures });
  } else {
    log?.info("relay_visibility_confirmed", { indexedBy, capableRelays });
  }
  return { ok, capableRelays, indexedBy, failures };
}

// ---------------------------------------------------------------------------
// createRequesterPDS -- adapted from hono-bidder pattern + reference server.ts
// ---------------------------------------------------------------------------

export async function createRequesterPDS(
  opts: PDSOptions,
): Promise<RequesterPDSImpl> {
  const logger: StructuredLoggerInterface = opts.logger;
  const serve = opts.serve;
  const privateKeyHex = opts.privateKeyHex ?? "";
  const plcDirectoryUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const ingressProxyHost = opts.ingressProxyHost ?? "xrpc.fedproxy.com";
  const label = opts.label ?? "requester";

  // -- keypair ----------------------------------------------------------

  const keypair = privateKeyHex
    ? await Secp256k1Keypair.import(privateKeyHex)
    : await Secp256k1Keypair.create({ exportable: true });

  const privateKeyHexFinal = privateKeyHex ||
    Array.from(await keypair.export()).map((b) => b.toString(16).padStart(2, "0")).join("");

  // -- attestation keypair -----------------------------------------------

  const attestationKp = await loadOrGenerateKeypair(privateKeyHexFinal);

  // -- did:plc registration ---------------------------------------------

  const plc = new PlcClient({ baseUrl: plcDirectoryUrl });
  const signingKeyDid = keypair.did();
  const epHost = ingressProxyHost.replace(/:\d+$/, "");

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

  try {
    await plc.resolve(did);
    logger.info("did_plc_already_registered", { did, label });
  } catch (err) {
    if (err instanceof PlcNotFoundError) {
      logger.info("did_plc_registering", { did, label });
      await plc.submitOp(did, op);
      logger.info("did_plc_registered", { did, label });
    } else {
      throw err;
    }
  }

  // -- signer -----------------------------------------------------------

  const signer: Signer = {
    did: () => did,
    sign: (bytes) => keypair.sign(bytes),
  };

  // -- pending bids -----------------------------------------------------

  const pendingBids: Map<string, CollectedBid[]> = new Map();

  // -- contract state (vm identity tracking) ---------------------------

  interface ContractState {
    receiptUri: string;
    receiptCid: string;
    winnerDid: string;
    identities: string[];  // active compute identities (did:key)
    revoked: string[];     // previously active, now revoked
  }
  const activeContracts = new Map<string, ContractState>();

  // -- association confirmation (webapp calls this before RFP) ---------
  let resolveAssociateCalled: ((callerDid: string) => void) | null = null;
  const associateCalled = new Promise<string>((r) => { resolveAssociateCalled = r; });
  let resolveAssociationApproved: (() => void) | null = null;
  let rejectAssociationApproved: ((err: Error) => void) | null = null;
  const associationApproved = new Promise<void>((resolve, reject) => {
    resolveAssociationApproved = resolve;
    rejectAssociationApproved = reject;
  });

  // -- repo factory -----------------------------------------------------

  const baseOrigin = `https://${keypair.did().replace(/:/g, "-").toLowerCase()}.${ingressProxyHost}`;

  const store = opts.storagePath
    ? await DenoKvStorage.create(opts.storagePath)
    : new MemoryStorage();

  const { app, api } = createRepoFactory({
    storage: store,
    signer,
    baseOrigin,
    didWebServices: [
      { id: "pdr_temp_market", type: "PDRTempMarket" },
      { id: "pdr_temp_compute_event", type: "PDRTempComputeEvent" },
      { id: "requester_associate", type: "PDRRequesterAssociate" },
    ],
    publicKeyDid: keypair.did(),
    attestationKeyDid: attestationKp.did(),
  });

  // -- request/response logging middleware ------------------------------

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

  // -- relay (WS connect deferred to serve.beginServe -> relay.onServe) --

  const skipIngress = opts.skipIngress ?? false;
  const relay = skipIngress
    ? { ingressRef: "", ingressUrl: "", ingressHost: "", close() {}, onServe: async () => {} } as IngressRef
    : createIngress({ logger, ingressProxyHost, signer, keypair, label });

  // -- submitBid handler ------------------------------------------------

  const idResolver = new IdResolver({ plcUrl: plcDirectoryUrl });

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
        return host ? host.split(":")[0] : (relay.ingressHost || ingressProxyHost);
      },
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
    },
    serviceIds: ["pdr_temp_market"],
    onBid,
  });
  app.post(`/xrpc/${SUBMIT_BID_NSID}`, (c: { req: { raw: Request } }) => bidHandler(c.req.raw));

  // -- submitEvent handler ----------------------------------------------

  // Resolvers keyed by the contract's receipt, set by runComputeContract.
  //
  // One requester can serve several contracts at once, so a single slot here
  // would let whichever run registered last answer for all of them: a guest
  // authorizes only the key from its own run, so a run pointed at another run's
  // guest polls until it times out with "All configured authentication methods
  // failed". The receipt is the only thing in the event that identifies which
  // contract it belongs to.
  const onNetworkResolvers = new Map<string, (address: string) => void>();

  const submitEventHandler = createSubmitEventHandler({
    deps: {
      hostname: (req) => relay.ingressHost || ingressProxyHost,
      idResolver,
      resolve: createRecordResolver(idResolver),
      audienceDids: [did],
      log: ((level: string, message: string, meta?: Record<string, unknown>) => {
        const l = level as "info" | "warn" | "error" | "debug";
        logger[l]?.(message, meta ?? {});
      }) as unknown as (level: string, message: string, meta?: Record<string, unknown>) => void,
    },
    callbacks: {
      pdr_temp_compute_event: {
        // Handle vm.registerIdentity events
        "com.publicdomainrelay.temp.compute.events.vm.registerIdentity": async (ctx) => {
          const evt = ctx.event as any;
          const receiptKey = `${evt.receipt.uri}#${evt.receipt.cid}`;
          const identity = evt.payload?.computeIdentity;
          if (!identity) return;

          let state = activeContracts.get(receiptKey);
          if (!state) {
            state = { receiptUri: evt.receipt.uri, receiptCid: evt.receipt.cid, winnerDid: ctx.issuerDid, identities: [], revoked: [] };
            activeContracts.set(receiptKey, state);
          }
          // Rotate: old identities become revoked, new becomes active
          if (state.identities.length > 0) state.revoked.push(...state.identities);
          state.identities = [identity];
          logger.info("registerIdentity: updated contract state", { receiptKey, identity });
        },
        // Handle vm.onNetwork events
        "com.publicdomainrelay.temp.compute.events.vm.onNetwork": async (ctx) => {
          const evt = ctx.event as any;
          const receiptKey = `${evt.receipt?.uri ?? ""}#${evt.receipt?.cid ?? ""}`;
          logger.info("vm.onNetwork received", { receiptKey });
          // Resolve the wrapped onNetwork payload to extract the guest's FQDN
          // for SSH tunnel routing. Container IPs (bidder-side onNetwork) are
          // skipped -- only dispatcher FQDNs are usable as SSH ProxyCommand targets.
          const resolveFqdn = onNetworkResolvers.get(receiptKey);
          if (resolveFqdn) {
            try {
              const payloadRef = evt.payload as { uri: string; cid: string } | undefined;
              if (payloadRef?.uri) {
                const onNetworkRecord = await ctx.resolve.resolve({ uri: payloadRef.uri, cid: payloadRef.cid ?? "" }) as Record<string, unknown> | null;
                const address = onNetworkRecord?.address as string | undefined;
                // submitEvent only fires for guest-side onNetwork (never bidder-side
                // container IP). Accept any non-empty address; the SSH ProxyCommand
                // always routes through the relay dispatcher.
                if (address) {
                  resolveFqdn(address);
                }
              }
            } catch { /* best-effort */ }
          }
        },
      },
    },
    background: true,
  });
  app.post(`/xrpc/${SUBMIT_EVENT_NSID}`, (c) => submitEventHandler(c.req.raw));

  // -- associateConfirm (webapp calls to confirm requester association) --
  app.post(`/xrpc/${ASSOCIATE_CONFIRM_NSID}`, async (c) => {
    const authHeader = c.req.header("Authorization");
    if (!authHeader) return c.json({ error: "Unauthorized" }, 401);
    try {
      const auth = await verifyServiceAuth({
        authHeader,
        hostname: relay.ingressHost || ingressProxyHost,
        lxm: ASSOCIATE_CONFIRM_NSID,
        serviceIds: ["requester_associate"],
        extraAudienceDids: [did],
        idResolver,
      });
      resolveAssociateCalled?.(auth.issuerDid);
      // Wait for CLI user to approve/reject before responding to webapp
      await associationApproved;
      // Persist association in our own repo so it survives restarts
      // (mirrors bidder pattern: badgeBlueKeys with challenge=self, keyId=caller)
      await api.applyWrites(did, [{
        action: "create",
        collection: BADGE_BLUE_KEYS_NSID,
        rkey: TID.next().toString(),
        record: {
          $type: BADGE_BLUE_KEYS_NSID,
          // Canonical shape: {challenge: OPERATOR, keyId: ASSOCIATED}. auth.issuerDid
          // is the operator; did is the requester. The old inverted shape
          // (challenge=self, keyId=operator) mis-resolved an operator's own
          // acknowledgment as its operator.
          challenge: auth.issuerDid,
          keyId: did,
          service: "requester_associate",
          createdAt: new Date().toISOString(),
        },
      }]);
      return c.json({ ok: true, requesterDid: did });
    } catch (err) {
      return c.json({ error: String(err) }, 401);
    }
  });

  // -- mount the repo app + relay on the shared serve handle ------------

  serve.app.route("/", app as never);
  serve.addRelay(relay);

  // -- helpers ----------------------------------------------------------

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
        // Bare did:web aud — every bidder handler (submitRfp/Bid/Accept/Event)
        // accepts the bare aud in addition to `did:web:HOST#<service>`, and this
        // endpoint may serve ANY of them (an offering URL is the market service,
        // a receipt's submitEvent ref is the compute-event service). Pinning one
        // service id here (e.g. #pdr_temp_market) rejects the others.
        audDid: `did:web:${new URL(endpointUrl).host}`,
      };
    }
    if (endpointUrl.startsWith("did:")) {
      const didPart = endpointUrl.split("#")[0];
      const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : "pdr_temp_market";
      const svcDoc = await idResolver.did.resolve(didPart);
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
      signal: AbortSignal.timeout(10_000),
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
    get ingressRef(): string { return relay.ingressRef; },
    get ingressUrl(): string { return relay.ingressUrl; },
    get ingressHost(): string { return relay.ingressHost; },
    get relaySubdomain(): string { return relay.ingressHost; },
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
    setOnNetworkResolved: (key: string, fn: (address: string) => void) => { onNetworkResolvers.set(key, fn); },
    clearOnNetworkResolved: (key: string) => { onNetworkResolvers.delete(key); },
    dispose: async () => { store.close(); },
  };
}

// ---------------------------------------------------------------------------
// SSH session provider
// ---------------------------------------------------------------------------

/**
 * Tunnel URL for a guest FQDN. A guest announces `<subdomain>.<ingressProxyHost>`,
 * so an explicit port means a local dispatcher on plain HTTP; production is
 * `<subdomain>.fedproxy.com` on 443. Forcing wss:// at a plaintext listener fails
 * the TLS handshake ("record overflow") rather than falling back.
 */
export function tunnelWsUrl(fqdn: string): string {
  const scheme = fqdn.includes(":") ? "ws" : "wss";
  return `${scheme}://${fqdn}/xrpc/com.fedproxy.temp.xrpc.tunnel`;
}

export function sshTunnelArgs(
  privateKeyPath: string,
  fqdn: string,
  proxyCmdOverride?: string,
): string[] {
  return [
    "-o", `ProxyCommand=${proxyCmdOverride ?? `websocat --binary ${tunnelWsUrl(fqdn)}`}`,
    "-o", `IdentityFile=${privateKeyPath}`,
    "-o", "IdentitiesOnly=yes",
    "-o", "StrictHostKeyChecking=no",
    "-o", "UserKnownHostsFile=/dev/null",
    "-o", "LogLevel=ERROR",
  ];
}

export function sshProxyCommandFor(target: string, dumbpipePath?: string): string {
  if (target.startsWith(IROH_SCHEME)) {
    return `${dumbpipePath ?? "dumbpipe"} connect ${target.slice(IROH_SCHEME.length)}`;
  }
  return `websocat --binary ${tunnelWsUrl(target)}`;
}

export function createSshSessionProvider(
  logger?: StructuredLoggerInterface,
  opts?: { proxyCommandFn?: (target: string) => string; dumbpipePath?: string },
): SshSessionProvider {
  const proxyCommandFor = (target: string): string =>
    opts?.proxyCommandFn?.(target) ?? sshProxyCommandFor(target, opts?.dumbpipePath);
  let lastOutput = "";
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
    const proxyCmd = proxyCommandFor(fqdn);
    log("ssh_poll_start", { fqdn, proxyCmd: proxyCmd?.slice(0, 150), timeoutMs });
    const deadline = Date.now() + timeoutMs;
    let attempt = 0;
    while (Date.now() < deadline) {
      attempt++;
      const sshArgs = [
        ...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd),
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=10",
        `root@${fqdn}`,
        "true",
      ];
      if (attempt === 1) log("ssh_poll_cmd", { args: sshArgs.slice(0, 6) });
      const cmd = new Deno.Command("ssh", {
        args: sshArgs,
        stdout: "piped",
        stderr: "piped",
      });
      const { code, stdout, stderr } = await cmd.output();
      if (code === 0) {
        log("vm_ssh_ready", { fqdn, attempt });
        return true;
      }
      const errText = new TextDecoder().decode(stderr).trim();
      const outText = new TextDecoder().decode(stdout).trim();
      const fullError = (errText + (outText ? " | stdout:" + outText : "")).slice(0, 400);
      log("vm_ssh_poll", { fqdn, attempt, code, error: errText.slice(0, 200), fullError });
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
    const proxyCmd = proxyCommandFor(fqdn);
    const interactive = Deno.stdin.isTerminal();
    const args = [...sshTunnelArgs(privateKeyPath, fqdn, proxyCmd)];
    lastOutput = "";
    if (interactive) {
      args.push("-tt", `root@${fqdn}`);
      const cmd = new Deno.Command("ssh", { args, stdin: "inherit", stdout: "inherit", stderr: "inherit" });
      const child = cmd.spawn();
      const { code } = await child.status;
      return code;
    }
    args.push(`root@${fqdn}`, program);
    // Non-interactive: capture stdout/stderr while still live-streaming them,
    // so a caller can assert on what the guest printed (and the operator still
    // sees it) without a second, out-of-band read of the guest.
    const cmd = new Deno.Command("ssh", { args, stdin: "inherit", stdout: "piped", stderr: "piped" });
    const child = cmd.spawn();
    const pump = async (
      stream: ReadableStream<Uint8Array>,
      sink: (chunk: Uint8Array) => Promise<void>,
    ) => {
      const decoder = new TextDecoder();
      for await (const chunk of stream) {
        lastOutput += decoder.decode(chunk, { stream: true });
        try {
          await sink(chunk);
        } catch { /* the sink (a closed terminal) must not fail the session */ }
      }
    };
    const [status] = await Promise.all([
      child.status,
      pump(child.stdout, (chunk) => Deno.stdout.write(chunk).then(() => {})),
      pump(child.stderr, (chunk) => Deno.stderr.write(chunk).then(() => {})),
    ]);
    return status.code;
  }

  return { generateKeypair, pollReady, runSession, lastSessionOutput: () => lastOutput };
}

// ---------------------------------------------------------------------------
// dumbpipe bootstrap -- the binary the default iroh ProxyCommand runs
// ---------------------------------------------------------------------------

/** Inflate a gzip member with the Deno runtime (no shell-out to a package manager). */
async function gunzipToBytes(data: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([data as unknown as BlobPart]).stream()
    .pipeThrough(new DecompressionStream("gzip"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

/**
 * Read a tar archive into name -> bytes. The dumbpipe release archive carries a
 * single regular entry named ./dumbpipe and nothing else, but the reader stays
 * general (regular files only, longer pax names ignored) so a future release
 * that adds a file still unpacks.
 */
function untar(archive: Uint8Array): Map<string, Uint8Array> {
  const entries = new Map<string, Uint8Array>();
  const decoder = new TextDecoder();
  let offset = 0;
  while (offset + 512 <= archive.length) {
    const header = archive.subarray(offset, offset + 512);
    if (header.every((b) => b === 0)) break;
    const name = decoder.decode(header.subarray(0, 100)).replace(/\0.*$/, "");
    const sizeText = decoder.decode(header.subarray(124, 136)).replace(/\0.*$/, "").trim();
    const size = Number.parseInt(sizeText, 8) || 0;
    const type = String.fromCharCode(header[156] || 48);
    const dataStart = offset + 512;
    if ((type === "0" || type === "\0") && name) {
      entries.set(name, archive.subarray(dataStart, dataStart + size));
    }
    offset = dataStart + Math.ceil(size / 512) * 512;
  }
  return entries;
}

/**
 * Make a dumbpipe binary available to this process and return the path the
 * iroh ProxyCommand should run. Prefers a dumbpipe already on PATH; otherwise
 * downloads the pinned release archive, unpacks the `dumbpipe` entry it carries
 * into a private temp dir with mode 0755 and prepends that dir to PATH. Never
 * throws: an unreachable release degrades to an operator-provided dumbpipe on
 * PATH. The pinned version is the same release the iroh cloud-init module
 * installs, so host and guest speak one dumbpipe.
 */
export async function ensureDumbpipe(
  logger?: StructuredLoggerInterface,
): Promise<string> {
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));
  try {
    const which = new Deno.Command("which", { args: ["dumbpipe"], stdout: "piped", stderr: "null" });
    const whichRes = await which.output();
    if (whichRes.code === 0) {
      const found = new TextDecoder().decode(whichRes.stdout).trim();
      log("dumbpipe_found", { source: "system", path: found });
      return found || "dumbpipe";
    }

    const os = Deno.build.os === "linux" || Deno.build.os === "darwin" ? Deno.build.os : null;
    const arch = Deno.build.arch === "x86_64" || Deno.build.arch === "aarch64"
      ? Deno.build.arch
      : null;
    if (!os || !arch) {
      log("dumbpipe_unsupported", { os: Deno.build.os, arch: Deno.build.arch });
      return "dumbpipe";
    }

    const url =
      `https://github.com/n0-computer/dumbpipe/releases/download/v${DUMBPIPE_VERSION_DEFAULT}/` +
      `dumbpipe-v${DUMBPIPE_VERSION_DEFAULT}-${os}-${arch}.tar.gz`;
    log("dumbpipe_downloading", { url, version: DUMBPIPE_VERSION_DEFAULT });

    const resp = await fetch(url);
    if (!resp.ok) {
      log("dumbpipe_download_failed", { status: resp.status, url });
      return "dumbpipe";
    }
    const entries = untar(await gunzipToBytes(new Uint8Array(await resp.arrayBuffer())));
    const binary = entries.get("./dumbpipe") ?? entries.get("dumbpipe") ??
      [...entries.entries()].find(([name]) => name.endsWith("/dumbpipe"))?.[1];
    if (!binary) {
      log("dumbpipe_download_failed", { reason: "archive carries no dumbpipe entry", url });
      return "dumbpipe";
    }

    const dir = await Deno.makeTempDir({ prefix: "dumbpipe-" });
    const binPath = `${dir}/dumbpipe`;
    await Deno.writeFile(binPath, binary, { mode: 0o755 });
    log("dumbpipe_downloaded", { path: binPath, version: DUMBPIPE_VERSION_DEFAULT });

    Deno.env.set("PATH", `${dir}:${Deno.env.get("PATH") ?? ""}`);
    log("dumbpipe_path_updated", { dir });
    return binPath;
  } catch (err) {
    log("dumbpipe_download_failed", { error: String(err) });
    return "dumbpipe";
  }
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

function waitForAbort(signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (!signal || signal.aborted) {
      resolve();
      return;
    }
    signal.addEventListener("abort", () => resolve(), { once: true });
  });
}

export function contractFlowResultOf(state: ContractState): ContractFlowResult {
  if (state.outcome && NO_CONTRACT_OUTCOMES.includes(state.outcome)) {
    return state.outcome === "no_bids"
      ? { event: state.outcome, error: state.error }
      : { event: state.outcome, error: state.error, bids: state.bids };
  }
  return { event: "compute_request_complete", ...flowViewOf(state) } as ContractFlowResult;
}

export async function runComputeContract(
  pds: RequesterPDS,
  opts: ContractFlowOptions & {
    sshProvider?: SshSessionProvider;
    relayUrls?: string[];
    relayUrl?: string;
    signer?: Signer;
    offeringWatcherDids?: () => string[];
    logger?: StructuredLoggerInterface;
    payloadFactory?: () => Promise<{ uri: string; cid: string }>;
    vmDisk?: string;
    eventStreams?: import("@publicdomainrelay/atproto-event-streams-client").ATProtoEventStreamsClient;
    scopeCache?: import("@publicdomainrelay/policy-engine-evaluator").ScopeCache;
  } = {},
): Promise<ContractFlowResult> {
  const vmName = opts.vmName ?? defaultVmName();
  const skipSsh = opts.skipSsh ?? false;
  const execProgram = opts.execProgram ?? "bash";
  const keepVm = opts.keepVm ?? false;
  const vmReadyTimeoutSec = opts.vmReadyTimeoutSec ?? DEFAULT_VM_READY_TIMEOUT_SEC;
  const transport = opts.userData?.transport ?? DEFAULT_TRANSPORT_MODULE;
  const logger = opts.logger;
  const log = (event: string, extra: Record<string, unknown> = {}) =>
    logger ? logger.info(event, extra) : console.log(JSON.stringify({ event, ...extra }));

  let dumbpipePath: string | undefined;
  if (!opts.sshProvider && !skipSsh) {
    if (transport === "iroh") dumbpipePath = await ensureDumbpipe(logger);
    else if (transport === "tunnel" || transport === "fedproxy-ssh") await ensureWebsocat(logger);
  }
  const sshProvider = opts.sshProvider ?? createSshSessionProvider(
    logger,
    { proxyCommandFn: opts.sshProxyCommandFn, dumbpipePath },
  );

  let privateKeyPath = "";
  let sshPublicKey: string | undefined;
  if (!skipSsh) {
    const ssh = await sshProvider.generateKeypair(vmName);
    privateKeyPath = ssh.privateKeyPath;
    sshPublicKey = ssh.publicKey;
    log("ssh_keypair_generated", {
      privateKeyPath,
      publicKey: ssh.publicKey,
      hint: "FQDN will be discovered from vm.onNetwork event after guest tunnel subscriber registers",
    });
  }

  const contract = await requestCompute(pds, { ...opts, vmName, sshPublicKey });
  const state = contract.state;
  const result = contractFlowResultOf(state);
  if (state.outcome && NO_CONTRACT_OUTCOMES.includes(state.outcome)) {
    await contract.dispose();
    return result;
  }

  if (!skipSsh && state.receiptOk) {
    const vmAddress = state.vmAddress ?? "";
    if (!vmAddress) {
      result.sshReady = false;
    } else {
      result.sshProxyCommand = opts.sshProxyCommandFn?.(vmAddress) ??
        sshProxyCommandFor(vmAddress, dumbpipePath);
      log("vm_ssh_waiting", { vmFqdn: vmAddress, sshProxyCommand: result.sshProxyCommand, timeoutSec: vmReadyTimeoutSec });
      const ready = await sshProvider.pollReady(privateKeyPath, vmAddress, vmReadyTimeoutSec * 1000);
      result.sshReady = ready;
      if (!ready) {
        log("vm_ssh_unavailable", { vmFqdn: vmAddress });
      } else if (opts.hold && opts.holdAbort) {
        log("vm_hold_started", { vmFqdn: vmAddress });
        await waitForAbort(opts.holdAbort);
        log("vm_hold_released", { vmFqdn: vmAddress });
      } else {
        opts.onSshStart?.();
        const code = await sshProvider.runSession(privateKeyPath, vmAddress, execProgram);
        await opts.onSshEnd?.();
        result.sshExitCode = code;
        result.sshOutput = sshProvider.lastSessionOutput?.();
        log("vm_ssh_session_exit", { vmFqdn: vmAddress, code });
      }
    }
  }

  if (keepVm) {
    log("vm_delete_skipped", { reason: "--keep-vm" });
  } else {
    await releaseCompute(pds, state, { logger, capabilities: contract.capabilities });
  }

  await contract.dispose();
  return result;
}

// ---------------------------------------------------------------------------
// OAuth requester -- lightweight RequesterPDS backed by OAuth agent
// ---------------------------------------------------------------------------

export interface OAuthRequesterHandle {
  pds: RequesterPDS;
  startFlow(): Promise<string>;
  completeFlow(params: Record<string, string>): Promise<void>;
  restore(): Promise<boolean>;
}

export interface CreateOAuthRequesterOpts {
  handle: string;
  sessionPath: string;
  clientId?: string;
  redirectUri?: string;
  scope?: string;
  pdsUrl?: string;
  plcDirectoryUrl?: string;
  logger?: StructuredLoggerInterface;
  attestationKp: AttestationKeypair;
  privateKeyHex: string;
}

/**
 * Point a RequesterPDS at a user's own account.
 *
 * In OAuth mode every market record must be authored by the signed-in user, not
 * by the requester's ephemeral repo: the ephemeral repo is reachable only
 * through the ingress relay and is invisible to the firehose every bidder
 * reads. Three things have to line up:
 *
 *  - createRepoRecord writes through the user's PDS, so the records are public.
 *  - createSignedRepoRecord must bind the attestation to the USER's DID as the
 *    repository. runComputeContract verifies that binding, and a mismatch fails
 *    the receipt, which skips SSH entirely.
 *  - callBidder must mint service-auth from the OAuth session, because a bidder
 *    rejects a token whose issuer is not the record's author.
 *
 * Extracted from request-vm-ssh's CLI so a library caller can embed
 * runComputeContract without reimplementing it.
 */
export function applyOAuthAgentToRequesterPDS(
  pds: RequesterPDS,
  agent: AtprotoAgentLike & { sessionData: { userDid: string }; getServiceAuth?: (aud: string, lxm?: string) => Promise<string> },
  opts?: { log?: (event: string, data: Record<string, unknown>) => void },
): void {
  const log = opts?.log ?? (() => {});
  const did = agent.sessionData.userDid;

  // The market identity is the signed-in user, not the ephemeral repo the
  // requester PDS was built on. requester-xrpc derives marketDid from this
  // (mod.ts:927) and every trust lookup -- vouch reads, operator resolution --
  // keys off it, so leaving it unset makes those read a repo that holds none of
  // the account's records.
  const target = pds as unknown as { oauthAgent?: unknown; oauthSession?: unknown };
  target.oauthAgent = agent;
  target.oauthSession = { userDid: did };

  pds.createRepoRecord = async (collection: string, record: Record<string, unknown>) => {
    const rkey = TID.next().toString();
    const { uri, cid } = await agent.createRecord!(did, collection, rkey, record);
    return { uri, cid };
  };

  pds.createSignedRepoRecord = async (
    collection: string,
    record: Record<string, unknown>,
    aKp?: { did(): string; privateKey: { bytes: Uint8Array } },
    issuer?: string,
  ) => {
    const rkey = TID.next().toString();
    const att = attestationFor(aKp as AttestationKeypair, issuer);
    const entry = await att.sign({ record, repository: did }) as InlineAttestation;
    const signed = { ...record, signatures: [toStorableEntry(entry)] };
    const { uri, cid } = await agent.createRecord!(did, collection, rkey, signed);
    return { uri, cid };
  };

  if (agent.getServiceAuth) {
    pds.callBidder = async (targetBase: string, nsid: string, lxm: string, audDid: string, body: Record<string, unknown>) => {
      try {
        const token = await agent.getServiceAuth!(audDid, lxm);
        const res = await fetch(`${targetBase}/${nsid}`, {
          method: "POST",
          headers: { "content-type": "application/json", authorization: `Bearer ${token}` },
          body: JSON.stringify(body),
          signal: AbortSignal.timeout(10_000),
        });
        const text = await res.text();
        let parsed: unknown;
        try { parsed = JSON.parse(text); } catch { parsed = text; }
        return { status: res.status, ok: res.ok, body: parsed };
      } catch (err) {
        log("callBidder_error", { nsid, lxm, audDid, targetBase, error: String(err) });
        throw err;
      }
    };
  }
}

export async function createOAuthRequester(opts: CreateOAuthRequesterOpts): Promise<OAuthRequesterHandle> {
  const clientId = opts.clientId ?? "http://localhost";
  const redirectUri = opts.redirectUri ?? "http://127.0.0.1:0/callback";
  const scope = opts.scope ?? "atproto";
  const log = opts.logger;

  const client = new OAuthClient({
    responseMode: "query",
    clientMetadata: {
      client_id: clientId,
      application_type: "web",
      dpop_bound_access_tokens: true,
      redirect_uris: [redirectUri],
      grant_types: ["authorization_code", "refresh_token"],
      response_types: ["code"],
      scope,
      token_endpoint_auth_method: "none",
    },
    stateStore: memoryStateStore(),
    sessionStore: jsonSessionStore(opts.sessionPath),
    runtimeImplementation: webCryptoRuntime(),
    identityResolver: {
      resolve: async (identifier: string) => {
        const resolver = new IdResolver({ plcUrl: opts.plcDirectoryUrl ?? "https://plc.directory" });
        const did = identifier.startsWith("did:") ? identifier : (await resolver.handle.resolve(identifier)) ?? identifier;
        const didDoc = await resolver.did.resolve(did) as Record<string, unknown>;
        const handle = ((didDoc?.alsoKnownAs as string[] | undefined)?.[0] ?? "").replace("at://", "");
        return { did, didDoc, handle: handle || "handle.invalid" };
      },
    } as never,
    allowHttp: clientId === "http://localhost",
  });

  let _session: Awaited<ReturnType<typeof client.restore>> | null = null;
  function getSession() { if (!_session) throw new Error("OAuth session not initialized"); return _session; }

  let _did = "";
  const idResolver = new IdResolver();

  const pds: RequesterPDS = {
    get did() { return _did; },
    serve: { tcpPort: 0, onConnected() {}, beginServe: async () => {}, shutdown() {}, app: { route() {}, fetch: async () => new Response() } } as unknown as ServeHandle,
    relay: { ingressRef: "", ingressUrl: "", ingressHost: "", close() {}, onServe: async () => {} } as IngressRef,
    ingressRef: "",
    relaySubdomain: "",
    get ingressUrl() { return ""; },
    get ingressHost() { return ""; },
    async beginServe() {},
    pendingBids: new Map(),
    attestationKp: opts.attestationKp,
    signer: {
      did: () => _did,
      sign: async () => { throw new Error("use getServiceAuth for OAuth requester"); },
    },
    privateKeyHex: opts.privateKeyHex,
    associateCalled: Promise.resolve(""),
    approveAssociation() {},
    rejectAssociation(_err: Error) {},
    async dispose() {},

    async createRepoRecord(collection: string, record: Record<string, unknown>) {
      const s = getSession();
      const rkey = TID.next().toString();
      const res = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.applyWrites`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ repo: _did, writes: [{ action: "create", collection, rkey, record }] }),
      });
      if (!res.ok) throw new Error(`applyWrites failed: ${res.status}`);
      const rec = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(_did)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`);
      if (!rec.ok) throw new Error(`getRecord failed: ${rec.status}`);
      const data = await rec.json() as { uri: string; cid?: string };
      return { uri: data.uri, cid: data.cid ?? "" };
    },

    async createSignedRepoRecord(collection: string, record: Record<string, unknown>, aKp: AttestationKeypair, issuer?: string) {
      const s = getSession();
      const rkey = TID.next().toString();
      const att = attestationFor(aKp, issuer);
      const entry = await att.sign({ record: record as Record<string, unknown>, repository: _did }) as InlineAttestation;
      const signed = { ...record, signatures: [toStorableEntry(entry)] };
      const res = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.applyWrites`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ repo: _did, writes: [{ action: "create", collection, rkey, record: signed }] }),
      });
      if (!res.ok) throw new Error(`applyWrites failed: ${res.status}`);
      const rec = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(_did)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`);
      if (!rec.ok) throw new Error(`getRecord failed: ${rec.status}`);
      const data = await rec.json() as { uri: string; cid?: string };
      return { uri: data.uri, cid: data.cid ?? "" };
    },

    async resolveBidderEndpoint(endpointUrl: string) {
      if (endpointUrl.startsWith("http://") || endpointUrl.startsWith("https://")) {
        return { targetUrl: `${endpointUrl.replace(/\/+$/, "")}/xrpc`, audDid: `did:web:${new URL(endpointUrl).host}#pdr_temp_market` };
      }
      if (endpointUrl.startsWith("did:")) {
        const didPart = endpointUrl.split("#")[0];
        const svcId = endpointUrl.includes("#") ? endpointUrl.split("#")[1] : "pdr_temp_market";
        const doc = await idResolver.did.resolve(didPart);
        const svc = doc?.service?.find?.((s: { id: string }) => s.id === `#${svcId}`);
        if (!svc) return null;
        const ep = (svc as { serviceEndpoint: string }).serviceEndpoint.replace(/\/+$/, "");
        return { targetUrl: `${ep}/xrpc`, audDid: `did:web:${new URL(ep).host}#pdr_temp_market` };
      }
      return null;
    },

    async callBidder(targetBase: string, nsid: string, lxm: string, audDid: string, body: Record<string, unknown>) {
      const s = getSession();
      const saRes = await s.fetchHandler(`${s.server.issuer}/xrpc/com.atproto.server.getServiceAuth`, {
        method: "POST", headers: { "content-type": "application/json" },
        body: JSON.stringify({ aud: audDid, lxm }),
      });
      if (!saRes.ok) throw new Error(`getServiceAuth failed: ${saRes.status}`);
      const saData = await saRes.json() as { token: string };
      const res = await fetch(`${targetBase}/${nsid}`, {
        method: "POST",
        headers: { "content-type": "application/json", authorization: `Bearer ${saData.token}` },
        body: JSON.stringify(body),
      });
      let resBody: unknown;
      try { resBody = await res.json(); } catch { resBody = await res.text(); }
      return { status: res.status, ok: res.ok, body: resBody };
    },

  };

  return {
    pds,
    async startFlow(): Promise<string> {
      const result = await client.authorize(opts.handle, { scope });
      return String(result);
    },
    async completeFlow(params: Record<string, string>): Promise<void> {
      const result = await client.callback(new URLSearchParams(params));
      _session = result.session;
      _did = result.session.did;
      log?.info("oauth_session_complete", { did: _did });
    },
    async restore(): Promise<boolean> {
      try { _session = await client.restore(opts.handle); _did = _session.did; log?.info("oauth_session_restored", { did: _did }); return true; }
      catch { return false; }
    },
  };
}
