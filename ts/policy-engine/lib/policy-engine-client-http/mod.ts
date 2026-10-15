import type { EngineRegistry, PolicyEngineExecutor } from "@publicdomainrelay/policy-engine-abc";
import { POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-engine-abc";
import { GhaLiteExecutor } from "@publicdomainrelay/policy-engine-executor-gha-lite";
import type {
  PolicyEngineRequest,
  PolicyEngineStatus,
} from "@publicdomainrelay/policy-engine-server-gha-lite";

export interface HttpWorkflowRunnerOptions {
  baseUrl: string;
  timeoutMs?: number;
  fetch?: typeof fetch;
}

export type HttpWorkflowRunner = (request: PolicyEngineRequest) => Promise<PolicyEngineStatus>;

const DEFAULT_TIMEOUT_MS = 30_000;

export const TYPESCRIPT_REFUSED =
  "typescript policy records need a worker in the caller; the policy engine pod runs gha-lite policies only";

export function createHttpWorkflowRunner(opts: HttpWorkflowRunnerOptions): HttpWorkflowRunner {
  const base = opts.baseUrl.replace(/\/+$/, "");
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  return async (request) => {
    const doFetch = opts.fetch ?? globalThis.fetch;
    const res = await doFetch(`${base}/request/run`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(request),
      signal: AbortSignal.timeout(timeoutMs),
    });
    const text = await res.text();
    let body: PolicyEngineStatus;
    try {
      body = JSON.parse(text) as PolicyEngineStatus;
    } catch {
      throw new Error(`policy engine ${base} answered ${res.status} with a body that is not JSON`);
    }
    if (typeof body?.status !== "string") {
      throw new Error(`policy engine ${base} answered ${res.status} with no status`);
    }
    return body;
  };
}

export function createRemoteEngineRegistry(opts: HttpWorkflowRunnerOptions): EngineRegistry {
  const ghaLite = new GhaLiteExecutor({
    sandbox: { netOnly: true, fsApi: false },
    workflowRunner: createHttpWorkflowRunner(opts),
    ...(opts.timeoutMs !== undefined ? { timeoutMs: opts.timeoutMs } : {}),
  });
  const typescript: PolicyEngineExecutor = {
    kind: POLICY_TYPESCRIPT_NSID,
    execute: () =>
      Promise.resolve({
        allow: false,
        violations: [{ msg: TYPESCRIPT_REFUSED, policyId: "policy-engine-client-http" }],
      }),
  };
  return {
    get: ($type) =>
      $type === POLICY_GHA_LITE_NSID ? ghaLite : $type === POLICY_TYPESCRIPT_NSID ? typescript : undefined,
    kinds: () => [POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID],
  };
}
