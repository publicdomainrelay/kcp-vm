/**
 * Shared scope-lane machinery: legacy-name aliasing + the in-process `decide`
 * gate the scope lane runs. Used by the gha-lite action (bundled), the
 * TypescriptExecutor scope lane, and the BuiltinExecutor.
 *
 *   resolvePolicyName(registry, name, perspective)  — settled alias contract.
 *       `only-me` from a bidder → `bidder-only-me`; from a requester →
 *       `requester-only-me`. Wrong-side canonical names throw.
 *   scopeDecide(...)                                 — run each named trust
 *       policy's decide() over a trust snapshot; first deny wins, any abstain
 *       escalates (returns undefined), all allow → allow.
 */

import type {
  Policy,
  PolicyArgs,
  PolicyPerspective,
  PolicyRegistry,
  PolicyResult,
  TrustQuery,
} from "./types.ts";

/** Legacy single-name trust policies that alias to a perspective variant. */
export const LEGACY_TRUST_NAMES = ["only-me", "tangled-vouch", "mutuals"] as const;

/** Soft alias resolution: canonical name, or undefined when nothing resolves. */
export function tryResolvePolicyName(
  registry: PolicyRegistry,
  name: string,
  perspective: PolicyPerspective,
): string | undefined {
  if (registry.get(name)) return name;
  if ((LEGACY_TRUST_NAMES as readonly string[]).includes(name)) {
    const canonical = `${perspective}-${name}`;
    if (registry.get(canonical)) return canonical;
  }
  return undefined;
}

/**
 * Settled alias contract. Legacy single names resolve to the perspective-split
 * variant inferred from the caller's role; canonical names are side-validated.
 * Throws on unknown names or wrong-side use — never silently runs the other
 * side's policy.
 */
export function resolvePolicyName(
  registry: PolicyRegistry,
  name: string,
  perspective: PolicyPerspective,
): string {
  const canonical = tryResolvePolicyName(registry, name, perspective);
  if (canonical === undefined) {
    throw new Error(`unknown policy "${name}" for ${perspective} side`);
  }
  assertPolicyPerspectiveSide(registry.get(canonical)!, canonical, perspective);
  return canonical;
}

/** Side-check a canonical trust name / work perspectives against a perspective. */
function assertPolicyPerspectiveSide(
  policy: Policy,
  name: string,
  perspective: PolicyPerspective,
): void {
  if (name.startsWith("bidder-") && perspective !== "bidder") {
    throw new Error(`policy "${name}" is not usable from the ${perspective} side`);
  }
  if (name.startsWith("requester-") && perspective !== "requester") {
    throw new Error(`policy "${name}" is not usable from the ${perspective} side`);
  }
  if (policy.kind === "work" && !policy.perspectives.includes(perspective)) {
    throw new Error(
      `policy "${name}" is not usable from the ${perspective} side (perspectives: ${policy.perspectives.join(", ")})`,
    );
  }
}

/** A sync trust snapshot over async resolvers, for decide(). */
async function buildTrustSnapshot(
  resolveOperatorDid: (did: string) => Promise<string | null>,
  getVouchedDids: (did: string) => Promise<Set<string>>,
  selfDid: string,
  counterpartyDid: string,
): Promise<TrustQuery> {
  const selfOp = (await resolveOperatorDid(selfDid)) ?? selfDid;
  const coOp = await resolveOperatorDid(counterpartyDid);

  const vouchSets = new Map<string, Set<string>>();
  const vouches = async (did: string): Promise<Set<string>> => {
    let s = vouchSets.get(did);
    if (!s) {
      s = await getVouchedDids(did);
      vouchSets.set(did, s);
    }
    return s;
  };

  const trustedOps = new Set<string>([selfDid, selfOp]);
  await vouches(selfDid);
  await vouches(selfOp);
  if (coOp && coOp !== selfDid && coOp !== selfOp) await vouches(coOp);

  const operatorOf = new Map<string, string>([[selfDid, selfOp]]);
  if (coOp) operatorOf.set(counterpartyDid, coOp);

  return {
    operatorOf: (did) => operatorOf.get(did),
    sameOperator: (a, b) => {
      if (a === b) return true;
      const oa = operatorOf.get(a);
      const ob = operatorOf.get(b);
      if (oa === undefined || ob === undefined) return undefined;
      return oa === ob;
    },
    isVouched: (voucher, vouchee) => vouchSets.get(voucher)?.has(vouchee) ?? false,
    vouchedBy: (voucher) => vouchSets.get(voucher) ?? new Set<string>(),
    trustedOperators: () => trustedOps,
    associatedWith: () => new Set<string>(),
  };
}

/** The flat vouch set of every trusted operator — the PreFilterInput fast path. */
function flatVouched(query: TrustQuery): Set<string> {
  const out = new Set<string>();
  for (const op of query.trustedOperators()) {
    for (const d of query.vouchedBy(op)) out.add(d);
  }
  return out;
}

export interface ScopeDecideOpts {
  registry: PolicyRegistry;
  /** Named policies in the record (or a single-entry list for a bare policy). */
  named: Array<{ name?: unknown; args?: unknown } | null | undefined>;
  perspective: PolicyPerspective;
  selfDid: string;
  counterpartyDid: string;
  resolveOperatorDid: (did: string) => Promise<string | null>;
  getVouchedDids: (did: string) => Promise<Set<string>>;
}

/**
 * Run each named trust policy's decide() over a trust snapshot.
 *
 * First deny wins; any unknown / work / abstaining policy escalates (returns
 * undefined); all allowed → allow. The scope lane never hard-denies a party
 * the graph could still admit after a refresh — the caller falls back to full
 * evaluate on undefined.
 */
export async function scopeDecide(opts: ScopeDecideOpts): Promise<PolicyResult | undefined> {
  const { registry, named, perspective, selfDid, counterpartyDid, resolveOperatorDid, getVouchedDids } = opts;
  if (!Array.isArray(named) || named.length === 0) return undefined;

  const snapshot = await buildTrustSnapshot(resolveOperatorDid, getVouchedDids, selfDid, counterpartyDid);

  let abstained = false;
  let firstDeny: PolicyResult | undefined;
  for (const raw of named) {
    const entry = raw as { name?: unknown; args?: unknown } | null;
    if (!entry || typeof entry !== "object") {
      abstained = true;
      continue;
    }
    const name = typeof entry.name === "string" ? entry.name : "";
    if (!name) {
      abstained = true;
      continue;
    }
    const args = (entry.args ?? {}) as PolicyArgs;
    const canonical = tryResolvePolicyName(registry, name, perspective);
    const policy = canonical !== undefined ? registry.get(canonical) : undefined;
    if (!policy || policy.kind !== "trust" || typeof policy.decide !== "function") {
      abstained = true;
      continue;
    }
    const verdict = policy.decide({
      did: counterpartyDid,
      selfDid,
      vouchedDids: flatVouched(snapshot),
      args,
      query: snapshot,
    });
    if (verdict === undefined) {
      abstained = true;
      continue;
    }
    if (!verdict) {
      firstDeny ??= { allow: false, violations: [{ msg: `scope denied by ${name}`, policyId: name }] };
    }
  }

  if (firstDeny) return firstDeny;
  if (abstained) return undefined;
  return { allow: true, violations: [] };
}
