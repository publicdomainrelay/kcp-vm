import type { ComputeContract, ComputeContractStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { assertComputeContractStatus, COMPUTE_CONTRACT_RESOURCE, parseComputeContract } from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface ComputeContractKcpOptions {
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

export class ComputeContractKcpError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "ComputeContractKcpError";
  }
}

const DEFAULT_TIMEOUT_MS = 15_000;
const DEFAULT_POLL_MS = 2_000;

export function createComputeContractKcpStore(opts: ComputeContractKcpOptions): ComputeContractStore {
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && opts.caCert ? Deno.createHttpClient({ caCerts: [opts.caCert] }) : undefined;
  const { group, version, plural } = COMPUTE_CONTRACT_RESOURCE;
  const root = opts.server.replace(/\/+$/, "") + (opts.workspace ? `/clusters/${opts.workspace}` : "");
  const url = `${root}/apis/${group}/${version}/${plural}/${encodeURIComponent(opts.name)}`;
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS;

  async function bearer(): Promise<string | undefined> {
    if (opts.tokenFile) return (await Deno.readTextFile(opts.tokenFile)).trim();
    return opts.token;
  }

  async function request(target: string, init: RequestInit = {}): Promise<unknown> {
    const headers = new Headers(init.headers);
    headers.set("accept", "application/json");
    const t = await bearer();
    if (t) headers.set("authorization", `Bearer ${t}`);
    const res = await baseFetch(target, {
      ...init,
      headers,
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(client ? { client } : {}),
    } as RequestInit);
    const text = await res.text();
    if (!res.ok) {
      throw new ComputeContractKcpError(res.status, `${init.method ?? "GET"} ${new URL(target).pathname}: ${res.status} ${text.slice(0, 300)}`);
    }
    return text ? JSON.parse(text) : null;
  }

  async function get(): Promise<ComputeContract | null> {
    try {
      return parseComputeContract(await request(url));
    } catch (err) {
      if (err instanceof ComputeContractKcpError && err.status === 404) return null;
      throw err;
    }
  }

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
      await request(`${url}/status`, {
        method: "PATCH",
        headers: { "content-type": "application/json-patch+json" },
        body: JSON.stringify([{ op: "add", path: "/status", value: status }]),
      });
    },
  };
}
