// Pure presentation logic for the session Activity panel: the agent/job tree
// built from GetSessionActivity's flat node list, the swimlane timeline's
// geometry, and the job-output follow buffer. No React, for unit tests.
import type { ActivityNode, GetSessionActivityResponse } from "../../gen/ycc/v1/ycc_pb";

export const ROOT_ID = "coordinator";

/** Statuses with distinct styling. The root reports the session phase instead. */
export type Status = "running" | "done" | "failed" | "blocked" | "killed" | "lost" | "idle" | "paused" | "error" | "stopped" | "unknown";

const KNOWN: ReadonlySet<string> = new Set(["running", "done", "failed", "blocked", "killed", "lost", "idle", "paused", "error", "stopped"]);

export function statusOf(raw: string): Status {
  return KNOWN.has(raw) ? (raw as Status) : "unknown";
}

export function isJob(node: ActivityNode): boolean {
  return node.id !== ROOT_ID && node.actor === "";
}

/** Poll while the session is live and something (the coordinator included) is running. */
export function shouldPoll(data: GetSessionActivityResponse | undefined): boolean {
  return !!data?.live && data.nodes.some((n) => n.status === "running");
}

export interface TreeRow {
  node: ActivityNode;
  depth: number;
}

/**
 * Nest the flat list by parentId (depth-first, siblings in the daemon's start
 * order). A node whose parent is unknown hangs off the root; a parent cycle
 * cannot hide a node or loop forever.
 */
export function buildTree(nodes: readonly ActivityNode[]): TreeRow[] {
  const ids = new Set(nodes.map((n) => n.id));
  const root = nodes.find((n) => n.id === ROOT_ID);
  const children = new Map<string, ActivityNode[]>();
  const tops: ActivityNode[] = [];
  for (const n of nodes) {
    if (n === root) continue;
    const parent = n.parentId && n.parentId !== n.id && ids.has(n.parentId) ? n.parentId : root ? ROOT_ID : "";
    if (!parent) {
      tops.push(n);
      continue;
    }
    const list = children.get(parent) ?? [];
    list.push(n);
    children.set(parent, list);
  }
  const out: TreeRow[] = [];
  const seen = new Set<string>();
  const visit = (n: ActivityNode, depth: number) => {
    if (seen.has(n.id)) return;
    seen.add(n.id);
    out.push({ node: n, depth });
    for (const c of children.get(n.id) ?? []) visit(c, depth + 1);
  };
  if (root) visit(root, 0);
  for (const n of tops) visit(n, 0);
  // Members of a parent cycle are unreachable from the root: list them flat.
  for (const n of nodes) if (!seen.has(n.id)) visit(n, root ? 1 : 0);
  return out;
}

/** Timeline lanes: the coordinator, then agents, then jobs (each in start order). */
export function laneOrder(nodes: readonly ActivityNode[]): ActivityNode[] {
  const root = nodes.filter((n) => n.id === ROOT_ID);
  const agents = nodes.filter((n) => n.id !== ROOT_ID && !isJob(n));
  const jobs = nodes.filter(isJob);
  return [...root, ...agents, ...jobs];
}

export function ms(ts: string): number | null {
  if (!ts) return null;
  const t = Date.parse(ts);
  return Number.isFinite(t) ? t : null;
}

export interface TimeRange {
  start: number;
  end: number;
  /** The end is "now": something is still running. */
  live: boolean;
}

/**
 * The x-axis: earliest start to now while anything runs, otherwise to the
 * latest known time (the coordinator's last activity is the session's last
 * durable event). Never shorter than a second.
 */
export function timeRange(data: Pick<GetSessionActivityResponse, "nodes" | "live">, now: number): TimeRange | null {
  let start = Infinity;
  let latest = -Infinity;
  const see = (ts: string, isStart: boolean) => {
    const t = ms(ts);
    if (t === null) return;
    if (isStart) start = Math.min(start, t);
    latest = Math.max(latest, t);
  };
  for (const n of data.nodes) {
    see(n.startedAt, true);
    see(n.finishedAt, false);
    see(n.lastActivityAt, false);
    for (const s of n.spans) {
      see(s.startedAt, true);
      see(s.finishedAt, false);
    }
  }
  if (!Number.isFinite(start)) return null;
  const live = shouldPoll(data as GetSessionActivityResponse);
  let end = live ? Math.max(now, latest) : latest;
  if (end - start < 1000) end = start + 1000;
  return { start, end, live };
}

