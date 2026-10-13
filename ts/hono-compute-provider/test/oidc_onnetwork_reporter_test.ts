import { assert } from "@std/assert";
import { createOidcIssuer } from "@publicdomainrelay/oidc-issuer-hono";

const ISSUER = "http://127.0.0.1:9978";
const issuer = createOidcIssuer({ getIssuerUrl: () => ISSUER });

function reporter(userData: string): { script: string; unit: string[] } {
  const scriptStart = userData.indexOf("#!/usr/bin/env bash", userData.indexOf("send-onnetwork.sh"));
  const script = userData.slice(scriptStart, userData.indexOf("onNetwork reported OK", scriptStart));
  const unitStart = userData.indexOf("Description=Report onNetwork to compute provider");
  const unit = userData.slice(unitStart, userData.indexOf("WantedBy=", unitStart)).split("\n").map((l) => l.trim());
  return { script, unit };
}

Deno.test("the oidc reporter never publishes the guest's hostname as its address", async () => {
  const { userData } = await issuer.createProvisioningData("reporterteam", "#cloud-config\n");
  const { script } = reporter(userData);
  assert(!script.includes("hostname"), "no hostname fallback: a bare name is not an address a requester can reach");
  assert(script.includes("/run/guest-fqdn"), "reads the rendezvous file the transport writes");
});
