import type { IdentityVault, VaultRef } from "@publicdomainrelay/market-identity-abc";

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
