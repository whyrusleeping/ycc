// Pure notification logic: which session-list changes are worth a browser
// notification (a new question, a finished turn, an error), whether to show
// one now (only when the user is not looking at that session), and a
// dedupe ledger shared by every tab through storage.
import type { SessionSummary } from "../../gen/ycc/v1/ycc_pb";
import { parseTimestamp, recencyStamp, type KeyValueStorage } from "../sessions/readStore";

export type NotifyKind = "question" | "done" | "error";

/** What one poll showed of a session. */
export interface Observation {
  live: boolean;
  waiting: boolean;
  awaitingJobs: boolean;
  status: string;
  lastActivity: string;
}

export interface SessionEvent {
  kind: NotifyKind;
  sessionId: string;
  project: string;
  /** Stable across tabs and reloads: one notification per key. */
  key: string;
  /** The activity stamp the event is about (read marks at or past it suppress it). */
  activity: string;
}

export interface WatchState {
  sessions: Map<string, Observation>;
  /** Newest activity seen in any earlier poll (ms): older first sightings are back-catalogue. */
  watermark: number;
}

function observation(s: SessionSummary): Observation {
  return {
    live: s.live,
    waiting: s.live && s.waitingInput,
    awaitingJobs: s.live && s.awaitingJobs,
    status: s.status.toLowerCase(),
    lastActivity: s.lastActivity || s.startedAt,
  };
}

/**
 * Compare a poll of the session list with the previous one. The first poll
 * (`prev` null) only baselines. A session first seen later counts as new
 * only when it is newer than everything seen before (a page of older history
 * is not news).
 */
export function detectEvents(
  prev: WatchState | null,
  rows: readonly { session: SessionSummary; project: string }[],
): { state: WatchState; events: SessionEvent[] } {
  const sessions = new Map<string, Observation>(prev?.sessions ?? []);
  let watermark = prev?.watermark ?? Number.NEGATIVE_INFINITY;
  const events: SessionEvent[] = [];
  for (const { session: s, project } of rows) {
    const now = observation(s);
    const before = prev ? prev.sessions.get(s.sessionId) : undefined;
    sessions.set(s.sessionId, now);
    if (!prev) continue;
    const recency = recencyStamp(s)?.ms ?? Number.NEGATIVE_INFINITY;
    if (!before && !(recency > prev.watermark)) continue;
    const was: Observation = before ?? { live: false, waiting: false, awaitingJobs: false, status: "", lastActivity: "" };
    const advanced = (parseTimestamp(now.lastActivity) ?? Number.NEGATIVE_INFINITY) > (parseTimestamp(was.lastActivity) ?? Number.NEGATIVE_INFINITY);
    const base = { sessionId: s.sessionId, project, activity: now.lastActivity };
    if (now.waiting) {
      if (!was.waiting) events.push({ ...base, kind: "question", key: `question:${s.sessionId}:${now.lastActivity}` });
      continue;
    }
    if (now.status === "error") {
      if (was.status !== "error" || advanced) events.push({ ...base, kind: "error", key: `error:${s.sessionId}:${now.lastActivity}` });
      continue;
    }
    // Activity that came to rest: a turn finished, or the session ended.
    const resting = (now.status === "idle" && !now.awaitingJobs) || now.status === "stopped";
    if (resting && advanced) events.push({ ...base, kind: "done", key: `done:${s.sessionId}:${now.lastActivity}` });
  }
  for (const { session: s } of rows) {
    const r = recencyStamp(s)?.ms;
    if (r !== undefined && r > watermark) watermark = r;
  }
  return { state: { sessions, watermark }, events };
}

export interface NotifyContext {
  /** Notifications enabled by the user and permitted by the browser. */
  enabled: boolean;
  /** The tab is hidden or the window does not have focus. */
  away: boolean;
  /** The session shown in this tab, if any. */
  activeSessionId: string | null;
  /** The user has already seen this activity (read marks, possibly from another tab). */
  seen: (sessionId: string, activity: string) => boolean;
}

/** Notify only about what the user is not looking at and has not already seen. */
export function shouldNotify(e: SessionEvent, ctx: NotifyContext): boolean {
  if (!ctx.enabled) return false;
  if (!ctx.away && e.sessionId === ctx.activeSessionId) return false;
  // A question stays relevant even if its row was read before it was asked.
  if (e.kind !== "question" && ctx.seen(e.sessionId, e.activity)) return false;
  return true;
}

export const NOTIFIED_KEY = "ycc.notified";
const LEDGER_LIMIT = 300;
const LEDGER_TTL_MS = 3 * 24 * 60 * 60 * 1000;

/**
 * Claim a notification key in the shared ledger: true the first time any tab
 * (or this one after a reload) claims it, false afterwards.
 */
export function claimNotification(storage: KeyValueStorage | null, key: string, now: number = Date.now()): boolean {
  if (!storage) return true;
  let ledger: [string, number][] = [];
  try {
    const parsed = JSON.parse(storage.getItem(NOTIFIED_KEY) || "[]");
    if (Array.isArray(parsed)) ledger = parsed.filter((e) => Array.isArray(e) && typeof e[0] === "string" && typeof e[1] === "number");
  } catch {
    ledger = [];
  }
  ledger = ledger.filter(([, at]) => now - at < LEDGER_TTL_MS);
  if (ledger.some(([k]) => k === key)) return false;
  ledger.push([key, now]);
  try {
    storage.setItem(NOTIFIED_KEY, JSON.stringify(ledger.slice(-LEDGER_LIMIT)));
  } catch {
    // ignore: worst case a second tab also notifies (the tag still collapses it)
  }
  return true;
}

/** The notification text for a session event. */
export function notificationText(kind: NotifyKind, title: string, project: string): { title: string; body: string } {
  const where = project ? ` · ${project}` : "";
  switch (kind) {
    case "question":
      return { title: `Question: ${title}`, body: `The agent is waiting for your answer${where}` };
    case "error":
      return { title: `Error: ${title}`, body: `The session stopped on an error${where}` };
    case "done":
      return { title: `Finished: ${title}`, body: `The agent finished and is idle${where}` };
  }
}

/** The tab title with the number of sessions waiting for an answer, e.g. "(2) Backlog · ycc". */
export function titleWithCount(base: string, needsAnswer: number): string {
  return needsAnswer > 0 ? `(${needsAnswer}) ${base}` : base;
}
