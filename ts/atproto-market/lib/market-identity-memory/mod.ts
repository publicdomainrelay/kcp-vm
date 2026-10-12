import type {
  IdentityVault,
  MarketIdentity,
  MarketIdentityObjects,
  MarketIdentityStatus,
  VaultRef,
} from "@publicdomainrelay/market-identity-abc";
import { findSecretMaterial } from "@publicdomainrelay/market-identity-abc";

export interface MemoryMarketIdentityObjects extends MarketIdentityObjects {
  readonly writes: Array<{ name: string; status: MarketIdentityStatus }>;
  put(obj: MarketIdentity): void;
}

export function createMemoryMarketIdentityObjects(initial: MarketIdentity[] = []): MemoryMarketIdentityObjects {
  const objects = new Map<string, MarketIdentity>();
  const writes: Array<{ name: string; status: MarketIdentityStatus }> = [];
  const put = (obj: MarketIdentity) => {
    const name = obj.metadata?.name;
    if (!name) throw new Error("a MarketIdentity needs metadata.name");
    objects.set(name, structuredClone(obj));
  };
  initial.forEach(put);
  return {
    writes,
    put,
    get(name) {
      const obj = objects.get(name);
      return Promise.resolve(obj ? structuredClone(obj) : null);
    },
    writeStatus(name, status) {
      const secrets = findSecretMaterial(status);
      if (secrets.length) return Promise.reject(new Error(`marketidentity ${name}: status carries secret material at ${secrets.join(", ")}`));
      const obj = objects.get(name);
      if (!obj) return Promise.reject(new Error(`marketidentity ${name} not found`));
      obj.status = structuredClone(status);
      writes.push({ name, status: structuredClone(status) });
      return Promise.resolve();
    },
  };
}

export interface MemoryIdentityVault extends IdentityVault {
  dump(): string;
}

function key(ref: VaultRef): string {
  return `${ref.mount}/${ref.path}`;
}

export function createMemoryIdentityVault(): MemoryIdentityVault {
  const data = new Map<string, Record<string, unknown>>();
  return {
    read(ref) {
      const v = data.get(key(ref));
      return Promise.resolve(v ? structuredClone(v) : null);
    },
    write(ref, value) {
      data.set(key(ref), structuredClone(value));
      return Promise.resolve();
    },
    remove(ref) {
      data.delete(key(ref));
      return Promise.resolve();
    },
    dump() {
      return JSON.stringify(Object.fromEntries(data));
    },
  };
}
