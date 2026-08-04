import type { CloudInitContext, UserDataModule } from "@publicdomainrelay/cloud-init-common";
import { renderSubject } from "@publicdomainrelay/fedproxy-rbac-common";

export const DEFAULT_SUBJECT_TEMPLATE = "actx:{actx}:plc:{did-plc-key}:role:{role}";

/** Resolved com.publicdomainrelay.temp.compute.config.wif.simple from the winning bid. */
export interface WifSimpleConfig {
  issuer_uri: string;
  actx: string;
  subject?: string;
  to_issue?: string;
  accept_path?: string;
  actx_path?: string;
  token_path?: string;
  url_path?: string;
  url_route?: string;
}

/** Everything a capability needs to authorize the guest, derived from the winning bid. */
export interface GrantVars {
  requesterDid: string;
  role: string;
  actx: string;
  issuerUri: string;
  subject: string;
  expectedAud: string;
}

export interface PrepareContext {
  vmName: string;
  requesterDid: string;
  ingressProxyHost: string;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  tls?: boolean;
  log: (event: string, extra?: Record<string, unknown>) => void;
}

export interface CapabilityPrepared {
  ctx?: Partial<CloudInitContext>;
}

/**
 * A guest-side concern that spans the compute contract: it contributes cloud-init
 * before the RFP is sent (prepare + userDataModule), receives its authorization
 * once a bid wins (onContract), loses it when the VM delete event is sent
 * (onRevoke), and releases resources at the end (dispose).
 */
export interface GuestCapability {
  readonly id: string;
  prepare?(ctx: PrepareContext): Promise<CapabilityPrepared>;
  readonly userDataModule?: string | UserDataModule;
  onContract?(vars: GrantVars): void | Promise<void>;
  onRevoke?(): void | Promise<void>;
  dispose?(): Promise<void>;
}

/**
 * Match the provider's tag-derived subject key. compute-provider-local tags the
 * droplet `oidc-sub:plc:<did.split(":").pop()>`, and the issuer's prove handler
 * assembles the token subject from those tags, so the same tail must be used
 * here or the rendered subject will not equal the one the guest presents.
 */
export function subjectKeyOf(did: string): string {
  return did.split(":").pop() ?? did;
}

export interface DeriveGrantVarsInput {
  cfg: WifSimpleConfig;
  /**
   * DID that authored and submitted the market records. The provider tags the
   * guest from this DID, so the token subject is derived from it. Under OAuth
   * this is the user's PDS DID, which is NOT the requester's local relay DID.
   */
  subjectDid: string;
  /**
   * DID the guest was told to ask for as its audience. Baked into cloud-init
   * before a bid exists, so it can only be the requester's own relay DID.
   */
  audienceDid: string;
  role: string;
}

export function deriveGrantVars(input: DeriveGrantVarsInput): GrantVars {
  const { cfg, subjectDid, audienceDid, role } = input;
  return {
    requesterDid: audienceDid,
    role,
    actx: cfg.actx,
    issuerUri: cfg.issuer_uri,
    subject: renderSubject(cfg.subject ?? DEFAULT_SUBJECT_TEMPLATE, {
      actx: cfg.actx,
      didPlcKey: subjectKeyOf(subjectDid),
      role,
    }),
    expectedAud: `api://ATProto?actx=${audienceDid}`,
  };
}

export function isWifSimpleConfig(v: unknown): v is WifSimpleConfig {
  if (!v || typeof v !== "object") return false;
  const o = v as Record<string, unknown>;
  return typeof o.issuer_uri === "string" && typeof o.actx === "string";
}
