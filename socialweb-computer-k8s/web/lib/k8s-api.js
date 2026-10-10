const enc = new TextEncoder();

function base64url(bytes) {
  let binary = '';
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

function randomHex(n) {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

async function signingKey(session) {
  const { key_ops, ext, use, alg, ...jwk } = session.dpopPrivateJwk;
  return crypto.subtle.importKey('jwk', jwk, { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
}

export async function requestProof(session, method, url) {
  const target = new URL(url, window.location.origin);
  const header = { typ: 'swc-proof+jwt', alg: 'ES256' };
  const claims = {
    iss: session.userDid,
    htm: method,
    htu: `${target.origin}${target.pathname}`,
    iat: Math.floor(Date.now() / 1000),
    jti: randomHex(16),
  };
  const input = `${base64url(enc.encode(JSON.stringify(header)))}.${base64url(enc.encode(JSON.stringify(claims)))}`;
  const sig = await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, await signingKey(session), enc.encode(input));
  return `${input}.${base64url(new Uint8Array(sig))}`;
}

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || `HTTP ${status}`);
    this.status = status;
    this.code = code;
  }
}

async function call(session, path, body) {
  const res = await fetch(path, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      authorization: `SWC-Proof ${await requestProof(session, 'POST', path)}`,
    },
    body: JSON.stringify(body || {}),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, data && data.error, data && data.message);
  return data;
}

export async function fetchConfig() {
  const res = await fetch('/api/config');
  if (!res.ok) throw new ApiError(res.status, 'ConfigUnavailable', 'could not load the door configuration');
  return res.json();
}

export async function listClusters(session) {
  const out = await call(session, '/api/clusters/list');
  return Array.isArray(out.clusters) ? out.clusters : [];
}

export function createCluster(session, name) {
  return call(session, '/api/clusters/create', { name });
}

export function clusterUp(session, name) {
  return call(session, '/api/clusters/up', { name });
}

export function clusterDown(session, name) {
  return call(session, '/api/clusters/down', { name });
}

export function deleteCluster(session, name) {
  return call(session, '/api/clusters/delete', { name });
}

export function fetchKubeconfig(session, name, ttlSeconds) {
  return call(session, '/api/clusters/kubeconfig', ttlSeconds ? { name, ttlSeconds } : { name });
}

export function saveText(filename, text) {
  const blob = new Blob([text], { type: 'application/yaml' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
