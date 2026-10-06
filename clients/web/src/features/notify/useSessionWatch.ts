// Shell-level watch over the session list (mounted once): baselines read
// marks for unread tracking, keeps the tab title's needs-answer count, and
// raises a browser notification for a question, a finished turn, or an
// error the user is not looking at.
import { useEffect, useRef } from "react";
import { useNavigate } from "react-router";
import { useSessionFeed } from "../../api/queries";
import { paths } from "../../app/paths";
import { setNeedsAnswerCount } from "../../app/title";
import { displayProject, displayTitle, needsAnswer, type FeedRow } from "../sessions/feed";
import { readMarks } from "../sessions/unread";
import { notificationsActive, showNotification, userAway } from "./notifier";
import { claimNotification, detectEvents, notificationText, shouldNotify, type WatchState } from "./policy";

function storage() {
  try {
    return localStorage;
  } catch {
    return null;
  }
}

export function useSessionWatch(activeSessionId: string | null) {
  const { loads } = useSessionFeed(null);
  const navigate = useNavigate();
  const watch = useRef<WatchState | null>(null);
  const active = useRef(activeSessionId);
  active.current = activeSessionId;
  useEffect(() => {
    if (!loads) return;
    const ok = loads.filter((l) => !l.error);
    const rows: FeedRow[] = [];
    const seen = new Set<string>();
    for (const l of ok) {
      for (const session of [...l.sessions, ...l.pinned]) {
        if (seen.has(session.sessionId)) continue;
        seen.add(session.sessionId);
        rows.push({ session, project: l.project });
      }
    }
    readMarks.noteSeen(rows.map((r) => r.session));
    setNeedsAnswerCount(rows.filter((r) => needsAnswer(r.session)).length);
    const { state, events } = detectEvents(watch.current, rows);
    watch.current = state;
    for (const e of events) {
      const away = userAway();
      const ctx = {
        enabled: notificationsActive(),
        away,
        activeSessionId: active.current,
        seen: (id: string, ts: string) => readMarks.hasSeen(id, ts),
      };
      if (!shouldNotify(e, ctx)) {
        // The user is looking at it here: record it as handled so a hidden
        // tab doesn't notify about it either.
        if (!away && e.sessionId === active.current) claimNotification(storage(), e.key);
        continue;
      }
      const row = rows.find((r) => r.session.sessionId === e.sessionId);
      if (!row) continue;
      const text = notificationText(e.kind, displayTitle(row.session), displayProject(row));
      showNotification({ key: e.key, ...text, onClick: () => navigate(paths.session(e.project, e.sessionId)) });
    }
  }, [loads, navigate]);
}
