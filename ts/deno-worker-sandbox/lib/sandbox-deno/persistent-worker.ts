import type { PersistentWorker } from "@publicdomainrelay/sandbox-abc";
import type { SandboxPermissions } from "@publicdomainrelay/sandbox-common";

export function createPersistentDenoWorker(
  workerUrl: string | URL,
  permissions?: SandboxPermissions,
): PersistentWorker {
  const denoPerms: Record<string, unknown> = {};
  if (permissions) {
    for (const [key, val] of Object.entries(permissions)) {
      if (val !== undefined) denoPerms[key] = val;
    }
  }

  const worker = new Worker(workerUrl, {
    type: "module",
    deno: { permissions: denoPerms },
  });

  let dead = false;

  return {
    postMessage(message: unknown): void {
      if (dead) return;
      worker.postMessage(message);
    },

    onMessage(handler: (message: unknown) => void): void {
      worker.onmessage = (ev: MessageEvent) => {
        handler(ev.data);
      };
      worker.onerror = (err) => {
        handler({ type: "error", message: err.message });
      };
    },

    async shutdown(): Promise<void> {
      dead = true;
      try {
        worker.terminate();
      } catch {
             }
    },
  };
}
