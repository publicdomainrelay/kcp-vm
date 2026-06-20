export { Command } from "./config.ts";
export type { ArgDef } from "./config.ts";

export class SandboxError extends Error {
  readonly status: number;
  constructor(message: string, status = 500) {
    super(message);
    this.status = status;
    this.name = "SandboxError";
  }
  toJSON(): { error: string; status: number } {
    return { error: this.message, status: this.status };
  }
}

export interface SandboxPermissions {
  net?: boolean | string[];
  read?: boolean | string[];
  write?: boolean | string[];
  env?: boolean | string[];
  run?: boolean | string[];
  ffi?: boolean | string[];
  sys?: boolean | string[];
  import?: boolean | string[];
}
