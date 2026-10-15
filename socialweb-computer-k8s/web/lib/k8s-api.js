export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || `HTTP ${status}`);
    this.status = status;
    this.code = code;
  }
}

async function call(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, data && data.error, data && data.message);
  return data;
}

export async function currentSession() {
  const res = await fetch('/session', { credentials: 'same-origin' });
  if (res.status === 401) return null;
  if (!res.ok) throw new ApiError(res.status, 'SessionUnavailable', 'could not read the sign-in');
  return res.json();
}

export async function startSignIn(handle) {
  const res = await fetch('/oauth/start', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ handle }),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok || !data || !data.authorizationUrl) {
    throw new ApiError(res.status, data && data.error, (data && data.error) || `HTTP ${res.status}`);
  }
  return data.authorizationUrl;
}

export async function signOut() {
  await fetch('/session', { method: 'DELETE', credentials: 'same-origin' });
}

export async function fetchConfig() {
  const res = await fetch('/api/config');
  if (!res.ok) throw new ApiError(res.status, 'ConfigUnavailable', 'could not load the door configuration');
  return res.json();
}

export async function listClusters() {
  const out = await call('/api/clusters/list');
  return Array.isArray(out.clusters) ? out.clusters : [];
}

export function createCluster(name) {
  return call('/api/clusters/create', { name });
}

export function clusterUp(name) {
  return call('/api/clusters/up', { name });
}

export function clusterDown(name) {
  return call('/api/clusters/down', { name });
}

export function deleteCluster(name) {
  return call('/api/clusters/delete', { name });
}

export function fetchKubeconfig(name, ttlSeconds, noExpiry) {
  const body = { name };
  if (noExpiry) body.noExpiry = true;
  else if (ttlSeconds) body.ttlSeconds = ttlSeconds;
  return call('/api/clusters/kubeconfig', body);
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
