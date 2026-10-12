import type { WorkPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const UNDER_4_CPUS_POLICY_NAME = "under-4-cpus";

export function createUnderFourCpusPolicy(): WorkPolicy {
  return {
    kind: "work",
    name: UNDER_4_CPUS_POLICY_NAME,
    description: "Only bid on VMs that need at most maxCpus vCPUs.",
    perspectives: ["bidder"],

    async evaluate(ctx) {
      const maxCpus = typeof ctx.args.maxCpus === "number" ? ctx.args.maxCpus : 4;
      if (!ctx.demand) return { allow: true, violations: [] };

      let payload = ctx.demand.payload;
      if (!payload) {
        try {
          payload = await ctx.resolve(ctx.demand.payloadRef);
        } catch (err) {
          return {
            allow: false,
            violations: [{ msg: `failed to resolve demand payload: ${err}`, policyId: UNDER_4_CPUS_POLICY_NAME }],
          };
        }
      }
      const cpus = payload.cpus;
      if (typeof cpus !== "number") return { allow: true, violations: [] };
      if (cpus > maxCpus) {
        return {
          allow: false,
          violations: [{ msg: `VM needs ${cpus} cpus; this bidder's cap is ${maxCpus}`, policyId: UNDER_4_CPUS_POLICY_NAME }],
        };
      }
      return { allow: true, violations: [] };
    },
  };
}
