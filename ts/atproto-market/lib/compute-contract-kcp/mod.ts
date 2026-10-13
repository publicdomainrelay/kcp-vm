import type { ComputeContract, ComputeContractStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { assertComputeContractStatus, COMPUTE_CONTRACT_RESOURCE, parseComputeContract } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import { createKcpRestClient } from "@publicdomainrelay/kcp-client-rest";

export interface ComputeContractKcpOptions {
  kcp?: KcpClient;
  server: string;
  workspace?: string;
  name: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
  pollMs?: number;
}

export interface ComputeContractStore {
  readonly name: string;
  get(): Promise<ComputeContract | null>;
  waitForSpec(onWaiting?: (reason: string) => void): Promise<ComputeContract>;
  waitFor(predicate: (o: ComputeContract) => boolean, signal?: AbortSignal): Promise<ComputeContract | null>;
  writeStatus(status: ComputeContractStatus): Promise<void>;
}

const DEFAULT_POLL_MS = 2_000;

export function createComputeContractKcpStore(opts: ComputeContractKcpOptions): ComputeContractStore {
  const kcp = opts.kcp ??
    createKcpRestClient(
      { server: opts.server, token: opts.token, tokenFile: opts.tokenFile, caCert: opts.caCert },
      { fetch: opts.fetch, timeoutMs: opts.timeoutMs },
    );
  const contracts = kcpResourceClient(kcp, { resource: COMPUTE_CONTRACT_RESOURCE, workspace: opts.workspace }, parseComputeContract);
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS;
  const get = () => contracts.get(opts.name);

  const sleep = (ms: number, signal?: AbortSignal) =>
    new Promise<void>((resolve) => {
      const timer = setTimeout(resolve, ms);
      signal?.addEventListener("abort", () => {
        clearTimeout(timer);
        resolve();
      }, { once: true });
    });

  return {
    name: opts.name,
    get,
    async waitForSpec(onWaiting) {
      for (;;) {
        let reason: string;
        try {
          const o = await get();
          if (o?.spec) return o;
          reason = o ? "the object has no spec" : "no such object";
        } catch (err) {
          reason = err instanceof Error ? err.message : String(err);
        }
        onWaiting?.(reason);
        await sleep(pollMs);
      }
    },
    async waitFor(predicate, signal) {
      while (!signal?.aborted) {
        const o = await get().catch(() => null);
        if (o && predicate(o)) return o;
        await sleep(pollMs, signal);
      }
      return null;
    },
    async writeStatus(status) {
      assertComputeContractStatus(status);
      await contracts.replaceStatus(opts.name, status);
    },
  };
}
