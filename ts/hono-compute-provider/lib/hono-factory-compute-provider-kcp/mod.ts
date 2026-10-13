import { Hono } from "@hono/hono";
import { createFactory } from "@hono/hono/factory";
import { EVENT_NSID, GUEST_LOGIN_ROUTE, ON_NETWORK_NSID, SUBMIT_EVENT_NSID } from "@publicdomainrelay/compute-provider-common";
import {
  type ComputeAtproto,
  type ComputeProvider,
  type ComputeProviderCtx,
  type GuestHostKeysAck,
  GuestHostKeysRelay,
  type RbacProvisioner,
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
  dropletOf,
  GUEST_PROVISIONING,
  hasEnded,
  isReleasing,
} from "@publicdomainrelay/compute-provider-kcp-abc";
import {
  type ComputeVM,
  type ComputeVMMarket,
  FIELD_MANAGER_COMPUTE_VM_GUEST,
  GUEST_HOST_ADDRESS,
} from "@publicdomainrelay/compute-provider-kcp-common";
import { type GuestLogin, guestLoginBody, isGuestLoginStep } from "@publicdomainrelay/guest-login-abc";
import { createKvNonceStore, type GuestKv, type JwkStore, type NonceStore, type OidcIssuer } from "@publicdomainrelay/oidc-issuer-abc";
import { createOidcIssuer, type HostKeysReportAck, type HostKeysReported } from "@publicdomainrelay/oidc-issuer-hono";

export const PROVIDER_MARKET_FIELDS: ReadonlyArray<keyof ComputeVMMarket> = ["onNetworkUri", "hostKeysUri"];
export const GUEST_MARKET_FIELDS: ReadonlyArray<keyof ComputeVMMarket> = ["address", "reportedAt", "hostKeys"];

const REPORT_RETRY_MS = 5_000;

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
  guestLoginMount?: string;
  bootTimeoutMs?: number;
  releaseTimeoutMs?: number;
  oidcProvisioning?: boolean;
  oidcJwkStore?: JwkStore;
  oidcNonceStore?: NonceStore;
  contractStoreReleases?: boolean;
  rbacProvisioner?: RbacProvisioner;
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
  oidcIssuer: OidcIssuer;
  client: KcpComputeVMClient;
  accepts: ComputeVMAccepts;
}

