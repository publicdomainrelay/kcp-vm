import type { GuestGone, VM } from "@publicdomainrelay/compute-provider-abc";
import {
  COMPUTE_VM_RESOURCE,
  type ComputeVM,
  type ComputeVMLostReason,
  type ComputeVMMarket,
  type ComputeVMPhase,
  type ComputeVMVaultRef,
  GUEST_PROVISIONING_ENTRY,
  GUEST_USER_DATA_ENTRY,
  GUEST_USER_DATA_KEY,
  GUEST_VAULT_PREFIX,
} from "@publicdomainrelay/compute-provider-kcp-common";

export type GuestGoneReason = "host_reboot" | "vmm_exited" | "oom" | "identity_mismatch";

const GONE_REASON_BY_LOST: Readonly<Record<ComputeVMLostReason, GuestGoneReason>> = {
  HostRebooted: "host_reboot",
  UnitGone: "vmm_exited",
  UnitFailed: "vmm_exited",
  VMMExited: "vmm_exited",
  OOMKilled: "oom",
  IdentityMismatch: "identity_mismatch",
};

export const COMPUTE_VM_API_VERSION = `${COMPUTE_VM_RESOURCE.group}/${COMPUTE_VM_RESOURCE.version}`;

export function goneReasonOf(lost: ComputeVMLostReason | undefined): GuestGoneReason {
  return (lost && GONE_REASON_BY_LOST[lost]) ?? "vmm_exited";
}

export function phaseOf(vm: ComputeVM | undefined): ComputeVMPhase | undefined {
  return vm?.status?.phase;
}

export function hasEnded(vm: ComputeVM | undefined): boolean {
  const phase = phaseOf(vm);
  return phase === "Lost" || phase === "Failed";
}

export function isReleasing(vm: ComputeVM | undefined): boolean {
  return vm?.metadata?.deletionTimestamp !== undefined || phaseOf(vm) === "Releasing";
}

export function goneOf(name: string, vm: ComputeVM | undefined): GuestGone {
  const gone: GuestGone = { id: name, reason: goneReasonOf(vm?.status?.lostReason) };
  if (vm && phaseOf(vm) === "Failed") gone.message = endedMessage(name, vm);
  else if (vm && !hasEnded(vm) && isReleasing(vm)) gone.message = releasedMessage(name);
  return gone;
}

export function endedMessage(name: string, vm: ComputeVM): string {
  const status = vm.status ?? {};
  const why = [status.lostReason, status.message].filter((part) => part).join(": ");
  return `ComputeVM ${name} ended ${status.phase ?? "without a phase"}${why ? ` (${why})` : ""}`;
}

export function releasedMessage(name: string): string {
  return `ComputeVM ${name} was released on the bidder without a vm.delete from the requester`;
}

export function guestVaultPath(name: string, entry: string): string {
  return `${GUEST_VAULT_PREFIX}/${name}/${entry}`;
}

export function userDataPath(name: string): string {
  return guestVaultPath(name, GUEST_USER_DATA_ENTRY);
}

export function userDataEntry(userData: string): Record<string, unknown> {
  return { [GUEST_USER_DATA_KEY]: userData };
}

export const GUEST_PROVISIONING = { prefix: GUEST_VAULT_PREFIX, entry: GUEST_PROVISIONING_ENTRY } as const;

export interface GuestVaultRef {
  server: string;
  mount: string;
}

export function userDataRefFor(name: string, vault: GuestVaultRef): ComputeVMVaultRef {
  return { server: vault.server, mount: vault.mount, path: userDataPath(name) };
}

export interface ComputeVMInput {
  name: string;
  handle: string;
  vm: Pick<VM, "cpus" | "mem" | "disk" | "role">;
  requesterDid: string;
  vault: GuestVaultRef;
  reuseStale?: boolean;
  stopGraceSeconds?: number;
  guestLogin?: { mount: string; bindSecretSha256: string };
}

export function computeVMFor(input: ComputeVMInput): ComputeVM {
  return {
    apiVersion: COMPUTE_VM_API_VERSION,
    kind: COMPUTE_VM_RESOURCE.kind,
    metadata: { name: input.name },
    spec: {
      vm: {
        cpus: typeof input.vm.cpus === "number" && input.vm.cpus > 0 ? input.vm.cpus : 0,
        mem: typeof input.vm.mem === "string" ? input.vm.mem : "",
        disk: typeof input.vm.disk === "string" ? input.vm.disk : "",
        role: input.vm.role ?? "",
      },
      owner: { handle: input.handle },
      userData: { vault: userDataRefFor(input.name, input.vault) },
      requester: { did: input.requesterDid },
      ...(input.reuseStale === undefined ? {} : { image: { reuseStale: input.reuseStale } }),
      ...(input.stopGraceSeconds === undefined ? {} : { stopGraceSeconds: input.stopGraceSeconds }),
      ...(input.guestLogin ? { guestLogin: input.guestLogin } : {}),
    },
  };
}

export function mergeMarketStatus(current: ComputeVMMarket | undefined, patch: ComputeVMMarket): ComputeVMMarket {
  const merged: ComputeVMMarket = { ...(current ?? {}) };
  for (const [key, value] of Object.entries(patch) as Array<[keyof ComputeVMMarket, unknown]>) {
    if (value !== undefined) (merged as Record<string, unknown>)[key] = value;
  }
  return merged;
}

