export class UnauthorizedException extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = "UnauthorizedException";
  }
}

export function parseAudience(aud: string): { actx: string; api: string } {
  const rest = aud.startsWith("api://") ? aud.slice(6) : null;
  if (!rest) throw new UnauthorizedException(`aud does not start with api://: ${aud}`);
  const qIdx = rest.indexOf("?");
  if (qIdx < 0) throw new UnauthorizedException(`aud missing ?actx=: ${aud}`);
  const api = rest.slice(0, qIdx);
  const params = new URLSearchParams(rest.slice(qIdx + 1));
  const actx = params.get("actx");
  if (!actx) throw new UnauthorizedException(`aud missing actx param: ${aud}`);
  return { actx, api };
}

export interface GuestKv {
  read(path: string): Promise<Record<string, unknown> | null>;
  write(path: string, data: Record<string, unknown>): Promise<void>;
  list(path: string): Promise<string[]>;
  remove(path: string): Promise<void>;
}
