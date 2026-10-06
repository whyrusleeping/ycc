// Per-session "seen up to here" marks: which sessions have agent activity this
// browser has not looked at yet. A port of YccKit's SessionReadStore so the web
// and iOS clients agree on what "unread" means:
//
// - Daemon clocks only: marks are daemon event / summary timestamps, never the
//   browser's clock, so a skewed clock cannot read or unread a session.
// - The first list is read: the newest activity shown in any list is kept as a
//   watermark. A fresh browser baselines its first list as read; a session first
//   seen later is new (unread) only when it is newer than that watermark.
// - Running (or idle-but-awaiting-jobs) live sessions are never unread: the row
//   already says it is live. It goes unread once it stops producing.
//
// Marks persist in localStorage (a UI convenience, not a secret), capped by
// evicting the least recently active sessions first.
import type { SessionSummary } from "../../gen/ycc/v1/ycc_pb";

export interface KeyValueStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/** Parse an RFC3339 daemon timestamp to epoch ms (fractions past ms are dropped). */
export function parseTimestamp(value: string): number | null {
  if (!value) return null;
  const t = Date.parse(value.replace(/(\.\d{3})\d+/, "$1"));
  return Number.isNaN(t) ? null : t;
}

/** A summary's recency (lastActivity, else startedAt) as text and ms. */
export function recencyStamp(s: Pick<SessionSummary, "lastActivity" | "startedAt">): { text: string; ms: number } | null {
  const a = parseTimestamp(s.lastActivity);
  if (a !== null) return { text: s.lastActivity, ms: a };
  const b = parseTimestamp(s.startedAt);
  return b !== null ? { text: s.startedAt, ms: b } : null;
}

type Summary = Pick<SessionSummary, "sessionId" | "lastActivity" | "startedAt" | "status" | "live" | "awaitingJobs">;

export const READ_MARKS_KEY = "ycc.sessionReadMarks";

export class SessionReadStore {
  private marks: Record<string, string> = {};
  private watermark: string | null = null;
  /** Bumped on every change (React subscribes to it). */
  revision = 0;
  private listeners = new Set<() => void>();

  constructor(
    private storage: KeyValueStorage | null = null,
    readonly key: string = READ_MARKS_KEY,
    private limit = 600,
  ) {
    this.load();
  }

