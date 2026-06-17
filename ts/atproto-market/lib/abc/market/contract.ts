import type { StrongRef, Resolved } from "@publicdomainrelay/market-common";

export interface ContractGraph {
  bid: StrongRef;
  rfp: StrongRef;
  rfpPayload: StrongRef;
  bidPayload?: StrongRef;
  bidConfig?: StrongRef;
  accept?: StrongRef;
  receipt?: StrongRef;
  event?: StrongRef;
}

export class ContractGraphError extends Error {
  status: number;
  constructor(message: string, status = 400) {
    super(message);
    this.status = status;
  }
}
