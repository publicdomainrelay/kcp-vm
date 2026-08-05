import type { PolicyEvalCtx, PolicyResult, PreFilterInput, TrustPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const BIDDER_TANGLED_VOUCH_POLICY_NAME = "bidder-tangled-vouch";
export const REQUESTER_TANGLED_VOUCH_POLICY_NAME = "requester-tangled-vouch";

function decide({ did, selfDid, vouchedDids, query }: PreFilterInput): boolean | undefined {
  if (did === selfDid) return true;
  if (vouchedDids?.has(did)) return true;
  if (query) {
    // Direct vouch from any trusted operator, or the evaluator resolves to an
    // operator vouched by self (the transitive promotion).
    for (const op of query.trustedOperators()) {
      if (query.vouchedBy(op).has(did)) return true;
    }
    const op = query.operatorOf(did);
    if (op === undefined) return undefined;
    return query.isVouched(selfDid, op) || query.isVouched(op, selfDid);
  }
  return false;
}

// Tangled-vouch admits a counterparty whose operator appears in the evaluator's
// (self's) Tangled vouch graph. Perspective is baked into the policy identity:
// subject is the bidder for requester-tangled-vouch, the requester for
// bidder-tangled-vouch.
async function evaluateTangledVouch(ctx: PolicyEvalCtx): Promise<PolicyResult> {
  ctx.log("info", "tangled-vouch evaluate", { subjectDid: ctx.subjectDid, selfDid: ctx.selfDid });

  if (ctx.subjectDid === ctx.selfDid) return { allow: true, violations: [] };

  // A subject with no separate operator IS its own operator (badgeBlueKeys
  // association absent ⇒ self-operated), so resolveOperatorDid returns null.
  // Fall back to the subject itself — the vouch-graph check below then admits a
  // subject the evaluator vouches directly, without a proxy operator.
  const operatorDid = await ctx.resolveOperatorDid(ctx.subjectDid) ?? ctx.subjectDid;
  if (operatorDid === ctx.selfDid) return { allow: true, violations: [] };

  try {
    const vouchedDids = await ctx.getVouchedDids(ctx.selfDid);
    const ok = vouchedDids.has(operatorDid);
    if (!ok) {
      ctx.log("info", "tangled-vouch: operator not in vouch set", {
        operatorDid, selfDid: ctx.selfDid, vouchedCount: vouchedDids.size,
      });
      return { allow: false, violations: [{ msg: "not vouched", policyId: ctx.policyName }] };
    }
    return { allow: true, violations: [] };
  } catch (err) {
    return { allow: false, violations: [{ msg: String(err), policyId: ctx.policyName }] };
  }
}

export function createBidderTangledVouchPolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: BIDDER_TANGLED_VOUCH_POLICY_NAME,
    description: "Only bid on RFPs from a requester whose operator appears in the bidder's Tangled vouch graph.",
    needsVouchSet: true,
    decide,
    evaluate: evaluateTangledVouch,
  };
}

export function createRequesterTangledVouchPolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: REQUESTER_TANGLED_VOUCH_POLICY_NAME,
    description: "Only accept bids from a bidder whose operator appears in the requester's Tangled vouch graph.",
    needsVouchSet: true,
    decide,
    evaluate: evaluateTangledVouch,
  };
}
