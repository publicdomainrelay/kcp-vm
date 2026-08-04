export const SECRETS_NSID = "com.publicdomainrelay.temp.compute.secrets";
export const SECRETS_ROUTE = `/xrpc/${SECRETS_NSID}.getSecrets`;
export const SECRETS_SCOPE = "secrets";
export const FEDPROXY_RBAC_NSID = "com.fedproxy.rbac";
export const DEFAULT_ACCEPT_PATH_VM = "/root/secrets/publicdomainrelay.com/market/accept.json";

export interface SecretEntry {
  path: string;
  value: string;
}

export interface RbacSchemaShape {
  type?: string;
  $schema?: string;
  required?: string[];
  properties: { capability: { enum: string[] } };
}

export interface RbacPolicyShape {
  meta: Record<string, string>;
  schemas: Record<string, RbacSchemaShape>;
}

export interface RbacRoleShape {
  role_name: string;
  definition: { iss?: string; aud?: string; sub: string; policies: string[] };
}

export interface RbacProtectsShape {
  service: string;
  scope?: string;
}

export interface RbacRecordShape {
  $type?: string;
  protects?: Record<string, RbacProtectsShape>;
  policies: Record<string, RbacPolicyShape>;
  roles: Record<string, RbacRoleShape>;
  createdAt?: string;
}

export class InvalidSecretsFileError extends Error {}

/**
 * Parse the --secrets payload: [{"path": "/some/path", "value": "secret-value"}].
 * Rejects relative paths and traversal so a malformed file cannot write outside
 * the location the operator named.
 */
export function parseSecretsFile(text: string): SecretEntry[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    throw new InvalidSecretsFileError(`secrets file is not valid JSON: ${String(err)}`);
  }
  if (!Array.isArray(parsed)) {
    throw new InvalidSecretsFileError("secrets file must be an array of {path, value}");
  }
  const seen = new Set<string>();
  return parsed.map((raw, i) => {
    if (!raw || typeof raw !== "object") {
      throw new InvalidSecretsFileError(`secrets[${i}] must be an object`);
    }
    const { path, value } = raw as Record<string, unknown>;
    if (typeof path !== "string" || path.length === 0) {
      throw new InvalidSecretsFileError(`secrets[${i}].path must be a non-empty string`);
    }
    if (!path.startsWith("/")) {
      throw new InvalidSecretsFileError(`secrets[${i}].path must be absolute: ${path}`);
    }
    if (path.split("/").includes("..")) {
      throw new InvalidSecretsFileError(`secrets[${i}].path must not traverse: ${path}`);
    }
    if (typeof value !== "string") {
      throw new InvalidSecretsFileError(`secrets[${i}].value must be a string`);
    }
    if (seen.has(path)) {
      throw new InvalidSecretsFileError(`secrets contains duplicate path: ${path}`);
    }
    seen.add(path);
    return { path, value };
  });
}

export interface SecretsRbacContext {
  role: string;
  subject: string;
  issuerUri: string;
  expectedAud: string;
  serviceUrl: string;
  route?: string;
  createdAt?: string;
}

/**
 * Grant exactly one thing: read on the secrets route, for one subject, from one
 * issuer, under one audience. Never published -- the ephemeral server holds it
 * in memory and evaluates it with the shared checkRBACPolicy.
 */
export function buildSecretsRbacRecord(ctx: SecretsRbacContext): RbacRecordShape {
  const route = ctx.route ?? SECRETS_ROUTE;
  const policyName = `${ctx.role}-secrets-read`;
  return {
    $type: FEDPROXY_RBAC_NSID,
    protects: {
      [ctx.role]: { service: ctx.serviceUrl, scope: SECRETS_SCOPE },
    },
    roles: {
      [ctx.role]: {
        role_name: ctx.role,
        definition: {
          iss: ctx.issuerUri,
          aud: ctx.expectedAud,
          sub: ctx.subject,
          policies: [policyName],
        },
      },
    },
    policies: {
      [policyName]: {
        meta: { policy: "secrets-read" },
        schemas: {
          [route]: {
            type: "object",
            $schema: "http://json-schema.org/draft-07/schema#",
            required: ["capability"],
            properties: { capability: { enum: ["read"] } },
          },
        },
      },
    },
    createdAt: ctx.createdAt ?? new Date().toISOString(),
  };
}
