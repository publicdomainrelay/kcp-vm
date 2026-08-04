import type { PolicyEvalCtx, PolicyResult, PreFilterInput, TrustPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const BIDDER_MUTUALS_POLICY_NAME = "bidder-mutuals";
export const REQUESTER_MUTUALS_POLICY_NAME = "requester-mutuals";

function decide({ did, selfDid, vouchedDids, query }: PreFilterInput): boolean | undefined {
  if (did === selfDid) return true;
  if (vouchedDids?.has(did)) return true;
  if (query) {
    for (const op of query.trustedOperators()) {
      if (query.vouchedBy(op).has(did)) return true;
    }
    const op = query.operatorOf(did);
    if (op === undefined) return undefined;
    return query.isVouched(selfDid, op) && query.isVouched(op, selfDid);
  }
  return false;
}

// Mutuals admits a counterparty whose operator mutually follows the evaluator
// (self) on Bluesky. Perspective is baked into the policy identity: subject is
// the bidder for requester-mutuals, the requester for bidder-mutuals.
async function evaluateMutuals(ctx: PolicyEvalCtx): Promise<PolicyResult> {
  ctx.log("info", "mutuals evaluate", { subjectDid: ctx.subjectDid, selfDid: ctx.selfDid });

  if (ctx.subjectDid === ctx.selfDid) return { allow: true, violations: [] };

  const operatorDid = await ctx.resolveOperatorDid(ctx.subjectDid);
  if (!operatorDid) {
    ctx.log("info", "mutuals: no operator association", { subjectDid: ctx.subjectDid });
    return { allow: false, violations: [{ msg: "no operator association", policyId: ctx.policyName }] };
  }

  if (operatorDid === ctx.selfDid) return { allow: true, violations: [] };

  try {
    const selfFollows = await ctx.getVouchedDids(ctx.selfDid);
    const operatorFollows = await ctx.getVouchedDids(operatorDid);

    const mutual = selfFollows.has(operatorDid) && operatorFollows.has(ctx.selfDid);
    if (!mutual) {
      ctx.log("info", "mutuals: not mutual follows", {
        operatorDid, selfDid: ctx.selfDid,
        selfFollowsCount: selfFollows.size, operatorFollowsCount: operatorFollows.size,
      });
      return { allow: false, violations: [{ msg: "not mutual follows", policyId: ctx.policyName }] };
    }
    return { allow: true, violations: [] };
  } catch (err) {
    return { allow: false, violations: [{ msg: String(err), policyId: ctx.policyName }] };
  }
}

export function createBidderMutualPolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: BIDDER_MUTUALS_POLICY_NAME,
    description: "Only bid on RFPs from a requester whose operator mutually follows the bidder on Bluesky.",
    needsVouchSet: true,
    decide,
    evaluate: evaluateMutuals,
  };
}

export function createRequesterMutualPolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: REQUESTER_MUTUALS_POLICY_NAME,
    description: "Only accept bids from a bidder whose operator mutually follows the requester on Bluesky.",
    needsVouchSet: true,
    decide,
    evaluate: evaluateMutuals,
  };
}
