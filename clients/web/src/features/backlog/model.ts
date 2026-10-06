// Pure backlog-browser logic (mirrors YccKit BacklogModel/TaskDetailModel and
// internal/docs semantics): task statuses, the actionable flag, filtering and
// sorting the ListBacklog table, keyboard cursor movement, editable-list
// parsing, the body/work-log split, and task links inside task bodies.
import { create } from "@bufbuild/protobuf";
import { BacklogTaskSummarySchema, type BacklogTaskSummary, type TaskDetail } from "../../gen/ycc/v1/ycc_pb";

/** Statuses UpdateTask accepts, in workflow (board) order. */
export const TASK_STATUSES = ["proposed", "todo", "in_progress", "in_review", "blocked", "done"] as const;
export type TaskStatus = (typeof TASK_STATUSES)[number];

const LABELS: Record<TaskStatus, string> = {
  proposed: "Proposed",
  todo: "Todo",
  in_progress: "In progress",
  in_review: "In review",
  blocked: "Blocked",
  done: "Done",
};

export function isTaskStatus(s: string): s is TaskStatus {
  return (TASK_STATUSES as readonly string[]).includes(s);
}

export function statusLabel(status: string): string {
  const s = status.toLowerCase();
  return isTaskStatus(s) ? LABELS[s] : status || "Unknown";
}

/**
 * "Most interesting first" order used by the default sort: active work, then
 * queued, blocked, proposed, unknown, with done trailing (YccKit sortOrder).
 */
export function statusRank(status: string): number {
  switch (status.toLowerCase()) {
    case "in_progress":
      return 0;
    case "in_review":
      return 1;
    case "todo":
      return 2;
    case "blocked":
      return 3;
    case "proposed":
      return 4;
    case "done":
      return 6;
    default:
      return 5;
  }
}

/**
 * Work-loop eligibility (docs.EligibilityFor): accepted active work — todo or
 * in progress — whose dependencies are all done. Proposed tasks never are.
 */
export function isActionable(t: { status: string; ready: boolean }): boolean {
  const s = t.status.toLowerCase();
  return t.ready && (s === "todo" || s === "in_progress");
}

export interface Readiness {
  kind: "actionable" | "blocked" | "gated" | "none";
  label: string;
  title: string;
}

/**
 * The Ready column: actionable work, dependency-blocked work, or the
 * lifecycle gate that keeps a dependency-ready task from being picked.
 */
export function readiness(t: { status: string; ready: boolean; blockedBy: readonly string[] }): Readiness {
  const s = t.status.toLowerCase();
  if (s === "done") return { kind: "none", label: "", title: "" };
  if (isActionable(t)) {
    return { kind: "actionable", label: "✓ actionable", title: "Accepted work whose dependencies are done: the work loop may pick it" };
  }
  if (!t.ready) {
    return { kind: "blocked", label: "blocked", title: blockedLabel(t) ?? "Blocked by dependencies" };
  }
  switch (s) {
    case "proposed":
      return { kind: "gated", label: "needs promotion", title: "Proposed tasks are not accepted scope until promoted to todo" };
    case "in_review":
      return { kind: "gated", label: "awaiting review", title: "In review: not picked until it returns to todo or is done" };
    case "blocked":
      return { kind: "gated", label: "on hold", title: "Status blocked: not picked until unblocked" };
    default:
      return { kind: "gated", label: "—", title: `Status ${t.status || "unknown"} is not picked by the work loop` };
  }
}

/** "Blocked by 0173, 0174" for a not-done row with open dependencies. */
export function blockedLabel(t: { status: string; ready: boolean; blockedBy: readonly string[] }): string | null {
  if (t.ready || t.blockedBy.length === 0 || t.status.toLowerCase() === "done") return null;
  return `Blocked by ${t.blockedBy.join(", ")}`;
}

/** A task id as the store normalizes it: numeric ids are zero-padded to 4. */
export function normalizeTaskId(id: string): string {
  const trimmed = id.trim();
  if (/^\d+$/.test(trimmed)) return String(Number(trimmed)).padStart(4, "0");
  return trimmed;
}

