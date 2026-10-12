import { assertEquals } from "@std/assert";

const ORG_ROOT = new URL("../../", import.meta.url).pathname;
const SCANNED_REPOS = ["atproto-market", "typescript-helpers", "socialweb-computer-kcp"];
const ADAPTER = "typescript-helpers/lib/market-network-fetch/";
const KCP_SERVICE_NAME_DNS_SHIM = [
  "socialweb-computer-kcp/internal/kcplibs/impl/assets/dnsshim.ts",
  "socialweb-computer-kcp/internal/denokcp/provider/runs/.kcpdns/shim.ts",
];
const SKIPPED_DIRS = new Set(["node_modules", ".git", "vendor", ".claude"]);
const FETCH_PATCH = /globalThis\.fetch\s*=(?!=)/;

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch {
    return false;
  }
}

async function tsFiles(repo: string): Promise<string[]> {
  const files: string[] = [];
  const stack = [`${ORG_ROOT}${repo}`];
  while (stack.length) {
    const dir = stack.pop()!;
    for await (const entry of Deno.readDir(dir)) {
      const path = `${dir}/${entry.name}`;
      if (entry.isDirectory) {
        if (!SKIPPED_DIRS.has(entry.name)) stack.push(path);
      } else if (entry.isFile && entry.name.endsWith(".ts")) {
        files.push(path.slice(ORG_ROOT.length));
      }
    }
  }
  return files;
}

Deno.test("no fetch patch is left outside the market-network-fetch adapter", async () => {
  const offenders: string[] = [];
  for (const repo of SCANNED_REPOS) {
    if (!(await exists(`${ORG_ROOT}${repo}`))) continue;
    for (const rel of await tsFiles(repo)) {
      if (rel.startsWith(ADAPTER) || KCP_SERVICE_NAME_DNS_SHIM.includes(rel)) continue;
      const lines = (await Deno.readTextFile(`${ORG_ROOT}${rel}`)).split("\n");
      lines.forEach((line, i) => {
        if (FETCH_PATCH.test(line)) offenders.push(`${rel}:${i + 1}`);
      });
    }
  }
  assertEquals(offenders, []);
});
