/**
 * policy-engine-cli-options — the shared CLI option surface every policy caller
 * consumes (`--policy`, `--policy-args`, exec gates) plus small arg helpers.
 * Moved from atproto-market's market-policy-abc so hono-bidder, request-vm-ssh
 * and digitalocean-bidder import one thing.
 */

import type { PolicyArgs } from "@publicdomainrelay/policy-common";

export const DEFAULT_POLICY_NAME = "only-me";
export const DEFAULT_BID_WINDOW_SEC = 30;

export const POLICY_CLI_OPTION = {
  type: "string" as const,
  description: "Policy name to evaluate bidders against",
  env: "POLICY",
  default: DEFAULT_POLICY_NAME,
};

export const POLICY_ARGS_CLI_OPTION = {
  type: "string" as const,
  description: 'Policy arguments as JSON, e.g. {"bidWindowSec":30,"firstFree":true}',
  env: "POLICY_ARGS",
};

export const ONLY_REMOTE_POLICY_EXEC_CLI_OPTION = {
  type: "boolean" as const,
  description: "Refuse to execute any policy locally; only evaluate via a remote policy engine",
  env: "ONLY_REMOTE_POLICY_EXEC",
};

export const ALLOW_UNTRUSTED_POLICY_EXEC_CLI_OPTION = {
  type: "boolean" as const,
  description: "Permit untrusted policy records (custom bundles/workflows) to run locally",
  env: "ALLOW_UNTRUSTED_POLICY_EXEC",
};

export function parsePolicyArgs(raw: unknown): PolicyArgs {
  if (raw === undefined || raw === null || raw === "") return {};
  if (typeof raw === "object") return raw as PolicyArgs;
  if (typeof raw !== "string") return {};
  const parsed = JSON.parse(raw) as unknown;
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new TypeError("policy args must be a JSON object");
  }
  return parsed as PolicyArgs;
}

export function bidWindowSecOf(args: PolicyArgs | undefined): number {
  const raw = args?.bidWindowSec;
  return typeof raw === "number" && Number.isFinite(raw) && raw >= 0 ? raw : DEFAULT_BID_WINDOW_SEC;
}

export function firstFreeOf(args: PolicyArgs | undefined): boolean {
  return args?.firstFree === true;
}
