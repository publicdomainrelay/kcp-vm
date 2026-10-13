export type GuestLoginStep = "bind" | "challenge" | "login";

export const GUEST_LOGIN_FIELDS: Readonly<Record<GuestLoginStep, readonly string[]>> = {
  bind: ["role", "bind_secret", "host_key", "signature"],
  challenge: ["role"],
  login: ["role", "challenge", "signature"],
};

export function isGuestLoginStep(step: string): step is GuestLoginStep {
  return Object.hasOwn(GUEST_LOGIN_FIELDS, step);
}

export function guestLoginBody(step: GuestLoginStep, input: unknown): Record<string, string> | null {
  if (!input || typeof input !== "object") return null;
  const out: Record<string, string> = {};
  for (const field of GUEST_LOGIN_FIELDS[step]) {
    const value = (input as Record<string, unknown>)[field];
    if (typeof value !== "string" || value === "" || value.length > 4096) return null;
    out[field] = value;
  }
  return out;
}

export interface GuestLoginReply {
  status: number;
  body: unknown;
}

export interface GuestLogin {
  step(step: GuestLoginStep, body: Record<string, string>): Promise<GuestLoginReply>;
}
