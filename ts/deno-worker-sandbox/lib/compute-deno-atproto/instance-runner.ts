import type { WorkerInstanceRunner } from "@publicdomainrelay/compute-deno-abc";
import type { WorkerRequest, WorkerResponse, StrongRef } from "@publicdomainrelay/compute-deno-common";
import { DenoComputeError } from "@publicdomainrelay/compute-deno-common";
import type { PersistentWorker, Bundler } from "@publicdomainrelay/sandbox-abc";
import { createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";
import type { WorkerManifestStore, WorkerInstanceStore } from "@publicdomainrelay/compute-deno-abc";

export interface RunnerOptions {
  manifestStore: WorkerManifestStore;
  instanceStore: WorkerInstanceStore;
  bundler: Bundler;
  timeoutMs?: number;
}

export function createDenoComputeInstanceRunner(opts: RunnerOptions): WorkerInstanceRunner {
  const workers = new Map<string, PersistentWorker>();

  return {
    async start(instanceRef: StrongRef, manifestRef: StrongRef): Promise<void> {
      const manifest = await opts.manifestStore.get(manifestRef.uri);
      if (!manifest) throw new DenoComputeError("Manifest not found", 404, "ManifestNotFound");

      const workerUrl = `data:application/javascript;base64,${btoa(manifest.bundle)}`;

      let worker: PersistentWorker;
      try {
        worker = createPersistentDenoWorker(workerUrl);
      } catch (err) {
        throw new DenoComputeError(
          `Worker start failed: ${String(err)}`,
          500,
          "WorkerStartFailed",
        );
      }

      workers.set(instanceRef.uri, worker);
    },

    async execute(
      instanceRef: StrongRef,
      request: WorkerRequest,
    ): Promise<WorkerResponse> {
      const worker = workers.get(instanceRef.uri);
      if (!worker) {
        throw new DenoComputeError("Instance not running", 404, "InstanceNotRunning");
      }

      const timeoutMs = opts.timeoutMs ?? 30_000;

      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          reject(new DenoComputeError("Worker timeout", 504, "WorkerTimeout"));
        }, timeoutMs);

        worker.onMessage((msg: unknown) => {
          clearTimeout(timer);
          const m = msg as Record<string, unknown>;
          if (m.type === "error") {
            reject(new DenoComputeError(
              `Worker error: ${m.message as string}`,
              500,
              "WorkerError",
            ));
            return;
          }
          resolve({
            status: (m.status as number) ?? 200,
            headers: (m.headers as Record<string, string>) ?? {},
            body: m.body,
          });
        });

        worker.postMessage({ type: "request", ...request });
      });
    },

    async stop(instanceRef: StrongRef): Promise<void> {
      const worker = workers.get(instanceRef.uri);
      if (!worker) return;
      workers.delete(instanceRef.uri);
      await worker.shutdown();
    },

    async stopAll(): Promise<void> {
      const promises = Array.from(workers.entries()).map(([_uri, w]) => w.shutdown());
      await Promise.allSettled(promises);
      workers.clear();
    },

    isRunning(instanceRef: StrongRef): boolean {
      return workers.has(instanceRef.uri);
    },
  };
}
