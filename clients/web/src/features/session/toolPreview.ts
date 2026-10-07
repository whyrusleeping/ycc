// Concise per-tool previews for transcript tool rows: which command ran (and
// how it exited), which file was read, written, or edited (with a hunk
// summary), which task was touched. The row's args/result payloads already
// carry this; the per-tool field choices mirror YccKit's ToolPreview so the
// clients agree. Pure, for unit tests.
import { lineDiff, opStats } from "../code/diff";

export type MetaTone = "ok" | "error" | "warn" | "muted" | "add" | "del";

export interface ToolMeta {
  text: string;
  tone: MetaTone;
}

export interface ToolPreview {
  /** A short glyph identifying the tool's kind. */
  glyph: string;
  /** One-line summary (command, query, task id …); "" when the path says it all. */
  summary: string;
  /** A file the call targets, shown as a copyable path. */
  path: string;
  meta: ToolMeta[];
}

/** The argument fields that best identify a call, per tool (YccKit parity). */
const PREVIEW_FIELDS: Record<string, string[]> = {
  Bash: ["command"],
  Read: ["file_path"],
  Write: ["file_path"],
  Edit: ["file_path"],
  Search: ["pattern"],
  web_search: ["query"],
  fetch_page: ["url"],
  get_task: ["task_id"],
  update_task: ["task_id"],
  create_task: ["title"],
  list_backlog: [],
  propose_plan: ["task_id"],
  spawn_implementer: ["task_id"],
  send_to_implementer: ["task_id"],
  spawn_reviewers: ["task_id"],
  re_review: ["task_id"],
  commit: ["message"],
  ask_user: ["question"],
  remember: ["note"],
  wait: ["job_ids"],
  job_output: ["job_id"],
  job_result: ["job_id"],
  kill_job: ["job_id"],
  tool_output: ["artifact_id"],
  finish: ["report"],
  report_blocked: ["reason"],
  request_integration: ["report"],
};

const FILE_TOOLS = new Set(["Read", "Write", "Edit"]);

export function toolGlyph(tool: string): string {
  switch (tool) {
    case "Bash":
      return "$";
    case "Read":
      return "▤";
    case "Write":
      return "✚";
    case "Edit":
      return "✎";
    case "Search":
    case "web_search":
      return "⌕";
    case "fetch_page":
      return "◍";
    case "commit":
      return "⎇";
    case "ask_user":
      return "?";
    case "finish":
    case "report_blocked":
    case "request_integration":
      return "⚑";
    case "wait":
    case "job_output":
    case "job_result":
    case "kill_job":
      return "◷";
    default:
      return "⚙";
  }
}

/** Parse a tool's JSON args into an object, or null. */
export function parseArgs(args: string): Record<string, unknown> | null {
  const trimmed = args.trim();
  if (!trimmed.startsWith("{")) return null;
  try {
    const v: unknown = JSON.parse(trimmed);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

/**
 * Top-level scalar fields recoverable from an args payload that does not
 * parse — the transcript page abbreviates long args mid-JSON. A string cut
 * off by the abbreviation keeps its readable prefix.
 */
export function salvageArgs(args: string): Record<string, string> | null {
  const trimmed = args.trim();
  if (!trimmed.startsWith("{")) return null;
  const out: Record<string, string> = {};
  // "key": "string…" (closed or cut off) | number | boolean
  const field = /"([A-Za-z_][A-Za-z0-9_]*)"\s*:\s*(?:"((?:[^"\\]|\\.)*)("?)|(-?\d+(?:\.\d+)?|true|false))/g;
  for (let m = field.exec(trimmed); m; m = field.exec(trimmed)) {
    const [, key, str, closed, scalar] = m;
    if (key in out) continue;
    if (scalar !== undefined) {
      out[key] = scalar;
      continue;
    }
    let value: string;
    try {
      // A cut can split a \uXXXX escape: drop the fragment.
      value = JSON.parse(`"${closed ? str : str.replace(/\\u[0-9a-fA-F]{0,3}(…?)$/, "$1")}"`) as string;
    } catch {
      value = str;
    }
    // The daemon marks its cut with "…"; keep exactly one.
    out[key] = closed ? value : `${value.replace(/\n?…$/, "")}…`;
  }
  return Object.keys(out).length > 0 ? out : null;
}

/** Scalars and short string arrays preview usefully; nested objects do not. */
function displayValue(v: unknown): string | null {
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  if (Array.isArray(v) && v.every((x) => typeof x === "string")) return v.join(", ");
  return null;
}

/** Collapse whitespace to one line and cap the length. */
export function oneLine(value: string, limit = 120): string {
  const single = value.split(/[\n\r\t]+/).join(" ").trim();
  return single.length > limit ? single.slice(0, limit) + "…" : single;
}

/** A one-line argument summary (YccKit ToolPreview.summary semantics). */
export function argSummary(tool: string, args: string, limit = 120): string {
  const trimmed = args.trim();
  if (!trimmed) return "";
  const obj = parseArgs(trimmed) ?? salvageArgs(trimmed);
  if (!obj) return oneLine(trimmed, limit);
  const fields = PREVIEW_FIELDS[tool];
  if (fields) {
    for (const f of fields) {
      const v = displayValue(obj[f]);
      if (v) return oneLine(v, limit);
    }
    // A known tool with nothing configured stays quiet.
    return "";
  }
  for (const key of Object.keys(obj).sort()) {
    const v = displayValue(obj[key]);
    if (v) return oneLine(v, limit);
  }
  return "";
}

export interface BashOutcome {
  /** null while running or when the result says nothing about the exit. */
  exitCode: number | null;
  timedOut: boolean;
  background: boolean;
  signal: string;
}

/** How a Bash call ended, from the result text the daemon recorded. */
export function bashOutcome(output: string, status: "running" | "ok" | "error"): BashOutcome {
  const out: BashOutcome = { exitCode: null, timedOut: false, background: false, signal: "" };
  if (status === "running") return out;
  if (/^started background job /m.test(output.slice(0, 400))) {
    out.background = true;
    return out;
  }
  const tail = output.slice(-600);
  if (/\[command timed out after [^\]]*\]/.test(tail)) out.timedOut = true;
  const exit = /\[exit: exit status (\d+)\]/.exec(tail);
  if (exit) {
    out.exitCode = Number(exit[1]);
  } else {
    const sig = /\[exit: signal: ([^\]]+)\]/.exec(tail);
    if (sig) out.signal = sig[1];
    else if (!out.timedOut && status === "ok") out.exitCode = 0;
  }
  return out;
}

