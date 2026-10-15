import { createOAuthAgentFromSession } from "@publicdomainrelay/atproto-helpers";
import type { AtprotoAgentLike } from "@publicdomainrelay/atproto-helpers";
import type { SessionStore } from "@publicdomainrelay/socialweb-computer-abc";

export interface AccountAgentsOptions {
  sessionStore: SessionStore;
  oauthClientId?: string;
  agentWithoutOAuthFor?: (did: string) => Promise<unknown> | null;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export type AccountAgent = Awaited<ReturnType<typeof createOAuthAgentFromSession>>;

export type LiveAccountAgent = AtprotoAgentLike & { sessionData: { userDid: string } };

export interface AccountAgents {
  agentFor(did: string): Promise<AccountAgent>;
  liveAgentFor(did: string): LiveAccountAgent;
  shutdown(): Promise<void>;
}

export function createAccountAgents(opts: AccountAgentsOptions): AccountAgents {
  const log = opts.log ?? (() => {});
  const agents = new Map<string, { refreshJwt: string; agent: Promise<AccountAgent> }>();
  const agentsWithoutOAuth = new Map<string, Promise<AccountAgent>>();

  function agentFor(did: string): Promise<AccountAgent> {
    const cachedWithoutOAuth = agentsWithoutOAuth.get(did);
    if (cachedWithoutOAuth) return cachedWithoutOAuth;
    const withoutOAuth = opts.agentWithoutOAuthFor?.(did);
    if (withoutOAuth) {
      const agent = withoutOAuth as Promise<AccountAgent>;
      agentsWithoutOAuth.set(did, agent);
      agent.catch(() => agentsWithoutOAuth.delete(did));
      log("account_agent_without_oauth", { did });
      return agent;
    }
    return (async () => {
      const stored = await opts.sessionStore.get(did);
      if (!stored) throw new Error(`no oauth session stored for ${did}`);
      const cached = agents.get(did);
      if (cached && cached.refreshJwt === stored.refreshJwt) return await cached.agent;
      if (cached) {
        agents.delete(did);
        void cached.agent
          .then((a) => (a as unknown as { dispose?: () => void }).dispose?.())
          .catch(() => {});
      }
      const clientId = stored.clientId ?? opts.oauthClientId;
      const agent = createOAuthAgentFromSession(stored, {
        clientId,
        saveSession: (updated) => opts.sessionStore.set(did, { ...updated, clientId }),
      });
      agents.set(did, { refreshJwt: stored.refreshJwt, agent });
      agent.catch(() => {
        if (agents.get(did)?.agent === agent) agents.delete(did);
      });
      return await agent;
    })();
  }

  function liveAgentFor(did: string): LiveAccountAgent {
    const current = () => agentFor(did) as Promise<AtprotoAgentLike>;
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
      for (const settled of await Promise.allSettled([...agents.values()].map((e) => e.agent))) {
        if (settled.status === "fulfilled") (settled.value as unknown as { dispose?: () => void }).dispose?.();
      }
      agents.clear();
    },
  };
}
