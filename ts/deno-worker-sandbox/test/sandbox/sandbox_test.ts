import { assertEquals } from "@std/assert";
import type { Sandbox } from "@publicdomainrelay/sandbox-abc";
import { createDenoSandbox } from "@publicdomainrelay/sandbox-deno";

Deno.test("Sandbox executes sync code and returns result", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: "return 42;",
  });

  assertEquals(result.exitCode, 0);
  assertEquals(result.result, 42);
  assertEquals(result.timedOut, false);

  await sandbox.shutdown();
});

Deno.test("Sandbox captures console.log output", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: 'console.log("hello world"); return 1;',
  });

  assertEquals(result.exitCode, 0);
  assertEquals(result.result, 1);
  assertEquals(result.stdout.includes("hello world"), true);

  await sandbox.shutdown();
});

Deno.test("Sandbox captures console.error output", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: 'console.error("oops");',
  });

  assertEquals(result.exitCode, 0);
  assertEquals(result.stderr.includes("oops"), true);

  await sandbox.shutdown();
});

Deno.test("Sandbox handles async code", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: 'return await Promise.resolve("done");',
  });

  assertEquals(result.exitCode, 0);
  assertEquals(result.result, "done");

  await sandbox.shutdown();
});

Deno.test("Sandbox timeout kills execution", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: `
      while (true) {}
    `,
    timeoutMs: 50,
  });

  assertEquals(result.timedOut, true);
  assertEquals(result.exitCode, 1);

  await sandbox.shutdown();
});

Deno.test("Sandbox handles thrown errors", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: 'throw new Error("bang");',
  });

  assertEquals(result.exitCode, 1);
  assertEquals(result.stderr.includes("bang"), true);

  await sandbox.shutdown();
});

Deno.test("Sandbox returns stderr on syntax error", async () => {
  const sandbox: Sandbox = createDenoSandbox();

  const result = await sandbox.execute({
    code: "}{ invalid syntax",
  });

  assertEquals(result.exitCode, 1);
  assertEquals(result.stderr.length > 0, true);

  await sandbox.shutdown();
});
