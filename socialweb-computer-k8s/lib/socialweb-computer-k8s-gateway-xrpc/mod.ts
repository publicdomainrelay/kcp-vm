import { ClusterGatewayError } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import type { ClusterCaller, ClusterGateway } from "@publicdomainrelay/socialweb-computer-k8s-abc";
import {
  CLUSTER_NSID,
  type ClusterNsid,
  type ClusterPhaseResult,
  type ClusterSummary,
  type KubeconfigGrant,
} from "@publicdomainrelay/socialweb-computer-k8s-common";

export interface XrpcClusterGatewayOptions {
  gatewayUrl: string | (() => Promise<string>);
  audience: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

const QUERIES: ReadonlySet<string> = new Set([CLUSTER_NSID.listClusters]);

export function createXrpcClusterGateway(opts: XrpcClusterGatewayOptions): ClusterGateway {
  const gatewayUrl = opts.gatewayUrl;
  const base = async () => (typeof gatewayUrl === "string" ? gatewayUrl : await gatewayUrl()).replace(/\/+$/, "");
  const doFetch = opts.fetch ?? fetch;
  const timeoutMs = opts.timeoutMs ?? 30_000;

  async function call<T>(caller: ClusterCaller, nsid: ClusterNsid, body?: Record<string, unknown>): Promise<T> {
    let token: string;
    try {
      token = await caller.serviceAuth(opts.audience, nsid);
    } catch (err) {
      throw new ClusterGatewayError(401, "ServiceAuthUnavailable", `could not obtain service auth from the account's PDS: ${String(err)}`);
    }
    const query = QUERIES.has(nsid);
    let res: Response;
    try {
      res = await doFetch(`${await base()}/xrpc/${nsid}`, {
        method: query ? "GET" : "POST",
        headers: {
          authorization: `Bearer ${token}`,
          ...(query ? {} : { "content-type": "application/json" }),
        },
        body: query ? undefined : JSON.stringify(body ?? {}),
        signal: AbortSignal.timeout(timeoutMs),
      });
    } catch (err) {
      throw new ClusterGatewayError(502, "GatewayUnreachable", `cluster gateway did not answer: ${String(err)}`);
    }
    const text = await res.text();
    let data: Record<string, unknown> = {};
    try {
      data = text ? JSON.parse(text) : {};
    } catch {
      data = {};
    }
    if (!res.ok) {
      const code = typeof data.error === "string" ? data.error : `HTTP${res.status}`;
      const message = typeof data.message === "string" ? data.message : text.slice(0, 500);
      throw new ClusterGatewayError(res.status, code, message);
    }
    return data as T;
  }

  return {
    async list(caller) {
      const out = await call<{ clusters?: ClusterSummary[] }>(caller, CLUSTER_NSID.listClusters);
      return Array.isArray(out.clusters) ? out.clusters : [];
    },
    create(caller, name) {
      return call<ClusterPhaseResult>(caller, CLUSTER_NSID.createCluster, { name });
    },
    kubeconfig(caller, name, ttlSeconds, noExpiry) {
      const body: Record<string, unknown> = { name };
      if (typeof ttlSeconds === "number") body.ttlSeconds = ttlSeconds;
      if (noExpiry === true) body.noExpiry = true;
      return call<KubeconfigGrant>(caller, CLUSTER_NSID.getKubeconfig, body);
    },
    up(caller, name) {
      return call<ClusterPhaseResult>(caller, CLUSTER_NSID.controlPlaneUp, { name });
    },
    down(caller, name) {
      return call<ClusterPhaseResult>(caller, CLUSTER_NSID.controlPlaneDown, { name });
    },
    remove(caller, name) {
      return call<ClusterPhaseResult>(caller, CLUSTER_NSID.deleteCluster, { name });
    },
  };
}
