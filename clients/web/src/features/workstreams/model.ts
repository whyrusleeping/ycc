// Pure workstream logic: lifecycle and integration-queue state, badge labels
// and tones, which actions a row offers, list grouping, the spawn request,
// and the sidebar indicator. Mirrors YccKit's WorkstreamsModel.
import type { BacklogTaskSummary, WorkstreamInfo } from "../../gen/ycc/v1/ycc_pb";
import { isActionable } from "../backlog/model";

export type WorkstreamStatus = "active" | "ready" | "needs_attention" | "merged" | "discarded" | "stale" | "unknown";

export function workstreamStatus(w: Pick<WorkstreamInfo, "status">): WorkstreamStatus {
  const s = w.status.trim().toLowerCase();
  switch (s) {
    case "active":
    case "ready":
    case "needs_attention":
    case "merged":
    case "discarded":
    case "stale":
      return s;
    default:
      return "unknown";
  }
}

export function statusTitle(s: WorkstreamStatus): string {
  switch (s) {
    case "active":
      return "Working";
    case "ready":
      return "Ready";
    case "needs_attention":
      return "Needs attention";
    case "merged":
      return "Merged";
    case "discarded":
      return "Discarded";
    case "stale":
      return "Stale";
    case "unknown":
      return "Unknown";
  }
}

/** Preview and merge are allowed for the daemon's in-flight statuses. */
export const isMergeable = (s: WorkstreamStatus) => s === "active" || s === "ready" || s === "needs_attention";
/** Discard is allowed for in-flight workstreams and stale registry entries. */
export const isDiscardable = (s: WorkstreamStatus) => isMergeable(s) || s === "stale";
export const isTerminal = (s: WorkstreamStatus) => s === "merged" || s === "discarded";

export type IntegrationState = "queued" | "integrating" | "idle" | "unknown";

export function integrationState(w: Pick<WorkstreamInfo, "integrationState">): IntegrationState {
  const s = w.integrationState.trim().toLowerCase();
  if (s === "") return "idle";
  if (s === "queued" || s === "integrating") return s;
  return "unknown";
}

export function integrationMode(w: Pick<WorkstreamInfo, "integrationMode">): string {
  return w.integrationMode.trim().toLowerCase();
}

/** Gate-mode ready rows: eligible for one-action "Merge all ready". */
export function isGateEligible(w: WorkstreamInfo): boolean {
  return workstreamStatus(w) === "ready" && integrationMode(w) === "gate";
}

/**
 * The badge: terminal and needs-attention lifecycle states win; otherwise a
 * live queue state (Queued/Integrating), then gate-mode ready reads "Gated".
 */
export function badgeTitle(w: WorkstreamInfo): string {
  const s = workstreamStatus(w);
  if (s === "needs_attention" || s === "merged" || s === "discarded" || s === "stale") return statusTitle(s);
  const q = integrationState(w);
  if (q === "queued") return "Queued";
  if (q === "integrating") return "Integrating";
  if (isGateEligible(w)) return "Gated";
  return statusTitle(s);
}

export type BadgeTone = "live" | "ready" | "queue" | "attention" | "done" | "muted" | "warn";

export function badgeTone(w: WorkstreamInfo): BadgeTone {
  const s = workstreamStatus(w);
  const q = integrationState(w);
  if ((s === "active" || s === "ready") && (q === "queued" || q === "integrating")) return "queue";
  switch (s) {
    case "active":
      return "live";
    case "ready":
      return "ready";
    case "needs_attention":
      return "attention";
    case "merged":
      return "done";
    case "stale":
      return "warn";
    default:
      return "muted";
  }
}

export function statusReason(w: WorkstreamInfo): string | null {
  const r = w.statusReason.trim();
  return r ? r : null;
}

export function commitSummary(w: Pick<WorkstreamInfo, "commitCount">): string {
  const n = Number(w.commitCount);
  if (n < 1) return "no commits";
  return n === 1 ? "1 commit" : `${n} commits`;
}

/**
 * Retry re-queues automatic integration: offered for needs-attention rows,
 * and for an auto-mode ready row that is not already queued or integrating.
 */
export function canRetry(w: WorkstreamInfo): boolean {
  const s = workstreamStatus(w);
  if (s === "needs_attention") return true;
  return s === "ready" && integrationMode(w) === "auto" && integrationState(w) === "idle";
}

/** Rows whose state can still change without the user (worth polling quickly). */
export function isBusy(w: WorkstreamInfo): boolean {
  const s = workstreamStatus(w);
  const q = integrationState(w);
  return s === "active" || q === "queued" || q === "integrating";
}

