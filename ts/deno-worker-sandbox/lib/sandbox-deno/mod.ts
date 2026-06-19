import type {
  Sandbox,
  SandboxPermissions,
  SandboxRequest,
  SandboxResponse,
} from "@publicdomainrelay/sandbox-abc";

function buildWorkerCode(code: string): string {
  return [
    'const captured = { stdout: "", stderr: "" };',
    "const _origLog = console.log;",
    "const _origErr = console.error;",
    'console.log = (...args) => { captured.stdout += args.map(String).join(" ") + "\\n"; };',
    'console.error = (...args) => { captured.stderr += args.map(String).join(" ") + "\\n"; };',
    "(async () => {",
    "  try {",
    `    const fn = new Function("return (async () => { " + ${JSON.stringify(code)} + " })()");`,
    "    const result = await fn();",
    '    postMessage({ type: "result", stdout: captured.stdout, stderr: captured.stderr, exitCode: 0, result });',
    "  } catch (err) {",
    '    const errMsg = err instanceof Error ? err.message : String(err);',
    '    postMessage({ type: "result", stdout: captured.stdout, stderr: captured.stderr + errMsg, exitCode: 1 });',
    "  } finally {",
    "    console.log = _origLog;",
    "    console.error = _origErr;",
    "    self.close();",
    "  }",
    "})();",
  ].join("\n");
}

export function createDenoSandbox(permissions?: SandboxPermissions): Sandbox {
  const denoPerms: Record<string, unknown> = {};
  if (permissions?.net) denoPerms.net = permissions.net;
  if (permissions?.read) denoPerms.read = permissions.read;
  if (permissions?.write) denoPerms.write = permissions.write;
  if (permissions?.env) denoPerms.env = permissions.env;
  if (permissions?.run) denoPerms.run = permissions.run;

  let dead = false;

  return {
    async execute(request: SandboxRequest): Promise<SandboxResponse> {
      if (dead) {
        return { stdout: "", stderr: "sandbox shut down", exitCode: 1, timedOut: false };
      }

      const workerCode = buildWorkerCode(request.code);
      const workerUrl = `data:application/javascript;base64,${btoa(workerCode)}`;

      const worker = new Worker(workerUrl, {
        type: "module",
        deno: { permissions: denoPerms },
      });

      return new Promise((resolve) => {
        let timer: ReturnType<typeof setTimeout> | undefined;
        let settled = false;

        worker.onmessage = (ev: MessageEvent) => {
          if (settled) return;
          settled = true;
          if (timer !== undefined) clearTimeout(timer);
          const m = ev.data as Record<string, unknown>;
          resolve({
            stdout: (m.stdout as string) ?? "",
            stderr: (m.stderr as string) ?? "",
            exitCode: (m.exitCode as number) ?? 1,
            timedOut: false,
            result: m.result,
          });
          try { worker.terminate(); } catch { /* already dead */ }
        };

        worker.onerror = (err) => {
          if (settled) return;
          settled = true;
          if (timer !== undefined) clearTimeout(timer);
          resolve({
            stdout: "",
            stderr: `worker error: ${err.message}`,
            exitCode: 1,
            timedOut: false,
          });
          try { worker.terminate(); } catch { /* already dead */ }
        };

        if (request.timeoutMs && request.timeoutMs > 0) {
          timer = setTimeout(() => {
            if (settled) return;
            settled = true;
            try { worker.terminate(); } catch { /* already dead */ }
            resolve({
              stdout: "",
              stderr: "execution timed out",
              exitCode: 1,
              timedOut: true,
            });
          }, request.timeoutMs);
        }
      });
    },

    async shutdown(): Promise<void> {
      dead = true;
    },
  };
}

export { createDenoBundler } from "./bundler.ts";
export { createPersistentDenoWorker } from "./persistent-worker.ts";
