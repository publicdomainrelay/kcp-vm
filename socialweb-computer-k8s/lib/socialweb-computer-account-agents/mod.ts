import type { AtprotoAgentLike } from "@publicdomainrelay/atproto-helpers";
import type { CustodyAgent, SessionCustody } from "@publicdomainrelay/atproto-session-custody-abc";
import { createCustodyAgent } from "@publicdomainrelay/atproto-session-custody-openbao";

export interface AccountAgentsOptions {
  custody: SessionCustody;
  agentWithoutOAuthFor?: (did: string) => Promise<unknown> | null;
  sessionCacheMs?: number;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export type AccountAgent = CustodyAgent;

export type LiveAccountAgent = AtprotoAgentLike & { sessionData: { userDid: string } };

export interface AccountAgents {
  agentFor(did: string): Promise<AccountAgent>;
  liveAgentFor(did: string): LiveAccountAgent;
  shutdown(): Promise<void>;
}

const DEFAULT_SESSION_CACHE_MS = 30_000;

export function createAccountAgents(opts: AccountAgentsOptions): AccountAgents {
  const log = opts.log ?? (() => {});
  const cacheMs = opts.sessionCacheMs ?? DEFAULT_SESSION_CACHE_MS;
  const agents = new Map<string, { agent: CustodyAgent; pds: string; checkedAt: number }>();
  const agentsWithoutOAuth = new Map<string, Promise<AccountAgent>>();

  async function agentFor(did: string): Promise<AccountAgent> {
    const cachedWithoutOAuth = agentsWithoutOAuth.get(did);
    if (cachedWithoutOAuth) return await cachedWithoutOAuth;
    const withoutOAuth = opts.agentWithoutOAuthFor?.(did);
    if (withoutOAuth) {
      const agent = withoutOAuth as Promise<AccountAgent>;
      agentsWithoutOAuth.set(did, agent);
      agent.catch(() => agentsWithoutOAuth.delete(did));
      log("account_agent_without_oauth", { did });
      return await agent;
    }
    const cached = agents.get(did);
    if (cached && Date.now() - cached.checkedAt < cacheMs) return cached.agent;
    const session = await opts.custody.session(did);
    if (!session) {
      agents.delete(did);
      throw new Error(`no oauth session held in the vault for ${did}`);
    }
    if (session.status !== "active") {
      agents.delete(did);
      throw new Error(`the oauth session of ${did} is ${session.status}: ${session.statusReason}`);
    }
    const agent = cached && cached.pds === session.pds
      ? cached.agent
      : createCustodyAgent({
        custody: opts.custody,
        did,
        pds: session.pds,
        onRetry: (event) => log("account_agent_request_retry", { did, ...event }),
        onGiveUp: (event) => log("account_agent_request_gave_up", { did, ...event }),
      });
    agents.set(did, { agent, pds: session.pds, checkedAt: Date.now() });
    return agent;
  }

  function liveAgentFor(did: string): LiveAccountAgent {
    const current = () => agentFor(did) as unknown as Promise<AtprotoAgentLike>;
    return {
      did,
      sessionData: { userDid: did },
      signer: {
        did: () => did,
        sign: async (bytes) => (await current()).signer.sign(bytes),
      },
      applyWrites: async (d, writes) => (await current()).applyWrites(d, writes),
      getRecord: async (d, collection, rkey) => (await current()).getRecord(d, collection, rkey),
      listRecords: async (d, collection, o) => (await current()).listRecords(d, collection, o),
      getServiceAuth: async (aud, lxm) => {
        const agent = await current();
        if (!agent.getServiceAuth) throw new Error("the account's agent cannot mint service auth");
        return await agent.getServiceAuth(aud, lxm);
      },
      createRecord: async (repo, collection, rkey, record) => {
        const agent = await current();
        if (!agent.createRecord) throw new Error("the account's agent cannot create records");
        return await agent.createRecord(repo, collection, rkey, record);
      },
      putRecord: async (repo, collection, rkey, record) => {
        const agent = await current();
        if (!agent.putRecord) throw new Error("the account's agent cannot put records");
        return await agent.putRecord(repo, collection, rkey, record);
      },
    };
  }

  return {
    agentFor,
    liveAgentFor,
    async shutdown(): Promise<void> {
      for (const settled of await Promise.allSettled([...agentsWithoutOAuth.values()])) {
        if (settled.status === "fulfilled") (settled.value as unknown as { dispose?: () => void }).dispose?.();
      }
      agents.clear();
      agentsWithoutOAuth.clear();
    },
  };
}
