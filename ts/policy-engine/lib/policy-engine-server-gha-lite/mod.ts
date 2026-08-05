/**
 * policy-engine-server-gha-lite — the gha-lite policy engine (workflow
 * executor + request/status models + sandbox config).
 *
 * Library surface for the package. The `main.ts` CLI entrypoint and its
 * tasks remain; consumers import this mod.ts by package name instead of
 * reaching into `src/*` internals.
 */

// Workflow executor.
export { WorkflowExecutor } from "./src/workflow.ts";

// Request / status models.
export {
  ExitStatusFailure,
  ExitStatusSuccess,
  StatusComplete,
} from "./src/models.ts";
export type {
  Cache,
  CacheEntry,
  CacheFile,
  PolicyEngineComplete,
  PolicyEngineRequest,
  PolicyEngineStatus,
} from "./src/models.ts";

// Sandbox configuration.
export { resolveSandboxConfig } from "./src/config.ts";
export type { SandboxConfig } from "./src/config.ts";
