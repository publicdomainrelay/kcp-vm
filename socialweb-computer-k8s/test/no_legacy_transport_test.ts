import { assertEquals } from "@std/assert";

const REPO = new URL("../../", import.meta.url).pathname;
const SELF = new URL(import.meta.url).pathname;
const SCANNED = [
  "socialweb-computer-k8s/lib",
  "socialweb-computer-k8s/hono-socialweb-computer-k8s",
  "socialweb-computer-k8s/scripts",
  "socialweb-computer-k8s/test",
  "deploy",
  "scripts",
  "user_data.yaml",
];
const SCANNED_EXTENSIONS = [".ts", ".json", ".sh", ".yaml", ".yml"];
const FORBIDDEN: RegExp[] = [
  /websocat/i,
  /tunnelWsUrl|sshTunnelArgs|runSessionOverTunnel/,
  /com\.fedproxy\.temp\.xrpc\.tunnel/,
  /tunnel-subscriber/,
  /fedproxy-client/,
  /wootty/i,
  /JSR_URL/,
  /user-data-transport|fedproxy-host|fedingressHost/,
  /relay-fqdn/,
  /transport\s*:\s*["'`](?!iroh["'`])/,
];

async function scannedFiles(): Promise<string[]> {
  const files: string[] = [];
  const stack = SCANNED.map((top) => `${REPO}${top}`);
  while (stack.length) {
    const path = stack.pop()!;
    const stat = await Deno.stat(path).catch(() => undefined);
    if (!stat) continue;
    if (stat.isFile) {
      files.push(path);
      continue;
    }
    for await (const entry of Deno.readDir(path)) {
      const child = `${path}/${entry.name}`;
      if (entry.name === "node_modules") continue;
      if (entry.isDirectory) stack.push(child);
      else if (SCANNED_EXTENSIONS.some((ext) => entry.name.endsWith(ext)) && child !== SELF) files.push(child);
    }
  }
  return files;
}

Deno.test("no source, script or manifest of socialweb-computer-kcp reaches a guest other than over iroh", async () => {
  const offenders: string[] = [];
  for (const path of await scannedFiles()) {
    const text = await Deno.readTextFile(path);
    for (const re of FORBIDDEN) if (re.test(text)) offenders.push(`${path.slice(REPO.length)} ${re}`);
  }
  assertEquals(offenders, []);
});
