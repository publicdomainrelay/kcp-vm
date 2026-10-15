export const GUEST_VAULT_TOKEN_HEADER = "x-vault-token";
export const GUEST_OIDC_ISSUER_PATH = "/v1/identity/oidc";
export const GUEST_OIDC_SUBJECT_CLAIM = "wid_sub";
export const GUEST_KCP_API = "guest-kcp";
export const GUEST_ENTITY_PREFIX = "guest-";
export const GUEST_ROLE_PATTERN = /^guest-[A-Za-z0-9][A-Za-z0-9._-]{0,200}-[0-9a-f]{16}$/;
export const GUEST_COMPUTEVM_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,200}$/;

export function guestAudience(api: string, actx: string): string {
  return `api://${api}?actx=${actx}`;
}

export function guestOidcIssuer(ingressUrl: string): string {
  return `${ingressUrl.replace(/\/+$/, "")}${GUEST_OIDC_ISSUER_PATH}`;
}

export function parseGuestAudience(aud: string): { api: string; actx: string } | null {
  if (!aud.startsWith("api://")) return null;
  const rest = aud.slice("api://".length);
  const q = rest.indexOf("?");
  if (q < 0) return null;
  const actx = new URLSearchParams(rest.slice(q + 1)).get("actx");
  const api = rest.slice(0, q);
  return api && actx ? { api, actx } : null;
}
