/**
 * oauth-scope — single source of ATProto OAuth scope tokens for the market
 * protocol. Every scope string across the polyrepo derives from the canonical
 * registry here; no repo hardcodes a scope list.
 *
 * Unified form (per atproto permission spec):
 *   collections:  repo:<collection>?action=<v1>&action=<v2>   (verbs repeated)
 *   endpoints:    rpc:<nsid>?aud=*                            (never repo-form)
 *   base:         atproto                                     (required)
 *
 * Role sets (REQUESTER_OAUTH_SCOPE, BIDDER_OAUTH_SCOPE, DESKTOP_OAUTH_SCOPE,
 * COMPUTE_SPA_OAUTH_SCOPE, DID_KEY_ASSOCIATOR_OAUTH_SCOPE) are derived from the
 * registry, never duplicated, so a new collection/verb added here flows to
 * every consumer and can't drift.
 */

/** Collection NSIDs that appear in any consumer's scope, keyed by short name. */
const C = {
  RBAC_DID: "com.publicdomainrelay.temp.auth.allowlist.rbacDid",
  OFFERING: "com.publicdomainrelay.temp.market.offering",
  BID: "com.publicdomainrelay.temp.market.bid",
  BIDS_FREE: "com.publicdomainrelay.temp.market.bids.free",
  BIDS_X402: "com.publicdomainrelay.temp.market.bids.x402",
  RECEIPT: "com.publicdomainrelay.temp.market.receipt",
  RECEIPTS_FREE: "com.publicdomainrelay.temp.market.receipts.free",
  RECEIPTS_X402: "com.publicdomainrelay.temp.market.receipts.x402",
  EVENT: "com.publicdomainrelay.temp.market.event",
  CONFIG_WIF_SIMPLE: "com.publicdomainrelay.temp.compute.config.wif.simple",
  COMPUTE_VM: "com.publicdomainrelay.temp.compute.vm",
  RFP: "com.publicdomainrelay.temp.market.rfp",
  ACCEPT: "com.publicdomainrelay.temp.market.accept",
  VM_DELETE: "com.publicdomainrelay.temp.compute.events.vm.delete",
  VM_ON_NETWORK: "com.publicdomainrelay.temp.compute.events.vm.onNetwork",
  BADGE_BLUE_KEYS: "com.publicdomainrelay.temp.badgeBlueKeys",
  FEDPROXY_RBAC: "com.fedproxy.rbac",
  FEDPROXY_SSH_PUBLIC_KEY: "com.fedproxy.sshPublicKey",
  BIDDER_ASSOCIATION: "com.publicdomainrelay.temp.market.bidderAssociation",
  POLICY_GHA_LITE: "computer.socialweb.temp.policy.ghalite",
  POLICY_TYPESCRIPT: "computer.socialweb.temp.policy.typescript",
} as const;

/** RPC endpoint scopes, already in unified `rpc:<nsid>?aud=*` form. */
const R = {
  SUBMIT_RFP: "rpc:com.publicdomainrelay.temp.market.submitRfp?aud=*",
  SUBMIT_ACCEPT: "rpc:com.publicdomainrelay.temp.market.submitAccept?aud=*",
  SUBMIT_BID: "rpc:com.publicdomainrelay.temp.market.submitBid?aud=*",
  SUBMIT_EVENT: "rpc:com.publicdomainrelay.temp.market.submitEvent?aud=*",
  ASSOCIATE_CONFIRM: "rpc:com.publicdomainrelay.temp.requester.associateConfirm?aud=*",
} as const;

/**
 * Canonical registry: collection → verbs it is ever granted (union across all
 * consumers). A verb listed here is granted to every set that includes the
 * collection — additive, never narrowing a consumer's existing grant.
 */
const COLLECTIONS: Record<string, readonly string[]> = {
  [C.RBAC_DID]: ["create", "update"],
  [C.OFFERING]: ["create", "update"],
  [C.BID]: ["create", "update"],
  [C.BIDS_FREE]: ["create", "update"],
  [C.BIDS_X402]: ["create", "update"],
  [C.RECEIPT]: ["create", "update"],
  [C.RECEIPTS_FREE]: ["create", "update"],
  [C.RECEIPTS_X402]: ["create", "update"],
  [C.EVENT]: ["create", "update"],
  [C.CONFIG_WIF_SIMPLE]: ["create", "update"],
  [C.COMPUTE_VM]: ["create"],
  [C.RFP]: ["create"],
  [C.ACCEPT]: ["create"],
  [C.VM_DELETE]: ["create"],
  [C.VM_ON_NETWORK]: ["create"],
  [C.BADGE_BLUE_KEYS]: ["create"],
  [C.FEDPROXY_RBAC]: ["create", "update"],
  [C.FEDPROXY_SSH_PUBLIC_KEY]: ["create"],
  [C.BIDDER_ASSOCIATION]: ["create", "update"],
  [C.POLICY_GHA_LITE]: ["create"],
  [C.POLICY_TYPESCRIPT]: ["create"],
};

/**
 * Render a collection's verbs as one unified scope token. `verbs` overrides
 * the registry's union verbs so each role requests only what it actually does
 * (e.g. a bidder requests create on `market.bid` even though desktop also
 * updates it — role subsets must not inherit every consumer's verbs).
 */
