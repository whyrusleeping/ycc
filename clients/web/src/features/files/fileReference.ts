// File references parsed from agent-written markdown and tool arguments: a
// port of YccKit's FileReference (clients/ios). Paths are root-relative,
// `/`-separated and normalized; anything that would escape the project root
// (a `..` above it, an absolute path outside every known root) is not a
// reference.

export interface LineRange {
  start: number;
  end: number;
}

export interface FileReference {
  /** Root-relative, normalized path; "" is the root itself. */
  path: string;
  isDirectory: boolean;
  lines: LineRange | null;
}

export interface FileLinkContext {
  project: string;
  sessionId: string;
  /** Root-relative directory relative links resolve against ("" for transcripts). */
  baseDirectory: string;
  /** Absolute daemon-host roots stripped from absolute paths (project path, worktree). */
  absoluteRoots: string[];
}

export function transcriptLinkContext(project: string, sessionId: string, absoluteRoots: string[] = []): FileLinkContext {
  return { project, sessionId, baseDirectory: "", absoluteRoots };
}

const KNOWN_EXTENSIONS = new Set(
  (
    "go mod sum work swift rs py pyi ipynb js mjs cjs jsx ts tsx c h cc cpp cxx hpp hh m mm java kt kts " +
    "scala cs rb php pl lua zig nim ex exs erl hs ml dart r jl sh bash zsh fish ps1 proto graphql sql " +
    "md markdown mdx rst txt adoc org toml yaml yml json jsonl ndjson xml plist ini cfg conf env " +
    "properties lock csv tsv html htm css scss sass less svg png jpg jpeg gif webp ico bmp pdf gradle " +
    "cmake mk make nix tf hcl dockerfile gitignore patch diff log vim el xcconfig pbxproj entitlements " +
    "strings storyboard xib pbtxt textproto tmpl tpl j2 service"
  ).split(" "),
);

const KNOWN_FILE_NAMES = new Set(
  (
    "Makefile GNUmakefile Dockerfile Containerfile Justfile justfile Rakefile Gemfile Procfile " +
    "Vagrantfile Brewfile Podfile Cartfile LICENSE LICENCE COPYING NOTICE AUTHORS CODEOWNERS README " +
    "CHANGELOG CONTRIBUTING VERSION BUILD WORKSPACE"
  ).split(" "),
);

export function looksLikeFileName(name: string): boolean {
  if (KNOWN_FILE_NAMES.has(name)) return true;
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return false;
  return KNOWN_EXTENSIONS.has(name.slice(dot + 1).toLowerCase());
}

const PATH_CHARS = /^[A-Za-z0-9_./@+-]+$/;

/**
 * An inline code span that looks like a repo path — the dominant way agents
 * cite files. Conservative: no whitespace, a path-ish character set, and a
 * trailing `/` or a last component with a known extension/file name, so
 * `go test ./...`, `session.Manager` and `--flag` stay plain.
 */
export function fromCodeSpan(raw: string, context?: FileLinkContext): FileReference | null {
  let text = raw.trim();
  if (!text || text.length > 300 || /\s/.test(text) || text.includes("://")) return null;
  if (text[0] === "-" || text[0] === "~" || text[0] === "$") return null;
  let lines: LineRange | null = null;
  const hash = text.indexOf("#");
  if (hash >= 0) {
    const fragment = lineFragment(text.slice(hash + 1));
    if (!fragment) return null;
    lines = fragment;
    text = text.slice(0, hash);
  }
  const suffix = stripLineSuffix(text);
  if (suffix) {
    text = suffix.path;
    lines = lines ?? suffix.lines;
  }
  if (!text || !PATH_CHARS.test(text) || !/[A-Za-z]/.test(text)) return null;
  if (!text.endsWith("/")) {
    const last = text.split("/").filter(Boolean).pop() ?? text;
    if (!looksLikeFileName(last)) return null;
  }
  return resolve(text, lines, { ...(context ?? transcriptLinkContext("", "")), baseDirectory: "" });
}

