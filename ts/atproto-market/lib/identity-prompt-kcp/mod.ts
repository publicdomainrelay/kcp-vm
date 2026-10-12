import type { MarketIdentity, MarketIdentityStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import { assertMarketIdentityStatus, MARKET_IDENTITY_RESOURCE, parseMarketIdentity } from "@publicdomainrelay/socialweb-computer-k8s-api";
import type { IdentityAnswers, IdentityPrompt } from "@publicdomainrelay/identity-prompt-abc";
import { refuseSecretMaterial, specWithAnswer } from "@publicdomainrelay/identity-prompt-abc";

export interface KcpIdentityPromptOptions {
  server: string;
  workspace: string;
  token?: string;
  tokenFile?: string;
  caCert?: string;
  fetch?: typeof fetch;
  timeoutMs?: number;
  conflictRetries?: number;
}

export class KcpIdentityPromptError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
    this.name = "KcpIdentityPromptError";
  }
}

const DEFAULT_TIMEOUT_MS = 15_000;
const DEFAULT_CONFLICT_RETRIES = 5;

export function createKcpIdentityPrompt(opts: KcpIdentityPromptOptions): IdentityPrompt & IdentityAnswers {
  const baseFetch = opts.fetch ?? fetch;
  const client = !opts.fetch && opts.caCert ? Deno.createHttpClient({ caCerts: [opts.caCert] }) : undefined;
  const { group, version, plural } = MARKET_IDENTITY_RESOURCE;
  const base = `${opts.server.replace(/\/+$/, "")}/clusters/${opts.workspace}/apis/${group}/${version}/${plural}`;

  async function token(): Promise<string | undefined> {
    if (opts.tokenFile) return (await Deno.readTextFile(opts.tokenFile)).trim();
    return opts.token;
  }

  async function request(url: string, init: RequestInit = {}): Promise<unknown> {
    const headers = new Headers(init.headers);
    headers.set("accept", "application/json");
    const bearer = await token();
    if (bearer) headers.set("authorization", `Bearer ${bearer}`);
    const res = await baseFetch(url, {
      ...init,
      headers,
      signal: AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS),
      ...(client ? { client } : {}),
    } as RequestInit);
    const text = await res.text();
    if (!res.ok) {
      throw new KcpIdentityPromptError(res.status, `${init.method ?? "GET"} ${new URL(url).pathname}: ${res.status} ${text.slice(0, 300)}`);
    }
    return text ? JSON.parse(text) : null;
  }

  async function get(name: string): Promise<MarketIdentity | null> {
    try {
      return parseMarketIdentity(await request(`${base}/${encodeURIComponent(name)}`));
    } catch (err) {
      if (err instanceof KcpIdentityPromptError && err.status === 404) return null;
      throw err;
    }
  }

  return {
    get,
    async writeStatus(name, status: MarketIdentityStatus) {
      refuseSecretMaterial(name, "status", status);
      assertMarketIdentityStatus(status);
      await request(`${base}/${encodeURIComponent(name)}/status`, {
        method: "PATCH",
        headers: { "content-type": "application/json-patch+json" },
        body: JSON.stringify([{ op: "add", path: "/status", value: status }]),
      });
    },
    async answerAssociation(name, callerDid, answer) {
      const retries = opts.conflictRetries ?? DEFAULT_CONFLICT_RETRIES;
      for (let attempt = 0;; attempt++) {
        const obj = await get(name);
        if (!obj?.spec) throw new Error(`marketidentity ${name} not found`);
        const spec = specWithAnswer(obj.spec, callerDid, answer);
        refuseSecretMaterial(name, "spec", spec);
        try {
          await request(`${base}/${encodeURIComponent(name)}`, {
            method: "PATCH",
            headers: { "content-type": "application/merge-patch+json" },
            body: JSON.stringify({
              metadata: { resourceVersion: obj.metadata?.resourceVersion },
              spec: {
                approvedAssociations: spec.approvedAssociations ?? null,
                rejectedAssociations: spec.rejectedAssociations ?? null,
              },
            }),
          });
          return;
        } catch (err) {
          if (err instanceof KcpIdentityPromptError && err.status === 409 && attempt < retries) continue;
          throw err;
        }
      }
    },
  };
}
