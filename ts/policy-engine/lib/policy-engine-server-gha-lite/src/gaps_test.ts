// Integration tests for the GitHub-Actions-parity gap-closing work: hashFiles,
// failure()/success()/always() error continuation, secrets/vars contexts, the
// standard expression functions, needs + job outputs + fromJSON, matrix,
// job-level if/env/timeout, working-directory, continue-on-error, GITHUB_PATH,
// CI/GITHUB_ACTIONS/GITHUB_EVENT_PATH, shell validation, and string-bool
// output comparison. Workflow strings use double-quoted JS literals (never
// backticks) so ${{ ... }} survives.

import { assertEquals, assertMatch, assertStringIncludes } from "@std/assert";
import { join } from "@std/path";
import { type PolicyEngineRequest, type PolicyEngineStatus } from "./models.ts";
import { WorkflowExecutor } from "./workflow.ts";

function runWorkflow(
  workflow: string,
  request: Partial<PolicyEngineRequest> = {},
): Promise<PolicyEngineStatus> {
  return new WorkflowExecutor({ sandbox: { netOnly: false } }).executeWorkflow({
    inputs: {},
    context: {},
    ...request,
    workflow,
  });
}

function singleJob(steps: string[]): string {
  return "name: t\non: push\njobs:\n  j:\n    runs-on: self-hosted\n    steps:\n" +
    steps.map((s) => "    " + s).join("\n") + "\n";
}

const status = (s: PolicyEngineStatus): { exit_status: string; annotations?: Record<string, unknown> } =>
  s.detail as { exit_status: string; annotations?: Record<string, unknown> };

Deno.test("gap: hashFiles() resolves against the workspace", async () => {
  const res = await runWorkflow(singleJob([
    "- run: mkdir -p pkg && printf 'lock' > pkg/a.lock",
    "- run: echo \"h=${{ hashFiles('pkg/*.lock') }}\"",
  ]));
  assertEquals(status(res).exit_status, "success");
  assertMatch(res.console_output ?? "", /h=[0-9a-f]{64}/);
});

Deno.test("gap: failure()/always() run after an error; success() skipped", async () => {
  const res = await runWorkflow(singleJob([
    "- run: exit 1",
    "- if: always()",
    "  run: echo cleanup-ran",
    "- if: failure()",
    "  run: echo failure-ran",
    "- if: success()",
    "  run: echo should-not",
  ]));
  assertEquals(status(res).exit_status, "failure");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "cleanup-ran");
  assertStringIncludes(out, "failure-ran");
  if (out.includes("should-not")) throw new Error("success() step should be skipped after failure");
});

Deno.test("gap: secrets and vars contexts", async () => {
  const res = await runWorkflow(
    singleJob(["- run: echo \"sec=${{ secrets.MY_SECRET }} var=${{ vars.PROJECT }}\""]),
    { context: { secrets: { MY_SECRET: "s3cr3t" }, vars: { PROJECT: "acme" } } },
  );
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "sec=s3cr3t var=acme");
});

Deno.test("gap: expression functions contains/startsWith/endsWith/format/join/fromJSON/toJSON", async () => {
  const res = await runWorkflow(singleJob([
    "- run: echo \"c=${{ contains('hello','ell') }} sw=${{ startsWith('hello','he') }} ew=${{ endsWith('hello','lo') }} f=${{ format('{0}-{1}','a','b') }} j=${{ join(fromJSON('[\"x\",\"y\"]'),'-') }} t=${{ toJSON(1) }}\"",
  ]));
  assertEquals(status(res).exit_status, "success");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "c=true sw=true ew=true f=a-b j=x-y t=1");
});

Deno.test("gap: needs + job outputs + fromJSON", async () => {
  const workflow =
    "name: t\non: push\njobs:\n" +
    "  gen:\n    runs-on: self-hosted\n    steps:\n" +
    "    - id: s\n      run: echo 'payload={\"a\":1}' | tee -a $GITHUB_OUTPUT\n" +
    "    outputs:\n      meta: ${{ steps.s.outputs.payload }}\n" +
    "  use:\n    runs-on: self-hosted\n    needs: gen\n    steps:\n" +
    "    - run: echo \"val=${{ fromJSON(needs.gen.outputs.meta).a }}\"";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "val=1");
});

Deno.test("gap: strategy.matrix expands jobs and exposes matrix context", async () => {
  const workflow =
    "name: t\non: push\njobs:\n" +
    "  build:\n    runs-on: self-hosted\n    strategy:\n      matrix:\n        version: [10, 11]\n    steps:\n" +
    "    - run: echo \"ver=${{ matrix.version }}\"";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "success");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "ver=10");
  assertStringIncludes(out, "ver=11");
});

Deno.test("gap: job-level if skips the job", async () => {
  const workflow =
    "name: t\non: push\njobs:\n" +
    "  a:\n    runs-on: self-hosted\n    if: ${{ false }}\n    steps:\n    - run: echo should-not-run\n" +
    "  b:\n    runs-on: self-hosted\n    steps:\n    - run: echo b-ran";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "success");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "b-ran");
  if (out.includes("should-not-run")) throw new Error("job with if:false should be skipped");
});

