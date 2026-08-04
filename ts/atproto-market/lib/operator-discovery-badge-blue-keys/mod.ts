import type { OperatorDiscovery } from "@publicdomainrelay/trust-graph-abc";
import { BADGE_BLUE_KEYS_NSID } from "@publicdomainrelay/market-lexicons";

export interface ListedRecord {
  uri: string;
  value: Record<string, unknown>;
}

export interface BadgeBlueKeysOperatorDiscoveryOpts {
  listRecordsOwn(collection: string, opts?: { limit?: number }): Promise<ListedRecord[]>;
  listRecordsPublic(repo: string, collection: string): Promise<ListedRecord[]>;
  log?(level: string, msg: string, meta?: Record<string, unknown>): void;
}

export function createBadgeBlueKeysOperatorDiscovery(opts: BadgeBlueKeysOperatorDiscoveryOpts): OperatorDiscovery {
  const { listRecordsOwn, listRecordsPublic, log } = opts;
  const noopLog = () => {};
  const logFn = log ?? noopLog;
  const cache = new Map<string, string[]>(); // atprotoDid -> [operatorDids]

  // An operator association is a badgeBlueKeys record whose challenge is the
  // subject's own DID and whose keyId is the operator. The service tag records
  // which side minted it: bidders write bidder_associate, requesters write
  // requester_associate. Both point at the same operator, so either resolves a
  // subject's operator -- a bidder's operator from its bidder_associate, and a
  // requester's operator from its requester_associate.
  const keyIdOf = (v: Record<string, unknown>, subjectDid: string): string | undefined => {
    if (v.challenge !== subjectDid) return undefined;
    const service = v.service;
    if (service !== "bidder_associate" && service !== "requester_associate") return undefined;
    const keyId = v.keyId;
    return typeof keyId === "string" && keyId.startsWith("did:") ? keyId : undefined;
  };

  return {
    async discoverOperatorDids(atprotoDid: string): Promise<string[]> {
      const cached = cache.get(atprotoDid);
      if (cached) return cached;
      const dids: string[] = [];
      try {
        const ownRecords = await listRecordsOwn(BADGE_BLUE_KEYS_NSID, { limit: 200 });
        for (const rec of ownRecords) {
          const keyId = keyIdOf(rec.value, atprotoDid);
          if (keyId) dids.push(keyId);
        }
      } catch {
        // fall through to public read below
      }
      if (dids.length === 0) {
        try {
          const publicRecords = await listRecordsPublic(atprotoDid, BADGE_BLUE_KEYS_NSID);
          for (const r of publicRecords) {
            const keyId = keyIdOf(r.value, atprotoDid);
            if (keyId) dids.push(keyId);
          }
        } catch {
          // non-critical
        }
      }
      if (dids.length > 0) {
        cache.set(atprotoDid, dids);
        logFn("info", "operator discovery: discovered operator DIDs", { bidderDid: atprotoDid, operatorDids: dids });
      }
      return dids;
    },
  };
}
