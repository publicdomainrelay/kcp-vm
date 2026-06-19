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

export interface ExecRequest {
  bundleJs: string;
  denoJson?: string;
  denoLock?: string;
  timeoutMs?: number;
}

export interface SandboxPermissions {
  net?: string[];
  read?: string[];
  write?: string[];
  env?: string[];
  run?: string[];
}

// Types moved to sandbox-common, re-exported here for backward compat
export type {
  Bundler,
  BundleRequest,
  BundleResponse,
  BundleTarRequest,
  BundleTarResponse,
  PersistentWorker,
} from "@publicdomainrelay/sandbox-common";
