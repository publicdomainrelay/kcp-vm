async function post(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = new Error((data && data.error) || `HTTP ${res.status}`);
    // Carried so the caller can tell "this session is no longer usable" from
    // "the service is unreachable": the first needs a sign-in, the second a retry.
    err.status = res.status;
    throw err;
  }
  return data;
}

export function cocoreStatus() {
  return post('/cocore/status');
}

export function cocorePairStart() {
  return post('/cocore/pair/start');
}

// No session: the pairId is the whole request, and the server advances a pairing
// it already holds. Nothing about this call needs an access token.
export function cocorePairPoll(pairId) {
  return post('/cocore/pair/poll', { pairId });
}
