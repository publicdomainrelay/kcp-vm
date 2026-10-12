import type { Logger, StrongRef } from "@publicdomainrelay/market-common";
import type { RecordResolver } from "./resolve.ts";

export type SettlementMode = "x402" | "free";

export interface SettlementCtx {
  getAgent: () => unknown;
  resolve: RecordResolver;
  getSigner: () => unknown;
  log: Logger;
  baseUrl: string;
}

export interface Settlement {
  readonly mode: SettlementMode;
  readonly bidPayloadNsid: string;
  receiptUrl(reqUrl: string): string;
  createBidPayload(receiptUrl: string, nowIso: string): Promise<StrongRef>;
}

export function receiptUrlFor(baseUrl: string, reqUrl: string, path: string): string {
  const base = baseUrl || new URL(reqUrl).origin;
  return `${base.replace(/\/+$/, "")}/${path}`;
}
