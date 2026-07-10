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
  const cache = new Map<string, string[]>(); // atprotoDid → [operatorDids]

  return {
    async discoverOperatorDids(atprotoDid: string): Promise<string[]> {
      const cached = cache.get(atprotoDid);
      if (cached) return cached;
      const dids: string[] = [];
      try {
        const ownRecords = await listRecordsOwn(BADGE_BLUE_KEYS_NSID, { limit: 200 });
        for (const rec of ownRecords) {
          const v = rec.value;
          if (v.challenge === atprotoDid && v.service === "bidder_associate") {
            const keyId = v.keyId as string | undefined;
            if (keyId && keyId.startsWith("did:")) dids.push(keyId);
          }
        }
      } catch {
        // fall through to public read below
      }
      if (dids.length === 0) {
        try {
          const publicRecords = await listRecordsPublic(atprotoDid, BADGE_BLUE_KEYS_NSID);
          for (const r of publicRecords) {
            const v = r.value;
            if (v.challenge === atprotoDid && v.service === "bidder_associate") {
              const keyId = v.keyId as string | undefined;
              if (keyId && keyId.startsWith("did:")) dids.push(keyId);
            }
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
