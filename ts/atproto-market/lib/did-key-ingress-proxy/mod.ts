import type { StructuredLoggerInterface } from "@publicdomainrelay/logger";
import { hostnameOnly, hostnameToDid } from "@publicdomainrelay/hostname-helpers";
import { signServiceAuth } from "@publicdomainrelay/atproto-repo-deno";
import { createSubscriber } from "@publicdomainrelay/did-key-ingress-proxy-subscriber-xrpc";
import { createSubscriberFactory } from "@publicdomainrelay/hono-factory-did-key-ingress-proxy-subscriber-xrpc";
import type { IngressRef } from "@publicdomainrelay/serve";

export interface CreateIngressOpts {
  logger: StructuredLoggerInterface;
  ingressProxyHost: string;
  signer: { did(): string; sign(bytes: Uint8Array): Promise<Uint8Array> };
  keypair: { did(): string; sign(data: Uint8Array): Promise<Uint8Array> };
  label?: string;
  /**
   * Lazily resolves the local serve's TCP address so inbound relay WS
   * subscriptions (e.g. com.atproto.sync.subscribeRepos from a crawling relay)
   * are forwarded to the local app's firehose. Read lazily because the port is
   * assigned when the serve starts listening.
   */
  localWsTarget?: () => { hostname: string; port: number } | undefined;
  /**
   * When set, inbound relay subscriptions for non-tunnel NSIDs call this
   * handler directly instead of opening a loopback WebSocket to localWsTarget.
   * Prefer this when the firehose source is in-process — no TCP listener needed.
   */
  directSubscriptionHandler?: (
    subscriptionId: string,
    nsid: string,
    params: Record<string, string>,
    onEvent: (event: unknown) => void,
    onData: (data: Uint8Array) => void,
  ) => (() => void) | void;
}

export function createIngress(opts: CreateIngressOpts): IngressRef {
  const { logger, ingressProxyHost, signer, keypair } = opts;
  const label = opts.label ?? "bidder";
  let subscriber: { close(): void } | null = null;

  const relay: IngressRef = {
    ingressRef: "",
    get ingressUrl(): string { return this.ingressRef ? "https://" + this.ingressRef.slice("did:web:".length) : ""; },
    get ingressHost(): string { return this.ingressRef.startsWith("did:web:") ? this.ingressRef.slice("did:web:".length) : this.ingressRef; },

    async onServe(fetch: (req: Request) => Promise<Response>): Promise<void> {
      const { handleRequest } = createSubscriberFactory({ app: { fetch } });

      async function getServiceAuthToken(lxm: string): Promise<string> {
        const aud = hostnameToDid(ingressProxyHost);
        return await signServiceAuth(signer, { aud, lxm });
      }

      const host = hostnameOnly(ingressProxyHost);

      logger.info("xrpc-relay connecting", { ingressProxyHost });

      const handle = await createSubscriber({
        label,
        keypair,
        getServiceAuthToken,
        ingressProxyHost,
        handleRequest,
        wsTarget: opts.localWsTarget,
        directSubscriptionHandler: opts.directSubscriptionHandler,
      });

      relay.ingressRef = handle.ingressRef;
      subscriber = handle;

      logger.info("xrpc-relay registered", {
        subdomain: handle.subdomain,
        ingressRef: handle.ingressRef,
      });
    },

    close(): void {
      subscriber?.close();
    },
  };

  return relay;
}
