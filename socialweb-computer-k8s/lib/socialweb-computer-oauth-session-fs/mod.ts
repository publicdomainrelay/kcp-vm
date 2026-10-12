import { createJsonFileStore } from "@publicdomainrelay/json-file-store-fs";
import type { OAuthSessionData } from "@publicdomainrelay/socialweb-computer-common";
import { createAccountLock, type SessionStore } from "@publicdomainrelay/socialweb-computer-abc";

export type { SessionStore } from "@publicdomainrelay/socialweb-computer-abc";

export interface FileSessionStore extends SessionStore {
  list(): Promise<string[]>;
}

export interface FileSessionStoreOptions {
  onCorrupt?: (info: { path: string; quarantine: string }) => void;
}

export function createFileSessionStore(filePath: string, opts: FileSessionStoreOptions = {}): FileSessionStore {
  const store = createJsonFileStore<OAuthSessionData>(filePath, opts);
  return {
    get: (did) => store.get(did),
    set: (did, session) => store.set(did, session),
    del: (did) => store.del(did),
    list: () => store.list(),
    withAccount: createAccountLock(),
  };
}
