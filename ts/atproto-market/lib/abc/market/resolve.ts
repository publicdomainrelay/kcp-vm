import type { Resolved, StrongRef } from "@publicdomainrelay/market-common";

export interface AtUriParts {
  repo: string;
  collection: string;
  rkey: string;
}

export function parseAtUri(uri: string): AtUriParts {
  const parts = uri.slice("at://".length).split("/");
  return { repo: parts[0], collection: parts[1], rkey: parts[2] };
}

export function atUriAuthority(uri: string): string {
  return uri.replace("at://", "").split("/")[0];
}

export function nsidFromUri(uri: string): string {
  const { collection } = parseAtUri(uri);
  return collection;
}

export interface RecordRef {
  uri: string;
  cid: string;
}

export function refKey(ref: RecordRef): string {
  return `${ref.uri}#${ref.cid}`;
}

export function refsEqual(a: RecordRef, b: RecordRef): boolean {
  return a.uri === b.uri && a.cid === b.cid;
}

export function stripResolved<T>(resolved: Resolved<T>): T {
  const { _uri: _, _cid: __, ...rest } = resolved as Resolved<T> & { _uri: string; _cid: string };
  return rest as unknown as T;
}

export function resolvedRef<T>(resolved: Resolved<T>): StrongRef {
  return { $type: "com.atproto.repo.strongRef", uri: resolved._uri, cid: resolved._cid } as StrongRef;
}

export class RecordVersionError extends Error {
  constructor(version: string) {
    super(`unknown record version ${version}`);
  }
}

export interface RecordResolver {
  resolve<T>(ref: RecordRef): Promise<Resolved<T>>;
}
