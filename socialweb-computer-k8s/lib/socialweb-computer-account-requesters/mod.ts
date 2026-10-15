import { applyOAuthAgentToRequesterPDS, createRequesterPDS } from "@publicdomainrelay/requester-xrpc";
import type { RequesterPDS } from "@publicdomainrelay/requester-abc";
import { createServe } from "@publicdomainrelay/serve";
import type { AccountAgents } from "@publicdomainrelay/socialweb-computer-account-agents";

export interface AccountRequestersOptions {
  agents: AccountAgents;
  requesterKeyHex: () => Promise<string>;
  plcDirectoryUrl?: string;
  ingressProxyHost?: string;
  label?: string;
  log?: (event: string, data?: Record<string, unknown>) => void;
}

export interface AccountRequesters {
  requesterFor(did: string): Promise<RequesterPDS>;
}

export function createAccountRequesters(opts: AccountRequestersOptions): AccountRequesters {
  const log = opts.log ?? (() => {});
  const plcUrl = opts.plcDirectoryUrl ?? "https://plc.directory";
  const logger = {
    debug: () => {},
    info: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    warn: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
    error: (message: string, meta?: Record<string, unknown>) => log(message, meta ?? {}),
  };
  const requesters = new Map<string, Promise<RequesterPDS>>();

  function requesterFor(did: string): Promise<RequesterPDS> {
    let entry = requesters.get(did);
    if (!entry) {
      entry = (async () => {
        await opts.agents.agentFor(did);
        const requesterServe = createServe({ logger, tcp: { addr: "127.0.0.1", port: 0 } });
        const pds = await createRequesterPDS({
          logger,
          serve: requesterServe,
          privateKeyHex: await opts.requesterKeyHex(),
          plcDirectoryUrl: plcUrl,
          ingressProxyHost: opts.ingressProxyHost,
          label: opts.label ?? "socialweb-computer-requester",
        });
        await pds.beginServe();
        applyOAuthAgentToRequesterPDS(pds, opts.agents.liveAgentFor(did) as never, { log });
        return pds;
      })();
      requesters.set(did, entry);
      entry.catch(() => requesters.delete(did));
    }
    return entry;
  }

  return { requesterFor };
}