export interface Interval {
  start: number;
  end: number;
  status: Status;
  /** Still running: the bar ends at now. */
  open: boolean;
  /** The end is unknown (in flight when the session stopped): it runs to the axis end. */
  unknownEnd: boolean;
  /** Agent round number (0 for the coordinator and jobs). */
  round: number;
}

/** Where the coordinator's lane ends: now while live, else its last activity. */
function rootEnd(node: ActivityNode, range: TimeRange): number {
  const last = range.live ? null : ms(node.lastActivityAt);
  return last ?? range.end;
}

/** What one lane draws: an agent's rounds, a job's lifetime, or the session. */
export function laneIntervals(node: ActivityNode, range: TimeRange): Interval[] {
  if (node.id === ROOT_ID) {
    const start = ms(node.startedAt) ?? range.start;
    return [{ start, end: rootEnd(node, range), status: statusOf(node.status), open: range.live, unknownEnd: false, round: 0 }];
  }
  const items = node.spans.length
    ? node.spans.map((s) => ({ startedAt: s.startedAt, finishedAt: s.finishedAt, status: s.status, round: s.round }))
    : [{ startedAt: node.startedAt, finishedAt: node.finishedAt, status: node.status, round: 0 }];
  const out: Interval[] = [];
  for (const it of items) {
    const start = ms(it.startedAt);
    if (start === null) continue;
    const status = statusOf(it.status);
    const finished = ms(it.finishedAt);
    const open = status === "running" && finished === null;
    const unknownEnd = !open && finished === null;
    out.push({ start, end: finished ?? range.end, status, open, unknownEnd, round: it.round });
  }
  return out;
}

export interface Bar extends Interval {
  x: number;
  w: number;
}

/** Narrowest drawn bar, so sub-pixel rounds stay visible and hoverable. */
export const MIN_BAR_PX = 3;

/** Place intervals on a track `width` pixels wide. */
export function barGeometry(intervals: readonly Interval[], range: TimeRange, width: number): Bar[] {
  const span = range.end - range.start;
  const px = (t: number) => ((Math.min(Math.max(t, range.start), range.end) - range.start) / span) * width;
  return intervals.map((iv) => {
    let x = px(iv.start);
    let w = Math.max(px(iv.end) - x, MIN_BAR_PX);
    if (x + w > width) x = Math.max(0, width - w);
    w = Math.min(w, width);
    return { ...iv, x, w };
  });
}

const TICK_STEPS_S = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800];

/** Axis ticks at a "nice" step at least `minPx` apart, labelled relative to the start. */
export function ticks(range: TimeRange, width: number, minPx = 72): { x: number; label: string }[] {
  const span = range.end - range.start;
  if (width <= 0 || span <= 0) return [];
  const stepS = TICK_STEPS_S.find((s) => (s * 1000 * width) / span >= minPx) ?? TICK_STEPS_S[TICK_STEPS_S.length - 1];
  const out: { x: number; label: string }[] = [];
  for (let t = 0; t <= span; t += stepS * 1000) out.push({ x: (t / span) * width, label: offsetLabel(t / 1000) });
  return out;
}

function offsetLabel(s: number): string {
  if (s === 0) return "0";
  if (s < 60) return `${s}s`;
  if (s < 3600) return s % 60 ? `${Math.floor(s / 60)}m${s % 60}s` : `${s / 60}m`;
  if (s < 86400) return s % 3600 ? `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60)}m` : `${s / 3600}h`;
  return s % 86400 ? `${Math.floor(s / 86400)}d${Math.floor((s % 86400) / 3600)}h` : `${s / 86400}d`;
}

