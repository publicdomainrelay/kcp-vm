// Builds the builtin typescript policy bundle into builtin-bundle.ts as
// `export const BUILTIN_POLICY_BUNDLE = "<escaped bundle string>"`, mirroring
// lib/policies/gha-lite/scripts/build-actions.ts: `deno bundle` inlines entry.ts
// (and the registry / policy factories / shared package it imports) into a
// single self-contained JS file, then JSON.stringify escapes that JS into a
// valid TS string literal. The TypescriptExecutor loads the string into a
// sandboxed worker, so no jsr/registry resolution is needed at runtime.
//
// Re-run with `deno task build-bundle` after editing entry.ts, registry.ts, or
// any policy factory / shared package.

const ROOT = new URL("../", import.meta.url); // lib/policies/deno-typescript/
const ENTRY = new URL("bundle/entry.ts", ROOT);
const TMP = new URL("bundle/.builtin-bundle.tmp.js", ROOT);
const DIST = new URL("bundle/builtin-bundle.ts", ROOT);

async function bundleEntry(): Promise<string> {
  const cmd = new Deno.Command(Deno.execPath(), {
    // -I: allow all remote imports (npm: @atproto/identity from the shared ctx).
    // --no-check: bundle without type-checking (deno check covers correctness).
    args: ["bundle", "-I", "--no-check", ENTRY.pathname, "-o", TMP.pathname],
    stdout: "inherit",
    stderr: "inherit",
  });
  const { success, code } = await cmd.output();
  if (!success) throw new Error(`deno bundle failed for entry.ts (code ${code})`);
  const bundle = await Deno.readTextFile(TMP);
  await Deno.remove(TMP).catch(() => {});
  return bundle;
}

const bundle = await bundleEntry();

// JSON.stringify escapes quotes / backslashes / newlines into a TS string
// literal, so the bundle text round-trips byte-for-byte when parsed.
await Deno.writeTextFile(
  DIST,
  `// Generated from bundle/entry.ts by bundle/build-bundle.ts — do not edit by hand.\n// Rebuild: deno task build-bundle\n//\n// The builtin workerManifest bundle for typescript policy records.\nexport const BUILTIN_POLICY_BUNDLE = ${JSON.stringify(bundle)};\n`,
);
console.log(`wrote ${DIST.pathname} (${bundle.length} bytes)`);
