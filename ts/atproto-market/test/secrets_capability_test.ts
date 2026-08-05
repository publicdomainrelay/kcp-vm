// Unit tests for the secrets capability: file parsing, the in-memory RBAC grant,
// and the token gate on the ephemeral server.
//
// Tokens are signed with a throwaway RSA key and served through a stub issuer, so
// the gate is exercised end to end (discovery -> JWKS -> jwtVerify -> RBAC) without
// standing up a provider.
//
// Run:
//   deno test -A test/secrets_capability_test.ts

import { assert, assertEquals, assertRejects, assertThrows } from "@std/assert";
import * as jose from "jose";
import {
  buildSecretsRbacRecord,
  InvalidSecretsFileError,
  parseSecretsFile,
  SECRETS_ROUTE,
} from "@publicdomainrelay/secrets-common";
import { createSecretsAuthorizer } from "@publicdomainrelay/secrets-oidc";
import { createSecretsApp } from "@publicdomainrelay/hono-factory-secrets-oidc";
import { deriveGrantVars, subjectKeyOf } from "@publicdomainrelay/guest-capability-abc";

const ISSUER = "https://issuer.example";
const REQUESTER_DID = "did:plc:requester123";
const EXPECTED_AUD = `api://ATProto?actx=${REQUESTER_DID}`;
const ROLE = "compute-abc123";
const SUBJECT = `actx:provider789:plc:requester123:role:${ROLE}`;

const keys = await crypto.subtle.generateKey(
  {
    name: "RSASSA-PKCS1-v1_5",
    modulusLength: 2048,
    publicExponent: new Uint8Array([1, 0, 1]),
    hash: "SHA-256",
  },
  true,
  ["sign", "verify"],
) as CryptoKeyPair;

const publicJwk = await jose.exportJWK(keys.publicKey);
publicJwk.use = "sig";
publicJwk.alg = "RS256";
publicJwk.kid = await jose.calculateJwkThumbprint(publicJwk);

function stubIssuerFetch(): typeof fetch {
  return ((input: string | URL | Request) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url === `${ISSUER}/.well-known/openid-configuration`) {
      return Promise.resolve(
        new Response(JSON.stringify({ issuer: ISSUER, jwks_uri: `${ISSUER}/.well-known/jwks` }), {
          headers: { "content-type": "application/json" },
        }),
      );
    }
    if (url === `${ISSUER}/.well-known/jwks`) {
      return Promise.resolve(
        new Response(JSON.stringify({ keys: [publicJwk] }), {
          headers: { "content-type": "application/json" },
        }),
      );
    }
    return Promise.resolve(new Response("not found", { status: 404 }));
  }) as typeof fetch;
}

async function mintToken(
  over: { sub?: string; aud?: string; iss?: string; expSecFromNow?: number } = {},
): Promise<string> {
  return await new jose.SignJWT({})
    .setProtectedHeader({ alg: "RS256", kid: publicJwk.kid })
    .setIssuer(over.iss ?? ISSUER)
    .setAudience(over.aud ?? EXPECTED_AUD)
    .setSubject(over.sub ?? SUBJECT)
    .setIssuedAt()
    .setExpirationTime(Math.floor(Date.now() / 1000) + (over.expSecFromNow ?? 300))
    .sign(keys.privateKey);
}

function grantedAuthorizer() {
  const authorizer = createSecretsAuthorizer({ fetch: stubIssuerFetch() });
  authorizer.install({
    rbac: buildSecretsRbacRecord({
      role: ROLE,
      subject: SUBJECT,
      issuerUri: ISSUER,
      expectedAud: EXPECTED_AUD,
      serviceUrl: "https://sec-abc.relay.local",
    }),
    issuerUri: ISSUER,
    expectedAud: EXPECTED_AUD,
  });
  return authorizer;
}

Deno.test("parseSecretsFile accepts the documented shape", () => {
  const entries = parseSecretsFile(
    JSON.stringify([{ path: "/some/path", value: "secret-value" }]),
  );
  assertEquals(entries, [{ path: "/some/path", value: "secret-value" }]);
});

Deno.test("parseSecretsFile rejects malformed input", () => {
  assertThrows(() => parseSecretsFile("not json"), InvalidSecretsFileError);
  assertThrows(() => parseSecretsFile('{"path":"/a","value":"b"}'), InvalidSecretsFileError);
  assertThrows(
    () => parseSecretsFile('[{"path":"relative","value":"b"}]'),
    InvalidSecretsFileError,
  );
  assertThrows(
    () => parseSecretsFile('[{"path":"/a/../../b","value":"c"}]'),
    InvalidSecretsFileError,
  );
  assertThrows(
    () => parseSecretsFile('[{"path":"/a","value":"x"},{"path":"/a","value":"y"}]'),
    InvalidSecretsFileError,
  );
});

Deno.test("parseSecretsFile auto-stringifies non-string JSON values", () => {
  const entries = parseSecretsFile(JSON.stringify([
    { path: "/a", value: { nested: [1, 2], ok: true } },
    { path: "/b", value: [1, "two", { three: 3 }] },
    { path: "/c", value: 42 },
    { path: "/d", value: true },
    { path: "/e", value: null },
  ]));
  assertEquals(entries, [
    { path: "/a", value: '{"nested":[1,2],"ok":true}' },
    { path: "/b", value: '[1,"two",{"three":3}]' },
    { path: "/c", value: "42" },
    { path: "/d", value: "true" },
    { path: "/e", value: "null" },
  ]);
});

