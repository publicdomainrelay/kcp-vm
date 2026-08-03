// Engine-level cache tests: seeding via request.context['cache'], transport
// through the GITHUB_CACHE command file (worker + subprocess), and the
// accumulated cache returned via PolicyEngineComplete.cache.

import { assertEquals } from "@std/assert";
import { fromFileUrl, join } from "@std/path";
import { WorkflowExecutor } from "./workflow.ts";

Deno.test("WorkflowExecutor: cache flows through net-only worker actions", async () => {
  const bundled = await Deno.makeTempDir({ prefix: "pe-bundled-" });
  const actDir = join(bundled, "test", "counter");
  await Deno.mkdir(actDir, { recursive: true });
  await Deno.writeTextFile(
    join(actDir, "action.yml"),
    `name: C\ndescription: t\ninputs:\n  step:\n    default: ""\nruns:\n  using: node20\n  main: index.ts\n`,
  );
  await Deno.writeTextFile(
    join(actDir, "index.ts"),
    `const path = Deno.env.get("GITHUB_CACHE") ?? "";
const c = JSON.parse(Deno.readTextFileSync(path));
const step = Deno.env.get("INPUT_STEP") ?? "";
c.meta = { count: ((c.meta?.count ?? 0) + 1), step };
Deno.writeTextFileSync(path, JSON.stringify(c));
`,
  );

  Deno.env.set("BUNDLED_ACTIONS_DIR", bundled);
  try {
    const executor = new WorkflowExecutor({ sandbox: { netOnly: true } });
    const status = await executor.executeWorkflow({
      context: { cache: { meta: { count: 0 } } },
      workflow: `name: t
on: push
jobs:
  j:
    runs-on: self-hosted
    steps:
    - id: a
      uses: test/counter@v1
      with:
        step: one
    - id: b
      uses: test/counter@v1
      with:
        step: two`,
    });
    const detail = status.detail as { exit_status: string; cache: Record<string, unknown> };
    assertEquals(detail.exit_status, "success");
    // Second step saw the first step's cache and bumped it again.
    assertEquals(detail.cache, { meta: { count: 2, step: "two" } });
  } finally {
    Deno.env.delete("BUNDLED_ACTIONS_DIR");
    await Deno.remove(bundled, { recursive: true });
  }
});

Deno.test("WorkflowExecutor: cache save/restore round-trips in full mode", async () => {
  const bundledUrl = fromFileUrl(new URL("../bundled-actions", import.meta.url));
  Deno.env.set("BUNDLED_ACTIONS_DIR", bundledUrl);
  try {
    const executor = new WorkflowExecutor({ sandbox: { netOnly: false } });
    const status = await executor.executeWorkflow({
      workflow: `name: t
on: push
jobs:
  j:
    runs-on: self-hosted
    steps:
    - run: mkdir -p dist && printf 'hello world' > dist/app.txt
    - uses: actions/cache/save@v5
      with:
        key: build-1
        path: dist
    - uses: actions/cache/restore@v5
      with:
        key: build-1
        path: dist
    - run: test "$(cat dist/app.txt)" = "hello world"`,
    });
    const detail = status.detail as { exit_status: string; cache?: Record<string, unknown> };
    assertEquals(detail.exit_status, "success");
    assertEquals(detail.cache?.["build-1"], {
      "dist/app.txt": { data: "hello world", encoding: "text" },
    });
  } finally {
    Deno.env.delete("BUNDLED_ACTIONS_DIR");
  }
});
