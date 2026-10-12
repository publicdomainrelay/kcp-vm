import type { IdentityVault } from "@publicdomainrelay/market-identity-abc";
import type { ClaimKeyStore, StoredClaimKey } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import { claimSecretsIngressKeyRef } from "@publicdomainrelay/socialweb-computer-k8s-common";

export interface VaultClaimKeyStoreOptions {
  vault: IdentityVault;
  mount: string;
}

const PRIVATE_KEY_HEX = /^[0-9a-f]{64}$/;

function heldKey(data: Record<string, unknown> | null, mount: string, keyRef: string): StoredClaimKey | null {
  if (!data) return null;
  const hex = data.privateKeyHex;
  if (typeof hex !== "string" || !PRIVATE_KEY_HEX.test(hex)) {
    throw new Error(`claim-keys-vault: ${mount}/${keyRef} holds no secp256k1 private key`);
  }
  return { privateKeyHex: hex, keyRef };
}

export function createVaultClaimKeyStore(opts: VaultClaimKeyStoreOptions): ClaimKeyStore {
  const refOf = (claimName: string) => ({ mount: opts.mount, path: claimSecretsIngressKeyRef(claimName) });
  return {
    durable: true,
    async read(claimName) {
      const ref = refOf(claimName);
      return heldKey(await opts.vault.read(ref), ref.mount, ref.path);
    },
    async ensure(claimName, candidateHex) {
      const ref = refOf(claimName);
      const held = heldKey(await opts.vault.create(ref, { privateKeyHex: candidateHex }), ref.mount, ref.path);
      if (!held) throw new Error(`claim-keys-vault: ${ref.mount}/${ref.path} reads back nothing after it was created`);
      return held;
    },
    async remove(claimName) {
      await opts.vault.remove(refOf(claimName));
    },
  };
}
