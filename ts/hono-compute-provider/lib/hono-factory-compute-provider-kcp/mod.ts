import { Hono } from "@hono/hono";
import { createFactory } from "@hono/hono/factory";
import { EVENT_NSID, ON_NETWORK_NSID, SUBMIT_EVENT_NSID } from "@publicdomainrelay/compute-provider-common";
import {
  type StrongRef,
  type ComputeAtproto,
  type ComputeProvider,
  type ComputeProviderCtx,
  type GuestHostKeysAck,
  GuestHostKeysRelay,
} from "@publicdomainrelay/compute-provider-abc";
import {
  createComputeProviderKcp,
  createComputeVMMarketRecorder,
  createKcpComputeVMClient,
  type GuestVault,
  type KcpComputeVMClient,
  type KcpCredentials,
} from "@publicdomainrelay/compute-provider-kcp";
import {
  ComputeVMAccepts,
  type ComputeVMChange,
  hasEnded,
  isReleasing,
} from "@publicdomainrelay/compute-provider-kcp-abc";
import {
  type ComputeVM,
  type ComputeVMMarket,
  FIELD_MANAGER_COMPUTE_VM_GUEST,
  GUEST_HOST_ADDRESS,
} from "@publicdomainrelay/compute-provider-kcp-common";
import {
  type GuestIdentity,
  guestLoginBody,
  type GuestTokenVerifier,
  isGuestLoginStep,
  TokenBuckets,
} from "@publicdomainrelay/guest-identity-abc";
import { createGuestIdentityGrantClient, createGuestIdentityGrants } from "@publicdomainrelay/guest-identity-grants-kcp";
import type { GuestIdentityGrants } from "@publicdomainrelay/guest-identity-abc";
import {
  GUEST_COMPUTEVM_PATTERN,
  GUEST_ROLE_PATTERN,
  GUEST_VAULT_TOKEN_HEADER,
  guestOidcIssuer,
} from "@publicdomainrelay/guest-identity-common";

export const PROVIDER_MARKET_FIELDS: ReadonlyArray<keyof ComputeVMMarket> = ["onNetworkUri", "hostKeysUri"];
export const GUEST_MARKET_FIELDS: ReadonlyArray<keyof ComputeVMMarket> = ["address", "reportedAt", "hostKeys"];

const REPORT_RETRY_MS = 5_000;
const REPORT_RETRY_MAX_MS = 300_000;

export interface ComputeProviderKcpFactoryOptions extends ComputeProviderCtx {
  atproto: ComputeAtproto;
  credentials: KcpCredentials;
  fetch?: typeof fetch;
  handle: string;
  guestVault: GuestVault;
  guestHostAddress?: string;
  getIssuerUrl: () => string;
  acceptPathVm?: string;
  guestTlsPort?: number;
  reuseStale?: boolean;
  stopGraceSeconds?: number;
  guestLoginMount: string;
  plcDirectoryUrl: string;
  guestIdentityResyncMs?: number;
  bootTimeoutMs?: number;
  releaseTimeoutMs?: number;
  contractStoreReleases?: boolean;
  createSignedRepoRecord?: (
    collection: string,
    record: Record<string, unknown>,
    issuer?: string,
  ) => Promise<{ uri: string; cid: string; record: Record<string, unknown> }>;
  callService?: (
    endpointUrl: string,
    nsid: string,
    lxm: string,
    body: Record<string, unknown>,
  ) => Promise<{ status: number; ok: boolean; body: unknown }>;
}

export interface ComputeProviderKcpFactory {
  provider: ComputeProvider;
  client: KcpComputeVMClient;
  accepts: ComputeVMAccepts;
  identities: GuestIdentityGrants;
}

