export interface Sandbox {
  execute(request: SandboxRequest): Promise<SandboxResponse>;
  shutdown(): Promise<void>;
}

export interface SandboxRequest {
  code: string;
  timeoutMs?: number;
}

export interface SandboxResponse {
  stdout: string;
  stderr: string;
  exitCode: number;
  timedOut: boolean;
  result?: unknown;
}

export interface SandboxPermissions {
  net?: string[];
  read?: string[];
  write?: string[];
  env?: string[];
  run?: string[];
}
