import type { PolicyEvalCtx, PolicyResult, PreFilterInput, TrustPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const BIDDER_ONLY_ME_POLICY_NAME = "bidder-only-me";
export const REQUESTER_ONLY_ME_POLICY_NAME = "requester-only-me";

// Identity trivially shares an operator with itself; anyone else must resolve
// to the same operator, from the sync trust cache when present.
function decide({ did, selfDid, query }: PreFilterInput): boolean | undefined {
  if (did === selfDid) return true;
  if (!query) return undefined;
  return query.sameOperator(did, selfDid);
}

// only-me admits a counterparty when BOTH sides resolve to the same operator —
// operatorOf(subject) === operatorOf(self). The perspective is baked into the
// policy identity: for requester-only-me the subject is the bidder, for
// bidder-only-me the subject is the requester. A side with no separate
// operator is its own operator.
async function evaluateOnlyMe(ctx: PolicyEvalCtx): Promise<PolicyResult> {
  ctx.log("info", "only-me evaluate", { subjectDid: ctx.subjectDid, selfDid: ctx.selfDid });

  const counterpartyOp = await ctx.resolveOperatorDid(ctx.subjectDid);
  if (!counterpartyOp) {
    ctx.log("info", "only-me: no operator association", { subjectDid: ctx.subjectDid });
    return { allow: false, violations: [{ msg: "no operator association", policyId: ctx.policyName }] };
  }

  const selfOp = (await ctx.resolveOperatorDid(ctx.selfDid)) ?? ctx.selfDid;

  if (counterpartyOp !== selfOp) {
    ctx.log("info", "only-me: operator mismatch", { counterpartyOp, selfOp });
    return { allow: false, violations: [{ msg: "operator mismatch", policyId: ctx.policyName }] };
  }
  return { allow: true, violations: [] };
}

export function createBidderOnlyMePolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: BIDDER_ONLY_ME_POLICY_NAME,
    description: "Only bid on RFPs from a requester that resolves to the bidder's own operator.",
    decide,
    evaluate: evaluateOnlyMe,
  };
}

export function createRequesterOnlyMePolicy(): TrustPolicy {
  return {
    kind: "trust",
    name: REQUESTER_ONLY_ME_POLICY_NAME,
    description: "Only accept bids from a bidder that resolves to the requester's own operator.",
    decide,
    evaluate: evaluateOnlyMe,
  };
}