/** Ascending id order: numeric ids numerically, before non-numeric ids. */
export function compareIds(a: string, b: string): number {
  const na = /^\d+$/.test(a) ? Number(a) : null;
  const nb = /^\d+$/.test(b) ? Number(b) : null;
  if (na !== null && nb !== null && na !== nb) return na < nb ? -1 : 1;
  if (na !== null && nb === null) return -1;
  if (na === null && nb !== null) return 1;
  return a === b ? 0 : a < b ? -1 : 1;
}

export type SortKey = "id" | "title" | "status" | "priority" | "deps" | "actionable";
export type SortDir = "asc" | "desc";
export interface BacklogSort {
  key: SortKey;
  dir: SortDir;
}

/** Status (active work first), then priority, newest first. */
export const DEFAULT_SORT: BacklogSort = { key: "status", dir: "asc" };

/** The direction a column starts in when first clicked. */
export function defaultDir(key: SortKey): SortDir {
  return key === "id" ? "desc" : "asc";
}

/** Clicking a column header: flip the active column, else start the new one. */
export function toggleSort(current: BacklogSort, key: SortKey): BacklogSort {
  if (current.key === key) return { key, dir: current.dir === "asc" ? "desc" : "asc" };
  return { key, dir: defaultDir(key) };
}

function priorityOf(t: BacklogTaskSummary): number {
  return t.priority > 0 ? t.priority : 99;
}

function primary(a: BacklogTaskSummary, b: BacklogTaskSummary, key: SortKey): number {
  switch (key) {
    case "id":
      return compareIds(a.id, b.id);
    case "title":
      return a.title.localeCompare(b.title, undefined, { sensitivity: "base", numeric: true });
    case "status":
      return statusRank(a.status) - statusRank(b.status) || priorityOf(a) - priorityOf(b);
    case "priority":
      return priorityOf(a) - priorityOf(b);
    case "deps":
      return a.blockedBy.length - b.blockedBy.length || a.dependsOn.length - b.dependsOn.length;
    case "actionable":
      // Actionable first, then ready-but-gated, then blocked.
      return rankActionable(a) - rankActionable(b);
  }
}

function rankActionable(t: BacklogTaskSummary): number {
  if (isActionable(t)) return 0;
  return t.ready ? 1 : 2;
}

/**
 * Sort rows by a column. Ties fall back to newest id first (ids are
 * allocated monotonically) so the order is total and stable.
 */
export function sortTasks(tasks: readonly BacklogTaskSummary[], sort: BacklogSort): BacklogTaskSummary[] {
  const sign = sort.dir === "asc" ? 1 : -1;
  return tasks
    .map((t, i) => ({ t, i }))
    .sort((x, y) => {
      const p = primary(x.t, y.t, sort.key) * sign;
      if (p !== 0) return p;
      if (sort.key !== "id") {
        const id = compareIds(y.t.id, x.t.id);
        if (id !== 0) return id;
      }
      return x.i - y.i;
    })
    .map((x) => x.t);
}

export interface BacklogFilter {
  /** Whitespace-separated terms; every term must match id, title, status, or a dependency. */
  text: string;
  /** Statuses to show; empty shows every status (done subject to showDone). */
  statuses: TaskStatus[];
  /** Include done tasks when no status filter names done explicitly. */
  showDone: boolean;
  /** Only work-loop eligible tasks. */
  actionableOnly: boolean;
}

export const DEFAULT_FILTER: BacklogFilter = { text: "", statuses: [], showDone: false, actionableOnly: false };

export function isFiltered(f: BacklogFilter): boolean {
  return f.text.trim() !== "" || f.statuses.length > 0 || f.actionableOnly;
}