export function createComputeProviderKcpFactory(opts: ComputeProviderKcpFactoryOptions): ComputeProviderKcpFactory {
  const { logger, atproto } = opts;
  const hostKeys = new GuestHostKeysRelay();
  const client = createKcpComputeVMClient({ credentials: opts.credentials, fetch: opts.fetch });
  const identities = createGuestIdentityGrants({
    grants: createGuestIdentityGrantClient({ credentials: opts.credentials, fetch: opts.fetch }),
    logger,
    issuer: () => guestOidcIssuer(opts.getIssuerUrl()),
    plcDirectoryUrl: opts.plcDirectoryUrl,
    resyncMs: opts.guestIdentityResyncMs,
  });
  const accepts = new ComputeVMAccepts();
  const market = createComputeVMMarketRecorder({ client, logger, fields: PROVIDER_MARKET_FIELDS });
  const latest = new Map<string, ComputeVM>();
  const published = new Set<string>();
  const inFlight = new Set<string>();
  const onNetworkWrites = new Map<string, { payload?: StrongRef; eventSigned?: boolean }>();
  const failures = new Map<string, number>();
  const notBefore = new Map<string, number>();

  async function publishOnNetwork(name: string, vm: ComputeVM, report: ComputeVMMarket): Promise<boolean> {
    const contract = vm.spec?.contract;
    if (!contract?.receiptUri || !contract.receiptCid) return false;
    const written = onNetworkWrites.get(name) ?? {};
    onNetworkWrites.set(name, written);
    written.payload ??= await atproto.createRecord(ON_NETWORK_NSID, {
      $type: ON_NETWORK_NSID,
      address: report.address,
      createdAt: report.reportedAt ?? new Date().toISOString(),
    });
    const ref = written.payload;
    if (opts.createSignedRepoRecord && !written.eventSigned) {
      const { uri: eventUri, cid: eventCid, record: eventRecord } = await opts.createSignedRepoRecord(
        EVENT_NSID,
        {
          $type: EVENT_NSID,
          receipt: { $type: "com.atproto.repo.strongRef", uri: contract.receiptUri, cid: contract.receiptCid },
          payload: { $type: "com.atproto.repo.strongRef", uri: ref.uri, cid: ref.cid },
        },
        atproto.getAgentDid(),
      );
      written.eventSigned = true;
      if (contract.submitEventUrl && opts.callService) {
        opts.callService(contract.submitEventUrl, SUBMIT_EVENT_NSID, SUBMIT_EVENT_NSID, {
          uri: eventUri,
          cid: eventCid,
          record: eventRecord,
        }).catch((err: unknown) => logger.warn("guest.onNetwork submitEvent failed", { name, error: String(err) }));
      }
      logger.info("guest.onNetwork recorded with event", { name, receiptUri: contract.receiptUri, uri: ref.uri });
    } else {
      logger.info("guest.onNetwork recorded (no event wrapper)", { name, uri: ref.uri, address: report.address });
    }
    return await market.record(name, { onNetworkUri: ref.uri });
  }

  async function publishHostKeys(name: string, report: ComputeVMMarket): Promise<boolean> {
    const ack: GuestHostKeysAck = await hostKeys.deliver(name, report.hostKeys ?? [], new Date().toISOString());
    if (ack.ok) {
      logger.info("guest_hostkeys_published", { name, uri: ack.uri });
      if (ack.uri) await market.record(name, { hostKeysUri: ack.uri });
      return true;
    }
    logger.warn("guest_hostkeys_not_published", { name, status: ack.status, reason: ack.reason });
    return ack.status === 400 || ack.status === 409;
  }

  function attempt(key: string, name: string, work: () => Promise<boolean>): void {
    if (published.has(key) || inFlight.has(key) || Date.now() < (notBefore.get(key) ?? 0)) return;
    inFlight.add(key);
    work().catch((err: unknown) => {
      logger.warn("guest_report_publish_failed", { name, report: key, error: String(err) });
      return false;
    }).then((done) => {
      inFlight.delete(key);
      if (done) {
        published.add(key);
        failures.delete(key);
        notBefore.delete(key);
        return;
      }
      const count = (failures.get(key) ?? 0) + 1;
      failures.set(key, count);
      const delayMs = Math.min(REPORT_RETRY_MAX_MS, REPORT_RETRY_MS * 2 ** (count - 1));
      notBefore.set(key, Date.now() + delayMs);
      const retry = setTimeout(() => consider(name), delayMs);
      Deno.unrefTimer(retry);
    });
  }

  function consider(name: string): void {
    const vm = latest.get(name);
    const report = vm?.status?.market;
    if (!vm || !report || hasEnded(vm) || isReleasing(vm)) return;
    if (report.address && !report.onNetworkUri) {
      const contract = vm.spec?.contract;
      if (contract?.receiptUri && contract.receiptCid) {
        attempt(`${name}#onNetwork`, name, () => publishOnNetwork(name, vm, report));
      } else {
        logger.info("guest_onnetwork_waits_for_contract", { name, address: report.address });
      }
    }
    if (report.hostKeys?.length && !report.hostKeysUri) attempt(`${name}#hostKeys`, name, () => publishHostKeys(name, report));
  }

  function onChange(change: ComputeVMChange): void {
    if (!change.after) {
      latest.delete(change.name);
      onNetworkWrites.delete(change.name);
      for (const key of [...published]) if (key.startsWith(`${change.name}#`)) published.delete(key);
      for (const table of [failures, notBefore]) for (const key of [...table.keys()]) if (key.startsWith(`${change.name}#`)) table.delete(key);
      return;
    }
    latest.set(change.name, change.after);
    consider(change.name);
  }

  const provider = createComputeProviderKcp({
    logger,
    atproto,
    client,
    handle: opts.handle,
    guestVault: opts.guestVault,
    guestHostAddress: opts.guestHostAddress ?? GUEST_HOST_ADDRESS,
    getIssuerUrl: opts.getIssuerUrl,
    acceptPathVm: opts.acceptPathVm,
    guestTlsPort: opts.guestTlsPort,
    reuseStale: opts.reuseStale,
    stopGraceSeconds: opts.stopGraceSeconds,
    guestLoginMount: opts.guestLoginMount,
    identities,
    bootTimeoutMs: opts.bootTimeoutMs,
    releaseTimeoutMs: opts.releaseTimeoutMs,
    hostKeys,
    accepts,
    contractStoreReleases: opts.contractStoreReleases,
    onChange,
  });

  return { provider, client, accepts, identities };
}

