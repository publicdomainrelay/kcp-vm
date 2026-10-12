import type {
  Condition,
  MarketIdentity,
  MarketIdentitySpec,
  MarketIdentityStatus,
  VaultRef,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export type {
  AssociationMode,
  Condition,
  MarketIdentity,
  MarketIdentityMode,
  MarketIdentityPendingAction,
  MarketIdentityPhase,
  MarketIdentitySpec,
  MarketIdentityStatus,
  PendingActionKind,
  VaultRef,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export {
  CONDITION_ASSOCIATED,
  CONDITION_SIGNED_IN,
  MARKET_IDENTITY_RESOURCE,
} from "@publicdomainrelay/socialweb-computer-k8s-api";

export interface MarketIdentityObjects {
  get(name: string): Promise<MarketIdentity | null>;
  writeStatus(name: string, status: MarketIdentityStatus): Promise<void>;
}

export interface IdentityVault {
  read(ref: VaultRef): Promise<Record<string, unknown> | null>;
  write(ref: VaultRef, data: Record<string, unknown>): Promise<void>;
  remove(ref: VaultRef): Promise<void>;
}

export interface AssociatingRepo {
  readonly did: string;
  readonly associateCalled: Promise<string>;
  approveAssociation(): void;
  rejectAssociation(err: Error): void;
  associations(): Promise<string[]>;
}

export interface BrowserOAuthClient {
  restore(): Promise<boolean>;
  startFlow(): Promise<string>;
  completeFlow(params: Record<string, string>): Promise<void>;
  readonly agent: { readonly did: string };
}

export type AssociationDecision = "approve" | "reject" | "ask";

export function decideAssociation(spec: MarketIdentitySpec, callerDid: string, callerHandle?: string): AssociationDecision {
  if (spec.rejectedAssociations?.includes(callerDid)) return "reject";
  if (spec.approvedAssociations?.includes(callerDid)) return "approve";
  if (spec.associateWith && (spec.associateWith === callerDid || spec.associateWith === callerHandle)) return "approve";
  return "ask";
}

export function oauthQrUrl(origin: string, cliDid: string, nonce: string): string {
  return `${origin.replace(/\/+$/, "")}/#oauth=${encodeURIComponent(cliDid)}&n=${nonce}`;
}

export function associationQrUrl(origin: string, param: string, did: string): string {
  return `${origin.replace(/\/+$/, "")}/#${param}=${did}`;
}

export function parseOAuthQrUrl(url: string): { cliDid: string; nonce: string } | null {
  const hash = url.includes("#") ? url.slice(url.indexOf("#") + 1) : "";
  const params = new URLSearchParams(hash);
  const cliDid = params.get("oauth");
  const nonce = params.get("n");
  return cliDid && nonce ? { cliDid, nonce } : null;
}

export function callbackRef(session: VaultRef): VaultRef {
  return { mount: session.mount, path: `${session.path}/callback` };
}

export function vaultRefs(name: string, spec: MarketIdentitySpec, mount: string): { keys: VaultRef; session: VaultRef } {
  return {
    keys: spec.keysRef ?? { mount, path: `identity/${name}/keys` },
    session: spec.sessionRef ?? { mount, path: `identity/${name}/session` },
  };
}

const SECRET_KEY = /(token|jwt|password|passwd|secret|private|jwk|dpop|^code$|authorization_?code|keyhex|credential)/i;
const JWT = /eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\./;
const LONG_HEX = /(^|[^0-9a-f])[0-9a-f]{64,}([^0-9a-f]|$)/i;
const SECRET_PARAM = /[?&#](code|access_token|refresh_token|id_token|password|client_secret)=/i;
const PEM_KEY = /-----BEGIN [A-Z ]*PRIVATE KEY-----/;

export function findSecretMaterial(value: unknown, path = "$"): string[] {
  if (typeof value === "string") {
    if (JWT.test(value) || LONG_HEX.test(value) || SECRET_PARAM.test(value) || PEM_KEY.test(value)) return [path];
    return [];
  }
  if (Array.isArray(value)) return value.flatMap((v, i) => findSecretMaterial(v, `${path}[${i}]`));
  if (value && typeof value === "object") {
    const obj = value as Record<string, unknown>;
    const out: string[] = [];
    if ("kty" in obj && "d" in obj) out.push(`${path}.d`);
    for (const [k, v] of Object.entries(obj)) {
      const p = `${path}.${k}`;
      if (!k.endsWith("Ref") && SECRET_KEY.test(k)) out.push(p);
      else out.push(...findSecretMaterial(v, p));
    }
    return out;
  }
  return [];
}

export function setCondition(
  conditions: Condition[] | undefined,
  next: { type: string; status: "True" | "False" | "Unknown"; reason: string; message?: string; observedGeneration?: number },
  now: Date,
): Condition[] {
  const list = [...(conditions ?? [])];
  const i = list.findIndex((c) => c.type === next.type);
  const prior = i >= 0 ? list[i] : undefined;
  const lastTransitionTime = prior && prior.status === next.status && typeof prior.lastTransitionTime === "string"
    ? prior.lastTransitionTime
    : now.toISOString().replace(/\.\d{3}Z$/, "Z");
  const cond: Condition = {
    type: next.type,
    status: next.status,
    reason: next.reason,
    message: next.message ?? "",
    lastTransitionTime,
    ...(next.observedGeneration !== undefined ? { observedGeneration: next.observedGeneration } : {}),
  };
  if (i >= 0) list[i] = cond;
  else list.push(cond);
  return list;
}

export function bidderAssociationCaller(value: Record<string, unknown>, did: string, serviceId: string): string | null {
  return value.challenge === did && value.service === serviceId && typeof value.keyId === "string" ? value.keyId : null;
}

export function requesterAssociationCaller(value: Record<string, unknown>, did: string, serviceId: string): string | null {
  if (value.service !== serviceId) return null;
  if (value.keyId === did && typeof value.challenge === "string") return value.challenge;
  if (value.challenge === did && typeof value.keyId === "string") return value.keyId;
  return null;
}
