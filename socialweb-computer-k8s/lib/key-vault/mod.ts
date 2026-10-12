import type { IdentityVault } from "@publicdomainrelay/market-identity-abc";
import type { VaultRef } from "@publicdomainrelay/socialweb-computer-k8s-api";

const PRIVATE_KEY_HEX = /^[0-9a-f]{64}$/;

function heldKey(data: Record<string, unknown> | null, ref: VaultRef): string | null {
  if (!data) return null;
  const hex = data.privateKeyHex;
  if (typeof hex !== "string" || !PRIVATE_KEY_HEX.test(hex)) {
    throw new Error(`key-vault: ${ref.mount}/${ref.path} holds no secp256k1 private key`);
  }
  return hex;
}

export async function loadOrCreateVaultKeyHex(vault: IdentityVault, ref: VaultRef): Promise<string> {
  const existing = heldKey(await vault.read(ref), ref);
  if (existing) return existing;
  const { Secp256k1Keypair } = await import("@atproto/crypto");
  const kp = await Secp256k1Keypair.create({ exportable: true });
  const hex = Array.from(await kp.export()).map((b) => b.toString(16).padStart(2, "0")).join("");
  const held = heldKey(await vault.create(ref, { privateKeyHex: hex }), ref);
  if (!held) throw new Error(`key-vault: ${ref.mount}/${ref.path} reads back nothing after it was created`);
  return held;
}
