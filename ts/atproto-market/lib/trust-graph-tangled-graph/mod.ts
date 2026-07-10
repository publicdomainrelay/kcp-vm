import type { VouchResolver } from "@publicdomainrelay/trust-graph-abc";
import { VOUCH_NSID } from "@publicdomainrelay/market-common";

export interface ListedRecord {
  uri: string;
  value: Record<string, unknown>;
}

export interface TangledGraphVouchResolverOpts {
  listRecords(repo: string, collection: string): Promise<ListedRecord[]>;
  log?(level: string, msg: string, meta?: Record<string, unknown>): void;
}

export function createTangledGraphVouchResolver(opts: TangledGraphVouchResolverOpts): VouchResolver {
  const { listRecords, log } = opts;
  const noopLog = () => {};
  const logFn = log ?? noopLog;

  return {
    async getVouchedDids(did: string): Promise<Set<string>> {
      const vouched = new Set<string>();
      try {
        const result = await listRecords(did, VOUCH_NSID);
        for (const r of result) {
          const v = r.value;
          if (v.kind === "denounce") continue;
          const rkey = r.uri.split("/").pop() ?? "";
          if (rkey.startsWith("did:")) vouched.add(rkey);
        }
      } catch (err) {
        logFn("warn", "tangled-graph vouch lookup failed", { did, error: String(err) });
      }
      return vouched;
    },
    async isVouched(voucher: string, vouchee: string): Promise<boolean> {
      const set = await this.getVouchedDids(voucher);
      return set.has(vouchee);
    },
  };
}
