import { Secp256k1Keypair } from "@atproto/crypto";
import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import type { IdentityVault } from "@publicdomainrelay/market-identity-abc";
import type { GuestKv, JwkStore } from "@publicdomainrelay/oidc-issuer-abc";

export const RELAY_KEYPAIR_PATH = "relay-keypair";
export const OIDC_JWK_PATH = "oidc-jwk";

export async function generatedKeyHex(): Promise<string> {
  const kp = await Secp256k1Keypair.create({ exportable: true });
  return Array.from(await kp.export()).map((b) => b.toString(16).padStart(2, "0")).join("");
}

export async function vaultRelayKeypair(
  vault: IdentityVault,
  mount: string,
  role: string,
  logger: StructuredLoggerInterface,
): Promise<Secp256k1Keypair> {
  const ref = { mount, path: `${RELAY_KEYPAIR_PATH}/${role}` };
  const read = await vault.read(ref);
  const held = typeof read?.privateKeyHex === "string" ? read : await vault.create(ref, { privateKeyHex: await generatedKeyHex() });
  if (typeof held.privateKeyHex !== "string") throw new Error(`the vault's ${ref.mount}/${ref.path} holds no privateKeyHex`);
  const kp = await Secp256k1Keypair.import(held.privateKeyHex, { exportable: true });
  logger.info("bidder_ingress_key", { role, vault: `${ref.mount}/${ref.path}`, did: kp.did() });
  return kp;
}

export function vaultOidcJwkStore(
  vault: IdentityVault,
  mount: string,
  role: string,
  logger: StructuredLoggerInterface,
): JwkStore {
  const ref = { mount, path: `${OIDC_JWK_PATH}/${role}` };
  return {
    async getJwkPem(issuer) {
      const held = await vault.read(ref);
      if (!held || typeof held.pem !== "string") return null;
      if (held.issuer !== issuer) {
        logger.warn("bidder_oidc_jwk_issuer_moved", { role, held: held.issuer, issuer, hint: "a new signing key is made for the new issuer" });
        return null;
      }
      return held.pem;
    },
    async saveJwkPem(issuer, pem) {
      const held = await vault.create(ref, { issuer, pem });
      if (held.issuer !== issuer) await vault.write(ref, { issuer, pem, rotation: held.rotation ?? 0 });
      logger.info("bidder_oidc_jwk_saved", { role, vault: `${ref.mount}/${ref.path}`, issuer });
    },
    async getPublishedJwkPems(issuer) {
      const held = await vault.read(ref);
      if (!held || held.issuer !== issuer || !Array.isArray(held.previous)) return [];
      const now = Date.now();
      return held.previous.flatMap((p: unknown) => {
        const k = p as { pem?: unknown; publishUntil?: unknown };
        return typeof k.pem === "string" && Date.parse(String(k.publishUntil)) > now ? [k.pem] : [];
      });
    },
  };
}

export function vaultGuestKv(vault: IdentityVault, mount: string): GuestKv {
  const ref = (path: string) => ({ mount, path });
  return {
    read: (path) => vault.read(ref(path)),
    write: (path, data) => vault.write(ref(path), data),
    list: (path) => vault.list(ref(path)),
    remove: (path) => vault.remove(ref(path)),
  };
}
