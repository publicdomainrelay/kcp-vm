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

// Bundler interface and supporting types (moved from sandbox-abc)
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

export interface BundleTarRequest {
  tarBase64: string;
}

export interface BundleTarResponse {
  bundleJs: string;
  stdout: string;
  stderr: string;
}

export interface Bundler {
  bundle(request: BundleRequest): Promise<BundleResponse>;
  bundleTar(request: BundleTarRequest): Promise<BundleTarResponse>;
}

// PersistentWorker interface (moved from sandbox-abc)
export interface PersistentWorker {
  postMessage(message: unknown): void;
  onMessage(handler: (message: unknown) => void): void;
  shutdown(): Promise<void>;
}
