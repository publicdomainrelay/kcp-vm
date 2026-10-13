import { OPEN_BAO_SERVER_RESOURCE } from "@publicdomainrelay/compute-provider-kcp-common";
import type { OpenBaoServer } from "@publicdomainrelay/compute-provider-kcp-common";
import { type KcpClient, kcpResourceClient } from "@publicdomainrelay/kcp-client-abc";
import { createKcpRestClient } from "@publicdomainrelay/kcp-client-rest";

export interface OpenBaoServerKcpOptions {
  kcp?: KcpClient;
  server: string;
  workspace?: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  name: string;
  client: string;
  kvMountClient?: string;
  readyTimeoutMs?: number;
  pollMs?: number;
  fetch?: typeof fetch;
  timeoutMs?: number;
  onWaiting?: (reason: string) => void;
}

export interface ResolvedOpenBaoServer {
  name: string;
  address: string;
  caCert: string;
  clientCert: string;
  clientKey: string;
  certLogin: { mount: string; role: string };
  kvMount: string;
}

const DEFAULT_READY_TIMEOUT_MS = 60_000;
const DEFAULT_POLL_MS = 1_000;

async function readRef(ref: string | undefined, what: string): Promise<string> {
  if (!ref) throw new Error(`openbaoserver: status names no ${what}`);
  if (!ref.startsWith("file:")) throw new Error(`openbaoserver: ${what} ref ${JSON.stringify(ref)} is not a file: ref`);
  return await Deno.readTextFile(ref.slice("file:".length));
}

export function createOpenBaoServerResolver(opts: OpenBaoServerKcpOptions): () => Promise<ResolvedOpenBaoServer> {
  const kcp = opts.kcp ??
    createKcpRestClient(
      { server: opts.server, token: opts.token, tokenFile: opts.tokenFile, caCert: opts.caCert },
      { fetch: opts.fetch, timeoutMs: opts.timeoutMs },
    );
  const servers = kcpResourceClient<OpenBaoServer>(kcp, { resource: OPEN_BAO_SERVER_RESOURCE, workspace: opts.workspace });
  const get = () => servers.get(opts.name);

  function notReady(o: OpenBaoServer | null): string | null {
    if (!o) return "no such object";
    const st = o.status;
    if (st?.phase !== "Ready") return `phase ${st?.phase ?? "unset"}: ${st?.message ?? ""}`;
    if (!st.address) return "no address in status";
    if (!st.clients?.some((c) => c.name === opts.client)) return `status names no client ${opts.client}`;
    return null;
  }

  return async () => {
    const deadline = Date.now() + (opts.readyTimeoutMs ?? DEFAULT_READY_TIMEOUT_MS);
    let o: OpenBaoServer | null = null;
    for (;;) {
      let reason: string | null;
      try {
        o = await get();
        reason = notReady(o);
      } catch (err) {
        reason = err instanceof Error ? err.message : String(err);
      }
      if (!reason) break;
      const current: OpenBaoServer | null = o;
      const st = current?.status;
      if (st?.phase === "Failed" && st.observedGeneration === current?.metadata?.generation) {
        throw new Error(`openbaoserver ${opts.name} failed: ${st.message ?? ""}`);
      }
      if (Date.now() > deadline) throw new Error(`openbaoserver ${opts.name} is not ready: ${reason}`);
      opts.onWaiting?.(reason);
      await new Promise((r) => setTimeout(r, opts.pollMs ?? DEFAULT_POLL_MS));
    }
    const st = o!.status!;
    const c = st.clients!.find((x) => x.name === opts.client)!;
    const kvMount = opts.kvMountClient ? st.clients!.find((x) => x.name === opts.kvMountClient)?.kvMount : c.kvMount;
    if (!kvMount || !c.certAuthMount || !c.certAuthRole) {
      throw new Error(`openbaoserver ${opts.name}: client ${opts.client} names no kvMount, certAuthMount or certAuthRole`);
    }
    return {
      name: opts.name,
      address: st.address!,
      caCert: await readRef(st.refs?.ca, "ca"),
      clientCert: await readRef(c.clientCert, `client ${opts.client} certificate`),
      clientKey: await readRef(c.clientKey, `client ${opts.client} key`),
      certLogin: { mount: c.certAuthMount, role: c.certAuthRole },
      kvMount,
    };
  };
}
