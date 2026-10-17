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
  // OPERATOR and whose keyId is the ASSOCIATED party (bidder/requester). This
  // is the canonical shape -- the operator writes {challenge: operator,
  // keyId: associated} to acknowledge, and the associated's own repo mirrors
  // {challenge: operator, keyId: self}. A subject's operator is therefore the
  // CHALLENGE of an association record whose keyId is the subject. (Older
  // records used the inverted {challenge: subject, keyId: operator}; those are
  // the acknowledgment of a DIFFERENT subject and must NOT be read as this
  // subject's operator -- that inversion mis-resolves operators as self-operated
  // bidders, e.g. ocnuqjlz -> 5jo53.)
  const operatorOf = (v: Record<string, unknown>, subjectDid: string): string | undefined => {
    if (v.keyId !== subjectDid) return undefined;
    const service = v.service;
    if (service !== "bidder_associate" && service !== "requester_associate") return undefined;
    const challenge = v.challenge;
    return typeof challenge === "string" && challenge.startsWith("did:") ? challenge : undefined;
  };

  return {
    async discoverOperatorDids(atprotoDid: string): Promise<string[]> {
      const cached = cache.get(atprotoDid);
      if (cached) return cached;
      const dids: string[] = [];
      try {
        const ownRecords = await listRecordsOwn(BADGE_BLUE_KEYS_NSID, { limit: 200 });
        for (const rec of ownRecords) {
          const op = operatorOf(rec.value, atprotoDid);
          if (op) dids.push(op);
        }
      } catch {
        // fall through to public read below
      }
      if (dids.length === 0) {
        try {
          const publicRecords = await listRecordsPublic(atprotoDid, BADGE_BLUE_KEYS_NSID);
          for (const r of publicRecords) {
            const op = operatorOf(r.value, atprotoDid);
            if (op) dids.push(op);
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

export interface OperatedDiscoveryOpts {
  operators: string[];
  service: string;
  listRecordsPublic(repo: string, collection: string): Promise<ListedRecord[]>;
  log?(level: string, msg: string, meta?: Record<string, unknown>): void;
}

function associates(v: Record<string, unknown>, operator: string, service: string): string | undefined {
  if (v.service !== service || v.challenge !== operator) return undefined;
  return typeof v.keyId === "string" && v.keyId.startsWith("did:") && v.keyId !== operator ? v.keyId : undefined;
}

export async function discoverOperatedDids(opts: OperatedDiscoveryOpts): Promise<string[]> {
  const log = opts.log ?? (() => {});
  const found = new Set<string>();
  await Promise.all(opts.operators.map(async (operator) => {
    const claimed = await opts.listRecordsPublic(operator, BADGE_BLUE_KEYS_NSID).catch(() => [] as ListedRecord[]);
    const candidates = [...new Set(claimed.map((r) => associates(r.value, operator, opts.service)).filter((d): d is string => !!d))];
    await Promise.all(candidates.map(async (did) => {
      const own = await opts.listRecordsPublic(did, BADGE_BLUE_KEYS_NSID).catch(() => [] as ListedRecord[]);
      if (own.some((r) => associates(r.value, operator, opts.service) === did)) found.add(did);
      else log("info", "operated discovery: the associated repo does not name the operator back", { operator, did });
    }));
  }));
  return [...found];
}
