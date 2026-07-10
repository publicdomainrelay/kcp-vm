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
        for (const r of badge) {
          const v = r.value;
          logFn("info", "delegated trust scanning badgeBlueKeys", { challenge: v.challenge, service: v.service, keyId: v.keyId, selfDid });
          if (v.challenge === selfDid && v.service === "requester_associate" && typeof v.keyId === "string" && v.keyId.startsWith("did:")) {
            const opVouched = await vouchResolver.getVouchedDids(v.keyId);
            logFn("info", "delegated trust resolved operator vouches", { keyId: v.keyId, opVouchCount: opVouched.size });
            opVouched.forEach(d => vouched.add(d));
          }
        }
      } catch (err) {
        logFn("warn", "delegated trust badgeBlueKeys lookup failed", { selfDid, error: String(err) });
      }
      return vouched;
    },
  };
}