/** Parse a markdown link destination that is not an external URL. */
export function fromLink(raw: string, context?: FileLinkContext): FileReference | null {
  const ctx = context ?? transcriptLinkContext("", "");
  let text = raw.trim();
  let lines: LineRange | null = null;
  const hash = text.indexOf("#");
  if (hash >= 0) {
    lines = lineFragment(text.slice(hash + 1));
    text = text.slice(0, hash);
  }
  const query = text.indexOf("?");
  if (query >= 0) text = text.slice(0, query);
  try {
    text = decodeURIComponent(text);
  } catch {
    // keep as written
  }
  if (text.startsWith("file://")) text = text.slice("file://".length);
  const suffix = stripLineSuffix(text);
  if (suffix) {
    text = suffix.path;
    lines = lines ?? suffix.lines;
  }
  if (!text) return null;
  return resolve(text, lines, ctx);
}

/** `L12`, `L12-L20`, `L12-20` → a line range. */
export function lineFragment(fragment: string): LineRange | null {
  if (!(fragment.startsWith("L") || fragment.startsWith("l"))) return null;
  const parts = fragment.slice(1).split("-");
  const start = Number(parts[0]);
  if (!Number.isInteger(start) || start <= 0 || parts[0] === "") return null;
  if (parts.length < 2) return { start, end: start };
  const endText = parts[1].replace(/^[Ll]/, "");
  const end = Number(endText);
  if (!endText || !Number.isInteger(end) || end < start) return { start, end: start };
  return { start, end };
}

/** Strip a trailing `:12`, `:12-20` or `:12:5` (line:column) suffix. */
export function stripLineSuffix(text: string): { path: string; lines: LineRange } | null {
  const m = /^(.+?):(\d+)(?:-(\d+)|:\d+)?$/.exec(text);
  if (!m) return null;
  const start = Number(m[2]);
  if (start <= 0) return null;
  let end = start;
  if (m[3] !== undefined && Number(m[3]) >= start) end = Number(m[3]);
  return { path: m[1], lines: { start, end } };
}

function resolve(text: string, lines: LineRange | null, ctx: FileLinkContext): FileReference | null {
  let path = text;
  let base = ctx.baseDirectory;
  if (path.startsWith("/")) {
    const relative = stripRoot(path, ctx.absoluteRoots);
    if (relative === null) return null;
    path = relative;
    base = "";
  }
  const isDirectory = path.endsWith("/");
  const normalized = normalize(base ? `${base}/${path}` : path);
  if (normalized === null) return null;
  const dir = isDirectory || normalized === "";
  return { path: normalized, isDirectory: dir, lines: dir ? null : lines };
}

function stripRoot(path: string, roots: readonly string[]): string | null {
  for (const root of [...roots].sort((a, b) => b.length - a.length)) {
    if (!root) continue;
    const trimmed = root.endsWith("/") ? root.slice(0, -1) : root;
    if (path === trimmed) return "";
    if (path.startsWith(trimmed + "/")) return path.slice(trimmed.length + 1);
  }
  return null;
}

/** Collapse `.`/`..`/empty components; null when `..` escapes the root. */
export function normalize(path: string): string | null {
  const stack: string[] = [];
  for (const c of path.split("/")) {
    if (!c || c === ".") continue;
    if (c === "..") {
      if (!stack.length) return null;
      stack.pop();
    } else {
      stack.push(c);
    }
  }
  return stack.join("/");
}

/** `internal/a.go:12-20` style display of a reference. */
export function formatReference(ref: FileReference): string {
  const path = ref.isDirectory && ref.path ? `${ref.path}/` : ref.path || "/";
  if (!ref.lines) return path;
  return ref.lines.start === ref.lines.end ? `${path}:${ref.lines.start}` : `${path}:${ref.lines.start}-${ref.lines.end}`;
}
