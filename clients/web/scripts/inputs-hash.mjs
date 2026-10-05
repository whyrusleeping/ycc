// Content hash of the web client's build inputs. The production build writes it
// to internal/web/dist/manifest.json; internal/web's Go tests recompute it with
// the same algorithm (no Node needed) and fail when the committed bundle is
// stale relative to these sources.
//
// Algorithm "sha256-v1": collect the listed files plus every regular file under
// the listed directories (recursively, skipping dot-entries), as "/"-separated
// paths relative to clients/web; sort them by byte order; then
// sha256 over the concatenation of `${path}\n${sha256hex(content)}\n`.
import { createHash } from "node:crypto";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

export const ALGORITHM = "sha256-v1";
export const INPUT_FILES = [
  ".nvmrc",
  "index.html",
  "package.json",
  "package-lock.json",
  "tsconfig.json",
  "vite.config.ts",
];
export const INPUT_DIRS = ["scripts", "src"];

export const webRoot = fileURLToPath(new URL("..", import.meta.url));

function walk(root, rel, out) {
  for (const entry of readdirSync(join(root, rel), { withFileTypes: true })) {
    if (entry.name.startsWith(".")) continue;
    const child = rel + "/" + entry.name;
    if (entry.isDirectory()) walk(root, child, out);
    else if (entry.isFile()) out.push(child);
  }
}

export function inputFiles(root = webRoot) {
  const files = [];
  for (const file of INPUT_FILES) {
    if (statSync(join(root, file)).isFile()) files.push(file);
  }
  for (const dir of INPUT_DIRS) walk(root, dir, files);
  // Byte-order sort, matching Go's sort.Strings for these ASCII paths.
  return files.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

export function computeInputsHash(root = webRoot) {
  const outer = createHash("sha256");
  const files = inputFiles(root);
  for (const file of files) {
    const inner = createHash("sha256").update(readFileSync(join(root, file))).digest("hex");
    outer.update(`${file}\n${inner}\n`);
  }
  return { hash: outer.digest("hex"), files: files.length };
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  console.log(computeInputsHash().hash);
}