const WIF = {
  issuer_uri: ISSUER,
  actx: "provider789",
  subject: "actx:{actx}:plc:{did-plc-key}:role:{role}",
};

Deno.test("deriveGrantVars matches the provider's tag-derived subject", () => {
  const vars = deriveGrantVars({
    cfg: WIF,
    subjectDid: REQUESTER_DID,
    audienceDid: REQUESTER_DID,
    role: ROLE,
  });
  assertEquals(vars.subject, SUBJECT);
  assertEquals(vars.expectedAud, EXPECTED_AUD);
  assertEquals(vars.issuerUri, ISSUER);
});

Deno.test("deriveGrantVars takes the subject from the record author, not the relay DID", () => {
  // Under OAuth the market records are authored by the user's PDS DID while the
  // requester's relay keeps a separate local DID. The provider tags the guest
  // from the authoring DID, so the subject must follow it or every fetch 401s.
  const AUTHOR_DID = "did:plc:lpfuqerea3deuoyrn7ojser4";
  const RELAY_DID = "did:plc:fs2tpkl4xrlsjf4hitl4fpuo";
  const vars = deriveGrantVars({
    cfg: WIF,
    subjectDid: AUTHOR_DID,
    audienceDid: RELAY_DID,
    role: ROLE,
  });
  assertEquals(vars.subject, `actx:provider789:plc:lpfuqerea3deuoyrn7ojser4:role:${ROLE}`);
  assertEquals(vars.expectedAud, `api://ATProto?actx=${RELAY_DID}`);
});

Deno.test("subjectKeyOf takes the DID tail, matching the provider's droplet tag", () => {
  assertEquals(subjectKeyOf("did:plc:abc123"), "abc123");
  assertEquals(subjectKeyOf("did:web:host.example"), "host.example");
});

Deno.test("authorizer denies until a grant is installed", async () => {
  const authorizer = createSecretsAuthorizer({ fetch: stubIssuerFetch() });
  const token = await mintToken();
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "GET"));
});

Deno.test("authorizer accepts the provider-issued token for the granted subject", async () => {
  const authorizer = grantedAuthorizer();
  const { sub } = await authorizer.authorize(await mintToken(), SECRETS_ROUTE, "GET");
  assertEquals(sub, SUBJECT);
});

Deno.test("authorizer rejects another VM's subject", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken({ sub: "actx:provider789:plc:requester123:role:compute-other" });
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "GET"));
});

Deno.test("authorizer rejects a token audienced at someone else", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken({ aud: "api://ATProto?actx=did:plc:someoneelse" });
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "GET"));
});

Deno.test("authorizer rejects an expired token", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken({ expSecFromNow: -600 });
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "GET"));
});

Deno.test("authorizer rejects a write to the read-only route", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken();
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "POST"));
});

Deno.test("authorizer rejects an unrelated route", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken();
  await assertRejects(() => authorizer.authorize(token, "/xrpc/other", "GET"));
});

Deno.test("revoke stops serving a previously valid token", async () => {
  const authorizer = grantedAuthorizer();
  const token = await mintToken();
  await authorizer.authorize(token, SECRETS_ROUTE, "GET");
  authorizer.revoke();
  assertEquals(authorizer.granted, false);
  await assertRejects(() => authorizer.authorize(token, SECRETS_ROUTE, "GET"));
});

Deno.test("server serves the bundle only to an authorized guest", async () => {
  const authorizer = grantedAuthorizer();
  const secrets = [{ path: "/some/path", value: "secret-value" }];
  const app = createSecretsApp({ authorizer, getSecrets: () => secrets });

  const anonymous = await app.request(`http://secrets.local${SECRETS_ROUTE}`);
  assertEquals(anonymous.status, 401);

  const token = await mintToken();
  const authorized = await app.request(`http://secrets.local${SECRETS_ROUTE}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  assertEquals(authorized.status, 200);
  assertEquals(await authorized.json(), secrets);

  authorizer.revoke();
  const afterDelete = await app.request(`http://secrets.local${SECRETS_ROUTE}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  assertEquals(afterDelete.status, 401);
});

Deno.test("grant covers exactly one route, one subject, one issuer", () => {
  const rbac = buildSecretsRbacRecord({
    role: ROLE,
    subject: SUBJECT,
    issuerUri: ISSUER,
    expectedAud: EXPECTED_AUD,
    serviceUrl: "https://sec-abc.relay.local",
  });
  assertEquals(Object.keys(rbac.roles), [ROLE]);
  assertEquals(rbac.roles[ROLE].definition.sub, SUBJECT);
  assertEquals(rbac.roles[ROLE].definition.iss, ISSUER);
  assertEquals(rbac.roles[ROLE].definition.aud, EXPECTED_AUD);
  const policy = rbac.policies[`${ROLE}-secrets-read`];
  assertEquals(Object.keys(policy.schemas), [SECRETS_ROUTE]);
  assertEquals(policy.schemas[SECRETS_ROUTE].properties.capability.enum, ["read"]);
  // getRBACRecord in rbac-atproto refuses records that protect nothing.
  assert(rbac.protects && Object.keys(rbac.protects).length === 1);
});
