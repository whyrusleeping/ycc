// Per-project activity counts for the project switcher (pure): sessions
// waiting for an answer, sessions with unread agent activity, and live
// sessions with work in flight, as the iOS workspace drawer's badges count them.
import { needsAnswer, type FeedRow } from "./feed";
import type { SessionSummary } from "../../gen/ycc/v1/ycc_pb";

export interface Activity {
  needsAnswer: number;
  unread: number;
  /** Live sessions running or paused, or idle while delegated work will resume them. */
  active: number;
}

const empty = (): Activity => ({ needsAnswer: 0, unread: 0, active: 0 });

/** Work in flight (YccKit's ProjectActivity.active); a persisted log's status is history. */
export function isActive(s: SessionSummary): boolean {
  if (!s.live) return false;
  switch (s.status.toLowerCase()) {
    case "running":
    case "paused":
      return true;
    case "idle":
      return s.awaitingJobs;
    default:
      return false;
  }
}

export function projectActivity(rows: readonly FeedRow[], isUnread: (row: FeedRow) => boolean): { total: Activity; byProject: Map<string, Activity> } {
  const total = empty();
  const byProject = new Map<string, Activity>();
  for (const row of rows) {
    const waiting = needsAnswer(row.session);
    // A waiting session is never unread or active (it is live and blocked), so they don't double count.
    const unread = !waiting && isUnread(row);
    const active = !waiting && isActive(row.session);
    if (!waiting && !unread && !active) continue;
    const a = byProject.get(row.project) ?? empty();
    for (const counts of [a, total]) {
      if (waiting) counts.needsAnswer++;
      if (unread) counts.unread++;
      if (active) counts.active++;
    }
    byProject.set(row.project, a);
  }
  return { total, byProject };
}

export function hasActivity(a: Activity | undefined): boolean {
  return !!a && (a.needsAnswer > 0 || a.unread > 0 || a.active > 0);
}

/** "1 waiting for an answer, 2 unread, 1 active" (empty when quiet), for labels and tooltips. */
export function activityDescription(a: Activity | undefined): string {
  if (!a) return "";
  const parts: string[] = [];
  if (a.needsAnswer) parts.push(`${a.needsAnswer} waiting for an answer`);
  if (a.unread) parts.push(`${a.unread} unread`);
  if (a.active) parts.push(`${a.active} active`);
  return parts.join(", ");
}