function matchesText(t: BacklogTaskSummary, terms: string[]): boolean {
  if (!terms.length) return true;
  const hay = [t.id, t.title, t.status, statusLabel(t.status), ...t.dependsOn].join("\n").toLowerCase();
  return terms.every((term) => {
    const bare = term.replace(/^#/, "");
    if (/^\d+$/.test(bare) && normalizeTaskId(bare) === t.id) return true;
    return hay.includes(bare);
  });
}

export function filterTasks(tasks: readonly BacklogTaskSummary[], f: BacklogFilter): BacklogTaskSummary[] {
  const terms = f.text.toLowerCase().split(/\s+/).filter(Boolean);
  const statuses = new Set<string>(f.statuses);
  return tasks.filter((t) => {
    const s = t.status.toLowerCase();
    if (statuses.size > 0 && !statuses.has(s)) return false;
    if (s === "done" && !f.showDone && !statuses.has("done")) return false;
    if (f.actionableOnly && !isActionable(t)) return false;
    return matchesText(t, terms);
  });
}

/** Per-status counts over the whole backlog (for the status filter chips). */
export function statusCounts(tasks: readonly BacklogTaskSummary[]): Record<string, number> {
  const out: Record<string, number> = {};
  for (const t of tasks) out[t.status.toLowerCase()] = (out[t.status.toLowerCase()] ?? 0) + 1;
  return out;
}

/**
 * j/k (or arrow) movement over the visible rows: from no cursor, down starts
 * at the first row and up at the last; movement clamps at the ends; a cursor
 * on a row that is no longer visible restarts from the top.
 */
export function moveCursor(ids: readonly string[], current: string | null, delta: number): string | null {
  if (!ids.length) return null;
  const i = current === null ? -1 : ids.indexOf(current);
  if (i < 0) return delta >= 0 ? ids[0] : ids[ids.length - 1];
  return ids[Math.max(0, Math.min(ids.length - 1, i + delta))];
}

/** Dependency ids typed as "0410, 0411" (commas, whitespace, or newlines). */
export function parseIdList(text: string): string[] {
  return dedupe(text.split(/[\s,]+/));
}

/** Spec references, one per line (a section title may contain commas or spaces). */
export function parseLines(text: string): string[] {
  return dedupe(text.split("\n"));
}

function dedupe(parts: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of parts) {
    const v = raw.trim();
    if (!v || seen.has(v)) continue;
    seen.add(v);
    out.push(v);
  }
  return out;
}

/**
 * Split a task body at its `## Work log` heading (outside code fences) so the
 * detail view can show the description and the log as separate sections.
 * `workLog` is null when the body has no such heading.
 */
export function splitWorkLog(body: string): { main: string; workLog: string | null } {
  const lines = body.split("\n");
  let fence: string | null = null;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const f = /^\s{0,3}(`{3,}|~{3,})/.exec(line);
    if (f) {
      if (fence === null) fence = f[1][0];
      else if (f[1][0] === fence) fence = null;
      continue;
    }
    if (fence === null && /^#{2}\s+work\s+log\s*#*\s*$/i.test(line)) {
      return {
        main: lines.slice(0, i).join("\n").trimEnd(),
        workLog: lines.slice(i + 1).join("\n").trim(),
      };
    }
  }
  return { main: body.trimEnd(), workLog: null };
}

/**
 * The task a root-relative file reference names, when it is a backlog task
 * file (`backlog/0412-some-slug.md`), so task bodies can link sibling tasks.
 */
export function taskIdFromPath(path: string): string | null {
  const m = /^backlog\/(\d+)(?:-[^/]*)?\.md$/i.exec(path);
  return m ? normalizeTaskId(m[1]) : null;
}

/** The list row for a created/updated task's canonical detail. */
export function summaryFromDetail(d: TaskDetail): BacklogTaskSummary {
  return create(BacklogTaskSummarySchema, {
    id: d.id,
    title: d.title,
    status: d.status,
    priority: d.priority,
    dependsOn: [...d.dependsOn],
    ready: d.ready,
    blockedBy: [...d.blockedBy],
  });
}

/** Replace (or append) a row with a task's canonical detail. */
export function upsertSummary(list: readonly BacklogTaskSummary[], d: TaskDetail): BacklogTaskSummary[] {
  const row = summaryFromDetail(d);
  const i = list.findIndex((t) => t.id === d.id);
  if (i < 0) return [...list, row];
  const next = list.slice();
  next[i] = row;
  return next;
}
