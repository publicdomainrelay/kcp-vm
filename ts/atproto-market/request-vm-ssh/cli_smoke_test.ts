import { assertEquals } from "@std/assert";

// Compute the atproto-market repo root from this test file's location.
// test file: <repo>/request-vm-ssh/cli_smoke_test.ts
// repo root: <repo>/
const thisDir = new URL(".", import.meta.url).pathname;
const repoDir = thisDir.replace(/\/request-vm-ssh\/?$/, "");

Deno.test("[cli-smoke] request-vm-ssh --help", async () => {
  const cmd = new Deno.Command("deno", {
    args: ["run", "-A", "request-vm-ssh/mod.ts", "--help"],
    cwd: repoDir,
    stdout: "piped",
    stderr: "piped",
  });
  const { code, stdout, stderr } = await cmd.output();
  const outStr = new TextDecoder().decode(stdout);
  const errStr = new TextDecoder().decode(stderr);
  if (code !== 0) {
    console.error("STDOUT:", outStr.slice(0, 500));
    console.error("STDERR:", errStr.slice(0, 500));
  }
  assertEquals(code, 0);
  if (!outStr.includes("request-vm-ssh")) {
    throw new Error(`Expected help text to mention "request-vm-ssh", got: ${outStr.slice(0, 200)}`);
  }
});