function collectionScope(nsid: string, verbs?: readonly string[]): string {
  const v = verbs ?? COLLECTIONS[nsid];
  if (!v) throw new Error(`oauth-scope: unknown collection "${nsid}"`);
  return `repo:${nsid}?action=${v.join("&action=")}`;
}

function collectionScopes(nsids: string[], verbs?: readonly string[]): string[] {
  return nsids.map((n) => collectionScope(n, verbs));
}

const CREATE = ["create"] as const;
const CREATE_UPDATE = ["create", "update"] as const;

/** Concatenate scope sets, dropping duplicates while preserving order. */
export function dedupeScopes(...sets: readonly (readonly string[])[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const set of sets) {
    for (const token of set) {
      if (!seen.has(token)) {
        seen.add(token);
        out.push(token);
      }
    }
  }
  return out;
}

/** requester (request-vm-ssh): collections/RPCs a requester writes — create only. */
export const REQUESTER_OAUTH_SCOPE: string[] = dedupeScopes(
  ["atproto"],
  collectionScopes(
    [
      C.COMPUTE_VM, C.RFP, C.ACCEPT, C.EVENT, C.VM_DELETE, C.VM_ON_NETWORK,
      C.BADGE_BLUE_KEYS, C.FEDPROXY_RBAC, C.POLICY_GHA_LITE, C.POLICY_TYPESCRIPT,
    ],
    CREATE,
  ),
  [R.SUBMIT_RFP, R.SUBMIT_ACCEPT, R.SUBMIT_BID, R.SUBMIT_EVENT],
);

/** bidder (hono-bidder, digitalocean-bidder): create everywhere except offering (create+update). */
export const BIDDER_OAUTH_SCOPE: string[] = dedupeScopes(
  ["atproto"],
  collectionScopes(
    [
      C.RBAC_DID, C.BID, C.BIDS_FREE, C.RECEIPT, C.EVENT, C.CONFIG_WIF_SIMPLE,
      C.COMPUTE_VM, C.RFP, C.ACCEPT, C.VM_DELETE, C.VM_ON_NETWORK,
      C.BADGE_BLUE_KEYS, C.FEDPROXY_RBAC, C.BIDDER_ASSOCIATION,
      C.POLICY_GHA_LITE, C.POLICY_TYPESCRIPT,
    ],
    CREATE,
  ),
  [collectionScope(C.OFFERING, CREATE_UPDATE)],
  [R.SUBMIT_RFP, R.SUBMIT_ACCEPT, R.SUBMIT_BID, R.SUBMIT_EVENT],
);

/** desktop (hono-desktop / hono-macos-runner-desktop): create+update on its collections. */
export const DESKTOP_OAUTH_SCOPE: string[] = dedupeScopes(
  ["atproto"],
  collectionScopes(
    [
      C.RBAC_DID, C.OFFERING, C.BID, C.BIDS_FREE, C.BIDS_X402, C.RECEIPT,
      C.RECEIPTS_FREE, C.RECEIPTS_X402, C.EVENT, C.CONFIG_WIF_SIMPLE,
      C.FEDPROXY_RBAC, C.BIDDER_ASSOCIATION,
    ],
    CREATE_UPDATE,
  ),
  [collectionScope(C.BADGE_BLUE_KEYS, CREATE)],
  [R.SUBMIT_BID, R.SUBMIT_EVENT],
);

/** compute-spa (browser requester): minimal requester-shaped set, create only. */
export const COMPUTE_SPA_OAUTH_SCOPE: string[] = dedupeScopes(
  ["atproto"],
  collectionScopes(
    [C.COMPUTE_VM, C.BADGE_BLUE_KEYS, C.FEDPROXY_RBAC, C.FEDPROXY_SSH_PUBLIC_KEY],
    CREATE,
  ),
  [R.SUBMIT_RFP, R.SUBMIT_ACCEPT],
);

/**
 * did-key-associator (registered QR client at qr.fedfork.com). The QR login
 * serves both requester and bidder flows, so its scope is the union of both,
 * plus the app's own associateConfirm RPC. Must ⊇ REQUESTER and ⊇ BIDDER.
 */
export const DID_KEY_ASSOCIATOR_OAUTH_SCOPE: string[] = dedupeScopes(
  BIDDER_OAUTH_SCOPE,
  REQUESTER_OAUTH_SCOPE,
  [R.ASSOCIATE_CONFIRM],
);

/**
 * The full canonical union of every scope token in the system — every role's
 * set (with its own verbs), plus every RPC endpoint. Each role ⊆ OAUTH_SCOPE.
 */
export const OAUTH_SCOPE: string[] = dedupeScopes(
  ["atproto"],
  REQUESTER_OAUTH_SCOPE,
  BIDDER_OAUTH_SCOPE,
  DESKTOP_OAUTH_SCOPE,
  COMPUTE_SPA_OAUTH_SCOPE,
  DID_KEY_ASSOCIATOR_OAUTH_SCOPE,
);

/** `OAUTH_SCOPE` joined into the space-separated form used in OAuth requests. */
export const OAUTH_SCOPE_STRING = OAUTH_SCOPE.join(" ");

/**
 * Small localhost-only scope used by did-key-associator's dynamic
 * `http://localhost?...` client id. Preserved verbatim (existing behavior).
 */
export const OAUTH_SCOPE_LOCALHOST =
  "atproto repo:com.publicdomainrelay.temp.badgeBlueKeys?action=create,update,delete " +
  "rpc:com.publicdomainrelay.temp.requester.associateConfirm?aud=*";
