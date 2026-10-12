/**
 * gen-workflows.ts — build workflows.ts from the source-of-truth workflows/*.yml.
 *
 * The yml files are the canonical policy workflows; workflows.ts embeds them as
 * a name → YAML map so callers (bidder scope gate, seeder, requester minting)
 * can build a policy record without reading files. Keep them in sync by editing
 * the yml and re-running `deno task gen-workflows` (or `deno task build`).
 *
 * Run: deno run -A scripts/gen-workflows.ts
 */

const ROOT = new URL("../", import.meta.url); // lib/policies/gha-lite/

const HEADER = `/**
 * GENERATED FILE — do not edit by hand.
 *
 * Built from workflows/*.yml by scripts/gen-workflows.ts (deno task gen-workflows).
 *
 * Maps canonical policy name → its gha-lite workflow YAML, so callers
 * (bidder scope gate, seeder, requester minting) can build a
 * computer.socialweb.temp.policy.ghalite record without reading files.
 */
`;

/** Sort keys so the output is stable and diff-friendly. */
function sortKeys(entries: Array<[string, string]>): Array<[string, string]> {
  return [...entries].sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
}

async function main(): Promise<void> {
  const dir = new URL("./workflows/", ROOT);
  const files: string[] = [];
  for await (const entry of Deno.readDir(dir)) {
    if (entry.isFile && entry.name.endsWith(".yml")) files.push(entry.name);
  }

  const entries: Array<[string, string]> = [];
  for (const file of files) {
    const name = file.replace(/\.yml$/, "");
    const content = await Deno.readTextFile(new URL(`./workflows/${file}`, ROOT));
    entries.push([name, content]);
  }

  const body = sortKeys(entries)
    .map(([name, yaml]) => `  ${JSON.stringify(name)}: ${JSON.stringify(yaml)},`)
    .join("\n");

  const out = `${HEADER}export const WORKFLOWS: Record<string, string> = {\n${body}\n};\n`;
  await Deno.writeTextFile(new URL("./workflows.ts", ROOT), out);
  console.log(`gen-workflows: wrote workflows.ts (${entries.length} workflows)`);
}

if (import.meta.main) {
  await main();
}
