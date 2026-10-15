import {
  addressTheRequesterNamed,
  injectAcceptBundle,
  GUEST_LOGIN_ROUTE,
  pointGuestAtHost,
  SECRETS_PREFIX,
  shortId,
  WIF_SIMPLE_NSID,
  withGuestLogin,
  withOnNetworkBaseUrl,
  withOnNetworkReporter,
} from "@publicdomainrelay/compute-provider-common";
import { GUEST_KCP_API, guestAudience, guestOidcIssuer } from "@publicdomainrelay/guest-identity-common";
import type { GuestIdentityGrants } from "@publicdomainrelay/guest-identity-abc";
import { GUEST_LOGIN_TOKEN_PATH } from "@publicdomainrelay/cloud-init-common";
import type {
  AdoptResult,
  ComputeProvider,
  ComputeProviderCtx,
  GuestGone,
  GuestHostKeysHandler,
  GuestHostKeysRelay,
  ProvisionResult,
  StrongRef,
  VM,
} from "@publicdomainrelay/compute-provider-abc";
import {
  acceptKeyOf,
  COMPUTE_VM_API_VERSION,
  ComputeVMAccepts,
  ComputeVMCache,
  type ComputeVMChange,
  acceptKeyOfComputeVM,
  contractReceiptOf,
  computeVMFor,
  dropletOf,
  endedMessage,
  goneOf,
  hasEnded,
  isReleasing,
  marketStatusFor,
  mergeMarketStatus,
  phaseOf,
  userDataEntry,
  userDataPath,
  type GuestVaultRef,
} from "@publicdomainrelay/compute-provider-kcp-abc";
import {
  assertComputeVM,
  COMPUTE_VM_RESOURCE,
  type ComputeVM,
  type ComputeVMContract,
  type ComputeVMMarket,
  FIELD_MANAGER_COMPUTE_VM_CONTRACT,
  FIELD_MANAGER_COMPUTE_VM_PROVIDER,
  parseComputeVM,
} from "@publicdomainrelay/compute-provider-kcp-common";
import type { GuestKv } from "@publicdomainrelay/oidc-issuer-abc";
import { DEFAULT_ACCEPT_PATH_VM } from "@publicdomainrelay/secrets-common";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import { absentOnNotFound, type KcpList, type KcpWatchEvent } from "@publicdomainrelay/kcp-client-common";
import { createKcpRestClient } from "@publicdomainrelay/kcp-client-rest";

export interface KcpCredentials {
  server: string;
  workspace: string;
  token?: string;
  tokenFile?: string;
  caFile?: string;
}

export interface KcpComputeVMClientOptions {
  credentials: KcpCredentials;
  kcp?: KcpClient;
  fetch?: typeof fetch;
  timeoutMs?: number;
  watchTimeoutSeconds?: number;
  fieldManager?: string;
}

export type ComputeVMWatchEvent = KcpWatchEvent;

export type ComputeVMList = KcpList<ComputeVM>;

export interface KcpComputeVMClient {
  apply(vm: ComputeVM): Promise<ComputeVM>;
  applyStatus(vm: ComputeVM): Promise<ComputeVM>;
  get(name: string): Promise<ComputeVM | null>;
  list(): Promise<ComputeVMList>;
  watch(resourceVersion: string, signal: AbortSignal): AsyncGenerator<ComputeVMWatchEvent>;
  delete(name: string): Promise<boolean>;
  applyContract(name: string, contract: ComputeVMContract): Promise<ComputeVM | null>;
}