  /** (Re)read persisted marks, e.g. after another tab wrote them. */
  load() {
    let marks: Record<string, string> = {};
    let watermark: string | null = null;
    try {
      const raw = this.storage?.getItem(this.key);
      const parsed = raw ? JSON.parse(raw) : null;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") marks[k] = v;
      }
      watermark = this.storage?.getItem(this.key + ".watermark") || null;
    } catch {
      marks = {};
    }
    this.marks = marks;
    this.watermark = watermark;
    if (!this.watermark && Object.keys(marks).length) {
      // Marks written without a watermark: derive one from the newest mark.
      let best: { text: string; ms: number } | null = null;
      for (const v of Object.values(marks)) {
        const ms = parseTimestamp(v);
        if (ms !== null && (!best || ms > best.ms)) best = { text: v, ms };
      }
      if (best) {
        this.watermark = best.text;
        try {
          this.storage?.setItem(this.key + ".watermark", best.text);
        } catch {
          // ignore
        }
      }
    }
    this.changed();
  }

  subscribe = (l: () => void) => {
    this.listeners.add(l);
    return () => {
      this.listeners.delete(l);
    };
  };

  getRevision = () => this.revision;

  // MARK: queries

  /** Whether a session has agent activity newer than what this browser saw. Unknown sessions are not unread. */
  isUnread(s: Summary): boolean {
    if (s.live && s.status.toLowerCase() === "running") return false;
    if (s.live && s.awaitingJobs) return false;
    const mark = this.marks[s.sessionId];
    if (mark === undefined) return false;
    const activity = recencyStamp(s);
    const markMs = parseTimestamp(mark);
    if (!activity || markMs === null) return false;
    return activity.ms > markMs;
  }

  unreadCount(sessions: readonly Summary[]): number {
    let n = 0;
    for (const s of sessions) if (this.isUnread(s)) n++;
    return n;
  }

  /** Whether everything through `timestamp` has been seen for a session. */
  hasSeen(sessionId: string, timestamp: string): boolean {
    const mark = this.marks[sessionId];
    const markMs = mark === undefined ? null : parseTimestamp(mark);
    const ms = parseTimestamp(timestamp);
    return markMs !== null && ms !== null && ms <= markMs;
  }

  // MARK: marking

  /**
   * Baseline the first list and later back-catalogue as read. An unknown
   * session newer than the pre-refresh watermark is genuinely new: its mark
   * starts at that watermark, so its activity is unread. Known sessions keep
   * their mark (that is what makes them go unread).
   */
  noteSeen(sessions: readonly Summary[]) {
    const priorText = this.watermark;
    const priorMs = priorText ? parseTimestamp(priorText) : null;
    let changed = false;
    for (const s of sessions) {
      if (this.marks[s.sessionId] !== undefined) continue;
      const recency = recencyStamp(s);
      if (priorText && priorMs !== null && recency && recency.ms > priorMs) {
        this.marks[s.sessionId] = priorText;
      } else {
        // No usable timestamp cannot be compared later either; record what we have.
        this.marks[s.sessionId] = recency?.text ?? stamp(s);
      }
      changed = true;
    }
    let newest = priorText && priorMs !== null ? { text: priorText, ms: priorMs } : null;
    for (const s of sessions) {
      const recency = recencyStamp(s);
      if (recency && (!newest || recency.ms > newest.ms)) newest = recency;
    }
    if (newest && newest.text !== this.watermark) {
      this.watermark = newest.text;
      changed = true;
    }
    if (changed) this.persist();
  }

  /** Everything up to `timestamp` (a daemon event stamp) is seen; never moves a mark back. */
  markRead(sessionId: string, timestamp: string) {
    if (this.apply(sessionId, timestamp)) this.persist();
  }

  /** Mark a listed session read at its last reported activity ("I know, stop nagging"). */
  markSummaryRead(s: Summary) {
    if (this.apply(s.sessionId, stamp(s))) this.persist();
  }

  /** Mark a whole list read in one write. */
  markAllRead(sessions: readonly Summary[]) {
    let changed = false;
    for (const s of sessions) if (this.apply(s.sessionId, stamp(s))) changed = true;
    if (changed) this.persist();
  }

  private apply(sessionId: string, timestamp: string): boolean {
    if (!sessionId || !timestamp) return false;
    const existing = this.marks[sessionId];
    if (existing !== undefined) {
      if (existing === timestamp) return false;
      const a = parseTimestamp(existing);
      const b = parseTimestamp(timestamp);
      if (a !== null && b !== null && b <= a) return false;
    }
    this.marks[sessionId] = timestamp;
    return true;
  }

  // MARK: persistence

  private persist() {
    this.evict();
    try {
      this.storage?.setItem(this.key, JSON.stringify(this.marks));
      if (this.watermark) this.storage?.setItem(this.key + ".watermark", this.watermark);
      else this.storage?.removeItem(this.key + ".watermark");
    } catch {
      // Storage full or unavailable: marks stay in memory for this tab.
    }
    this.changed();
  }

  private changed() {
    this.revision++;
    for (const l of this.listeners) l();
  }

  /** Keep the most recently active marks; unparseable marks go first (they can never make a row unread). */
  private evict() {
    const entries = Object.entries(this.marks);
    if (entries.length <= this.limit) return;
    const ranked = entries
      .map(([k, v]) => ({ k, v, ms: parseTimestamp(v) ?? Number.NEGATIVE_INFINITY }))
      .sort((a, b) => (a.ms !== b.ms ? b.ms - a.ms : a.k < b.k ? -1 : 1));
    this.marks = Object.fromEntries(ranked.slice(0, this.limit).map((e) => [e.k, e.v]));
  }
}

function stamp(s: Summary): string {
  return s.lastActivity || s.startedAt;
}
