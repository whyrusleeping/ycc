// How transcript rows group for display. A long run of consecutive tool and
// reasoning rows (an agent working autonomously) folds into one activity block
// that shows a summary and only its last few steps until expanded; repeated
// identical system notices ("job claimed" ×3) collapse into one row with a
// count. Pure, for unit tests: the Transcript owns which blocks are expanded.
import type { TranscriptRow } from "./projection";

/** Runs at least this long fold; shorter runs show every row. */
export const ACTIVITY_FOLD_MIN = 8;
/** Steps a folded run keeps visible (the newest). */
export const ACTIVITY_TAIL = 3;

export type TranscriptBlock =
  /** One row; `repeat` > 1 stands for that many identical consecutive notices. */
  | { type: "row"; index: number; repeat: number }
  /** rows[start, end): a foldable run of tool/reasoning rows, keyed by its first row id. */
  | { type: "activity"; start: number; end: number; key: string };

function isActivity(row: TranscriptRow): boolean {
  return (row.kind.type === "tool" && row.kind.status !== "error") || row.kind.type === "thinking" ||
    (row.kind.type === "system" && row.kind.activity === true);
}

function sameNotice(a: TranscriptRow, b: TranscriptRow): boolean {
  return a.kind.type === "system" && b.kind.type === "system" && a.kind.text === b.kind.text && a.actor === b.actor;
}

/**
 * Group rows into display blocks. `reveal` is a row that must stay addressable
 * (the current search match): a notice run containing it is not merged.
 */
export function transcriptBlocks(rows: readonly TranscriptRow[], reveal: string | null = null): TranscriptBlock[] {
  const out: TranscriptBlock[] = [];
  let i = 0;
  while (i < rows.length) {
    const row = rows[i];
    if (isActivity(row)) {
      let j = i + 1;
      while (j < rows.length && isActivity(rows[j])) j++;
      if (j - i >= ACTIVITY_FOLD_MIN) {
        out.push({ type: "activity", start: i, end: j, key: row.id });
      } else {
        for (let k = i; k < j; k++) out.push({ type: "row", index: k, repeat: 1 });
      }
      i = j;
      continue;
    }
    if (row.kind.type === "system") {
      let j = i + 1;
      while (j < rows.length && sameNotice(row, rows[j])) j++;
      const hidesReveal = reveal !== null && rows.slice(i + 1, j).some((r) => r.id === reveal);
      if (j - i > 1 && !hidesReveal) {
        out.push({ type: "row", index: i, repeat: j - i });
        i = j;
        continue;
      }
    }
    out.push({ type: "row", index: i, repeat: 1 });
    i++;
  }
  return out;
}

/** The index of the first row a folded activity block still shows. */
export function foldedTailStart(block: { start: number; end: number }): number {
  return Math.max(block.start, block.end - ACTIVITY_TAIL);
}

/** Whether a folded block hides `rowId` (search must open it). */
export function blockHides(rows: readonly TranscriptRow[], block: { start: number; end: number }, rowId: string | null): boolean {
  if (!rowId) return false;
  const tail = foldedTailStart(block);
  for (let k = block.start; k < tail; k++) if (rows[k].id === rowId) return true;
  return false;
}

export interface ActivitySummary {
  steps: number;
  /** Tool calls by name, most frequent first (ties keep first-seen order). */
  tools: { name: string; count: number }[];
  reasoning: number;
  failed: number;
  running: number;
  /** Wall time from the first step to the last, in ms (null without timestamps). */
  durationMs: number | null;
}

export function activitySummary(rows: readonly TranscriptRow[], start: number, end: number): ActivitySummary {
  const counts = new Map<string, number>();
  let reasoning = 0;
  let failed = 0;
  let running = 0;
  for (let k = start; k < end; k++) {
    const kind = rows[k].kind;
    if (kind.type === "thinking") reasoning++;
    else if (kind.type === "tool") {
      counts.set(kind.name, (counts.get(kind.name) ?? 0) + 1);
      if (kind.status === "error") failed++;
      else if (kind.status === "running") running++;
    }
  }
  const tools = [...counts.entries()].map(([name, count]) => ({ name, count }));
  tools.sort((a, b) => b.count - a.count);
  const first = Date.parse(rows[start]?.ts ?? "");
  const last = Date.parse(rows[end - 1]?.ts ?? "");
  const durationMs = Number.isFinite(first) && Number.isFinite(last) && last >= first ? last - first : null;
  return { steps: tools.reduce((n, tool) => n + tool.count, reasoning), tools, reasoning, failed, running, durationMs };
}

/** "45s", "4m 12s", "1h 03m" (null under a second). */
export function compactDuration(ms: number | null): string | null {
  if (ms === null || ms < 1000) return null;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}
