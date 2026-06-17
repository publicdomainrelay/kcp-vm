import type { Main as _StrongRef } from "../market-lexicons/com/atproto/repo/strongRef.ts";
import type { Main as _Offering } from "../market-lexicons/com/publicdomainrelay/temp/market/offering.ts";

export type StrongRef = _StrongRef;
export type Offering = _Offering;

export type Resolved<T> = T & { _uri: string; _cid: string };

export type LogLevel = "debug" | "info" | "warn" | "error";

export type Logger = (level: LogLevel, msg: string, fields?: Record<string, unknown>) => void;

export const noopLogger: Logger = () => {};

export function strongRef(uri: string, cid: string): StrongRef {
  return { $type: "com.atproto.repo.strongRef", uri, cid } as StrongRef;
}
