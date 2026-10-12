import type { RecordResolver } from "./resolve.ts";
import type {
  Accept,
  Bid,
  Logger,
  MarketEvent,
  Resolved,
  RFP,
} from "@publicdomainrelay/market-common";

export type HandlerResult = { status?: number; body?: unknown } | void;

export interface SubmitRfpContext {
  rfpUri: string;
  rfpCid: string;
  rfp: Resolved<RFP & { $type?: string }>;
  /** NSID of the RFP's payload record (its collection). */
  payloadNsid: string;
  issuerDid: string;
  resolve: RecordResolver;
  log: Logger;
  /**
   * The original inbound request, for callbacks that need its url/headers.
   * Absent when the RFP was self-discovered off a firehose (pull mode) rather
   * than pushed over submitRfp.
   */
  req?: Request;
}

export type SubmitRfpCallback = (ctx: SubmitRfpContext) => Promise<HandlerResult> | HandlerResult;

/** callbacks[serviceId][payloadNsid] -> handler for that RFP type. */
export type RfpCallbacks = Record<string, Record<string, SubmitRfpCallback>>;

export interface SubmitBidContext {
  uri: string;
  cid: string;
  /** The bid record as sent inline in the request body. */
  record: Bid & { $type?: string };
  issuerDid: string;
  resolve: RecordResolver;
  log: Logger;
  /** The original inbound request, for callbacks that need its url/headers. */
  req: Request;
}

export type SubmitBidCallback = (ctx: SubmitBidContext) => Promise<HandlerResult> | HandlerResult;

export interface SubmitAcceptContext {
  acceptUri: string;
  acceptCid: string;
  accept: Resolved<Accept & { $type?: string }>;
  issuerDid: string;
  resolve: RecordResolver;
  log: Logger;
  /** The original inbound request, for callbacks that need its url/headers. */
  req: Request;
}

export type SubmitAcceptCallback = (ctx: SubmitAcceptContext) => Promise<HandlerResult> | HandlerResult;

export interface EventDispatchContext {
  uri: string;
  cid: string;
  event: Resolved<MarketEvent & { $type?: string }>;
  /** NSID of the event's payload record (its collection). */
  payloadNsid: string;
  issuerDid: string;
  /** Which configured service-id the token's `aud` matched (if any). */
  serviceId?: string;
  resolve: RecordResolver;
  log: Logger;
  /** The original inbound request, for callbacks that need its url/headers. */
  req: Request;
}

export type EventCallback = (ctx: EventDispatchContext) => Promise<HandlerResult> | HandlerResult;

/** callbacks[serviceId][payloadNsid] -> handler for that event type. */
export type EventCallbacks = Record<string, Record<string, EventCallback>>;
