export interface Sandbox {
  execute(request: SandboxRequest): Promise<SandboxResponse>;
  shutdown(): Promise<void>;
}

export interface Bundler {
  bundle(request: BundleRequest): Promise<BundleResponse>;
  bundleTar(request: BundleTarRequest): Promise<BundleTarResponse>;
}

export interface BundleTarRequest {
  tarBase64: string;
}

export interface BundleTarResponse {
  bundleJs: string;
  stdout: string;
  stderr: string;
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

export interface BundleRequest {
  denoJson: string;
  denoLock?: string;
  source: string;
}

export interface BundleResponse {
  bundleJs: string;
  stdout: string;
  stderr: string;
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

export interface PersistentWorker {
  postMessage(message: unknown): void;
  onMessage(handler: (message: unknown) => void): void;
  shutdown(): Promise<void>;
}
