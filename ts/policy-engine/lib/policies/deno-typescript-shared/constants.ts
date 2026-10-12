/** Shared wire constants for policy evaluation. Mirrors lib/common/market-common + market-lexicons. */

export const VOUCH_NSID = "sh.tangled.graph.vouch";
export const BADGE_BLUE_KEYS_NSID = "com.publicdomainrelay.temp.badgeBlueKeys";
export const BIDDER_ASSOCIATE = "bidder_associate";
export const REQUESTER_ASSOCIATE = "requester_associate";

/** Split an at:// URI into repo / collection / rkey. */
export function splitAtUri(uri: string): { repo: string; collection: string; rkey: string } {
  const parts = uri.replace(/^at:\/\//, "").split("/");
  return { repo: parts[0] ?? "", collection: parts[1] ?? "", rkey: parts[2] ?? "" };
}
