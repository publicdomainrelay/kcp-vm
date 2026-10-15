/**
 * policy-engine-evaluator — the ONE import callers use to evaluate policies.
 *
 * Dispatches through an EngineRegistry ($type → executor). The only record
 * kinds are `computer.socialweb.temp.policy.ghalite` (a GitHub Actions
 * workflow) and `computer.socialweb.temp.policy.typescript` (a workerManifest
 * bundle). Callers — bidder, requester, gateway, hono server — supply `resolve`
 * and the trust resolvers; every policy decision runs through the executors.
 *
 *   evaluatePolicies(refs, ctx)  — RFP.policies[] strongRefs; all must allow.
 *   scope({ ref, ... })          — hot path / checkScope: scope-mode verdict for
 *                                  one record ref, verdict-cached per counterparty.
 *   buildPolicyRecord(opts)      — mint a gha-lite / typescript policy record.
 *   resolvePolicyName(...)       — legacy-name aliasing (re-exported).
 */

import type { EngineRegistry, ScopeInput } from "@publicdomainrelay/policy-engine-abc";
import type {
  PolicyArgs,
  PolicyEvalCtx,
  PolicyPerspective,
  PolicyRecord,
  PolicyResult,
  StrongRef,
} from "@publicdomainrelay/policy-common";
import {
  createConcurrencyLimiter,
  DEFAULT_POLICY_CONCURRENCY,
  POLICY_GHA_LITE_NSID,
  POLICY_TYPESCRIPT_NSID,
} from "@publicdomainrelay/policy-common";
import { resolvePolicyName, type PolicyRegistry } from "@publicdomainrelay/policy-deno-typescript-shared";
import type { ScopeCache, ScopeVerdict } from "@publicdomainrelay/policy-engine-scope-cache";

export { resolvePolicyName } from "@publicdomainrelay/policy-deno-typescript-shared";
export type { PolicyIdentity, ScopeCache, ScopeVerdict } from "@publicdomainrelay/policy-engine-scope-cache";

export interface PolicyEvaluatorOptions {
  /** $type → executor dispatch (gha-lite, typescript). */
  registry: EngineRegistry;
  attachedRegistry?: EngineRegistry;
  /** Resolve a strongRef to a record body (policy records + workerManifests). */
  resolve: (ref: StrongRef) => Promise<Record<string, unknown>>;
  /** Trust resolvers handed to in-process scope lanes / escalated evaluates. */
  resolveOperatorDid?: (did: string) => Promise<string | null>;
  getVouchedDids?: (did: string) => Promise<Set<string>>;
  /** Host scope verdict cache — converts per-event cost to per-novel-party. */
  scopeCache?: ScopeCache;
  /** Cap on policy evaluations running at once (scope runs and full evaluates). */
  maxConcurrentEvaluations?: number;
  /** First-party policy registry — used to canonicalize legacy names at mint. */
  policies?: PolicyRegistry;
  log?: (level: string, msg: string, meta?: Record<string, unknown>) => void;
}

export interface PolicyEvaluator {
  /** Evaluate every ref in RFP.policies[]; all must allow, first deny short-circuits. */
  evaluatePolicies(input: {
    refs: StrongRef[];
    ctx: PolicyEvalCtx;
    permissions?: Record<string, unknown>;
  }): Promise<PolicyResult>;
  /** Hot path / checkScope: scope-mode verdict for one record, cached. Pass a
   *  `ref` to resolve the record, or a pre-built `policyRecord` (e.g. the
   *  bidder's own in-memory gha-lite record). Cache identity = the ref, or the
   *  policyRecord's uri/cid. */
  scope(input: {
    ref?: StrongRef;
    policyRecord?: PolicyRecord;
    perspective: PolicyPerspective;
    selfDid: string;
    counterpartyDid: string;
    args: PolicyArgs;
    refresh?: boolean;
  }): Promise<PolicyResult>;
  /** Mint a policy record (gha-lite workflow or typescript bundle). */
  buildPolicyRecord(opts: BuildPolicyRecordOpts): BuiltPolicyRecord;
}

export interface BuiltPolicyRecord {
  nsid: string;
  record: Record<string, unknown>;
}

export interface BuildPolicyRecordOpts {
  name: string;
  description?: string;
  args?: PolicyArgs;
  requesterDid: string;
  /** Side of the minting caller — canonicalizes legacy single names. */
  perspective?: PolicyPerspective;
  kind: "gha-lite" | "typescript";
  /** gha-lite: inline GitHub Actions workflow YAML. */
  workflow?: string;
  /** typescript: strongRef to the workerManifest holding the bundled policy. */
  manifest?: StrongRef;
  /** typescript: the named policy set the record carries (also used by scope). */
  policies?: Array<{ name: string; args?: PolicyArgs }>;
  permissions?: Record<string, unknown>;
}