export interface ComputeProviderKcpGuestFactoryOptions {
  logger: ComputeProviderCtx["logger"];
  credentials: KcpCredentials;
  fetch?: typeof fetch;
  handle: string;
  guestIdentity: GuestIdentity;
  verifier: GuestTokenVerifier;
  loginMount: string;
}

export interface ComputeProviderKcpGuestFactory {
  client: KcpComputeVMClient;
  createApp(): Hono;
}

const HOST_KEY_RECORD_RETRY_MS = 5_000;
const HOST_KEY_RECORD_ATTEMPTS = 24;

async function fingerprintOf(blobBase64: string): Promise<string> {
  const blob = Uint8Array.from(atob(blobBase64), (c) => c.charCodeAt(0));
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", blob));
  return "SHA256:" + btoa(String.fromCharCode(...digest)).replace(/=+$/, "");
}

export function createComputeProviderKcpGuestFactory(opts: ComputeProviderKcpGuestFactoryOptions): ComputeProviderKcpGuestFactory {
  const { logger, handle, guestIdentity, verifier } = opts;
  const client = createKcpComputeVMClient({ credentials: opts.credentials, fetch: opts.fetch, fieldManager: FIELD_MANAGER_COMPUTE_VM_GUEST });
  const market = createComputeVMMarketRecorder({ client, logger, fields: GUEST_MARKET_FIELDS });
  const loginLimit = new TokenBuckets(30, 1);
  const tokenLimit = new TokenBuckets(20, 0.5);
  const publicLimit = new TokenBuckets(60, 10);

  async function guestVM(name: string): Promise<ComputeVM | undefined> {
    const vm = await client.get(name);
    if (!vm || vm.spec?.owner?.handle !== handle || hasEnded(vm) || isReleasing(vm)) return undefined;
    return vm;
  }

  async function recordBoundHostKey(name: string): Promise<boolean> {
    const vm = await guestVM(name);
    if (!vm) return false;
    if (vm.status?.market?.hostKeys?.length) return true;
    const bound = await guestIdentity.boundHostKey(name);
    if (!bound) return false;
    const [type, publicKey] = bound.split(/\s+/);
    if (!type || !publicKey) return false;
    const key = { type, publicKey, fingerprint: await fingerprintOf(publicKey) };
    if (!(await market.record(name, { hostKeys: [key] }))) return false;
    logger.info("guest_hostkeys_recorded", { name, types: [type] });
    return true;
  }

  function recordBoundHostKeyUntilDone(name: string, attempt = 0): void {
    recordBoundHostKey(name).catch((err: unknown) => {
      logger.warn("guest_hostkeys_record_failed", { name, error: String(err) });
      return false;
    }).then((done) => {
      if (done || attempt + 1 >= HOST_KEY_RECORD_ATTEMPTS) return;
      const retry = setTimeout(() => recordBoundHostKeyUntilDone(name, attempt + 1), HOST_KEY_RECORD_RETRY_MS);
      Deno.unrefTimer(retry);
    });
  }

  function onNetworkRoutes(): Hono {
    const events = new Hono();
    events.post("/v1/on-network", async (c) => {
      const authHeader = c.req.header("Authorization");
      const token = authHeader?.startsWith("Bearer ") ? authHeader.slice(7) : null;
      if (!token) return c.json({ error: "AuthenticationRequired" }, 401);
      const validated = await verifier.verify(token).catch((err: unknown) => {
        logger.warn("guest.onNetwork: the token is refused", { error: String(err) });
        return null;
      });
      if (!validated) return c.json({ error: "InvalidToken" }, 401);
      let body: Record<string, unknown>;
      try {
        body = await c.req.json();
      } catch {
        return c.json({ error: "InvalidRequest" }, 400);
      }
      const acceptUri = body.acceptUri as string | undefined;
      const acceptCid = body.acceptCid as string | undefined;
      const address = body.address;
      if (!acceptUri || !acceptCid || typeof address !== "string" || !address) {
        return c.json({ error: "InvalidRequest", message: "acceptUri, acceptCid and address are required" }, 400);
      }
      const name = validated.computevm;
      const vm = await guestVM(name);
      if (!vm) {
        logger.warn("guest.onNetwork: no live ComputeVM holds this token", { name, acceptUri });
        return c.json({ error: "UnknownGuest" }, 404);
      }
      const contract = vm.spec?.contract;
      if (contract?.acceptUri && (contract.acceptUri !== acceptUri || contract.acceptCid !== acceptCid)) {
        logger.warn("guest.onNetwork: the accept is not this guest's", { name, acceptUri, contractAcceptUri: contract.acceptUri });
        return c.json({ error: "UnknownAccept" }, 404);
      }
      const reportedAt = typeof body.createdAt === "string" ? body.createdAt : new Date().toISOString();
      if (!(await market.record(name, { address, reportedAt }))) {
        return c.json({ error: "Unavailable", message: "the report could not be recorded on the ComputeVM" }, 503);
      }
      logger.info("guest.onNetwork recorded on the ComputeVM", { name, address, reportedAt });
      return c.json({ ok: true, name });
    });
    return events;
  }

  function identityRoutes(): Hono {
    const routes = new Hono();
    routes.post(`/v1/auth/${opts.loginMount}/:step`, async (c) => {
      const step = c.req.param("step");
      if (!isGuestLoginStep(step)) return c.json({ errors: ["no such step"] }, 404);
      const body = guestLoginBody(step, await c.req.json().catch(() => null));
      if (!body) return c.json({ errors: ["the body names the step's fields as strings and nothing else"] }, 400);
      if (!GUEST_COMPUTEVM_PATTERN.test(body.role)) return c.json({ errors: ["the role is not a ComputeVM name"] }, 400);
      if (!loginLimit.take(body.role, Date.now())) return c.json({ errors: ["too many requests"] }, 429);
      const reply = await guestIdentity.step(step, body).catch((err) => ({ status: 502, body: { errors: [String(err)] } }));
      logger.info("guest_login_step", { step, role: body.role, status: reply.status });
      if (step === "bind" && reply.status >= 200 && reply.status < 300) recordBoundHostKeyUntilDone(body.role);
      return c.json(reply.body ?? {}, reply.status as 200);
    });
    routes.get("/v1/identity/oidc/token/:role", async (c) => {
      const role = c.req.param("role");
      const vaultToken = c.req.header(GUEST_VAULT_TOKEN_HEADER) ?? "";
      if (!GUEST_ROLE_PATTERN.test(role)) return c.json({ errors: ["no such role"] }, 404);
      if (!vaultToken || vaultToken.length > 512) return c.json({ errors: ["missing token"] }, 403);
      if (!tokenLimit.take(vaultToken.slice(-24), Date.now())) return c.json({ errors: ["too many requests"] }, 429);
      const reply = await guestIdentity.token(role, vaultToken).catch((err) => ({ status: 502, body: { errors: [String(err)] } }));
      logger.info("guest_oidc_token", { role, status: reply.status });
      return c.json(reply.body ?? {}, reply.status as 200);
    });
    routes.get("/v1/identity/oidc/.well-known/openid-configuration", async (c) => {
      if (!publicLimit.take("discovery", Date.now())) return c.json({ errors: ["too many requests"] }, 429);
      const reply = await guestIdentity.discovery().catch((err) => ({ status: 502, body: { errors: [String(err)] } }));
      return c.json(reply.body ?? {}, reply.status as 200);
    });
    routes.get("/v1/identity/oidc/.well-known/keys", async (c) => {
      if (!publicLimit.take("keys", Date.now())) return c.json({ errors: ["too many requests"] }, 429);
      const reply = await guestIdentity.keys().catch((err) => ({ status: 502, body: { errors: [String(err)] } }));
      return c.json(reply.body ?? {}, reply.status as 200);
    });
    return routes;
  }

  const factory = createFactory({
    initApp: (app) => {
      app.route("/", identityRoutes());
      app.route("/", onNetworkRoutes());
    },
  });

  return { client, createApp: () => factory.createApp() as unknown as Hono };
}
