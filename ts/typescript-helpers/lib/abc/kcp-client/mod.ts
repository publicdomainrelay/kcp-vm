import type {
  KcpDeletePreconditions,
  KcpList,
  KcpListOptions,
  KcpObject,
  KcpPatch,
  KcpPatchOptions,
  KcpTarget,
  KcpWatchEvent,
  KcpWatchOptions,
} from "@publicdomainrelay/kcp-client-common";

export interface KcpCreateResult<T> {
  object: T;
  created: boolean;
}

export interface KcpPatchStatusOptions {
  retries?: number;
}

export interface KcpClient {
  readonly server: string;
  get(target: KcpTarget, name: string): Promise<unknown | null>;
  list(target: KcpTarget, opts?: KcpListOptions): Promise<KcpList<unknown>>;
  watch(target: KcpTarget, opts: KcpWatchOptions): AsyncGenerator<KcpWatchEvent>;
  create(target: KcpTarget, object: KcpObject): Promise<unknown>;
  createIfAbsent(target: KcpTarget, object: KcpObject): Promise<KcpCreateResult<unknown>>;
  patch(target: KcpTarget, name: string, patch: KcpPatch, opts?: KcpPatchOptions): Promise<unknown>;
  patchStatus(
    target: KcpTarget,
    name: string,
    next: (current: unknown) => unknown | null,
    opts?: KcpPatchStatusOptions,
  ): Promise<unknown | null>;
  delete(target: KcpTarget, name: string, preconditions?: KcpDeletePreconditions): Promise<boolean>;
}

export type KcpParse<T> = (raw: unknown) => T;

export interface KcpResourceClient<T> {
  readonly target: KcpTarget;
  get(name: string): Promise<T | null>;
  list(opts?: KcpListOptions): Promise<KcpList<T>>;
  watch(opts: KcpWatchOptions): AsyncGenerator<KcpWatchEvent>;
  create(object: T & KcpObject): Promise<T>;
  createIfAbsent(object: T & KcpObject): Promise<KcpCreateResult<T>>;
  patch(name: string, patch: KcpPatch, opts?: KcpPatchOptions): Promise<T>;
  patchStatus(name: string, next: (current: T) => unknown | null, opts?: KcpPatchStatusOptions): Promise<T | null>;
  replaceStatus(name: string, status: unknown): Promise<T>;
  delete(name: string, preconditions?: KcpDeletePreconditions): Promise<boolean>;
}

export function kcpResourceClient<T>(
  client: KcpClient,
  target: KcpTarget,
  parse: KcpParse<T> = (raw) => raw as T,
): KcpResourceClient<T> {
  return {
    target,
    async get(name) {
      const raw = await client.get(target, name);
      return raw === null ? null : parse(raw);
    },
    async list(opts) {
      const out = await client.list(target, opts);
      return { items: out.items.map(parse), resourceVersion: out.resourceVersion };
    },
    watch: (opts) => client.watch(target, opts),
    async create(object) {
      return parse(await client.create(target, object));
    },
    async createIfAbsent(object) {
      const out = await client.createIfAbsent(target, object);
      return { object: parse(out.object), created: out.created };
    },
    async patch(name, patch, opts) {
      return parse(await client.patch(target, name, patch, opts));
    },
    async patchStatus(name, next, opts) {
      const raw = await client.patchStatus(target, name, (current) => next(parse(current)), opts);
      return raw === null ? null : parse(raw);
    },
    async replaceStatus(name, status) {
      return parse(
        await client.patch(target, name, { type: "json", ops: [{ op: "add", path: "/status", value: status }] }, {
          subresource: "status",
        }),
      );
    },
    delete: (name, preconditions) => client.delete(target, name, preconditions),
  };
}

export interface KcpCondition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason: string;
  message: string;
  lastTransitionTime: string;
  observedGeneration?: number;
}

export interface ExecutionSnapshot {
  step?: string;
  stepStartedAt?: string;
  lastEventAt?: string;
  lastError?: string;
  attempts?: number;
}

export interface ExecutionStatusView {
  observedGeneration?: number;
  phase?: string;
  message?: string;
  conditions?: KcpCondition[];
  execution: ExecutionSnapshot;
}

export function kcpTime(at: Date): string {
  return at.toISOString().replace(/\.\d+Z$/, "Z");
}

export class ExecutionState {
  #phase: string | undefined;
  #message: string | undefined;
  #observedGeneration: number | undefined;
  #conditions: KcpCondition[] = [];
  #execution: ExecutionSnapshot = {};

  constructor(initial?: Partial<ExecutionStatusView>) {
    this.#phase = initial?.phase;
    this.#message = initial?.message;
    this.#observedGeneration = initial?.observedGeneration;
    this.#conditions = [...(initial?.conditions ?? [])];
    this.#execution = { ...(initial?.execution ?? {}) };
  }

  get phase(): string | undefined {
    return this.#phase;
  }

  get message(): string | undefined {
    return this.#message;
  }

  setPhase(phase: string, message: string, at: Date): boolean {
    const changed = phase !== this.#phase;
    this.#phase = phase;
    this.#message = message;
    this.#execution.lastEventAt = kcpTime(at);
    return changed;
  }

  observe(generation: number | undefined): void {
    this.#observedGeneration = generation;
  }

  step(step: string, at: Date): boolean {
    const stamp = kcpTime(at);
    this.#execution.lastEventAt = stamp;
    if (step === this.#execution.step) {
      this.#execution.attempts = (this.#execution.attempts ?? 1) + 1;
      return false;
    }
    this.#execution = { step, stepStartedAt: stamp, lastEventAt: stamp, attempts: 1 };
    return true;
  }

  event(at: Date): void {
    this.#execution.lastEventAt = kcpTime(at);
  }

  error(err: unknown, at: Date): void {
    this.#execution.lastError = (err instanceof Error ? err.message : String(err)).slice(0, 1_000);
    this.#execution.lastEventAt = kcpTime(at);
  }

  condition(type: string, status: KcpCondition["status"], reason: string, message: string, at: Date): boolean {
    const i = this.#conditions.findIndex((c) => c.type === type);
    const prev = i >= 0 ? this.#conditions[i] : undefined;
    if (prev && prev.status === status && prev.reason === reason && prev.message === message) return false;
    const next: KcpCondition = {
      type,
      status,
      reason,
      message,
      lastTransitionTime: prev && prev.status === status ? prev.lastTransitionTime : kcpTime(at),
      ...(this.#observedGeneration !== undefined ? { observedGeneration: this.#observedGeneration } : {}),
    };
    if (i >= 0) this.#conditions[i] = next;
    else this.#conditions.push(next);
    return true;
  }

  view(): ExecutionStatusView {
    return {
      ...(this.#observedGeneration !== undefined ? { observedGeneration: this.#observedGeneration } : {}),
      ...(this.#phase !== undefined ? { phase: this.#phase } : {}),
      ...(this.#message !== undefined ? { message: this.#message } : {}),
      ...(this.#conditions.length ? { conditions: this.#conditions.map((c) => ({ ...c })) } : {}),
      execution: { ...this.#execution },
    };
  }
}

export interface CoalescedStatusWriter<T> {
  set(value: T, urgent?: boolean): void;
  flush(): Promise<void>;
  close(): Promise<void>;
}

export function nextWriteDelayMs(lastWriteAt: number | undefined, now: number, minIntervalMs: number): number {
  if (lastWriteAt === undefined) return 0;
  return Math.max(0, lastWriteAt + minIntervalMs - now);
}
