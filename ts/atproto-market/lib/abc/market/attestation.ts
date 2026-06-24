import type { KeyData } from "@atiproto/atproto-attestation";

/**
 * A signing identity: a secp256k1 (k256) private key plus the did:key that
 * verifies it. Constructed from a hex private key (stable identity) or freshly
 * generated (ephemeral — does not survive restarts and is not in any DID doc).
 */
export interface AttestationKeypair {
  /** did:key public-key reference for the verification side. */
  did(): string;
  /** Private key material handed to @atiproto's Attestation. */
  privateKey: KeyData;
}
