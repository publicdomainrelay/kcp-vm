import type { TrustPolicy } from "@publicdomainrelay/policy-deno-typescript-shared";

export const OPEN_POLICY_NAME = "open";

export function createOpenPolicy(): TrustPolicy {
  return {
    kind: "trust" as const,
    name: OPEN_POLICY_NAME,
    description: "Open admission — no restriction on who may bid or fulfill.",
    decide() {
      return true;
    },
    async evaluate() {
      return { allow: true, violations: [] };
    },
  };
}
