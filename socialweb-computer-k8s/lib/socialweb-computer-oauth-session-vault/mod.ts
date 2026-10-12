import type { IdentityVault } from "@publicdomainrelay/market-identity-abc";
import type { VaultRef } from "@publicdomainrelay/socialweb-computer-k8s-api";
import type { OAuthSessionData } from "@publicdomainrelay/socialweb-computer-common";
import { createAccountLock, type SessionStore } from "@publicdomainrelay/socialweb-computer-abc";

export const SESSIONS_PATH = "oauth-sessions";

export function vaultSegment(did: string): string {
  return did.replace(/[^A-Za-z0-9.-]/g, (c) => `_${c.charCodeAt(0).toString(16).padStart(2, "0")}`);
}

export function sessionVaultRef(mount: string, did: string): VaultRef {
  return { mount, path: `${SESSIONS_PATH}/${vaultSegment(did)}` };
}

export interface VaultSessionStoreOptions {
  vault: IdentityVault;
  mount: string;
  onSet?: (did: string, session: OAuthSessionData, ref: VaultRef) => Promise<void> | void;
  onDel?: (did: string, ref: VaultRef) => Promise<void> | void;
}

function isSession(v: unknown): v is OAuthSessionData {
  const s = v as OAuthSessionData | null;
  return !!s && typeof s === "object" && typeof s.userDid === "string" && typeof s.accessJwt === "string" &&
    typeof s.refreshJwt === "string";
}

export function createVaultSessionStore(opts: VaultSessionStoreOptions): SessionStore {
  return {
    async get(did) {
      const data = await opts.vault.read(sessionVaultRef(opts.mount, did));
      return isSession(data?.session) ? data.session : undefined;
    },
    async set(did, session) {
      const ref = sessionVaultRef(opts.mount, did);
      await opts.vault.write(ref, { session });
      await opts.onSet?.(did, session, ref);
    },
    async del(did) {
      const ref = sessionVaultRef(opts.mount, did);
      await opts.vault.remove(ref);
      await opts.onDel?.(did, ref);
    },
    withAccount: createAccountLock(),
  };
}
