// Unified-diff parsing (a port of YccKit's DiffFormatter) plus a small line
// diff for Edit tool calls. Pure and O(n) over the input (the line diff is
// bounded), so it is unit-testable and never hangs the UI on a large diff.
import { languageForPath, type Language } from "./highlight";

export type DiffLineKind = "fileHeader" | "hunkHeader" | "addition" | "deletion" | "context" | "truncationNotice";

export interface DiffLine {
  kind: DiffLineKind;
  /** Verbatim line text (including the leading +/-/space). */
  text: string;
  /** Language of the file this line belongs to, for highlighting its code. */
  language: Language | null;
  /** The file this line belongs to ("" before the first file header). */
  path: string;
}

/** A safety cap on rendered rows; the daemon already caps the payload. */
export const DEFAULT_MAX_DIFF_LINES = 4000;

/**
 * Parse a unified diff (`git show` / `git diff` output) into typed rows. A
 * daemon-reported truncation (or hitting the row cap) appends a notice row.
 */
export function parseDiff(diff: string, truncated = false, maxLines = DEFAULT_MAX_DIFF_LINES): DiffLine[] {
  const raw = diff.split("\n");
  if (raw.length && raw[raw.length - 1] === "") raw.pop();
  const lines: DiffLine[] = [];
  let capped = false;
  let path = "";
  let language: Language | null = null;
  for (const line of raw) {
    if (lines.length >= maxLines) {
      capped = true;
      break;
    }
    const text = line.endsWith("\r") ? line.slice(0, -1) : line;
    const kind = diffLineKind(text);
    if (kind === "fileHeader") {
      const next = headerPath(text);
      if (next !== null) {
        path = next;
        language = languageForPath(next);
      }
    }
    lines.push({ kind, text, language, path });
  }
  if (truncated || capped) {
    lines.push({ kind: "truncationNotice", text: "… diff truncated …", language: null, path });
  }
  return lines;
}

/** Classify a single (CR-stripped) diff line. */
export function diffLineKind(line: string): DiffLineKind {
  if (line.startsWith("@@")) return "hunkHeader";
  if (line.startsWith("+++") || line.startsWith("---")) return "fileHeader";
  if (
    line.startsWith("diff --git") ||
    line.startsWith("index ") ||
    line.startsWith("new file mode") ||
    line.startsWith("deleted file mode") ||
    line.startsWith("old mode") ||
    line.startsWith("new mode") ||
    line.startsWith("rename from") ||
    line.startsWith("rename to") ||
    line.startsWith("copy from") ||
    line.startsWith("copy to") ||
    line.startsWith("similarity index") ||
    line.startsWith("dissimilarity index") ||
    line.startsWith("Binary files")
  ) {
    return "fileHeader";
  }
  // `git show` prepends commit metadata; de-tint it like headers.
  if (line.startsWith("commit ") || line.startsWith("Author:") || line.startsWith("Date:") || line.startsWith("Merge:")) {
    return "fileHeader";
  }
  if (line.startsWith("+")) return "addition";
  if (line.startsWith("-")) return "deletion";
  return "context";
}

/** The new-side path a file header names, or null. */
function headerPath(line: string): string | null {
  if (line.startsWith("diff --git ")) {
    const m = / b\/(.+)$/.exec(line);
    return m ? m[1] : null;
  }
  if (line.startsWith("+++ ")) {
    const p = line.slice(4).trim();
    if (p === "/dev/null") return null;
    return p.startsWith("b/") ? p.slice(2) : p;
  }
  return null;
}

/** Whether text looks like a unified diff (so tool output can render as one). */
export function looksLikeDiff(text: string): boolean {
  const head = text.slice(0, 4000);
  return /^(diff --git |--- \S.*\n\+\+\+ \S)/m.test(head) && /^@@ /m.test(head);
}

export interface DiffStats {
  additions: number;
  deletions: number;
  files: number;
}

