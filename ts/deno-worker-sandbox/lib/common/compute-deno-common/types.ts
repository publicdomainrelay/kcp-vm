export interface StrongRef {
  $type: "com.atproto.repo.strongRef";
  uri: string;
  cid: string;
}

export interface WorkerManifestRecord {
  source?: {
    tangled?: StrongRef;
    git?: string;
  };
  lock: string;
  json: string;
  bundle: string;
  config?: string;
  configref?: StrongRef;
  signatures?: unknown[];
}

export interface WorkerInstanceRecord {
  manifest: StrongRef;
  signatures?: unknown[];
}

export interface WorkerRequest {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: unknown;
}

export interface WorkerResponse {
  status: number;
  headers: Record<string, string>;
  body: unknown;
}
