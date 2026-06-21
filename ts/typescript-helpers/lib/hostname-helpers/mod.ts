export function hostnameOnly(host: string): string {
  let h = host;
  if (h.startsWith("[")) {
    const end = h.indexOf("]");
    if (end !== -1) h = h.slice(1, end);
  }
  const portIdx = h.lastIndexOf(":");
  if (portIdx !== -1 && portIdx > (h.startsWith("[") ? h.indexOf("]") : 0)) {
    h = h.slice(0, portIdx);
  }
  return h;
}

export function hostnameToDid(hostname: string): string {
  return `did:web:${hostnameOnly(hostname)}`;
}

export function didToSubdomain(did: string): string {
  return did.replaceAll(":", "-").toLowerCase();
}
