#!/usr/bin/env -S deno run --allow-net
// Raw-deno policy evaluation entrypoint.
//
// Evaluates a named policy against live network data using the shared
// record-centric ctx (atproto Agent → badgeBlueKeys operator resolution and
// Tangled vouch sets), memoizing the verdict in the JSON-object cache. The
// cache is seeded from `--context` and returned in the output, so a second run
// can feed it back and short-circuit the network:
//
//   deno run -A main.ts --policy requester-only-me \
//     --self-did did:plc:<requester> \
//     --bid '{"uri":"at://did:plc:<bidder>/com.publicdomainrelay.temp.market.bid/3mm...","cid":"bafyrei...","value":{"payload":{...}}}'
//
// Policies are perspective-named (bidder-* / requester-*). The caller passes
// the market records it has at its stage — --rfp / --bid / --accept, any
// combination, each optional — and `--self-did` (the evaluator's own DID).
// subject/root-requester/demand/offer are derived from whichever records are
// present. A record may be given as {uri, cid} only; its value is hydrated
// over the network (and cached under its strongRef).
//
// Output JSON: { exit_status, outputs: { allow, violations, fromCache, cacheKey }, cache }.

import { parseArgs } from "@std/cli/parse-args";
import {
  createCacheStore,
  createPolicyCtx,
  runPolicy,
  type PolicyArgs,
  type PolicyPerspective,
  type PolicyRecord,
} from "@publicdomainrelay/policy-deno-typescript-shared";
import { createPolicyRegistry } from "./registry.ts";

function usage(): never {
  console.error(`policy-deno-typescript — evaluate a market policy against live network data

Usage:
  deno run -A main.ts [flags]

Flags:
  --policy <name>            Policy to evaluate (default: requester-only-me)
  --self-did <did>           DID running this evaluation (required)
  --rfp <json>               RFP record {uri,cid,value?} present at this stage
  --bid <json>               Bid record {uri,cid,value?} present at this stage
  --accept <json>            Accept record {uri,cid,value?} present at this stage
  --policy-args <json>       Policy arguments, e.g. {"maxCpus":4} or {"cacheTtlSec":60}
  --plc-url <url>            PLC directory for DID->PDS resolution (default: https://plc.directory)
  --context <json>           Request context, e.g. {"cache":...} to seed the cache
  --cache-key <s>            Explicit cache key override
`);
  Deno.exit(2);
}

function parseJson<T>(raw: string | undefined, name: string): T | undefined {
  if (!raw) return undefined;
  try {
    return JSON.parse(raw) as T;
  } catch {
    console.error(`--${name} is not valid JSON: ${raw}`);
    Deno.exit(2);
  }
}

/** The side a policy acts on: work policies declare it, trust policies carry it
 * in the name (bidder-* / requester-*; open is symmetric → requester). */
function policySide(policy: {
  kind: string;
  name: string;
  perspectives?: PolicyPerspective[];
}): PolicyPerspective {
  if (policy.kind === "work") return policy.perspectives?.[0] ?? "requester";
  return policy.name.startsWith("bidder-") ? "bidder" : "requester";
}

async function main(): Promise<void> {
  const flags = parseArgs(Deno.args, {
    string: [
      "policy", "self-did", "rfp", "bid", "accept",
      "policy-args", "plc-url", "context", "cache-key",
    ],
    default: { policy: "requester-only-me" },
  });

  const selfDid = flags["self-did"] as string | undefined;
  if (!selfDid) {
    console.error("--self-did is required (the evaluator's own DID)");
    Deno.exit(2);
  }

  const registry = createPolicyRegistry();
  const policy = registry.get(flags.policy as string);
  if (!policy) {
    console.error(`unknown policy "${flags.policy}" (available: ${registry.names().join(", ")})`);
    Deno.exit(2);
  }

  const args = parseJson<PolicyArgs>(flags["policy-args"] as string | undefined, "policy-args") ?? {};
  const context = parseJson<{ cache?: Record<string, unknown> }>(
    flags.context as string | undefined,
    "context",
  );

  const store = createCacheStore(context?.cache as Parameters<typeof createCacheStore>[0]);
  const ctx = await createPolicyCtx({
    policyName: policy.name,
    args,
    perspective: policySide(policy),
    selfDid,
    rfp: parseJson<PolicyRecord>(flags.rfp as string | undefined, "rfp"),
    bid: parseJson<PolicyRecord>(flags.bid as string | undefined, "bid"),
    accept: parseJson<PolicyRecord>(flags.accept as string | undefined, "accept"),
    plcUrl: (flags["plc-url"] as string | undefined) ?? undefined,
    store,
  });

  const { result, fromCache, cacheKey } = await runPolicy(store, policy, ctx, {
    cacheKey: flags["cache-key"] as string | undefined,
  });

  console.log(JSON.stringify({
    exit_status: result.allow ? "success" : "failure",
    outputs: { allow: result.allow, violations: result.violations, fromCache, cacheKey },
    cache: store.read(),
  }, null, 2));
}

if (import.meta.main) {
  await main();
}
