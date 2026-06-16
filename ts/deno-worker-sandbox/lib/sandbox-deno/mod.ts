import type { Sandbox, SandboxRequest, SandboxResponse, SandboxPermissions } from "@publicdomainrelay/sandbox-abc";

export function createDenoSandbox(permissions?: SandboxPermissions): Sandbox {
  const workerUrl = new URL("./worker.ts", import.meta.url);

  const denoPerms: Record<string, unknown> = {};
  if (permissions?.net) denoPerms.net = permissions.net;
  if (permissions?.read) denoPerms.read = permissions.read;
  if (permissions?.write) denoPerms.write = permissions.write;
  if (permissions?.env) denoPerms.env = permissions.env;
  if (permissions?.run) denoPerms.run = permissions.run;

  const worker = new Worker(workerUrl, {
    type: "module",
    deno: {
      permissions: denoPerms,
    },
  });

  let nextId = 1;
  const pending = new Map<number, (res: SandboxResponse) => void>();

  worker.onmessage = (ev: MessageEvent) => {
    const m = ev.data as Record<string, unknown>;
    if (m.type === "result") {
      const id = m.id as number;
      const resolve = pending.get(id);
      pending.delete(id);
      if (resolve) {
        resolve({
          stdout: (m.stdout as string) ?? "",
          stderr: (m.stderr as string) ?? "",
          exitCode: (m.exitCode as number) ?? 1,
          timedOut: (m.timedOut as boolean) ?? false,
          result: m.result,
        });
      }
    }
  };

  worker.onerror = (err) => {
    for (const [id, resolve] of pending) {
      resolve({
        stdout: "",
        stderr: `worker error: ${err.message}`,
        exitCode: 1,
        timedOut: false,
      });
      pending.delete(id);
    }
  };

  return {
    async execute(request: SandboxRequest): Promise<SandboxResponse> {
      const id = nextId++;
      worker.postMessage({
        type: "execute",
        id,
        code: request.code,
        timeoutMs: request.timeoutMs ?? 0,
      });

      return new Promise((resolve) => {
        pending.set(id, resolve);
      });
    },

    async shutdown(): Promise<void> {
      worker.postMessage({ type: "shutdown" });
      await new Promise((resolve) => setTimeout(resolve, 200));
      worker.terminate();
    },
  };
}
