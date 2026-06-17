import type {
  Sandbox,
  SandboxPermissions,
  SandboxRequest,
  SandboxResponse,
} from "jsr:@publicdomainrelay/sandbox-abc@0.0.0";

export function createDenoSandbox(permissions?: SandboxPermissions): Sandbox {
  const workerUrl = new URL("./worker.ts", import.meta.url);

  const denoPerms: Record<string, unknown> = {};
  if (permissions?.net) denoPerms.net = permissions.net;
  if (permissions?.read) denoPerms.read = permissions.read;
  if (permissions?.write) denoPerms.write = permissions.write;
  if (permissions?.env) denoPerms.env = permissions.env;
  if (permissions?.run) denoPerms.run = permissions.run;

  let worker = createWorker(workerUrl, denoPerms);
  let nextId = 1;
  const pending = new Map<
    number,
    { resolve: (res: SandboxResponse) => void; timer?: ReturnType<typeof setTimeout> }
  >();
  let dead = false;

  function createWorker(url: URL, perms: Record<string, unknown>): Worker {
    const w = new Worker(url, {
      type: "module",
      deno: { permissions: perms },
    });

    w.onmessage = (ev: MessageEvent) => {
      const m = ev.data as Record<string, unknown>;
      if (m.type === "result") {
        const id = m.id as number;
        const entry = pending.get(id);
        pending.delete(id);
        if (entry) {
          if (entry.timer !== undefined) clearTimeout(entry.timer);
          entry.resolve({
            stdout: (m.stdout as string) ?? "",
            stderr: (m.stderr as string) ?? "",
            exitCode: (m.exitCode as number) ?? 1,
            timedOut: false,
            result: m.result,
          });
        }
      }
    };

    w.onerror = (err) => {
      for (const [id, entry] of pending) {
        if (entry.timer !== undefined) clearTimeout(entry.timer);
        entry.resolve({
          stdout: "",
          stderr: `worker error: ${err.message}`,
          exitCode: 1,
          timedOut: false,
        });
        pending.delete(id);
      }
    };

    return w;
  }

  function killWorker() {
    try {
      worker.terminate();
    } catch { /* already dead */ }
  }

  return {
    async execute(request: SandboxRequest): Promise<SandboxResponse> {
      if (dead) {
        return { stdout: "", stderr: "sandbox shut down", exitCode: 1, timedOut: false };
      }

      const id = nextId++;
      worker.postMessage({
        type: "execute",
        id,
        code: request.code,
      });

      return new Promise((resolve) => {
        const entry: {
          resolve: (res: SandboxResponse) => void;
          timer?: ReturnType<typeof setTimeout>;
        } = { resolve };

        if (request.timeoutMs && request.timeoutMs > 0) {
          entry.timer = setTimeout(() => {
            pending.delete(id);
            killWorker();
            worker = createWorker(workerUrl, denoPerms);

            for (const [otherId, otherEntry] of pending) {
              if (otherEntry.timer !== undefined) clearTimeout(otherEntry.timer);
              otherEntry.resolve({
                stdout: "",
                stderr: "worker terminated due to timeout on request " + id,
                exitCode: 1,
                timedOut: true,
              });
              pending.delete(otherId);
            }

            resolve({
              stdout: "",
              stderr: "execution timed out",
              exitCode: 1,
              timedOut: true,
            });
          }, request.timeoutMs);
        }

        pending.set(id, entry);
      });
    },

    async shutdown(): Promise<void> {
      dead = true;
      for (const [id, entry] of pending) {
        if (entry.timer !== undefined) clearTimeout(entry.timer);
        entry.resolve({ stdout: "", stderr: "shutdown", exitCode: 1, timedOut: false });
        pending.delete(id);
      }
      killWorker();
    },
  };
}

export { createDenoBundler } from "./bundler.ts";
