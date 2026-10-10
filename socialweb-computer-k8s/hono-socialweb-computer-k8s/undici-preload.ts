import process from "node:process";

const versions = process.versions as Record<string, string>;
if (!versions.undici) versions.undici = "6.11.1";
