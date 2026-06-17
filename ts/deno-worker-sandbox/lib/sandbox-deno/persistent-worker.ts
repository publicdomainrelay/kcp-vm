import type { PersistentWorker, SandboxPermissions } from "jsr:@publicdomainrelay/sandbox-abc@0.0.0";

export function createPersistentDenoWorker(
  workerUrl: string | URL,
  permissions?: SandboxPermissions,
): PersistentWorker {
  const denoPerms: Record<string, unknown> = {};
  if (permissions?.net) denoPerms.net = permissions.net;
  if (permissions?.read) denoPerms.read = permissions.read;
  if (permissions?.write) denoPerms.write = permissions.write;
  if (permissions?.env) denoPerms.env = permissions.env;
  if (permissions?.run) denoPerms.run = permissions.run;

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
        /* already dead */
      }
    },
  };
}
