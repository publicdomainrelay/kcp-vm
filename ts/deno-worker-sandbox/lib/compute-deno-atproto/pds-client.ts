import type { PdsClient } from "./manifest-store.ts";

export async function createRemotePdsClient(
  pdsUrl: string,
  handle: string,
  password: string,
): Promise<{ client: PdsClient; did: string }> {
  const baseUrl = pdsUrl.replace(/\/$/, "");
  const sessionRes = await fetch(`${baseUrl}/xrpc/com.atproto.server.createSession`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ identifier: handle, password }),
  });
  if (!sessionRes.ok) throw new Error(`createSession failed: ${sessionRes.status}`);

  const session = await sessionRes.json() as {
    accessJwt: string;
    did: string;
    handle: string;
  };

  const token = session.accessJwt;
  const did = session.did;

  return {
    did,
    client: {
      async createRecord(
        repoDid: string,
        collection: string,
        record: Record<string, unknown>,
        rkey?: string,
      ) {
        const res = await fetch(`${baseUrl}/xrpc/com.atproto.repo.createRecord`, {
          method: "POST",
          headers: {
            "content-type": "application/json",
            authorization: `Bearer ${token}`,
          },
          body: JSON.stringify({ repo: repoDid, collection, record, rkey }),
        });
        if (!res.ok) {
          const err = await res.json().catch(() => ({}));
          throw new Error(`createRecord failed: ${res.status} ${JSON.stringify(err)}`);
        }
        const data = await res.json() as { uri: string; cid: string };
        return { uri: data.uri, cid: data.cid };
      },
      async getRecord(repoDid: string, collection: string, rkey: string) {
        const res = await fetch(
          `${baseUrl}/xrpc/com.atproto.repo.getRecord?repo=${encodeURIComponent(repoDid)}&collection=${encodeURIComponent(collection)}&rkey=${encodeURIComponent(rkey)}`,
          {
            headers: { authorization: `Bearer ${token}` },
          },
        );
        if (!res.ok) return null;
        const data = await res.json() as { uri: string; cid?: string; value: Record<string, unknown> };
        return { uri: data.uri, cid: data.cid ?? "", value: data.value };
      },
    },
  };
}