/** Compact elapsed time: "450ms", "12s", "3m 05s", "2h 10m". */
export function formatDuration(msValue: number): string {
  if (msValue < 1000) return `${Math.max(0, Math.round(msValue))}ms`;
  const s = Math.round(msValue / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ${String(m % 60).padStart(2, "0")}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

/**
 * Time a node spent working: the sum of an agent's rounds, a job's lifetime,
 * or the session's span for the root. Null when unknown.
 */
export function elapsedMs(node: ActivityNode, range: TimeRange | null, now: number): number | null {
  if (node.id === ROOT_ID) {
    const start = ms(node.startedAt);
    return start !== null && range ? rootEnd(node, range) - start : null;
  }
  const one = (startedAt: string, finishedAt: string, status: string): number | null => {
    const start = ms(startedAt);
    if (start === null) return null;
    const end = ms(finishedAt) ?? (status === "running" ? now : null);
    return end === null ? null : Math.max(0, end - start);
  };
  if (node.spans.length) {
    let total: number | null = null;
    for (const s of node.spans) {
      const d = one(s.startedAt, s.finishedAt, s.status);
      if (d !== null) total = (total ?? 0) + d;
    }
    return total;
  }
  return one(node.startedAt, node.finishedAt, node.status);
}

export function compactTokens(n: bigint): string {
  const v = Number(n);
  if (v < 1000) return String(v);
  if (v < 1_000_000) return `${(v / 1000).toFixed(v < 10_000 ? 1 : 0)}k`;
  return `${(v / 1_000_000).toFixed(1)}M`;
}

/** The kind/model badge text. */
export function badgeText(node: ActivityNode): string {
  if (node.id === ROOT_ID) return node.model || "coordinator";
  if (isJob(node)) return node.kind || "job";
  if (node.kind === "reviewer") return "reviewer";
  return node.model ? `${node.kind} · ${node.model}` : node.kind;
}

/** Bytes kept in the job-output viewer (older text is trimmed client-side). */
export const OUTPUT_VIEW_CHARS = 512 * 1024;

export interface OutputChunk {
  data: Uint8Array;
  start: bigint;
  end: bigint;
  retainedStart: bigint;
}

/**
 * Accumulates a job's output from GetJobOutput responses: tail first, then
 * forward from the end cursor. One streaming decoder keeps multi-byte
 * characters split across chunks intact; a gap (bytes the daemon dropped
 * before the client read them) resets it and leaves an inline marker.
 */
export class JobOutputBuffer {
  text = "";
  /** Absolute offset of the next byte to request. */
  cursor = 0n;
  /** Absolute offset of the first byte shown (null before the first chunk). */
  firstStart: bigint | null = null;
  /** Bytes before `firstStart` the daemon had already dropped at the first read. */
  droppedBefore = 0n;
  /** Bytes dropped between reads while following. */
  droppedWhileFollowing = 0n;
  /** The viewer trimmed its own oldest text to bound memory. */
  trimmed = false;
  private decoder = new TextDecoder();

  apply(chunk: OutputChunk): void {
    if (this.firstStart === null) {
      this.firstStart = chunk.start;
      this.droppedBefore = chunk.retainedStart;
    } else if (chunk.start > this.cursor) {
      const gap = chunk.start - this.cursor;
      this.droppedWhileFollowing += gap;
      this.text += this.decoder.decode();
      this.decoder = new TextDecoder();
      if (this.text && !this.text.endsWith("\n")) this.text += "\n";
      this.text += `[… ${gap} bytes dropped …]\n`;
    }
    if (chunk.data.length) this.text += this.decoder.decode(chunk.data, { stream: true });
    if (chunk.end > this.cursor) this.cursor = chunk.end;
    if (this.text.length > OUTPUT_VIEW_CHARS) {
      const cut = this.text.length - OUTPUT_VIEW_CHARS;
      const nl = this.text.indexOf("\n", cut);
      this.text = this.text.slice(nl >= 0 && nl - cut < 4096 ? nl + 1 : cut);
      this.trimmed = true;
    }
  }

  /** The stream ended: emit any bytes the decoder held back. */
  finish(): void {
    this.text += this.decoder.decode();
  }
}
