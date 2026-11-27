// Session coordination (spec §7.3): pure helpers over ListProjectNotices for
// the live sessions view, held-task badge labels, the question screen's notices,
// and coordination_notice transcript rows. Notice and intent text is
// untrusted peer content: it renders as plain text, never markup.
import type { ProjectNotice, TaskHolder } from "../../gen/ycc/v1/ycc_pb";

/**
 * Daemon session ids as they appear in notice text: named Docker-style ids
 * ("s_amber-cedar-4f2a") and legacy hex ids ("s_" and 16 hex digits).
 */
const SESSION_ID = /\bs_(?:[a-z]+-[a-z]+-[0-9a-f]{4}|[0-9a-f]{16})\b/g;
const NAMED_SESSION_ID = /^s_([a-z]+-[a-z]+)-[0-9a-f]{4}$/;

export type TextPart = { kind: "text"; text: string } | { kind: "session"; id: string };

/** Split text into plain runs and session ids, so the ids can link to their session. */
export function splitSessionIds(text: string): TextPart[] {
  const parts: TextPart[] = [];
  let last = 0;
  for (const m of text.matchAll(SESSION_ID)) {
    const at = m.index ?? 0;
    if (at > last) parts.push({ kind: "text", text: text.slice(last, at) });
    parts.push({ kind: "session", id: m[0] });
    last = at + m[0].length;
  }
  if (last < text.length) parts.push({ kind: "text", text: text.slice(last) });
  return parts;
}

/** A board digest ("[project notice board] …") or an overlap notice ("[coordination notice] …"). */
export type CoordinationVariant = "board" | "overlap";

export function coordinationVariant(text: string): CoordinationVariant {
  return text.trimStart().startsWith("[project notice board]") ? "board" : "overlap";
}

/** The row's one-line summary: the first line without its bracketed tag. */
export function coordinationHeadline(text: string): string {
  for (const line of text.split("\n")) {
    const t = line.replace(/^\s*\[(?:project notice board|coordination notice)\]\s*/, "").trim();
    if (t) return t;
  }
  return "";
}

/** "in 7m", "in 1h12m"; "expired" once past. Empty for an unparsable time. */
export function expiresIn(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const mins = Math.ceil((t - now) / 60_000);
  if (mins <= 0) return "expired";
  if (mins < 60) return `in ${mins}m`;
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  return m ? `in ${h}h${String(m).padStart(2, "0")}m` : `in ${h}h`;
}

/** Expiry choices for a notice the user posts (the daemon allows 1 minute to 24 hours). */
export const EXPIRY_CHOICES: readonly { label: string; seconds: number }[] = [
  { label: "30 minutes", seconds: 30 * 60 },
  { label: "1 hour", seconds: 60 * 60 },
  { label: "3 hours", seconds: 3 * 60 * 60 },
  { label: "8 hours", seconds: 8 * 60 * 60 },
  { label: "24 hours", seconds: 24 * 60 * 60 },
];

/** Bounds the daemon applies to a notice (spec §7.3). */
export const NOTICE_MAX = 300;
export const SUBJECT_MAX = 60;

/** Who posted a notice: the user, or the posting session's actor. */
export function posterLabel(n: ProjectNotice): string {
  if (!n.sessionId) return "you";
  return n.actor && n.actor !== "coordinator" ? n.actor : "";
}

const ACTIVITY: Record<string, { label: string; tone: string }> = {
  question: { label: "waiting on a question", tone: "waiting" },
  jobs: { label: "background work", tone: "background" },
  running: { label: "running", tone: "running" },
  paused: { label: "paused", tone: "paused" },
  idle: { label: "idle", tone: "idle" },
  error: { label: "error", tone: "error" },
  stopped: { label: "stopped", tone: "idle" },
};

/** A live session's activity as a badge label and tone (badge-<tone>). */
export function activityBadge(activity: string): { label: string; tone: string } {
  return ACTIVITY[activity] ?? { label: activity || "unknown", tone: "idle" };
}

/**
 * A compact human-readable handle for a session id: the word pair
 * ("amber-cedar") for named ids, or a truncated prefix ("s_1a2b3c4d…") for
 * legacy hex ids.
 */
export function shortSessionId(id: string): string {
  const m = NAMED_SESSION_ID.exec(id);
  if (m) return m[1];
  return id.length > 10 ? `${id.slice(0, 10)}…` : id;
}

/**
 * Compact holder label for tight surfaces (board cards): a named loop session
 * shows its handle ("amber-cedar"), a legacy hex loop session shows "loop"
 * (avoiding truncated hex soup), and other sessions show their title or short id.
 */
export function compactHolderLabel(s: TaskHolder): string {
  if (s.loop) {
    const m = NAMED_SESSION_ID.exec(s.sessionId);
    return m ? m[1] : "loop";
  }
  return s.title.trim() || shortSessionId(s.sessionId);
}

/**
 * Who holds a task, for the held badge: a work-loop session by its short id
 * (loop sessions share one opening prompt, so their titles do not tell them
 * apart), any other session by its title, else its short id.
 */
export function holderLabel(s: TaskHolder): string {
  if (s.loop) return `loop ${shortSessionId(s.sessionId)}`;
  return s.title.trim() || shortSessionId(s.sessionId);
}
