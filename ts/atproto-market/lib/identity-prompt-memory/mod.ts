import type { MarketIdentity, MarketIdentitySpec, MarketIdentityStatus } from "@publicdomainrelay/socialweb-computer-k8s-api";
import type { IdentityAnswers, IdentityPrompt } from "@publicdomainrelay/identity-prompt-abc";
import { nextGeneration, refuseSecretMaterial, specWithAnswer } from "@publicdomainrelay/identity-prompt-abc";

export interface MemoryIdentityPrompt extends IdentityPrompt, IdentityAnswers {
  readonly writes: Array<{ name: string; status: MarketIdentityStatus }>;
  readonly specWrites: Array<{ name: string; spec: MarketIdentitySpec }>;
  put(obj: MarketIdentity): void;
}

export function createMemoryIdentityPrompt(initial: MarketIdentity[] = []): MemoryIdentityPrompt {
  const objects = new Map<string, MarketIdentity>();
  const writes: MemoryIdentityPrompt["writes"] = [];
  const specWrites: MemoryIdentityPrompt["specWrites"] = [];
  const put = (obj: MarketIdentity) => {
    const name = obj.metadata?.name;
    if (!name) throw new Error("a MarketIdentity needs metadata.name");
    objects.set(name, structuredClone({ ...obj, metadata: { ...obj.metadata, generation: obj.metadata?.generation ?? 1 } }));
  };
  initial.forEach(put);
  return {
    writes,
    specWrites,
    put,
    get(name) {
      const obj = objects.get(name);
      return Promise.resolve(obj ? structuredClone(obj) : null);
    },
    writeStatus(name, status) {
      try {
        refuseSecretMaterial(name, "status", status);
      } catch (err) {
        return Promise.reject(err);
      }
      const obj = objects.get(name);
      if (!obj) return Promise.reject(new Error(`marketidentity ${name} not found`));
      obj.status = structuredClone(status);
      writes.push({ name, status: structuredClone(status) });
      return Promise.resolve();
    },
    answerAssociation(name, callerDid, answer) {
      const obj = objects.get(name);
      if (!obj?.spec) return Promise.reject(new Error(`marketidentity ${name} not found`));
      const spec = specWithAnswer(obj.spec, callerDid, answer);
      try {
        refuseSecretMaterial(name, "spec", spec);
      } catch (err) {
        return Promise.reject(err);
      }
      obj.spec = spec;
      obj.metadata = { ...obj.metadata, generation: nextGeneration(obj) };
      specWrites.push({ name, spec: structuredClone(spec) });
      return Promise.resolve();
    },
  };
}
