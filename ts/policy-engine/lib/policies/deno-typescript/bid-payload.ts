import type { WorkPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const BID_PAYLOAD_FILTER_POLICY_NAME = "bid-payload";

export function createBidPayloadFilterPolicy(): WorkPolicy {
  const DEFAULT_ALLOWED = ["com.publicdomainrelay.temp.market.bids.free"];
  return {
    kind: "work",
    name: BID_PAYLOAD_FILTER_POLICY_NAME,
    description: "Only accept bids whose payload type is allowed.",
    perspectives: ["requester"],

    async evaluate(ctx) {
      const allowed = Array.isArray(ctx.args.allowedPayloadNsids)
        ? (ctx.args.allowedPayloadNsids as string[])
        : DEFAULT_ALLOWED;
      if (!ctx.offer) return { allow: true, violations: [] };
      if (!allowed.includes(ctx.offer.payloadNsid)) {
        return {
          allow: false,
          violations: [{ msg: `bid payload type ${ctx.offer.payloadNsid} not allowed`, policyId: BID_PAYLOAD_FILTER_POLICY_NAME }],
        };
      }
      return { allow: true, violations: [] };
    },
  };
}
