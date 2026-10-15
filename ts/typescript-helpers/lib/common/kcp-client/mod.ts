import { parse as parseYaml } from "@std/yaml";

export interface KcpResource {
  group: string;
  version: string;
  plural: string;
  kind?: string;
}

export interface KcpTarget {
  resource: KcpResource;
  workspace?: string;
  namespace?: string;
}

export interface KcpObjectMeta {
  name?: string;
  namespace?: string;
  uid?: string;
  resourceVersion?: string;
  generation?: number;
  creationTimestamp?: string;
  deletionTimestamp?: string;
  finalizers?: string[];
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
}

export interface KcpObject {
  apiVersion?: string;
  kind?: string;
  metadata?: KcpObjectMeta;
}

export interface KcpList<T> {
  items: T[];
  resourceVersion: string;
  remainingItemCount?: number;
  continueToken?: string;
}

export type KcpWatchEventType = "ADDED" | "MODIFIED" | "DELETED" | "BOOKMARK" | "ERROR";

export interface KcpWatchEvent<T = unknown> {
  type: KcpWatchEventType;
  object: T;
}

export interface KcpStatusObject {
  kind?: string;
  status?: string;
  message?: string;
  reason?: string;
  code?: number;
}

export interface KcpListOptions {
  labelSelector?: string;
  fieldSelector?: string;
  limit?: number;
}

export interface KcpWatchOptions extends KcpListOptions {
  resourceVersion?: string;
  signal: AbortSignal;
  reconnect?: boolean;
}

export type KcpPatch =
  | { type: "merge"; body: unknown }
  | { type: "json"; ops: unknown[] }
  | { type: "apply"; body: unknown; fieldManager: string; force?: boolean };

export interface KcpPatchOptions {
  subresource?: "status";
}

export interface KcpDeletePreconditions {
  resourceVersion?: string;
  uid?: string;
}

export interface KcpCredentials {
  server: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  caFile?: string;
  clientCert?: string;
  clientKey?: string;
}

export class KcpError extends Error {
  constructor(
    readonly status: number,
    readonly method: string,
    readonly path: string,
    readonly body: string,
  ) {
    super(`${method} ${path}: ${status} ${body.slice(0, 300)}`);
    this.name = "KcpError";
  }
}

export const isKcpStatus = (err: unknown, ...statuses: number[]): err is KcpError =>
  err instanceof KcpError && statuses.includes(err.status);

export const isNotFound = (err: unknown): err is KcpError => isKcpStatus(err, 404);

export const isConflict = (err: unknown): err is KcpError => isKcpStatus(err, 409, 422);

export const isQuotaExceeded = (err: unknown): err is KcpError => isKcpStatus(err, 403) && /exceeded quota/.test(err.body);

export const RESOURCE_QUOTA_RESOURCE: KcpResource = { group: "", version: "v1", plural: "resourcequotas", kind: "ResourceQuota" };

export const BOUNDED_QUOTA_NAME = "socialweb-bounded";

export function quotaCountKey(plural: string, group: string): string {
  return `count/${plural}.${group}`;
}

export async function absentOnNotFound<T>(work: Promise<T>): Promise<T | null> {
  try {
    return await work;
  } catch (err) {
    if (isNotFound(err)) return null;
    throw err;
  }
}

export function apiVersionOf(resource: KcpResource): string {
  return resource.group ? `${resource.group}/${resource.version}` : resource.version;
}

export function serverRoot(server: string): string {
  return server.replace(/\/+$/, "").split("/clusters/")[0];
}

export function workspaceOfServer(server: string): string | undefined {
  const at = server.replace(/\/+$/, "").split("/clusters/")[1];
  return at || undefined;
}

export function resourcePath(target: KcpTarget, name?: string, subresource?: string): string {
  const { resource, workspace, namespace } = target;
  const cluster = workspace ? `/clusters/${workspace}` : "";
  const api = resource.group ? `/apis/${resource.group}/${resource.version}` : `/api/${resource.version}`;
  const ns = namespace ? `/namespaces/${encodeURIComponent(namespace)}` : "";
  const item = name ? `/${encodeURIComponent(name)}` : "";
  const sub = name && subresource ? `/${subresource}` : "";
  return `${cluster}${api}${ns}/${resource.plural}${item}${sub}`;
}

export function patchContentType(patch: KcpPatch): string {
  switch (patch.type) {
    case "merge":
      return "application/merge-patch+json";
    case "json":
      return "application/json-patch+json";
    case "apply":
      return "application/apply-patch+yaml";
  }
}

export function patchBody(patch: KcpPatch): string {
  return JSON.stringify(patch.type === "json" ? patch.ops : patch.body);
}

export function resourceVersionOf(object: unknown): string | undefined {
  const rv = (object as KcpObject | null | undefined)?.metadata?.resourceVersion;
  return typeof rv === "string" && rv !== "" ? rv : undefined;
}

export function watchErrorCode(event: KcpWatchEvent): number | undefined {
  if (event.type !== "ERROR") return undefined;
  const code = (event.object as KcpStatusObject | null | undefined)?.code;
  return typeof code === "number" ? code : undefined;
}

export function splitWatchLines(buffered: string): { lines: string[]; rest: string } {
  const parts = buffered.split("\n");
  const rest = parts.pop() ?? "";
  return { lines: parts.map((l) => l.trim()).filter(Boolean), rest };
}

interface KubeconfigDoc {
  "current-context"?: string;
  contexts?: { name: string; context: { cluster: string; user: string } }[];
  clusters?: { name: string; cluster: { server: string; "certificate-authority-data"?: string; "certificate-authority"?: string } }[];
  users?: {
    name: string;
    user: {
      token?: string;
      tokenFile?: string;
      "client-certificate-data"?: string;
      "client-key-data"?: string;
    };
  }[];
}

function fromBase64(data: string | undefined): string | undefined {
  return data ? new TextDecoder().decode(Uint8Array.from(atob(data), (c) => c.charCodeAt(0))) : undefined;
}

export function parseKubeconfig(text: string, contextName?: string): KcpCredentials {
  const doc = parseYaml(text) as KubeconfigDoc;
  const name = contextName ?? doc["current-context"];
  const context = doc.contexts?.find((c) => c.name === name)?.context;
  if (!context) throw new Error(`kubeconfig has no context ${name ?? "(none)"}`);
  const cluster = doc.clusters?.find((c) => c.name === context.cluster)?.cluster;
  const user = doc.users?.find((u) => u.name === context.user)?.user;
  if (!cluster?.server || !user) throw new Error(`kubeconfig context ${name} names a missing cluster or user`);
  return {
    server: new URL(cluster.server).origin,
    token: user.token,
    tokenFile: user.tokenFile,
    caCert: fromBase64(cluster["certificate-authority-data"]),
    caFile: cluster["certificate-authority"],
    clientCert: fromBase64(user["client-certificate-data"]),
    clientKey: fromBase64(user["client-key-data"]),
  };
}
