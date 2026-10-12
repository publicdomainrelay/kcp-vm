import type { ClaimKeyStore, StoredClaimKey } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { claimSecretsIngressKeyRef } from "@publicdomainrelay/socialweb-computer-k8s-common";

export const DEFAULT_OPENBAO_KV_MOUNT = "secret";

export interface OpenBaoClaimKeyStoreOptions {
  address: string;
  token: () => Promise<string>;
  mount?: string;
  fetch?: typeof fetch;
}

const PRIVATE_KEY_HEX = /^[0-9a-f]{64}$/;

export function createOpenBaoClaimKeyStore(opts: OpenBaoClaimKeyStoreOptions): ClaimKeyStore {
  const base = opts.address.replace(/\/+$/, "");
  const mount = (opts.mount ?? DEFAULT_OPENBAO_KV_MOUNT).replace(/^\/+|\/+$/g, "");
  const doFetch = opts.fetch ?? fetch;

  const urlOf = (kind: "data" | "metadata", keyRef: string) => `${base}/v1/${mount}/${kind}/${keyRef}`;

  async function call(method: string, url: string, body?: unknown): Promise<Response> {
    return await doFetch(url, {
      method,
      headers: {
        "X-Vault-Token": await opts.token(),
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  }

  async function failure(what: string, keyRef: string, res: Response): Promise<Error> {
    const text = await res.text().catch(() => "");
    return new Error(`openbao ${what} ${mount}/${keyRef}: ${res.status} ${text.slice(0, 300)}`);
  }

  async function read(claimName: string): Promise<StoredClaimKey | null> {
    const keyRef = claimSecretsIngressKeyRef(claimName);
    const res = await call("GET", urlOf("data", keyRef));
    if (res.status === 404) {
      await res.body?.cancel();
      return null;
    }
    if (!res.ok) throw await failure("read", keyRef, res);
    const body = await res.json() as { data?: { data?: { privateKeyHex?: unknown } } };
    const hex = body.data?.data?.privateKeyHex;
    if (typeof hex !== "string" || !PRIVATE_KEY_HEX.test(hex)) {
      throw new Error(`openbao read ${mount}/${keyRef}: the stored value is not a secp256k1 private key`);
    }
    return { privateKeyHex: hex, keyRef };
  }

  return {
    durable: true,
    read,
    async ensure(claimName, candidateHex) {
      const keyRef = claimSecretsIngressKeyRef(claimName);
      const res = await call("POST", urlOf("data", keyRef), {
        options: { cas: 0 },
        data: { privateKeyHex: candidateHex },
      });
      if (res.ok) {
        await res.body?.cancel();
        return { privateKeyHex: candidateHex, keyRef };
      }
      const text = await res.text().catch(() => "");
      if (res.status === 400 && text.includes("check-and-set")) {
        const winner = await read(claimName);
        if (winner) return winner;
      }
      throw new Error(`openbao write ${mount}/${keyRef}: ${res.status} ${text.slice(0, 300)}`);
    },
    async remove(claimName) {
      const keyRef = claimSecretsIngressKeyRef(claimName);
      const res = await call("DELETE", urlOf("metadata", keyRef));
      if (res.ok || res.status === 404) {
        await res.body?.cancel();
        return;
      }
      throw await failure("delete", keyRef, res);
    },
  };
}
