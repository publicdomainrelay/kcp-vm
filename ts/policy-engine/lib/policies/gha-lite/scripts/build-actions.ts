// Builds each bundled policy action into its self-contained dist/index.js via
// `deno bundle`, the GitHub Actions pattern: the engine runs the bundled file,
// so no jsr/registry resolution is needed at runtime (full or net-only mode).
//
// The source index.ts imports the policy factory from
// @publicdomainrelay/policy-deno-typescript and the evaluation helper from
// action-common.ts; `deno bundle` inlines those (and the shared package they
// import) into the single output file. Re-run with `deno task build` after
// editing any action source or the shared packages.

const ACTIONS = [
  "policy-open",
  "policy-bidder-only-me",
  "policy-requester-only-me",
  "policy-bidder-mutuals",
  "policy-requester-mutuals",
  "policy-bidder-tangled-vouch",
  "policy-requester-tangled-vouch",
  "policy-under-4-cpus",
  "policy-bid-payload",
];

const ROOT = new URL("../", import.meta.url); // lib/policies/gha-lite/

async function bundleAction(name: string): Promise<void> {
  const src = new URL(`bundled-actions/tangy/${name}/index.ts`, ROOT);
  const dist = new URL(`bundled-actions/tangy/${name}/dist/index.js`, ROOT);
  await Deno.mkdir(new URL(`bundled-actions/tangy/${name}/dist/`, ROOT), { recursive: true });

  const cmd = new Deno.Command(Deno.execPath(), {
    // -I: allow all remote imports (npm: @atproto/api from the shared package).
    // --no-check: bundle without type-checking (deno check covers correctness).
    // --packages defaults to "bundle" so npm deps are inlined into the output.
    // Output goes to -o (new `deno bundle` treats all positionals as inputs).
    args: ["bundle", "-I", "--no-check", src.pathname, "-o", dist.pathname],
    stdout: "inherit",
    stderr: "inherit",
  });
  const { success, code } = await cmd.output();
  if (!success) throw new Error(`deno bundle failed for ${name} (code ${code})`);
  console.log(`bundled ${name} -> ${dist.pathname}`);
}

for (const name of ACTIONS) {
  await bundleAction(name);
}
console.log("done");