export function createComputeProviderKcpFactory(opts: ComputeProviderKcpFactoryOptions): ComputeProviderKcpFactory {
  const { logger, atproto } = opts;
  const nonceStore = opts.oidcNonceStore ?? createKvNonceStore({ kv: opts.guestVault.kv, ...GUEST_PROVISIONING });
  const hostKeys = new GuestHostKeysRelay((nonce) => nonceStore.dropletOfNonce(nonce));
  const client = createKcpComputeVMClient({ credentials: opts.credentials, fetch: opts.fetch });
  const accepts = new ComputeVMAccepts();
  const market = createComputeVMMarketRecorder({ client, logger, fields: PROVIDER_MARKET_FIELDS });
  const latest = new Map<string, ComputeVM>();
  const published = new Set<string>();
  const inFlight = new Set<string>();

  async function publishOnNetwork(name: string, vm: ComputeVM, report: ComputeVMMarket): Promise<boolean> {
    const contract = vm.spec?.contract;
    if (!contract?.receiptUri || !contract.receiptCid) {
      logger.info("guest_onnetwork_waits_for_contract", { name, address: report.address });
      return false;
    }
    const ref = await atproto.createRecord(ON_NETWORK_NSID, {
      $type: ON_NETWORK_NSID,
      address: report.address,
      createdAt: report.reportedAt ?? new Date().toISOString(),
    });
    if (opts.createSignedRepoRecord) {
      const { uri: eventUri, cid: eventCid, record: eventRecord } = await opts.createSignedRepoRecord(
        EVENT_NSID,
        {
          $type: EVENT_NSID,
          receipt: { $type: "com.atproto.repo.strongRef", uri: contract.receiptUri, cid: contract.receiptCid },
          payload: { $type: "com.atproto.repo.strongRef", uri: ref.uri, cid: ref.cid },
        },
        atproto.getAgentDid(),
      );
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
    if (published.has(key) || inFlight.has(key)) return;
    inFlight.add(key);
    work().catch((err: unknown) => {
      logger.warn("guest_report_publish_failed", { name, report: key, error: String(err) });
      return false;
    }).then((done) => {
      inFlight.delete(key);
      if (done) {
        published.add(key);
        return;
      }
      const retry = setTimeout(() => consider(name), REPORT_RETRY_MS);
      Deno.unrefTimer(retry);
    });
  }

  function consider(name: string): void {
    const vm = latest.get(name);
    const report = vm?.status?.market;
    if (!vm || !report || hasEnded(vm) || isReleasing(vm)) return;
    if (report.address && !report.onNetworkUri) attempt(`${name}#onNetwork`, name, () => publishOnNetwork(name, vm, report));
    if (report.hostKeys?.length && !report.hostKeysUri) attempt(`${name}#hostKeys`, name, () => publishHostKeys(name, report));
  }

  function onChange(change: ComputeVMChange): void {
    if (!change.after) {
      latest.delete(change.name);
      for (const key of [...published]) if (key.startsWith(`${change.name}#`)) published.delete(key);
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
    bootTimeoutMs: opts.bootTimeoutMs,
    releaseTimeoutMs: opts.releaseTimeoutMs,
    provisioningEnricher: opts.oidcProvisioning
      ? { enrich: (userData, teamUuid) => oidcIssuer.provisioningEnricher.enrich(userData, teamUuid) }
      : undefined,
    hostKeys,
    rbacProvisioner: opts.rbacProvisioner,
    accepts,
    contractStoreReleases: opts.contractStoreReleases,
    onChange,
  });

  const oidcIssuer = createOidcIssuer({
    getIssuerUrl: opts.getIssuerUrl,
    getDroplet: (id: string) => provider.getDroplet?.(id),
    store: opts.oidcJwkStore,
    nonceStore,
    log: (level: string, msg: string, extra?: Record<string, unknown>) => {
      const at = level as "info" | "warn" | "error" | "debug";
      if (typeof logger?.[at] === "function") logger[at](msg, extra);
    },
  });

  return { provider, oidcIssuer, client, accepts };
}

export interface ComputeProviderKcpGuestFactoryOptions {
  logger: ComputeProviderCtx["logger"];
  credentials: KcpCredentials;
  fetch?: typeof fetch;
  handle: string;
  guestKv: GuestKv;
  getIssuerUrl: () => string;
  oidcJwkStore?: JwkStore;
  plcDirectoryUrl?: string;
  guestLogin?: GuestLogin;
}

export interface ComputeProviderKcpGuestFactory {
  oidcIssuer: OidcIssuer;
  client: KcpComputeVMClient;
  createApp(): Hono;
}

function sameHostKeys(a: ComputeVMMarket["hostKeys"], b: ComputeVMMarket["hostKeys"]): boolean {
  const key = (k: { type: string; publicKey: string }) => `${k.type} ${k.publicKey}`;
  const left = (a ?? []).map(key).sort();
  const right = (b ?? []).map(key).sort();
  return left.length === right.length && left.every((v, i) => v === right[i]);
}

export function createComputeProviderKcpGuestFactory(opts: ComputeProviderKcpGuestFactoryOptions): ComputeProviderKcpGuestFactory {
  const { logger, handle } = opts;
  const nonceStore = createKvNonceStore({ kv: opts.guestKv, ...GUEST_PROVISIONING });
  const client = createKcpComputeVMClient({ credentials: opts.credentials, fetch: opts.fetch, fieldManager: FIELD_MANAGER_COMPUTE_VM_GUEST });
  const market = createComputeVMMarketRecorder({ client, logger, fields: GUEST_MARKET_FIELDS });

  async function guestVM(name: string): Promise<ComputeVM | undefined> {
    const vm = await client.get(name);
    if (!vm || vm.spec?.owner?.handle !== handle || hasEnded(vm) || isReleasing(vm)) return undefined;
    return vm;
  }

  async function recordHostKeys(report: HostKeysReported): Promise<HostKeysReportAck> {
    const name = await nonceStore.dropletOfNonce(report.nonce);
    if (name === undefined) return { ok: false, status: 404, reason: "no guest holds this provisioning token" };
    const vm = await guestVM(name);
    if (!vm) return { ok: false, status: 404, reason: `no live ComputeVM ${name} of ${handle}` };
    const keys = report.keys.map((k) => ({ type: k.type, publicKey: k.publicKey, ...(k.fingerprint ? { fingerprint: k.fingerprint } : {}) }));
    const held = vm.status?.market?.hostKeys;
    if (held?.length) {
      if (sameHostKeys(held, keys)) {
        logger.info("guest_hostkeys_duplicate_report", { name });
        return { ok: true };
      }
      logger.warn("guest_hostkeys_second_report_refused", { name });
      return { ok: false, status: 409, reason: "host keys already reported for this guest" };
    }
    if (!(await market.record(name, { hostKeys: keys }))) {
      return { ok: false, status: 503, reason: "the host keys could not be recorded on the ComputeVM" };
    }
    logger.info("guest_hostkeys_recorded", { name, types: keys.map((k) => k.type) });
    return { ok: true };
  }

  const oidcIssuer = createOidcIssuer({
    getIssuerUrl: opts.getIssuerUrl,
    getDroplet: async (id: string) => {
      const vm = await guestVM(id);
      return vm ? dropletOf(vm) : undefined;
    },
    onHostKeys: recordHostKeys,
    store: opts.oidcJwkStore,
    nonceStore,
    plcDirectoryUrl: opts.plcDirectoryUrl,
    log: (level: string, msg: string, extra?: Record<string, unknown>) => {
      const at = level as "info" | "warn" | "error" | "debug";
      if (typeof logger?.[at] === "function") logger[at](msg, extra);
    },
  });

  function onNetworkRoutes(): Hono {
    const events = new Hono();
    events.post("/v1/on-network", async (c) => {
      const authHeader = c.req.header("Authorization");
      const token = authHeader?.startsWith("Bearer ") ? authHeader.slice(7) : null;
      if (!token) return c.json({ error: "AuthenticationRequired" }, 401);
      const validated = await oidcIssuer.validateToken(token).catch(() => null);
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
      const dropletId = validated.claims?.["droplet_id"];
      const nonce = validated.claims?.["nonce"];
      const name = typeof dropletId === "string" && dropletId
        ? dropletId
        : typeof nonce === "string"
        ? await nonceStore.dropletOfNonce(nonce)
        : undefined;
      const vm = name === undefined ? undefined : await guestVM(name);
      if (name === undefined || !vm) {
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

  function guestLoginRoutes(login: GuestLogin): Hono {
    const routes = new Hono();
    routes.post(`${GUEST_LOGIN_ROUTE}/:step`, async (c) => {
      const step = c.req.param("step");
      if (!isGuestLoginStep(step)) return c.json({ errors: ["no such step"] }, 404);
      const body = guestLoginBody(step, await c.req.json().catch(() => null));
      if (!body) return c.json({ errors: ["the body names the step's fields as strings and nothing else"] }, 400);
      const reply = await login.step(step, body).catch((err) => ({ status: 502, body: { errors: [String(err)] } }));
      logger.info("guest_login_step", { step, role: body.role, status: reply.status });
      return c.json(reply.body ?? {}, reply.status as 200);
    });
    return routes;
  }

  const factory = createFactory({
    initApp: (app) => {
      app.route("/", oidcIssuer.app as never);
      app.route("/", onNetworkRoutes());
      if (opts.guestLogin) app.route("/", guestLoginRoutes(opts.guestLogin));
    },
  });

  return { oidcIssuer, client, createApp: () => factory.createApp() as unknown as Hono };
}
