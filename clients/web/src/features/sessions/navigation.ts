// Keyboard navigation through the session list (pure): the next or previous
// row in the sidebar's visual order, and the next session waiting for an answer.
import { needsAnswer, sections, type FeedRow } from "./feed";

/** Rows in the order the sidebar shows them (needs-answer section first). */
export function visualOrder(rows: readonly FeedRow[]): FeedRow[] {
  return sections([...rows]).flatMap((s) => s.rows);
}

/**
 * The row `delta` steps from the active one, clamped at the ends; with no
 * active row (or one not listed), the first row going down and the last going up.
 */
export function adjacentSession(rows: readonly FeedRow[], activeId: string | null, delta: 1 | -1): FeedRow | null {
  const order = visualOrder(rows);
  if (!order.length) return null;
  const i = activeId ? order.findIndex((r) => r.session.sessionId === activeId) : -1;
  if (i < 0) return delta > 0 ? order[0] : order[order.length - 1];
  const j = Math.max(0, Math.min(order.length - 1, i + delta));
  return j === i ? null : order[j];
}

/** The next session waiting for an answer after the active one, wrapping; null when none (other than the active one) waits. */
export function nextNeedsAnswer(rows: readonly FeedRow[], activeId: string | null): FeedRow | null {
  const waiting = rows.filter((r) => needsAnswer(r.session));
  if (!waiting.length) return null;
  const i = activeId ? waiting.findIndex((r) => r.session.sessionId === activeId) : -1;
  const next = waiting[(i + 1) % waiting.length];
  return next.session.sessionId === activeId ? null : next;
}