export function diffStats(lines: readonly DiffLine[]): DiffStats {
  let additions = 0;
  let deletions = 0;
  const files = new Set<string>();
  for (const l of lines) {
    if (l.kind === "addition") additions++;
    else if (l.kind === "deletion") deletions++;
    if (l.path) files.add(l.path);
  }
  return { additions, deletions, files: files.size };
}

// MARK: Edit tool line diff

export interface LineOp {
  op: " " | "-" | "+";
  text: string;
}

/** Above this many old×new lines the diff degrades to delete-all/add-all. */
const MAX_LCS_CELLS = 4_000_000;

function splitLines(s: string): string[] {
  if (s === "") return [];
  const lines = s.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/** A minimal line diff (LCS) between two texts. */
export function lineDiff(before: string, after: string): LineOp[] {
  const a = splitLines(before);
  const b = splitLines(after);
  // Trim the common prefix/suffix first: edits are usually local.
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const ops: LineOp[] = a.slice(0, start).map((text) => ({ op: " ", text }));
  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  if (midA.length * midB.length > MAX_LCS_CELLS) {
    for (const text of midA) ops.push({ op: "-", text });
    for (const text of midB) ops.push({ op: "+", text });
  } else {
    const n = midA.length;
    const m = midB.length;
    // lcs[i][j] = LCS length of midA[i:] and midB[j:], flattened.
    const lcs = new Uint32Array((n + 1) * (m + 1));
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        lcs[i * (m + 1) + j] =
          midA[i] === midB[j]
            ? lcs[(i + 1) * (m + 1) + j + 1] + 1
            : Math.max(lcs[(i + 1) * (m + 1) + j], lcs[i * (m + 1) + j + 1]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
      if (midA[i] === midB[j]) {
        ops.push({ op: " ", text: midA[i] });
        i++;
        j++;
      } else if (lcs[(i + 1) * (m + 1) + j] >= lcs[i * (m + 1) + j + 1]) {
        ops.push({ op: "-", text: midA[i++] });
      } else {
        ops.push({ op: "+", text: midB[j++] });
      }
    }
    while (i < n) ops.push({ op: "-", text: midA[i++] });
    while (j < m) ops.push({ op: "+", text: midB[j++] });
  }
  for (const text of a.slice(endA)) ops.push({ op: " ", text });
  return ops;
}

/**
 * Render line ops as unified-diff text with `context` lines around changes
 * and `@@` hunk headers (line numbers relative to the edited snippet).
 */
export function unifiedFromOps(ops: readonly LineOp[], path = "", context = 3): string {
  const out: string[] = [];
  if (path) out.push(`--- a/${path}`, `+++ b/${path}`);
  // Group changed lines into hunks: [start, end) windows with context,
  // merging windows that touch.
  const hunks: [number, number][] = [];
  ops.forEach((o, i) => {
    if (o.op === " ") return;
    const start = Math.max(0, i - context);
    const end = Math.min(ops.length, i + context + 1);
    const last = hunks[hunks.length - 1];
    if (last && start <= last[1]) last[1] = Math.max(last[1], end);
    else hunks.push([start, end]);
  });
  let oldLine = 1;
  let newLine = 1;
  let k = 0;
  for (const [start, end] of hunks) {
    for (; k < start; k++) {
      if (ops[k].op !== "+") oldLine++;
      if (ops[k].op !== "-") newLine++;
    }
    let oldCount = 0;
    let newCount = 0;
    const body: string[] = [];
    for (; k < end; k++) {
      if (ops[k].op !== "+") oldCount++;
      if (ops[k].op !== "-") newCount++;
      body.push(ops[k].op + ops[k].text);
    }
    out.push(`@@ -${oldLine},${oldCount} +${newLine},${newCount} @@`, ...body);
    oldLine += oldCount;
    newLine += newCount;
  }
  return out.join("\n");
}

export function opStats(ops: readonly LineOp[]): { additions: number; deletions: number } {
  let additions = 0;
  let deletions = 0;
  for (const o of ops) {
    if (o.op === "+") additions++;
    else if (o.op === "-") deletions++;
  }
  return { additions, deletions };
}
