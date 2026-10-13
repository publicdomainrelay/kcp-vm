import { CopyObjectCommand, PutObjectCommand, S3Client } from "npm:@aws-sdk/client-s3@3.700.0";
import { parseArgs } from "jsr:@std/cli@1/parse-args";
import { walk } from "jsr:@std/fs@1/walk";
import { relative } from "jsr:@std/path@1";

const args = parseArgs(Deno.args, { string: ["dir", "prefix", "also"] });
const need = (name: string): string => {
  const v = Deno.env.get(name);
  if (!v) {
    console.error(`publish-spaces: ${name} is not set`);
    Deno.exit(2);
  }
  return v;
};
if (!args.dir || !args.prefix) {
  console.error("usage: publish-spaces.ts --dir <local dir> --prefix <bucket prefix> [--also <second prefix>]");
  Deno.exit(2);
}
const bucket = need("SPACES_BUCKET");
const region = need("SPACES_REGION");
const s3 = new S3Client({
  region: "us-east-1",
  endpoint: `https://${region}.digitaloceanspaces.com`,
  credentials: { accessKeyId: need("SPACES_ACCESS_KEY_ID"), secretAccessKey: need("SPACES_SECRET_ACCESS_KEY") },
});

const sha256 = async (path: string): Promise<string> => {
  const p = new Deno.Command("sha256sum", { args: [path], stdout: "piped" });
  const out = await p.output();
  if (!out.success) throw new Error(`sha256sum ${path} failed`);
  return new TextDecoder().decode(out.stdout).split(" ")[0];
};
const typeOf = (name: string): string =>
  name.endsWith(".json") ? "application/json" : name.endsWith(".txt") || name.endsWith("SUMS") ? "text/plain" : "application/octet-stream";

const files: Array<{ rel: string; path: string; size: number; sha256: string }> = [];
for await (const e of walk(args.dir, { includeDirs: false })) {
  const st = await Deno.stat(e.path);
  files.push({ rel: relative(args.dir, e.path), path: e.path, size: st.size, sha256: await sha256(e.path) });
}
files.sort((a, b) => a.rel.localeCompare(b.rel));
const manifest = {
  commit: Deno.env.get("CI_COMMIT_SHA") ?? "",
  ref: Deno.env.get("CI_COMMIT_REF_NAME") ?? "",
  pipeline: Deno.env.get("CI_PIPELINE_ID") ?? "",
  published: new Date().toISOString(),
  files: files.map(({ rel, size, sha256 }) => ({ path: rel, size, sha256 })),
};

const put = async (prefix: string, copyFrom?: string) => {
  for (const f of files) {
    const started = Date.now();
    const key = `${prefix}/${f.rel}`;
    if (copyFrom) {
      await s3.send(new CopyObjectCommand({
        Bucket: bucket,
        Key: key,
        CopySource: encodeURI(`${bucket}/${copyFrom}/${f.rel}`),
        MetadataDirective: "COPY",
        ACL: "private",
      }));
    } else {
      await s3.send(new PutObjectCommand({
        Bucket: bucket,
        Key: key,
        Body: await Deno.readFile(f.path),
        ContentType: typeOf(f.rel),
        ACL: "private",
        Metadata: { sha256: f.sha256 },
      }));
    }
    const how = copyFrom ? `copied from ${copyFrom}` : "uploaded";
    console.log(`${key} ${f.size} bytes sha256 ${f.sha256} ${how} (${((Date.now() - started) / 1000).toFixed(1)}s)`);
  }
  await s3.send(new PutObjectCommand({
    Bucket: bucket,
    Key: `${prefix}/manifest.json`,
    Body: new TextEncoder().encode(JSON.stringify(manifest, null, 2) + "\n"),
    ContentType: "application/json",
    ACL: "private",
  }));
  console.log(`${prefix}/manifest.json (${files.length} files)`);
};

await put(args.prefix);
if (args.also) await put(args.also, args.prefix);
