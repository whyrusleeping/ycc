// Pure pieces of the file browser: locations (route path + session + line
// fragment), the lazily loaded tree flattened for rendering, text splitting,
// and display helpers. A port of the logic in YccKit's FileBrowserModel.
import { lineFragment, normalize, type FileReference, type LineRange } from "./fileReference";

/** Where the browser points: a root-relative path, an optional session worktree, a line range. */
export interface FileLocation {
  path: string;
  session: string;
  lines: LineRange | null;
}

/**
 * Parse the browser's route pieces: the splat after `/files/` (already
 * URL-decoded by the router), the `?session=` query, and a `#L12[-L20]`
 * fragment. A path that would escape the root falls back to the root.
 */
export function parseFileLocation(splat: string, search: string, hash: string): FileLocation {
  const path = normalize(splat) ?? "";
  const session = new URLSearchParams(search).get("session") ?? "";
  const frag = hash.startsWith("#") ? hash.slice(1) : hash;
  return { path, session, lines: frag ? lineFragment(frag) : null };
}

/** The parent directory of a root-relative path ("" for top-level entries and the root). */
export function dirname(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

export function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? path : path.slice(i + 1);
}

/** A child entry's root-relative path. */
export function childPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

/** Every directory above `path`, outermost first ("" excluded): `a/b/c` → [`a`, `a/b`]. */
export function ancestorDirs(path: string): string[] {
  const parts = path.split("/").filter(Boolean);
  const out: string[] = [];
  for (let i = 1; i < parts.length; i++) out.push(parts.slice(0, i).join("/"));
  return out;
}

/** Breadcrumb segments for a root-relative path, starting with the root (""). */
export function crumbs(path: string, rootLabel: string): { label: string; path: string }[] {
  const out = [{ label: rootLabel, path: "" }];
  const parts = path.split("/").filter(Boolean);
  parts.forEach((p, i) => out.push({ label: p, path: parts.slice(0, i + 1).join("/") }));
  return out;
}

/** The minimal entry shape the tree needs (a subset of the generated FileEntry). */
export interface TreeEntry {
  name: string;
  isDir: boolean;
  ignored?: boolean;
  isSymlink?: boolean;
}

export interface TreeRow {
  path: string;
  name: string;
  depth: number;
  isDir: boolean;
  ignored: boolean;
  isSymlink: boolean;
  /** Directories: shown expanded. */
  expanded: boolean;
  /** Expanded directories whose listing has not arrived yet. */
  loading: boolean;
}

/**
 * Flatten a lazily loaded tree for rendering: `listings` maps a directory's
 * root-relative path ("" is the root) to its entries (as the daemon orders
 * them: directories first), `expanded` names the open directories. Children
 * of an expanded directory follow it, one level deeper.
 */
export function flattenTree(listings: ReadonlyMap<string, readonly TreeEntry[]>, expanded: ReadonlySet<string>): TreeRow[] {
  const rows: TreeRow[] = [];
  const walk = (dir: string, depth: number) => {
    const entries = listings.get(dir);
    if (!entries) return;
    for (const e of entries) {
      const path = childPath(dir, e.name);
      const open = e.isDir && expanded.has(path);
      rows.push({
        path,
        name: e.name,
        depth,
        isDir: e.isDir,
        ignored: !!e.ignored,
        isSymlink: !!e.isSymlink,
        expanded: open,
        loading: open && !listings.has(path),
      });
      if (open) walk(path, depth + 1);
    }
  };
  walk("", 0);
  return rows;
}

/** Expanding to show `path`: every ancestor directory opens (and `path` itself when it is a directory). */
export function expandTo(expanded: ReadonlySet<string>, path: string, isDirectory: boolean): Set<string> {
  const next = new Set(expanded);
  for (const a of ancestorDirs(path)) next.add(a);
  if (isDirectory && path) next.add(path);
  return next;
}

export function toggle(expanded: ReadonlySet<string>, path: string): Set<string> {
  const next = new Set(expanded);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  return next;
}

/**
 * Split file text into lines without terminators: `\n` and `\r\n` both end a
 * line, and a final terminator does not start an empty last line.
 */
export function splitLines(text: string): string[] {
  if (!text) return [];
  const lines = text.split(/\r?\n/);
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** Clamp a target range to a file of `count` lines; null when it starts past the end. */
export function clampLines(lines: LineRange | null, count: number): LineRange | null {
  if (!lines || count <= 0 || lines.start > count) return null;
  return { start: lines.start, end: Math.min(lines.end, count) };
}

/** Parse what the user typed into "Go to line": `12`, `12-20`, `L12`. */
export function parseGoToLine(input: string): LineRange | null {
  const t = input.trim().replace(/^[Ll]/, "");
  const m = /^(\d+)(?:\s*[-:]\s*[Ll]?(\d+))?$/.exec(t);
  if (!m) return null;
  const start = Number(m[1]);
  if (start <= 0) return null;
  const end = m[2] !== undefined ? Number(m[2]) : start;
  return { start, end: end >= start ? end : start };
}

export function isMarkdownPath(path: string): boolean {
  return /\.(md|markdown|mdx)$/i.test(path);
}

/** "1.2 KB"-style size. */
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** A reference for a tree/listing entry. */
export function entryReference(dir: string, entry: TreeEntry): FileReference {
  return { path: childPath(dir, entry.name), isDirectory: entry.isDir, lines: null };
}

/** Texts this long (in lines) are shown without syntax highlighting. */
export const HIGHLIGHT_LINE_LIMIT = 20_000;
/** ReadFile cap for the viewer (the daemon truncates at a line boundary). */
export const VIEWER_MAX_BYTES = 4 << 20;
