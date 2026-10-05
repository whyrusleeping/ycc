// Production build: bundles the client into internal/web/dist (emptied first by
// Vite) and writes manifest.json with the content hash of the build inputs, so
// a Go test can detect a committed bundle that is stale relative to its sources.
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "vite";
import { ALGORITHM, INPUT_DIRS, INPUT_FILES, computeInputsHash, webRoot } from "./inputs-hash.mjs";

const outDir = fileURLToPath(new URL("../../../internal/web/dist", import.meta.url));

await build({ root: webRoot, configFile: join(webRoot, "vite.config.ts"), logLevel: "info" });

const { hash, files } = computeInputsHash();
const manifest = {
  algorithm: ALGORITHM,
  inputs_hash: hash,
  input_files: INPUT_FILES,
  input_dirs: INPUT_DIRS,
  file_count: files,
};
writeFileSync(join(outDir, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
console.log(`wrote ${join(outDir, "manifest.json")} (${files} inputs, ${hash.slice(0, 12)})`);
