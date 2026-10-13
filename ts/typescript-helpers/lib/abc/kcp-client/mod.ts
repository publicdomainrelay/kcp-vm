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