function countLines(s: string): number {
  if (!s) return 0;
  const n = s.split("\n").length;
  return s.endsWith("\n") ? n - 1 : n;
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

/** Lines of a `cat -n`-style Read result (excluding non-numbered notes). */
export function catNLineCount(output: string): number {
  let n = 0;
  for (const line of output.split("\n")) if (/^\s*\d+\t/.test(line)) n++;
  return n;
}

/** The file a Write/Edit mutation receipt names ("created <path>" first line). */
export function receiptPath(output: string): string {
  const m = /^(?:created|overwrote|edited) (.+)$/.exec(output.split("\n", 1)[0] ?? "");
  return m ? m[1].trim() : "";
}

export function toolPreview(tool: string, args: string, output: string, status: "running" | "ok" | "error"): ToolPreview {
  const obj = parseArgs(args);
  const str = (k: string) => (obj && typeof obj[k] === "string" ? (obj[k] as string) : "");
  const num = (k: string) => (obj && typeof obj[k] === "number" ? (obj[k] as number) : null);
  const preview: ToolPreview = { glyph: toolGlyph(tool), summary: "", path: "", meta: [] };
  // An abbreviated payload still names its file (unless the path itself was cut).
  const salvagedPath = obj ? "" : (salvageArgs(args)?.file_path ?? "");
  const path =
    str("file_path") ||
    (salvagedPath.endsWith("…") ? "" : salvagedPath) ||
    (FILE_TOOLS.has(tool) && status !== "running" ? receiptPath(output) : "");
  if (FILE_TOOLS.has(tool) && path) {
    preview.path = path;
  } else {
    preview.summary = argSummary(tool, args);
  }
  switch (tool) {
    case "Bash": {
      const o = bashOutcome(output, status);
      if (obj?.background === true || obj?.run_in_background === true || o.background) {
        preview.meta.push({ text: "background", tone: "muted" });
      } else if (o.timedOut) {
        preview.meta.push({ text: "timed out", tone: "error" });
      } else if (o.signal) {
        preview.meta.push({ text: o.signal, tone: "error" });
      }
      if (o.exitCode !== null) preview.meta.push({ text: `exit ${o.exitCode}`, tone: o.exitCode === 0 ? "ok" : "error" });
      break;
    }
    case "Read": {
      const offset = num("offset");
      const limit = num("limit");
      if (offset !== null || limit !== null) {
        const from = offset ?? 1;
        preview.meta.push({ text: limit !== null ? `lines ${from}–${from + limit - 1}` : `from line ${from}`, tone: "muted" });
      }
      if (status === "ok") {
        const n = catNLineCount(output);
        if (n) preview.meta.push({ text: plural(n, "line"), tone: "muted" });
      }
      break;
    }
    case "Write": {
      if (obj && typeof obj.content === "string") {
        preview.meta.push({ text: plural(countLines(obj.content), "line"), tone: "muted" });
      } else {
        // Abbreviated args: the mutation receipt still counts the lines.
        const after = /^after: \d+ bytes, (\d+) lines?$/m.exec(output);
        if (after) preview.meta.push({ text: plural(Number(after[1]), "line"), tone: "muted" });
      }
      break;
    }
    case "Edit": {
      if (obj && typeof obj.old_string === "string" && typeof obj.new_string === "string") {
        const s = opStats(lineDiff(obj.old_string, obj.new_string));
        preview.meta.push({ text: `+${s.additions}`, tone: "add" }, { text: `−${s.deletions}`, tone: "del" });
        if (obj.replace_all === true) preview.meta.push({ text: "all occurrences", tone: "muted" });
      }
      break;
    }
  }
  if (status === "error" && !preview.meta.some((m) => m.tone === "error")) {
    preview.meta.push({ text: "failed", tone: "error" });
  }
  return preview;
}
