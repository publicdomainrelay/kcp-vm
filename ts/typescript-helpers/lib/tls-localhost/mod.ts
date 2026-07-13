// Generate self-signed CA + server cert for local TLS development.
// Uses @peculiar/x509 + Web Crypto API — no external CLI tools needed.
//
//   import { generateLocalhostTlsCert } from "@publicdomainrelay/tls-localhost";
//   const { caCertPem, serverCertPem, serverKeyPem } = await generateLocalhostTlsCert();

import * as x509 from "@peculiar/x509";

export interface LocalhostTlsCert {
  /** CA certificate PEM — inject into guest trust store. */
  caCertPem: string;
  /** CA private key PEM — kept for signing, not distributed. */
  caKeyPem: string;
  /** Server certificate PEM for *.localhost — used by Deno.serve. */
  serverCertPem: string;
  /** Server private key PEM — used by Deno.serve. */
  serverKeyPem: string;
}

function pemBlock(label: string, der: Uint8Array): string {
  const b64 = btoa(String.fromCharCode(...der));
  // Wrap at 64 chars
  const lines: string[] = [`-----BEGIN ${label}-----`];
  for (let i = 0; i < b64.length; i += 64) lines.push(b64.slice(i, i + 64));
  lines.push(`-----END ${label}-----`);
  return lines.join("\n") + "\n";
}

async function exportCryptoKeyPem(key: CryptoKey): Promise<string> {
  const der = new Uint8Array(await crypto.subtle.exportKey("pkcs8", key));
  return pemBlock("PRIVATE KEY", der);
}

export interface GenerateLocalhostTlsCertOpts {
  /** Extra IP SANs, e.g. the container gateway IP so guests connecting to
   * https://<gateway-ip>:port pass cert validation. */
  extraIpSans?: string[];
  /** Extra DNS SANs. */
  extraDnsSans?: string[];
}

export async function generateLocalhostTlsCert(
  opts?: GenerateLocalhostTlsCertOpts,
): Promise<LocalhostTlsCert> {
  // ── CA keypair ──────────────────────────────────────────────────────
  const caKeyPair = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign"],
  );

  const caName = "CN=Test Relay CA,O=PDR,C=US";
  const caCert = await x509.X509CertificateGenerator.createSelfSigned({
    serialNumber: "01",
    name: caName,
    notBefore: new Date(),
    notAfter: new Date(Date.now() + 365 * 86400000 * 10), // 10 years
    signingAlgorithm: { name: "ECDSA", hash: "SHA-256" },
    keys: caKeyPair as CryptoKeyPair,
    extensions: [
      new x509.BasicConstraintsExtension(true, 0),
      new x509.KeyUsagesExtension(
        x509.KeyUsageFlags.keyCertSign | x509.KeyUsageFlags.cRLSign,
      ),
    ],
  });

  // ── Server keypair ──────────────────────────────────────────────────
  const serverKeyPair = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" },
    true,
    ["sign"],
  );

  const serverCert = await x509.X509CertificateGenerator.create({
    serialNumber: "02",
    issuer: caName,
    subject: "CN=*.localhost,O=PDR,C=US",
    publicKey: serverKeyPair.publicKey,
    signingKey: caKeyPair.privateKey,
    notBefore: new Date(),
    notAfter: new Date(Date.now() + 365 * 86400000), // 1 year
    signingAlgorithm: { name: "ECDSA", hash: "SHA-256" },
    extensions: [
      new x509.BasicConstraintsExtension(false),
      new x509.KeyUsagesExtension(
        x509.KeyUsageFlags.digitalSignature | x509.KeyUsageFlags.keyEncipherment,
      ),
      new x509.SubjectAlternativeNameExtension([
        { type: "dns", value: "*.localhost" },
        { type: "dns", value: "localhost" },
        ...(opts?.extraDnsSans ?? []).map((value) => ({ type: "dns" as const, value })),
        ...(opts?.extraIpSans ?? []).map((value) => ({ type: "ip" as const, value })),
      ]),
    ],
  });

  return {
    caCertPem: pemBlock("CERTIFICATE", new Uint8Array(caCert.rawData)),
    caKeyPem: await exportCryptoKeyPem(caKeyPair.privateKey),
    serverCertPem: pemBlock("CERTIFICATE", new Uint8Array(serverCert.rawData)),
    serverKeyPem: await exportCryptoKeyPem(serverKeyPair.privateKey),
  };
}
