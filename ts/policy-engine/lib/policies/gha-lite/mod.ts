/** policies-gha-lite — shared action machinery + the bundled policy workflows.
 * Re-exports action-common (inlined into each bundled action) and WORKFLOWS
 * (the canonical policy → workflow-YAML map, so callers can build a
 * computer.socialweb.temp.policy.gha-lite record without reading files). */
export * from "./action-common.ts";
export * from "./workflows.ts";