Deno.test("gap: job env is scoped to the job", async () => {
  const workflow =
    "name: t\non: push\njobs:\n" +
    "  j:\n    runs-on: self-hosted\n    env:\n      MY: hello\n    steps:\n    - run: echo \"my=$MY\"";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "my=hello");
});

Deno.test("gap: timeout-minutes fails an overlong job", async () => {
  const workflow =
    "name: t\non: push\njobs:\n" +
    "  j:\n    runs-on: self-hosted\n    timeout-minutes: 0\n    steps:\n    - run: echo hi";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "failure");
});

Deno.test("gap: working-directory changes the step cwd", async () => {
  const res = await runWorkflow(singleJob([
    "- run: mkdir -p sub",
    "- working-directory: sub",
    "  run: echo hello > out.txt",
    "- run: test -f sub/out.txt && echo wd-ok",
  ]));
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "wd-ok");
});

Deno.test("gap: continue-on-error keeps the job green", async () => {
  const res = await runWorkflow(singleJob([
    "- continue-on-error: true",
    "  run: exit 1",
    "- run: echo survived",
  ]));
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "survived");
});

Deno.test("gap: GITHUB_PATH prepends to subsequent step PATH", async () => {
  const res = await runWorkflow(singleJob([
    "- run: echo \"$PWD/.bin\" >> $GITHUB_PATH",
    "- run: echo \"path=$PATH\"",
  ]));
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "/.bin");
});

Deno.test("gap: CI, GITHUB_ACTIONS and GITHUB_EVENT_PATH are set", async () => {
  const res = await runWorkflow(singleJob([
    "- run: echo \"ci=$CI actions=$GITHUB_ACTIONS\"",
    "- run: test -f \"$GITHUB_EVENT_PATH\" && echo event-ok",
  ]));
  assertEquals(status(res).exit_status, "success");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "ci=true actions=true");
  assertStringIncludes(out, "event-ok");
});

Deno.test("gap: unsupported shell fails clearly", async () => {
  const res = await runWorkflow(singleJob([
    "- shell: pwsh -command {0}",
    "  run: Write-Output hi",
  ]));
  assertEquals(status(res).exit_status, "failure");
  const annotations = status(res).annotations as { error?: string[] };
  assertStringIncludes(annotations.error?.[0] ?? "", "not supported");
});

Deno.test("gap: string 'true' output compares with == 'true'", async () => {
  const res = await runWorkflow(singleJob([
    "- id: a",
    "  run: echo flag=true | tee -a $GITHUB_OUTPUT",
    "- if: ${{ steps.a.outputs.flag == 'true' }}",
    "  run: echo bool-ok",
  ]));
  assertEquals(status(res).exit_status, "success");
  assertStringIncludes(res.console_output ?? "", "bool-ok");
});

Deno.test("gap: net-only actions read GITHUB_EVENT_PATH via the virtual FS", async () => {
  const bundled = await Deno.makeTempDir({ prefix: "pe-bundled-" });
  const actDir = join(bundled, "test", "ev");
  await Deno.mkdir(actDir, { recursive: true });
  await Deno.writeTextFile(
    join(actDir, "action.yml"),
    "name: E\ndescription: t\nruns:\n  using: node20\n  main: index.ts\n",
  );
  await Deno.writeTextFile(
    join(actDir, "index.ts"),
    `const ep = Deno.env.get("GITHUB_EVENT_PATH") ?? "";
const data = JSON.parse(Deno.readTextFileSync(ep));
console.log("ev:" + data.inputs.repo);
`,
  );
  Deno.env.set("BUNDLED_ACTIONS_DIR", bundled);
  try {
    const res = await new WorkflowExecutor({ sandbox: { netOnly: true } }).executeWorkflow({
      inputs: { repo: "x/y" },
      workflow:
        "name: t\non: push\njobs:\n  j:\n    runs-on: self-hosted\n    steps:\n    - uses: test/ev@v1",
    });
    assertEquals(status(res).exit_status, "success");
    assertStringIncludes(res.console_output ?? "", "ev:x/y");
  } finally {
    Deno.env.delete("BUNDLED_ACTIONS_DIR");
    await Deno.remove(bundled, { recursive: true });
  }
});

Deno.test("gap: concurrency groups are accepted and jobs serialize within a group", async () => {
  const workflow =
    "name: t\non: push\nconcurrency:\n  group: ci-main\n  cancel-in-progress: true\njobs:\n" +
    "  a:\n    runs-on: self-hosted\n    concurrency: build\n    steps:\n    - run: echo a-ran\n" +
    "  b:\n    runs-on: self-hosted\n    concurrency: build\n    steps:\n    - run: echo b-ran";
  const res = await runWorkflow(workflow);
  assertEquals(status(res).exit_status, "success");
  const out = res.console_output ?? "";
  assertStringIncludes(out, "a-ran");
  assertStringIncludes(out, "b-ran");
});
