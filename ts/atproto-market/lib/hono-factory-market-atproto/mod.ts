import { createFactory } from "@hono/hono/factory";
import type {
  EventCallbacks,
  HandlerResult,
  MarketServerDeps,
  RfpCallbacks,
  SubmitAcceptCallback,
  SubmitBidCallback,
} from "@publicdomainrelay/market-atproto";
import {
  createDidKeyResolver,
  createSubmitAcceptHandler,
  createSubmitBidHandler,
  createSubmitEventHandler,
  createSubmitRfpHandler,
  createVerifyHandler,
  type SubmitRfpHandlerConfig,
} from "@publicdomainrelay/market-atproto";
import {
  SUBMIT_ACCEPT_NSID,
  SUBMIT_BID_NSID,
  SUBMIT_EVENT_NSID,
  SUBMIT_RFP_NSID,
} from "@publicdomainrelay/market-common";
import { NETWORK_ATTESTED_VERIFY_NSID } from "@publicdomainrelay/market-lexicons";

export type { EventCallbacks, HandlerResult, MarketServerDeps, RfpCallbacks, SubmitAcceptCallback, SubmitBidCallback };

export type MarketEnv = {
  Variables: {
    marketDeps: MarketServerDeps;
  };
};

export interface MarketFactoryHandlers {
  rfp?: RfpCallbacks;
  /** Optional scope filter for submitRfp push path. */
  rfpScopeFilter?: SubmitRfpHandlerConfig["acceptScopeFilter"];
  bid?: { serviceIds: string[]; onBid: SubmitBidCallback };
  accept?: { serviceIds: string[]; onAccept: SubmitAcceptCallback };
  event?: { callbacks: EventCallbacks; background?: boolean };
}

export function createMarketFactory(
  deps: MarketServerDeps,
  handlers?: MarketFactoryHandlers,
) {
  return createFactory<MarketEnv>({
    initApp: (app) => {
      app.onError((err, c) => {
        deps.log?.("error", "market route error", { path: c.req.path, method: c.req.method, error: String(err) });
        return c.json({ ok: false, error: "internal error" }, 500);
      });

      app.use(async (c, next) => {
        c.set("marketDeps", deps);
        await next();
      });

      if (handlers?.rfp) {
        const h = createSubmitRfpHandler({
          deps, callbacks: handlers.rfp,
          acceptScopeFilter: handlers.rfpScopeFilter,
        });
        app.post(`/xrpc/${SUBMIT_RFP_NSID}`, (c) => h(c.req.raw));
      }
      if (handlers?.bid) {
        const h = createSubmitBidHandler({ deps, ...handlers.bid });
        app.post(`/xrpc/${SUBMIT_BID_NSID}`, (c) => h(c.req.raw));
      }
      if (handlers?.accept) {
        const h = createSubmitAcceptHandler({ deps, ...handlers.accept });
        app.post(`/xrpc/${SUBMIT_ACCEPT_NSID}`, (c) => h(c.req.raw));
      }
      if (handlers?.event) {
        const h = createSubmitEventHandler({ deps, ...handlers.event });
        app.post(`/xrpc/${SUBMIT_EVENT_NSID}`, (c) => h(c.req.raw));
      }

      const verify = createVerifyHandler({
        idResolver: deps.idResolver,
        keysForDid: createDidKeyResolver(),
        log: deps.log,
      });
      app.get(`/xrpc/${NETWORK_ATTESTED_VERIFY_NSID}`, (c) => verify(c.req.raw));
    },
  });
}
