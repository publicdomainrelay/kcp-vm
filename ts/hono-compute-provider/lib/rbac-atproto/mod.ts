import { UnauthorizedException } from "@publicdomainrelay/oidc-issuer-abc";

export interface RBACPolicy {
  meta: Record<string, string>;
  schemas: Record<string, RBACSchema>;
}

export interface RBACSchema {
  properties: {
    capability: { enum: string[] };
    body?: unknown;
  };
}

export interface RBACRoleDefinition {
  iss?: string;
  aud?: string;
  sub: string;
  policies: string[];
}

export interface RBACRole {
  role_name: string;
  definition: RBACRoleDefinition;
}

export interface RBACProtects {
  service: string;
  scope?: string;
}

export interface RBACRecord {
  protects?: Record<string, RBACProtects>;
  policies: Record<string, RBACPolicy>;
  roles: Record<string, RBACRole>;
}

export async function resolvePDS(
  did: string,
  plcDirectoryUrl = "https://plc.directory",
): Promise<string> {
  let didDoc: { service?: { id: string; type: string; serviceEndpoint: string }[] };

  if (did.startsWith("did:plc:")) {
    const res = await fetch(`${plcDirectoryUrl}/${encodeURIComponent(did)}`);
    if (!res.ok) throw new Error(`PLC lookup failed for ${did}: ${res.status}`);
    didDoc = await res.json();
  } else if (did.startsWith("did:web:")) {
    const host = did.slice("did:web:".length).replace(/:/g, "/");
    const res = await fetch(`https://${host}/.well-known/did.json`);
    if (!res.ok) throw new Error(`did:web lookup failed for ${did}: ${res.status}`);
    didDoc = await res.json();
  } else {
    throw new Error(`unsupported DID method: ${did}`);
  }

  const pds = didDoc.service?.find(
    (s) => s.type === "AtprotoPersonalDataServer" || s.id === "#atproto_pds",
  )?.serviceEndpoint;
  if (!pds) throw new Error(`no PDS in DID document for ${did}`);
  return pds;
}

export interface RBACRecordEntry {
  uri: string;
  value: RBACRecord;
}

export async function listRBACRecords(pdsURL: string, did: string): Promise<RBACRecordEntry[]> {
  const out: RBACRecordEntry[] = [];
  let cursor = "";
  for (;;) {
    const url = new URL(`${pdsURL}/xrpc/com.atproto.repo.listRecords`);
    url.searchParams.set("repo", did);
    url.searchParams.set("collection", "com.fedproxy.rbac");
    url.searchParams.set("limit", "100");
    if (cursor) url.searchParams.set("cursor", cursor);
    const res = await fetch(url.toString());
    if (!res.ok) throw new Error(`listRecords failed pds=${pdsURL} did=${did}: ${res.status}`);
    const page = await res.json() as { records?: RBACRecordEntry[]; cursor?: string };
    for (const rec of page.records ?? []) out.push({ uri: rec.uri, value: rec.value });
    if (!page.cursor) return out;
    cursor = page.cursor;
  }
}

function globMatch(pattern: string, s: string): boolean {
  if (pattern === "*") return true;
  const parts = pattern.split("*");
  let rest = s;
  for (let i = 0; i < parts.length; i++) {
    const prefix = parts[i];
    if (i === parts.length - 1) return rest === prefix;
    if (prefix.length > 0) {
      const idx = rest.indexOf(prefix);
      if (idx < 0) return false;
      rest = rest.slice(idx + prefix.length);
    }
  }
  return true;
}

function findMatchingSchema(
  schemas: Record<string, RBACSchema>,
  path: string,
): RBACSchema | null {
  if (schemas[path]) return schemas[path];
  let best = "";
  let bestSchema: RBACSchema | null = null;
  for (const [pattern, schema] of Object.entries(schemas)) {
    if (globMatch(pattern, path) && pattern.length > best.length) {
      best = pattern;
      bestSchema = schema;
    }
  }
  return bestSchema;
}

const HTTP_METHOD_CAPABILITY: Record<string, string> = {
  GET: "read",
  HEAD: "read",
  OPTIONS: "read",
  POST: "create",
  PUT: "update",
  PATCH: "update",
  DELETE: "delete",
};

export function checkRBACPolicy(
  rbac: RBACRecord,
  sub: string,
  path: string,
  method: string,
): void {
  const capability = HTTP_METHOD_CAPABILITY[method.toUpperCase()];
  if (!capability) throw new UnauthorizedException(`unsupported HTTP method ${method}`);

  const matchingPolicies: string[] = [];
  for (const role of Object.values(rbac.roles)) {
    if (role.definition.sub === sub) {
      matchingPolicies.push(...role.definition.policies);
    }
  }

  if (matchingPolicies.length === 0) {
    throw new UnauthorizedException(`no matching role found for sub: ${sub}`);
  }

  const denials: string[] = [];
  for (const policyName of matchingPolicies) {
    const policy = rbac.policies[policyName];
    if (!policy) continue;

    const schema = findMatchingSchema(policy.schemas, path);
    if (!schema) continue;

    const allowed = schema.properties.capability.enum;
    if (allowed.includes(capability)) return;

    denials.push(
      `policy '${policyName}': capability '${capability}' not in [${allowed.join(", ")}] for path '${path}'`,
    );
  }

  if (denials.length > 0) throw new UnauthorizedException(denials.join("; "));
  throw new UnauthorizedException(
    `no policy covers path='${path}' for sub='${sub}'`,
  );
}
