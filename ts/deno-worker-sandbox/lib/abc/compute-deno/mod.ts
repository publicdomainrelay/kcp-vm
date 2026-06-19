import type { StrongRef, WorkerManifestRecord, WorkerInstanceRecord, WorkerRequest, WorkerResponse } from "@publicdomainrelay/compute-deno-common";

export interface SigningKey {
  did(): string;
  sign(bytes: Uint8Array): Promise<Uint8Array>;
}

export interface WorkerManifestStore {
  register(record: WorkerManifestRecord, signingKey?: SigningKey): Promise<StrongRef>;
  get(uri: string): Promise<WorkerManifestRecord | null>;
}

export interface WorkerInstanceStore {
  register(record: WorkerInstanceRecord, signingKey?: SigningKey): Promise<StrongRef>;
  get(uri: string): Promise<WorkerInstanceRecord | null>;
  delete(uri: string): Promise<void>;
}

export interface WorkerInstanceRunner {
  start(instanceRef: StrongRef, manifestRef: StrongRef): Promise<void>;
  execute(instanceRef: StrongRef, request: WorkerRequest): Promise<WorkerResponse>;
  stop(instanceRef: StrongRef): Promise<void>;
  isRunning(instanceRef: StrongRef): boolean;
}
