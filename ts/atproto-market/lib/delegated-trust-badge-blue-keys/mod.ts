import type { DelegatedTrustResolver, VouchResolver } from "@publicdomainrelay/trust-graph-abc";
import { BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";

export interface ListedRecord {
  uri: string;
  value: Record<string, unknown>;
}

export interface DelegatedTrustBadgeBlueKeysOpts {
  vouchResolver: VouchResolver;
  listOwnRecords(collection: string, opts?: { limit?: number }): Promise<ListedRecord[]>;
  log?(level: string, msg: string, meta?: Record<string, unknown>): void;
}

export function createBadgeBlueKeysDelegatedTrustResolver(
  opts: DelegatedTrustBadgeBlueKeysOpts,
): DelegatedTrustResolver {
  const { vouchResolver, listOwnRecords, log } = opts;
  const noopLog = () => {};
  const logFn = log ?? noopLog;

  return {
    async getDelegatedTrustedDids(selfDid: string): Promise<Set<string>> {
      const vouched = await vouchResolver.getVouchedDids(selfDid).catch(() => new Set<string>());
      try {
        const badge = await listOwnRecords(BADGE_BLUE_KEYS_NSID, { limit: 200 });
        // Union of both association shapes as candidate operators; a wrong
        // candidate (e.g. an operator misreading its own acknowledgment) has an
        // empty vouch set and adds nothing, while the correct operator adds the
        // vouches that matter.
        const candidateOperators = new Set<string>();
        for (const r of badge) {
          const v = r.value;
          logFn("info", "delegated trust scanning badgeBlueKeys", { challenge: v.challenge, service: v.service, keyId: v.keyId, selfDid });
          if (v.service !== "requester_associate" && v.service !== "bidder_associate") continue;
          // Canonical: {challenge: operator, keyId: associated} -- self is the
          // associated party, challenge is its operator.
          if (v.keyId === selfDid && typeof v.challenge === "string" && v.challenge.startsWith("did:")) {
            candidateOperators.add(v.challenge);
          }
          // Legacy inverted shape: {challenge: self, keyId: operator} -- self's
          // own declaration. Harmless for operators (their keyIds are associated
          // DIDs with empty vouch sets).
          if (v.challenge === selfDid && typeof v.keyId === "string" && v.keyId.startsWith("did:")) {
            candidateOperators.add(v.keyId);
          }
        }
        for (const op of candidateOperators) {
          const opVouched = await vouchResolver.getVouchedDids(op);
          logFn("info", "delegated trust resolved operator vouches", { keyId: op, opVouchCount: opVouched.size });
          opVouched.forEach(d => vouched.add(d));
        }
      } catch (err) {
        logFn("warn", "delegated trust badgeBlueKeys lookup failed", { selfDid, error: String(err) });
      }
      return vouched;
    },
  };
}