export function pollInterval(list: readonly WorkstreamInfo[] | undefined): number {
  return list?.some(isBusy) ? 3_000 : 15_000;
}

/** In-flight rows (needs attention first, then newest), and finished history (newest first). */
export function groupWorkstreams(list: readonly WorkstreamInfo[]): { inFlight: WorkstreamInfo[]; finished: WorkstreamInfo[] } {
  const newest = (a: WorkstreamInfo, b: WorkstreamInfo) => (a.createdAt < b.createdAt ? 1 : a.createdAt > b.createdAt ? -1 : 0);
  const rank = (w: WorkstreamInfo) => (workstreamStatus(w) === "needs_attention" ? 0 : 1);
  const inFlight = list.filter((w) => !isTerminal(workstreamStatus(w))).sort((a, b) => rank(a) - rank(b) || newest(a, b));
  const finished = list.filter((w) => isTerminal(workstreamStatus(w))).sort(newest);
  return { inFlight, finished };
}

/** A short label: the branch, else the id. */
export function branchLabel(w: WorkstreamInfo): string {
  return w.branch.trim() || w.id;
}

export interface WorkstreamCounts {
  inFlight: number;
  attention: number;
  working: number;
}

export function counts(list: readonly WorkstreamInfo[]): WorkstreamCounts {
  let inFlight = 0;
  let attention = 0;
  let working = 0;
  for (const w of list) {
    const s = workstreamStatus(w);
    if (isTerminal(s)) continue;
    inFlight++;
    if (s === "needs_attention") attention++;
    if (isBusy(w)) working++;
  }
  return { inFlight, attention, working };
}

/** The sidebar's workstreams indicator: null with nothing in flight. */
export function workstreamIndicator(list: readonly WorkstreamInfo[] | undefined): { label: string; title: string; tone: "live" | "attention" | "idle" } | null {
  if (!list) return null;
  const c = counts(list);
  if (c.inFlight === 0) return null;
  const parts = [`${c.inFlight} in flight`];
  if (c.working) parts.push(`${c.working} working`);
  if (c.attention) parts.push(`${c.attention} need${c.attention === 1 ? "s" : ""} attention`);
  return {
    label: c.attention ? `${c.attention}!` : String(c.inFlight),
    title: parts.join(" · "),
    tone: c.attention ? "attention" : c.working ? "live" : "idle",
  };
}

export interface MergeAllSummary {
  merged: number;
  error: string | null;
}

export function mergeAllMessage(s: MergeAllSummary): string {
  const merged = `Merged ${s.merged} workstream${s.merged === 1 ? "" : "s"}`;
  return s.error ? `${merged}, then failed: ${s.error}` : merged;
}

/**
 * Accept every gate-mode ready row in order, stopping at the first failure or
 * non-merged response (an honest partial count).
 */
export async function mergeAllReady(
  rows: readonly WorkstreamInfo[],
  merge: (id: string) => Promise<{ merged: boolean; needsAccept: boolean; conflicts: string[] }>,
  describeError: (err: unknown) => string,
): Promise<MergeAllSummary> {
  let merged = 0;
  for (const w of rows.filter(isGateEligible)) {
    try {
      const r = await merge(w.id);
      if (r.merged) {
        merged++;
        continue;
      }
      if (r.conflicts.length) return { merged, error: `${w.id} conflicts: ${r.conflicts.join(", ")}` };
      return { merged, error: r.needsAccept ? `${w.id} still needs review` : `${w.id} was not merged` };
    } catch (err) {
      return { merged, error: describeError(err) };
    }
  }
  return { merged, error: null };
}

export interface SpawnDraft {
  taskId: string;
  prompt: string;
  baseRef: string;
}

/** The default prompt for a task-backed workstream (as the TUI seeds it). */
export function taskPrompt(t: Pick<BacklogTaskSummary, "id" | "title">): string {
  return `Work on backlog task ${t.id}: ${t.title}`;
}

export function buildSpawnRequest(project: string, d: SpawnDraft) {
  return { project, taskId: d.taskId.trim(), prompt: d.prompt.trim(), baseRef: d.baseRef.trim() };
}

/** A workstream needs either a task or a prompt to start useful work. */
export function canSpawn(project: string, d: SpawnDraft): boolean {
  return project.trim() !== "" && (d.taskId.trim() !== "" || d.prompt.trim() !== "");
}

/** Tasks offered to a new workstream: actionable work first (priority, then id). */
export function spawnableTasks(tasks: readonly BacklogTaskSummary[]): BacklogTaskSummary[] {
  return tasks
    .filter((t) => isActionable(t))
    .sort((a, b) => (a.priority || 99) - (b.priority || 99) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}
