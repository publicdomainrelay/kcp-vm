import { assertEquals } from "@std/assert";

const REPO_ROOT = new URL("..", import.meta.url).pathname;

Deno.test("[cli-smoke] hono-sandbox --help", async () => {
  const cmd = new Deno.Command("deno", {
    args: ["run", "-A", "hono-sandbox/mod.ts", "--help"],
    cwd: REPO_ROOT,
    stdout: "piped",
    stderr: "piped",
  });
  const { code } = await cmd.output();
  assertEquals(code, 0);
});
