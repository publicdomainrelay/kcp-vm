import type { SessionCustodyConnector } from "@publicdomainrelay/atproto-session-custody-abc";
import { createCustodyAgent } from "@publicdomainrelay/atproto-session-custody-openbao";
import type { WebAccountAgent, WebCaller, WebSignedInUser, WebSignIn, WebSignOutResult } from "@publicdomainrelay/socialweb-computer-abc";

export interface OpenBaoWebSignInOptions {
  connector: SessionCustodyConnector;
  role: string;
  renewBelowSeconds?: number;
  renewIncrementSeconds?: number;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

const DEFAULT_RENEW_BELOW_SECONDS = 1_800;
const DEFAULT_RENEW_INCREMENT_SECONDS = 3_600;

export function createOpenBaoWebSignIn(opts: OpenBaoWebSignInOptions): WebSignIn {
  const log = opts.log ?? (() => {});
  const renewBelow = opts.renewBelowSeconds ?? DEFAULT_RENEW_BELOW_SECONDS;
  const renewIncrement = opts.renewIncrementSeconds ?? DEFAULT_RENEW_INCREMENT_SECONDS;
  return {
    clientMetadata: () => opts.connector.clientMetadata(),
    async start(identifier, redirectUri) {
      const started = await opts.connector.own.start({
        role: opts.role,
        redirectUri,
        ...(identifier.startsWith("did:") ? { did: identifier } : { handle: identifier }),
      });
      return { authorizationUrl: started.authorizationUrl, state: started.state };
    },
    async complete(params) {
      const login = await opts.connector.own.complete(params);
      return { did: login.did, handle: login.handle, pds: login.pds, token: login.token, leaseSeconds: login.leaseSeconds };
    },
    async identify(token): Promise<WebSignedInUser | null> {
      const custody = opts.connector.forToken(token);
      const identity = await custody.identity();
      if (!identity || !identity.did) return null;
      if (identity.renewable && identity.ttlSeconds < renewBelow) {
        await custody.renew(renewIncrement).catch((err) => log("web_token_renew_failed", { did: identity.did, error: String(err) }));
      }
      return { did: identity.did, handle: identity.handle, pds: identity.pds };
    },
    async signOut(caller): Promise<WebSignOutResult> {
      const custody = opts.connector.forToken(caller.token);
      try {
        return await custody.signOut(caller.did);
      } finally {
        await custody.revokeToken().catch((err) => log("web_token_revoke_failed", { did: caller.did, error: String(err) }));
      }
    },
    agentFor(caller: WebCaller): WebAccountAgent {
      return createCustodyAgent({ custody: opts.connector.forToken(caller.token), did: caller.did, pds: caller.pds });
    },
  };
}
