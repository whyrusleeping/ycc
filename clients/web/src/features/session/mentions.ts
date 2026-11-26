// Pure logic for `#123` task mentions in a composer: the token at the caret,
// the backlog tasks it may name, and the text after picking one.
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { compareIds, normalizeTaskId } from "../backlog/model";

/** A `#digits` token: `start` is the `#`, `end` is past its last digit. */
export interface MentionToken {
  start: number;
  end: number;
  digits: string;
}

/**
 * The `#123` token the caret is in or just after. The `#` must start a word
 * (so `a#1` and `##1` don't count) and the digits must end the word (so `#1a`
 * doesn't either).
 */
export function mentionAt(text: string, caret: number): MentionToken | null {
  const m = /(?:^|[^\w#&/])#(\d{1,6})$/.exec(text.slice(0, caret));
  if (!m) return null;
  const rest = /^\d*/.exec(text.slice(caret))![0];
  const end = caret + rest.length;
  if (/^\w/.test(text.slice(end))) return null;
  const digits = m[1] + rest;
  return { start: caret - m[1].length - 1, end, digits };
}

/**
 * Tasks a typed number may name: with a leading zero, ids starting with it
 * (`06` → 0600–0699); otherwise ids whose number does (`61` → 0061,
 * 0610–0619). An exact id comes first, then newest first.
 */
export function matchTasks(tasks: readonly BacklogTaskSummary[], digits: string, limit = 8): BacklogTaskSummary[] {
  const padded = digits.startsWith("0");
  const exact = normalizeTaskId(digits);
  return tasks
    .filter((t) => /^\d+$/.test(t.id) && (padded ? t.id.startsWith(digits) : String(Number(t.id)).startsWith(digits)))
    .sort((a, b) => Number(b.id === exact) - Number(a.id === exact) || compareIds(b.id, a.id))
    .slice(0, limit);
}

/** The text with the token replaced by `#id ` and the caret just after it. */
export function applyMention(text: string, token: MentionToken, id: string): { text: string; caret: number } {
  const after = text.slice(token.end);
  const insert = `#${id}${/^\s/.test(after) ? "" : " "}`;
  return { text: text.slice(0, token.start) + insert + after, caret: token.start + insert.length + (/^\s/.test(after) ? 1 : 0) };
}
