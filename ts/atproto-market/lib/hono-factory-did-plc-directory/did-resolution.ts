import type {
  DidDocument,
  LogEntry,
  Operation,
  PlcOp,
  LegacyCreateOp,
} from "@publicdomainrelay/did-plc";

export function resolveDidDocument(
  did: string,
  currentOps: LogEntry[],
): DidDocument | null {
  if (currentOps.length === 0) return null;

  const lastOp = currentOps[currentOps.length - 1].operation;
  if (lastOp.type === "plc_tombstone") return null;

  let verificationMethods: Record<string, string> = {};
  let alsoKnownAs: string[] = [];
  let services: Record<string, { type: string; endpoint: string }> = {};

  for (const entry of currentOps) {
    const op = entry.operation;
    if (op.type === "plc_operation") {
      verificationMethods = { ...verificationMethods, ...op.verificationMethods };
      alsoKnownAs = dedupe([...alsoKnownAs, ...op.alsoKnownAs]);
      services = { ...services, ...op.services };
    } else if (op.type === "create") {
      verificationMethods = { atproto: op.signingKey, ...verificationMethods };
      if (op.handle) alsoKnownAs = dedupe([...alsoKnownAs, op.handle]);
      services = {
        atproto_pds: { type: "AtprotoPersonalDataServer", endpoint: op.service },
        ...services,
      };
    }
  }

  const vm: DidDocument["verificationMethod"] = [];
  for (const [id, key] of Object.entries(verificationMethods)) {
    const multibaseKey = key.startsWith("did:key:") ? key.slice("did:key:".length) : key;
    vm.push({
      id: `#${id}`,
      type: "Multikey",
      controller: did,
      publicKeyMultibase: multibaseKey,
    });
  }

  const svc: DidDocument["service"] = [];
  for (const [id, s] of Object.entries(services)) {
    svc.push({
      id: `#${id}`,
      type: s.type,
      serviceEndpoint: s.endpoint,
    });
  }

  return {
    "@context": [
      "https://www.w3.org/ns/did/v1",
      "https://w3id.org/security/multikey/v1",
    ],
    id: did,
    alsoKnownAs: alsoKnownAs.length > 0 ? alsoKnownAs : undefined,
    verificationMethod: vm.length > 0 ? vm : undefined,
    service: svc.length > 0 ? svc : undefined,
  };
}

function dedupe<T>(arr: T[]): T[] {
  return [...new Set(arr)];
}