export function marketStatusFor(name: string, market: ComputeVMMarket): ComputeVM {
  return {
    apiVersion: COMPUTE_VM_API_VERSION,
    kind: COMPUTE_VM_RESOURCE.kind,
    metadata: { name },
    status: { market },
  };
}

export function dropletOf(vm: ComputeVM): Record<string, unknown> | undefined {
  const name = vm.metadata?.name;
  if (!name || !vm.spec) return undefined;
  return {
    id: name,
    tags: [
      `oidc-sub:plc:${vm.spec.requester.did.split(":").pop() ?? ""}`,
      `oidc-sub:role:${vm.spec.vm.role}`,
    ],
  };
}

export interface ComputeVMChange {
  name: string;
  before?: ComputeVM;
  after?: ComputeVM;
}

export class ComputeVMCache {
  readonly #items = new Map<string, ComputeVM>();
  readonly #waiters = new Map<string, Set<(change: ComputeVMChange) => void>>();

  constructor(readonly handle: string) {}

  owns(vm: ComputeVM | undefined): boolean {
    return !!vm?.metadata?.name && vm.spec?.owner?.handle === this.handle;
  }

  get(name: string): ComputeVM | undefined {
    return this.#items.get(name);
  }

  names(): string[] {
    return [...this.#items.keys()];
  }

  replace(items: readonly ComputeVM[]): ComputeVMChange[] {
    const owned = new Map<string, ComputeVM>();
    for (const vm of items) if (this.owns(vm)) owned.set(vm.metadata!.name!, vm);
    const changes: ComputeVMChange[] = [];
    for (const name of [...this.#items.keys()]) {
      if (!owned.has(name)) changes.push(this.#set(name, undefined));
    }
    for (const [name, vm] of owned) {
      const before = this.#items.get(name);
      if (before && before.metadata?.resourceVersion === vm.metadata?.resourceVersion) continue;
      changes.push(this.#set(name, vm));
    }
    return changes;
  }

  upsert(vm: ComputeVM): ComputeVMChange | undefined {
    if (!this.owns(vm)) return undefined;
    return this.#set(vm.metadata!.name!, vm);
  }

  remove(name: string): ComputeVMChange | undefined {
    if (!this.#items.has(name)) return undefined;
    return this.#set(name, undefined);
  }

  nextChange(name: string, signal?: AbortSignal): Promise<ComputeVMChange | undefined> {
    return new Promise((resolve) => {
      const waiters = this.#waiters.get(name) ?? new Set();
      this.#waiters.set(name, waiters);
      const drop = () => {
        waiters.delete(settle);
        if (waiters.size === 0 && this.#waiters.get(name) === waiters) this.#waiters.delete(name);
      };
      const abandon = () => {
        drop();
        resolve(undefined);
      };
      const settle = (change: ComputeVMChange) => {
        signal?.removeEventListener("abort", abandon);
        drop();
        resolve(change);
      };
      if (signal?.aborted) return resolve(undefined);
      waiters.add(settle);
      signal?.addEventListener("abort", abandon, { once: true });
    });
  }

  #set(name: string, after: ComputeVM | undefined): ComputeVMChange {
    const before = this.#items.get(name);
    if (after) this.#items.set(name, after);
    else this.#items.delete(name);
    const change: ComputeVMChange = { name, before, after };
    for (const waiter of [...(this.#waiters.get(name) ?? [])]) waiter(change);
    return change;
  }
}

export function acceptKeyOf(accept: { uri: string; cid: string } | undefined): string | undefined {
  return accept?.uri && accept.cid ? `${accept.uri}#${accept.cid}` : undefined;
}

export function contractReceiptOf(vm: ComputeVM | undefined): string | undefined {
  return vm?.spec?.contract?.receiptUri || undefined;
}

interface AcceptSlot {
  settled: boolean;
  name?: string;
  waiters: Array<(name: string | undefined) => void>;
}

export class ComputeVMAccepts {
  readonly #slots = new Map<string, AcceptSlot>();

  #slot(key: string): AcceptSlot {
    let slot = this.#slots.get(key);
    if (!slot) {
      slot = { settled: false, waiters: [] };
      this.#slots.set(key, slot);
    }
    return slot;
  }

  #settle(key: string, name: string | undefined): void {
    const slot = this.#slot(key);
    slot.settled = true;
    slot.name = name;
    for (const w of slot.waiters.splice(0)) w(name);
  }

  applied(key: string | undefined, name: string): void {
    if (key) this.#settle(key, name);
  }

  failed(key: string | undefined): void {
    if (key && !this.#slot(key).settled) this.#settle(key, undefined);
  }

  wait(key: string, signal?: AbortSignal): Promise<string | undefined> {
    const slot = this.#slot(key);
    if (slot.settled) return Promise.resolve(slot.name);
    return new Promise((resolve) => {
      const done = (name: string | undefined) => {
        signal?.removeEventListener("abort", abandon);
        resolve(name);
      };
      const abandon = () => {
        slot.waiters = slot.waiters.filter((w) => w !== done);
        resolve(undefined);
      };
      if (signal?.aborted) return resolve(undefined);
      slot.waiters.push(done);
      signal?.addEventListener("abort", abandon, { once: true });
    });
  }

  forget(key: string): void {
    const slot = this.#slots.get(key);
    if (slot && slot.waiters.length === 0) this.#slots.delete(key);
  }
}