export function createKcpComputeVMClient(opts: KcpComputeVMClientOptions): KcpComputeVMClient {
  const creds = opts.credentials;
  const kcp = opts.kcp ??
    createKcpRestClient(
      { server: creds.server, token: creds.token, tokenFile: creds.tokenFile, caFile: creds.caFile },
      { fetch: opts.fetch, timeoutMs: opts.timeoutMs, watchTimeoutSeconds: opts.watchTimeoutSeconds },
    );
  const vms = kcpResourceClient(kcp, { resource: COMPUTE_VM_RESOURCE, workspace: creds.workspace }, parseComputeVM);
  const fieldManager = opts.fieldManager ?? FIELD_MANAGER_COMPUTE_VM_PROVIDER;

  return {
    async apply(vm) {
      assertComputeVM(vm);
      return await vms.patch(vm.metadata!.name!, { type: "apply", body: vm, fieldManager, force: true });
    },
    async applyStatus(vm) {
      return await vms.patch(vm.metadata!.name!, { type: "apply", body: vm, fieldManager, force: true }, { subresource: "status" });
    },
    async applyContract(name, contract) {
      if (!(await vms.get(name))) return null;
      const body = { apiVersion: COMPUTE_VM_API_VERSION, kind: COMPUTE_VM_RESOURCE.kind, metadata: { name }, spec: { contract } };
      return await absentOnNotFound(
        vms.patch(name, { type: "apply", body, fieldManager: FIELD_MANAGER_COMPUTE_VM_CONTRACT, force: true }),
      );
    },
    get: (name) => vms.get(name),
    list: () => vms.list(),
    watch: (resourceVersion, signal) => vms.watch({ resourceVersion, signal, reconnect: false }),
    delete: (name) => vms.delete(name),
  };
}

export interface ComputeVMMarketRecorder {
  record(name: string, patch: ComputeVMMarket): Promise<boolean>;
}

export function createComputeVMMarketRecorder(opts: {
  client: KcpComputeVMClient;
  logger: ComputeProviderCtx["logger"];
  fields: ReadonlyArray<keyof ComputeVMMarket>;
}): ComputeVMMarketRecorder {
  const chains = new Map<string, Promise<boolean>>();

  function owned(market: ComputeVMMarket | undefined): ComputeVMMarket {
    const out: Record<string, unknown> = {};
    for (const f of opts.fields) if (market?.[f] !== undefined) out[f] = market[f];
    return out as ComputeVMMarket;
  }

  async function write(name: string, patch: ComputeVMMarket): Promise<boolean> {
    try {
      const current = await opts.client.get(name);
      if (!current) {
        opts.logger.warn("computevm_market_status_no_object", { name, fields: Object.keys(patch) });
        return false;
      }
      const market = mergeMarketStatus(owned(current.status?.market), owned(patch));
      await opts.client.applyStatus(marketStatusFor(name, market));
      opts.logger.info("computevm_market_status_recorded", { name, fields: Object.keys(patch) });
      return true;
    } catch (err) {
      opts.logger.warn("computevm_market_status_failed", { name, fields: Object.keys(patch), error: String(err) });
      return false;
    }
  }

  return {
    record(name, patch) {
      const next = (chains.get(name) ?? Promise.resolve(true)).then(() => write(name, patch));
      chains.set(name, next);
      next.finally(() => {
        if (chains.get(name) === next) chains.delete(name);
      });
      return next;
    },
  };
}

export interface GuestVault extends GuestVaultRef {
  kv: GuestKv;
}

export interface ComputeProviderKcpCtx extends ComputeProviderCtx {
  client: KcpComputeVMClient;
  handle: string;
  guestVault: GuestVault;
  guestHostAddress: string;
  getIssuerUrl: () => string;
  acceptPathVm?: string;
  guestTlsPort?: number;
  reuseStale?: boolean;
  stopGraceSeconds?: number;
  guestLoginMount: string;
  identities: GuestIdentityGrants;
  bootTimeoutMs?: number;
  releaseTimeoutMs?: number;
  pollIntervalMs?: number;
  resyncBackoffMs?: number;
  hostKeys?: GuestHostKeysRelay;
  accepts?: ComputeVMAccepts;
  contractStoreReleases?: boolean;
  onChange?: (change: ComputeVMChange) => void;
}

