import type { Policy, PolicyRegistry } from "@publicdomainrelay/policy-deno-typescript-shared";
import { createOpenPolicy } from "./open.ts";
import { createBidderOnlyMePolicy, createRequesterOnlyMePolicy } from "./only-me.ts";
import { createBidderMutualPolicy, createRequesterMutualPolicy } from "./mutuals.ts";
import { createBidderTangledVouchPolicy, createRequesterTangledVouchPolicy } from "./tangled-vouch.ts";
import { createBidPayloadFilterPolicy } from "./bid-payload.ts";
import { createUnderFourCpusPolicy } from "./under-4-cpus.ts";

export const BUILTIN_POLICY_FACTORIES: Record<string, () => Policy> = {
  "open": createOpenPolicy,
  "bidder-only-me": createBidderOnlyMePolicy,
  "requester-only-me": createRequesterOnlyMePolicy,
  "bidder-mutuals": createBidderMutualPolicy,
  "requester-mutuals": createRequesterMutualPolicy,
  "bidder-tangled-vouch": createBidderTangledVouchPolicy,
  "requester-tangled-vouch": createRequesterTangledVouchPolicy,
  "under-4-cpus": createUnderFourCpusPolicy,
  "bid-payload": createBidPayloadFilterPolicy,
};

export function createPolicyRegistry(extra?: Policy[]): PolicyRegistry {
  const policies = new Map<string, Policy>();
  for (const [name, factory] of Object.entries(BUILTIN_POLICY_FACTORIES)) {
    policies.set(name, factory());
  }
  for (const policy of extra ?? []) policies.set(policy.name, policy);

  return {
    get(name: string): Policy | undefined {
      return policies.get(name);
    },
    names(): string[] {
      return [...policies.keys()];
    },
  };
}

export function policyNames(): string[] {
  return Object.keys(BUILTIN_POLICY_FACTORIES);
}