export function createPolicyEvaluator(opts: PolicyEvaluatorOptions): PolicyEvaluator {
  const { registry, scopeCache, policies, log } = opts;
  const attachedRegistry = opts.attachedRegistry ?? registry;
  const resolveOperatorDid = opts.resolveOperatorDid ?? (async () => null);
  const getVouchedDids = opts.getVouchedDids ?? (async () => new Set<string>());
  const limited = createConcurrencyLimiter(opts.maxConcurrentEvaluations ?? DEFAULT_POLICY_CONCURRENCY);

  function deny(msg: string, policyId: string): PolicyResult {
    return { allow: false, violations: [{ msg, policyId }] };
  }

  async function evaluatePolicies(input: {
    refs: StrongRef[];
    ctx: PolicyEvalCtx;
    permissions?: Record<string, unknown>;
  }): Promise<PolicyResult> {
    const { refs, ctx, permissions } = input;
    for (const ref of refs) {
      let value: Record<string, unknown>;
      try {
        value = await opts.resolve(ref);
      } catch (err) {
        return deny(`failed to resolve policy ref ${ref.uri}: ${err}`, ref.uri);
      }
      const $type = String(value.$type ?? ref.uri.split("/")[3] ?? "");
      const executor = attachedRegistry.get($type);
      if (!executor) return deny(`unknown policy record type: ${$type}`, ref.uri);

      const policyRecord: PolicyRecord = { uri: ref.uri, cid: ref.cid, value };
      const result = await limited(() =>
        executor.execute({
          policyRecord,
          ctx,
          permissions: permissions ?? (value.permissions as Record<string, unknown> | undefined),
        })
      );
      if (!result.allow) return result;
    }
    return { allow: true, violations: [] };
  }

  /** A caller-built ctx for scope escalation — the scope lane's own subject. */
  function ctxFromScope(
    perspective: PolicyPerspective,
    selfDid: string,
    counterpartyDid: string,
    args: PolicyArgs,
  ): PolicyEvalCtx {
    return {
      policyName: "scope",
      args,
      perspective,
      selfDid,
      counterpartyDid,
      subjectDid: counterpartyDid,
      rootRequesterDid: perspective === "requester" ? selfDid : counterpartyDid,
      resolve: opts.resolve,
      resolveOperatorDid,
      getVouchedDids,
      log: log ?? (() => {}),
    };
  }

  async function scope(input: {
    ref?: StrongRef;
    policyRecord?: PolicyRecord;
    perspective: PolicyPerspective;
    selfDid: string;
    counterpartyDid: string;
    args: PolicyArgs;
    refresh?: boolean;
  }): Promise<PolicyResult> {
    const { ref, policyRecord: prebuilt, counterpartyDid, args } = input;
    const uri = ref?.uri ?? prebuilt?.uri ?? "";
    const cid = ref?.cid ?? prebuilt?.cid ?? "";
    const identity = { kind: "ref" as const, uri, cid };
    if (!input.refresh) {
      const cacheHit = scopeCache?.get(identity, counterpartyDid, args);
      if (cacheHit) return cacheHit;
    }
    const work = () => limited(() => runScope(input, uri));
    if (!scopeCache) return (await work()).result;
    return await scopeCache.once(identity, counterpartyDid, args, work);
  }

  async function runScope(
    input: {
      ref?: StrongRef;
      policyRecord?: PolicyRecord;
      perspective: PolicyPerspective;
      selfDid: string;
      counterpartyDid: string;
      args: PolicyArgs;
    },
    uri: string,
  ): Promise<ScopeVerdict> {
    const { ref, policyRecord: prebuilt, perspective, selfDid, counterpartyDid, args } = input;
    let policyRecord: PolicyRecord;
    if (prebuilt) {
      policyRecord = prebuilt;
    } else if (ref) {
      let value: Record<string, unknown>;
      try {
        value = await opts.resolve(ref);
      } catch (err) {
        return { result: deny(`failed to resolve policy ref ${ref.uri}: ${err}`, ref.uri), cacheable: false };
      }
      policyRecord = { uri: ref.uri, cid: ref.cid, value };
    } else {
      return { result: deny("scope requires a ref or a policyRecord", "server"), cacheable: false };
    }
    const $type = String(policyRecord.value?.$type ?? "");
    const executor = registry.get($type);

    const scopeInput: ScopeInput = { perspective, selfDid, counterpartyDid, args, resolveOperatorDid, getVouchedDids };
    let result: PolicyResult | undefined;
    if (executor?.scope) {
      result = await executor.scope({ policyRecord, scope: scopeInput });
    }

    if (result === undefined) {
      // Abstained / no scope lane → escalate to full evaluate so the gate still
      // yields a verdict. The gha-lite scope lane already falls back inside the
      // workflow, so this path is mostly typescript records that could not
      // decide from the snapshot.
      result = executor
        ? await executor.execute({ policyRecord, ctx: ctxFromScope(perspective, selfDid, counterpartyDid, args) })
        : deny(`unknown policy record type: ${$type === "" ? "(empty)" : $type}`, $type);
    }

    (log ?? (() => {}))("debug", "scope verdict", { ref: uri, counterpartyDid, allow: result.allow });
    return { result, cacheable: true };
  }

  function buildPolicyRecord(build: BuildPolicyRecordOpts): BuiltPolicyRecord {
    const name = build.perspective !== undefined && policies !== undefined
      ? resolvePolicyName(policies, build.name, build.perspective)
      : build.name;
    const createdAt = new Date().toISOString();
    const permissions = build.permissions !== undefined ? { permissions: build.permissions } : {};

    if (build.kind === "gha-lite") {
      return {
        nsid: POLICY_GHA_LITE_NSID,
        record: {
          $type: POLICY_GHA_LITE_NSID,
          name,
          description: build.description ?? "",
          workflow: build.workflow ?? "",
          ...permissions,
          createdAt,
        },
      };
    }

    return {
      nsid: POLICY_TYPESCRIPT_NSID,
      record: {
        $type: POLICY_TYPESCRIPT_NSID,
        name,
        description: build.description ?? "",
        policies: build.policies ?? [{ name, args: build.args ?? {} }],
        manifest: build.manifest
          ? { $type: "com.atproto.repo.strongRef", uri: build.manifest.uri, cid: build.manifest.cid }
          : undefined,
        ...permissions,
        createdAt,
      },
    };
  }

  return { evaluatePolicies, scope, buildPolicyRecord };
}
