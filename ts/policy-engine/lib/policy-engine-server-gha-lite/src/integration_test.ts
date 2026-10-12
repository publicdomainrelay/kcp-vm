// Integration tests closing parity gaps with the original Python policy engine
// (../../../../johnandersen777/sshai/src/sshai/policy_engine.py), ignoring
// github-app reporting and celery. Covers: github-context expression eval,
// github.event.inputs, run-step outputs → steps context, GITHUB_ENV updates,
// if-condition semantics (always(), empty string), error abort, annotations,
// and the HTTP create→status→console_output lifecycle.
//
// NOTE: workflow strings use double-quoted JS literals, never backticks —
// ${{ ... }} would be swallowed by template-literal interpolation.

import { assertEquals, assertStringIncludes } from "@std/assert";
import { type PolicyEngineRequest, type PolicyEngineStatus } from "./models.ts";
import { createApp } from "./server.ts";
import { WorkflowExecutor } from "./workflow.ts";

function runWorkflow(
  lines: string[],
  request: Partial<PolicyEngineRequest> = {},
): Promise<PolicyEngineStatus> {
  const workflow = "name: t\non: push\njobs:\n  j:\n    runs-on: self-hosted\n    steps:\n" +
    lines.map((l) => "    " + l).join("\n") + "\n";
  return new WorkflowExecutor({ sandbox: { netOnly: false } }).executeWorkflow({
    inputs: {},
    context: {},
    ...request,
    workflow,
  });
}

Deno.test("integration: github.* expression context mirrors Python", async () => {
  // Mirrors Python test_evaluate_using_javascript / test_read_main step 1.
  const status = await runWorkflow(
    [
      "- id: greeting-step",
      "  env:",
      "    REPO_NAME: ${{ github.event.inputs.repo_name }}",
      "  run: echo hello=$REPO_NAME | tee -a $GITHUB_OUTPUT",
      "- run: echo \"actor=${{ github.actor }}\"",
      "- run: echo \"combo=${{ github.actor_id + ' ' + 'x' }}\"",
      "- run: echo \"repo=${{ github.event.inputs.repo_name }}\"",
      "- run: echo \"seen=${{ steps.greeting-step.outputs.hello }}\"",
    ],
    {
      inputs: { repo_name: "scitt-community/scitt-api-emulator" },
      context: {
        config: {
          env: { GITHUB_ACTOR: "aliceoa", GITHUB_ACTOR_ID: "1234567" },
        },
      },
    },
  );
  assertEquals((status.detail as { exit_status: string }).exit_status, "success");
  const out = status.console_output ?? "";
  assertStringIncludes(out, "actor=aliceoa");
  assertStringIncludes(out, "combo=1234567 x");
  assertStringIncludes(out, "repo=scitt-community/scitt-api-emulator");
  assertStringIncludes(out, "seen=scitt-community/scitt-api-emulator");
});

Deno.test("integration: run-step GITHUB_OUTPUT feeds steps context", async () => {
  const status = await runWorkflow([
    "- id: a",
    "  run: echo greeting=hi | tee -a $GITHUB_OUTPUT",
    "- run: test \"${{ steps.a.outputs.greeting }}\" = \"hi\"",
  ]);
  assertEquals((status.detail as { exit_status: string }).exit_status, "success");
});

Deno.test("integration: GITHUB_ENV updates flow to later steps", async () => {
  const status = await runWorkflow([
    "- run: echo \"MY_VAR=hello\" >> $GITHUB_ENV",
    "- run: echo \"got=$MY_VAR\"",
  ]);
  assertEquals((status.detail as { exit_status: string }).exit_status, "success");
  assertStringIncludes(status.console_output ?? "", "got=hello");
});

Deno.test("integration: if always() runs; empty if is skipped", async () => {
  const status = await runWorkflow([
    "- if: always()",
    "  run: echo always-ran",
    "- if: ''",
    "  run: echo empty-should-skip",
  ]);
  assertEquals((status.detail as { exit_status: string }).exit_status, "success");
  const out = status.console_output ?? "";
  assertStringIncludes(out, "always-ran");
  // Empty if-condition is treated as false (skipped), matching Python.
  if (out.includes("empty-should-skip")) {
    throw new Error("expected empty if-condition step to be skipped");
  }
});

Deno.test("integration: first step error aborts the job", async () => {
  const status = await runWorkflow([
    "- run: exit 1",
    "- run: echo after-error",
  ]);
  const detail = status.detail as { exit_status: string; annotations?: { error?: string[] } };
  assertEquals(detail.exit_status, "failure");
  const out = status.console_output ?? "";
  if (out.includes("after-error")) {
    throw new Error("expected later steps not to run after a step failure");
  }
});

Deno.test("integration: workflow command annotations are collected", async () => {
  const status = await runWorkflow([
    "- run: echo \"::error file=app.js,line=1::Missing semicolon\"",
  ]);
  assertEquals((status.detail as { exit_status: string }).exit_status, "success");
  const detail = status.detail as { annotations?: { error?: Array<Record<string, unknown>> } };
  const errors = detail.annotations?.error ?? [];
  assertEquals(errors.length, 1);
  assertEquals(errors[0]["message"], "Missing semicolon");
  assertEquals(errors[0]["path"], "app.js");
  assertEquals(errors[0]["start_line"], 1);
});

Deno.test("integration: HTTP lifecycle create → status → console_output", async () => {
  const { app } = createApp({ netOnly: false });
  const req: PolicyEngineRequest = {
    workflow: "name: t\non: push\njobs:\n  j:\n    runs-on: self-hosted\n    steps:\n    - run: echo \"Hello World\"",
  };

  const createRes = await app.request("/request/create", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(req),
  });
  assertEquals(createRes.status, 200);
  const submitted = await createRes.json() as { status: string; detail: { id: string } };
  assertEquals(submitted.status, "submitted");
  const id = submitted.detail.id;

  let status: PolicyEngineStatus;
  for (let i = 0; i < 500; i++) {
    const res = await app.request(`/request/status/${id}`);
    status = await res.json() as PolicyEngineStatus;
    if (status.status === "complete") break;
    await new Promise((r) => setTimeout(r, 20));
  }
  assertEquals(status!.status, "complete");
  assertEquals((status!.detail as { exit_status: string }).exit_status, "success");
  assertStringIncludes(status!.console_output ?? "", "Hello World");

  const consoleRes = await app.request(`/request/console_output/${id}`);
  assertStringIncludes(await consoleRes.text(), "Hello World");
});
