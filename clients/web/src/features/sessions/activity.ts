// Per-project attention counts for the project switcher (pure): sessions
// waiting for an answer and sessions with unread agent activity, as the iOS
// workspace drawer's badges count them.
import { needsAnswer, type FeedRow } from "./feed";

export interface Activity {
  needsAnswer: number;
  unread: number;
}

export function projectActivity(rows: readonly FeedRow[], isUnread: (row: FeedRow) => boolean): { total: Activity; byProject: Map<string, Activity> } {
  const total: Activity = { needsAnswer: 0, unread: 0 };
  const byProject = new Map<string, Activity>();
  for (const row of rows) {
    const waiting = needsAnswer(row.session);
    // A waiting session is never unread (it is live and blocked), so they don't double count.
    const unread = !waiting && isUnread(row);
    if (!waiting && !unread) continue;
    const a = byProject.get(row.project) ?? { needsAnswer: 0, unread: 0 };
    if (waiting) {
      a.needsAnswer++;
      total.needsAnswer++;
    } else {
      a.unread++;
      total.unread++;
    }
    byProject.set(row.project, a);
  }
  return { total, byProject };
}

/** " · 1 waiting · 2 unread" (empty when nothing needs attention). */
export function activitySuffix(a: Activity | undefined): string {
  if (!a) return "";
  const parts: string[] = [];
  if (a.needsAnswer) parts.push(`${a.needsAnswer} waiting`);
  if (a.unread) parts.push(`${a.unread} unread`);
  return parts.length ? ` · ${parts.join(" · ")}` : "";
}
