export interface SubscribeReposOp {
  action: string;
  path: string;
  cid: { $link: string } | null;
  prev?: unknown;
}

export interface SubscribeReposFrame {
  $type?: string;
  seq: number;
  repo: string;
  commit?: { $link: string };
  rev?: string;
  since?: string | null;
  blocks?: unknown;
  ops: SubscribeReposOp[];
  time?: string;
}

export interface RecordPath {
  collection: string;
  rkey: string;
}

export function splitRecordPath(path: string): RecordPath | undefined {
  const slashIdx = path.indexOf("/");
  if (slashIdx <= 0) return undefined;
  return { collection: path.slice(0, slashIdx), rkey: path.slice(slashIdx + 1) };
}

export const FUTURE_CURSOR_ERROR = "FutureCursor";

export interface SubscribeReposErrorFrame {
  error: string;
  message?: string;
}

export function isSubscribeReposError(frame: unknown): frame is SubscribeReposErrorFrame {
  return !!frame && typeof frame === "object" && typeof (frame as { error?: unknown }).error === "string";
}
