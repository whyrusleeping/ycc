// Inbox eligibility is separate from read watermarks: hiding automatic work
// neither deletes its history nor pretends the user has read it.
import type { SessionSummary } from "../../gen/ycc/v1/ycc_pb";
import { displayProject, displayTitle, needsAnswer, taskIds, type FeedRow } from "./feed";
import { isActive } from "./activity";

export type SessionFilter = "inbox" | "unread" | "questions" | "followup" | "active" | "all";

export const SESSION_FILTERS: readonly { value: SessionFilter; label: string }[] = [
  { value: "inbox", label: "Your inbox" },
  { value: "unread", label: "Unread" },
  { value: "questions", label: "Needs answer" },
  { value: "followup", label: "Follow-up" },
  { value: "active", label: "Active" },
  { value: "all", label: "All history" },
];

/** Unknown legacy origin stays visible; current-loop membership is positive evidence. */
export function isInboxSession(s: SessionSummary, loopOwned = false): boolean {
  if (s.waitingInput || s.followUp || s.humanParticipated || s.origin === "user") return true;
  if (["work_loop", "memory_groom", "automation"].includes(s.origin ?? "")) return false;
  return !loopOwned;
}

/** A pending human question is never hidden by inbox filters or a search query. */
export function filterSessions(
  rows: readonly FeedRow[],
  filter: SessionFilter,
  query: string,
  unread: (row: FeedRow) => boolean,
  loopIds: ReadonlySet<string>,
  activeSessionId?: string,
): FeedRow[] {
  const needle = query.trim().toLocaleLowerCase();
  return rows.filter((row) => {
    const s = row.session;
    // Keep the selected sidebar row as a navigation anchor after it becomes read.
    if (needsAnswer(s) || s.sessionId === activeSessionId) return true;
    const personal = isInboxSession(s, loopIds.has(s.sessionId));
    let include: boolean;
    switch (filter) {
      case "inbox": include = personal; break;
      case "unread": include = personal && unread(row); break;
      case "questions": include = false; break;
      case "followup": include = s.followUp; break;
      case "active": include = personal && isActive(s); break;
      case "all": include = true; break;
    }
    const text = [displayTitle(s), displayProject(row), s.sessionId, ...taskIds(s)].join(" ").toLocaleLowerCase();
    return include && (!needle || text.includes(needle));
  });
}