const DEFAULT_BOOT_TIMEOUT_MS = 100_000;
const DEFAULT_RELEASE_TIMEOUT_MS = 150_000;
const DEFAULT_POLL_INTERVAL_MS = 2_000;
const DEFAULT_RESYNC_BACKOFF_MS = 2_000;

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal?.aborted) return resolve();
    const done = () => {
      clearTimeout(timer);
      signal?.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal?.addEventListener("abort", done, { once: true });
  });
}

export function createComputeProviderKcp(ctx: ComputeProviderKcpCtx): ComputeProvider {
  const { logger, atproto, client, handle } = ctx;
  const acceptPathVm = ctx.acceptPathVm ?? DEFAULT_ACCEPT_PATH_VM;
  const bootTimeoutMs = ctx.bootTimeoutMs ?? DEFAULT_BOOT_TIMEOUT_MS;
  const releaseTimeoutMs = ctx.releaseTimeoutMs ?? DEFAULT_RELEASE_TIMEOUT_MS;
  const pollIntervalMs = ctx.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS;
  const resyncBackoffMs = ctx.resyncBackoffMs ?? DEFAULT_RESYNC_BACKOFF_MS;

  const cache = new ComputeVMCache(handle);
  const provisioning = new Set<string>();
  const releasing = new Set<string>();
  const reportedGone = new Set<string>();
  const goneListeners = new Set<(gone: GuestGone) => void>();
  let syncStarted: Promise<void> | undefined;
  let syncAbort: AbortController | undefined;
  let syncLoop: Promise<void> | undefined;

  async function releaseGone(name: string, vm: ComputeVM | undefined): Promise<void> {
    ctx.identities.forget(name);
    if (ctx.contractStoreReleases && contractReceiptOf(vm)) {
      logger.info("computevm_kept_for_its_contract", { name, receiptUri: contractReceiptOf(vm), phase: phaseOf(vm) });
      return;
    }
    if (vm && vm.metadata?.deletionTimestamp === undefined) {
      await client.delete(name).catch((err: unknown) =>
        logger.warn("computevm_release_failed", { name, error: String(err) })
      );
    }
  }

  function reportGone(name: string, vm: ComputeVM | undefined): void {
    reportedGone.add(name);
    const gone = goneOf(name, vm);
    logger.warn("computevm_guest_gone", {
      name,
      reason: gone.reason,
      phase: phaseOf(vm),
      lostReason: vm?.status?.lostReason,
      message: vm?.status?.message,
    });
    for (const listener of goneListeners) {
      try {
        listener(gone);
      } catch (err) {
        logger.warn("guest_gone_listener_failed", { name, error: String(err) });
      }
    }
    void releaseGone(name, vm);
  }

  function notify(change: ComputeVMChange): void {
    try {
      ctx.onChange?.(change);
    } catch (err) {
      logger.warn("computevm_change_listener_failed", { name: change.name, error: String(err) });
    }
  }

  function observe(change: ComputeVMChange): void {
    notify(change);
    const { name, before, after } = change;
    if (!after) {
      const ours = provisioning.has(name) || releasing.has(name) || reportedGone.has(name);
      releasing.delete(name);
      reportedGone.delete(name);
      if (ours || hasEnded(before)) return;
      reportGone(name, before);
      return;
    }
    if (provisioning.has(name) || releasing.has(name) || reportedGone.has(name)) return;
    if (hasEnded(before)) return;
    if (hasEnded(after) || (isReleasing(after) && !isReleasing(before))) reportGone(name, after);
  }

  function applyEvent(event: ComputeVMWatchEvent): string | undefined {
    const raw = event.object as ComputeVM | undefined;
    const resourceVersion = raw?.metadata?.resourceVersion;
    if (event.type === "BOOKMARK") return resourceVersion;
    let vm: ComputeVM;
    try {
      vm = parseComputeVM(raw);
    } catch (err) {
      logger.warn("computevm_watch_object_refused", { error: String(err) });
      return resourceVersion;
    }
    const name = vm.metadata?.name;
    if (!name) return resourceVersion;
    const change = event.type === "DELETED" ? cache.remove(name) : cache.upsert(vm);
    if (change) observe(change);
    return resourceVersion;
  }

  async function runSync(signal: AbortSignal, listedAt: string): Promise<void> {
    let resourceVersion: string | undefined = listedAt;
    let mode: "watch" | "relist" | undefined;
    while (!signal.aborted) {
      try {
        if (resourceVersion === undefined) {
          const listed = await client.list();
          for (const change of cache.replace(listed.items)) observe(change);
          resourceVersion = listed.resourceVersion;
        }
        let events = 0;
        for await (const event of client.watch(resourceVersion, signal)) {
          events++;
          if (mode !== "watch") {
            mode = "watch";
            logger.info("computevm_sync_mode", { mode, handle });
          }
          if (event.type === "ERROR") {
            resourceVersion = undefined;
            break;
          }
          resourceVersion = applyEvent(event) ?? resourceVersion;
        }
        if (events === 0) await delay(resyncBackoffMs, signal);
      } catch (err) {
        if (signal.aborted) break;
        if (mode !== "relist") {
          mode = "relist";
          logger.warn("computevm_sync_mode", { mode, handle, everyMs: resyncBackoffMs, error: String(err) });
        }
        resourceVersion = undefined;
        await delay(resyncBackoffMs, signal);
      }
    }
  }

  async function startSync(): Promise<void> {
    const listed = await client.list();
    for (const change of cache.replace(listed.items)) notify(change);
    syncAbort = new AbortController();
    syncLoop = runSync(syncAbort.signal, listed.resourceVersion);
    logger.info("computevm_cache_filled", { handle, guests: cache.names().length });
  }

  function ensureSync(): Promise<void> {
    syncStarted ??= startSync().catch((err) => {
      syncStarted = undefined;
      throw err;
    });
    return syncStarted;
  }

  async function stopSync(): Promise<void> {
    syncAbort?.abort();
    await syncLoop;
    syncAbort = undefined;
    syncLoop = undefined;
    syncStarted = undefined;
  }

  async function nextChangeWithin(name: string, ms: number): Promise<void> {
    const abort = new AbortController();
    await Promise.race([cache.nextChange(name, abort.signal), delay(ms, abort.signal)]);
    abort.abort();
  }

  async function untilRunning(name: string): Promise<ComputeVM> {
    const deadline = Date.now() + bootTimeoutMs;
    let seen = false;
    while (true) {
      const vm = cache.get(name);
      if (vm) seen = true;
      if (phaseOf(vm) === "Running") return vm!;
      if (vm && hasEnded(vm)) throw new Error(endedMessage(name, vm));
      if (vm && isReleasing(vm)) throw new Error(`ComputeVM ${name} was deleted before it was Running`);
      if (!vm && seen) throw new Error(`ComputeVM ${name} disappeared from kcp before it was Running`);
      const left = deadline - Date.now();
      if (left <= 0) {
        throw new Error(
          `ComputeVM ${name} was not Running within ${bootTimeoutMs} ms (phase ${phaseOf(vm) ?? "none yet"}); ` +
            `the ComputeVM controller of this bidder's kcp boots it, so a phase that never moves means no controller is reconciling it`,
        );
      }
      await nextChangeWithin(name, Math.min(left, pollIntervalMs));
    }
  }

  async function untilAbsent(name: string): Promise<void> {
    const deadline = Date.now() + releaseTimeoutMs;
    while ((await client.get(name)) !== null) {
      const left = deadline - Date.now();
      if (left <= 0) {
        throw new Error(
          `ComputeVM ${name} still exists ${releaseTimeoutMs} ms after its delete; the controller holds its ` +
            `finalizer until the VM is stopped and its work dir is removed`,
        );
      }
      await nextChangeWithin(name, Math.min(left, pollIntervalMs));
    }
  }

  async function abandon(name: string): Promise<void> {
    releasing.add(name);
    ctx.identities.forget(name);
    await client.delete(name).catch((err: unknown) =>
      logger.warn("computevm_release_failed", { name, error: String(err) })
    );
  }

  async function createBidConfig(nowIso: string): Promise<StrongRef> {
    const agentDid = atproto.getAgentDid();
    return await atproto.createRecord(WIF_SIMPLE_NSID, {
      $type: WIF_SIMPLE_NSID,
      accept_path: acceptPathVm,
      issuer_uri: guestOidcIssuer(ctx.getIssuerUrl()),
      to_issue: "openbao-identity-oidc",
      actx: agentDid.split(":").slice(-1)[0],
      token_path: GUEST_LOGIN_TOKEN_PATH,
      url_path: `${SECRETS_PREFIX}/base_url`,
      url_route: "/v1/identity/oidc/token",
      subject: "actx:{actx}:plc:{did-plc-key}:role:{role}",
      createdAt: nowIso,
    });
  }

  async function guestLoginFor(name: string): Promise<{ mount: string; bindSecret: string; bindSecretSha256: string }> {
    const bytes = crypto.getRandomValues(new Uint8Array(32));
    const bindSecret = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(bindSecret)));
    const bindSecretSha256 = Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
    logger.info("computevm_guest_login", { name, mount: ctx.guestLoginMount });
    return { mount: ctx.guestLoginMount, bindSecret, bindSecretSha256 };
  }

  async function releaseOrphansOfAccept(acceptKey: string): Promise<void> {
    for (const name of cache.names()) {
      if (provisioning.has(name)) continue;
      const vm = cache.get(name);
      if (!cache.owns(vm) || hasEnded(vm) || isReleasing(vm) || contractReceiptOf(vm)) continue;
      if (acceptKeyOfComputeVM(vm) !== acceptKey) continue;
      logger.warn("computevm_orphan_of_accept_released", {
        name,
        acceptKey,
        why: "this process now holds the Bid claim for the accept; a ComputeVM for it with no receipt was started by a bidder process that stopped",
      });
      await abandon(name);
    }
  }

  async function provision(vm: VM, requesterDid: string): Promise<ProvisionResult> {
    const acceptKey = acceptKeyOf(vm._accept);
    try {
      return await provisionGuest(vm, requesterDid, acceptKey);
    } catch (err) {
      ctx.accepts?.failed(acceptKey);
      throw err;
    }
  }

  async function provisionGuest(vm: VM, requesterDid: string, acceptKey: string | undefined): Promise<ProvisionResult> {
    if (!vm.user_data) {
      throw new Error(
        `an RFP for ${vm.role} arrived with no user_data. On this provider the user_data IS the ` +
          `guest: nothing else is baked into it at boot, so a guest started without one comes up ` +
          `with no sshd, no iroh listener and no secrets, and reaches Running while being unreachable.`,
      );
    }
    await ensureSync();
    if (acceptKey) await releaseOrphansOfAccept(acceptKey);
    const name = `fc-${shortId(requesterDid)}-${crypto.randomUUID().slice(0, 8)}`;
    const teamUuid = atproto.getAgentDid().split(":").slice(-1)[0];
    const issuerUrl = ctx.getIssuerUrl();
    const login = await guestLoginFor(name);
    const withLogin = withGuestLogin(vm.user_data, {
      url: `${issuerUrl}${GUEST_LOGIN_ROUTE}`,
      role: name,
      bindSecret: login.bindSecret,
      mount: login.mount,
    });
    const reported = withOnNetworkBaseUrl(
      withOnNetworkReporter(withLogin, issuerUrl, acceptPathVm, { audience: guestAudience(GUEST_KCP_API, name) }),
      issuerUrl,
    );
    const hostAddress = addressTheRequesterNamed(reported) ?? ctx.guestHostAddress;
    const userData = pointGuestAtHost(reported, hostAddress, ctx.guestTlsPort);
    const desired = computeVMFor({
      name,
      handle,
      vm,
      requesterDid,
      vault: { server: ctx.guestVault.server, mount: ctx.guestVault.mount },
      reuseStale: ctx.reuseStale,
      stopGraceSeconds: ctx.stopGraceSeconds,
      guestLogin: { mount: login.mount, bindSecretSha256: login.bindSecretSha256 },
      acceptKey,
    });

    provisioning.add(name);
    let applied: ComputeVM | undefined;
    try {
      await ctx.guestVault.kv.write(userDataPath(name), userDataEntry(userData));
      applied = await client.apply(desired);
      cache.upsert(applied);
      const requesterPlc = requesterDid.split(":").pop() ?? requesterDid;
      await ctx.identities.register({
        computevm: name,
        ownerUid: applied.metadata?.uid ?? "",
        requesterDid,
        subject: `actx:${teamUuid}:plc:${requesterPlc}:role:${vm.role}`,
      });
      ctx.accepts?.applied(acceptKey, name);
      logger.info("computevm_applied", { name, handle, guestHostAddress: hostAddress, cpus: vm.cpus, mem: vm.mem });
      const running = await untilRunning(name);
      provisioning.delete(name);

      const status = running.status ?? {};
      logger.info("computevm_running", { name, guestIp: status.network?.guestIP, image: status.image?.ref });
      return {
        providerId: name,
        metadata: {
          name,
          ip: status.network?.guestIP,
          hostAddress: status.network?.hostAddress,
          mode: "kcp",
          sshReady: true,
          vmmPid: status.runtime?.pid,
          console: status.workDir ? `${status.workDir}/console.log` : undefined,
          imageFingerprint: status.image?.sha256,
          imageRef: status.image?.ref,
          imageSha256: status.image?.sha256,
        },
      };
    } catch (err) {
      provisioning.delete(name);
      if (applied) await abandon(name);
      logger.warn("computevm_provision_failed", { name, error: String(err) });
      throw err;
    }
  }

  async function destroy(id: string | number): Promise<void> {
    const name = String(id);
    releasing.add(name);
    ctx.identities.forget(name);
    if (await client.delete(name)) await untilAbsent(name);
    logger.info("computevm_released", { name });
  }

  async function adopt(id: string | number): Promise<AdoptResult> {
    const name = String(id);
    await ensureSync();
    const fetched = cache.get(name) ?? (await client.get(name)) ?? undefined;
    const vm = cache.owns(fetched) ? fetched : undefined;
    if (vm && !hasEnded(vm) && !isReleasing(vm)) {
      logger.info("computevm_adopted", { name, phase: phaseOf(vm) });
      return { state: "running" };
    }
    reportedGone.add(name);
    const gone = goneOf(name, vm);
    logger.warn("computevm_gone_at_adopt", { name, reason: gone.reason, phase: phaseOf(vm), lostReason: vm?.status?.lostReason });
    if (vm) void releaseGone(name, vm);
    return { state: "gone", gone };
  }

  function getDroplet(id: string): Record<string, unknown> | undefined {
    const vm = cache.get(id);
    if (!vm || hasEnded(vm) || isReleasing(vm)) return undefined;
    return dropletOf(vm);
  }

  function onGuestGone(listener: (gone: GuestGone) => void): () => void {
    goneListeners.add(listener);
    return () => goneListeners.delete(listener);
  }

  const provider: ComputeProvider = {
    name: "kcp",
    provision,
    destroy,
    adopt,
    createBidConfig,
    injectAcceptBundle: (userData: string, bundle: Record<string, unknown>) =>
      injectAcceptBundle(userData, bundle, acceptPathVm),
    getDroplet,
    onGuestGone,
    setup: ensureSync,
    teardown: stopSync,
  };
  if (ctx.hostKeys) {
    const relay = ctx.hostKeys;
    provider.onHostKeys = (handler: GuestHostKeysHandler) => relay.onHostKeys(handler);
  }
  return provider;
}
