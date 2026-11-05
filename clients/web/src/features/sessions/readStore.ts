// Per-session "seen up to here" marks: which sessions have agent activity the
// user has not looked at yet. The daemon keeps one shared mark per session
// (SessionSummary.readThrough, advanced by MarkSessionsRead), so reading a
// session on any client clears it everywhere. This store layers a local cache
// over it:
//
// - Effective mark = the later of the local mark and the daemon's readThrough.
//   Local marks apply at once (optimistic) and are pushed to the daemon,
//   coalesced per project. A local mark later than the daemon's is re-pushed
//   whenever the session is listed again, which retries failed pushes and
//   migrates marks this browser kept before the daemon tracked them.
// - Listing never creates marks: the daemon's per-project baseline decides
//   what a new client sees as read. No mark at all reads as read.
// - Daemon clocks only: marks are daemon event / summary timestamps, never the
//   browser's clock, so a skewed clock cannot read or unread a session.
// - Running (or idle-but-awaiting-jobs) live sessions are never unread: the row
//   already says it is live. It goes unread once it stops producing.
//
// Local marks persist in localStorage (a UI convenience, not a secret), capped
// by evicting the least recently active sessions first.
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

type Summary = Pick<SessionSummary, "sessionId" | "lastActivity" | "startedAt" | "status" | "live" | "awaitingJobs" | "readThrough">;

/** A listed session with the project argument its RPCs use. */
export interface ReadRow {
  session: Summary;
  project: string;
}

export interface ReadMark {
  sessionId: string;
  readThrough: string;
}

/** Sends one project's marks to the daemon; resolves to the resulting marks. */
export type ReadMarkSink = (project: string, marks: ReadMark[]) => Promise<readonly ReadMark[]>;

export const READ_MARKS_KEY = "ycc.sessionReadMarks";
/** Per-device baseline from before the daemon kept read state; removed on load. */
const LEGACY_WATERMARK_SUFFIX = ".watermark";

export interface ReadStoreOptions {
  limit?: number;
  sink?: ReadMarkSink | null;
  /** How long marks are collected before one push per project. */
  pushDelayMs?: number;
}

export class SessionReadStore {
  private marks: Record<string, string> = {};
  /** The daemon's mark per session, from summaries and push responses. */
  private server = new Map<string, string>();
  /** Marks waiting to be pushed, by project then session. */
  private pending = new Map<string, Map<string, string>>();
  /** Marks in flight, by session. */
  private inflight = new Map<string, string>();
  private timer: ReturnType<typeof setTimeout> | null = null;
  private limit: number;
  private sink: ReadMarkSink | null;
  private pushDelayMs: number;
  /** Bumped on every change (React subscribes to it). */
  revision = 0;
  private listeners = new Set<() => void>();

  constructor(
    private storage: KeyValueStorage | null = null,
    readonly key: string = READ_MARKS_KEY,
    { limit = 600, sink = null, pushDelayMs = 1500 }: ReadStoreOptions = {},
  ) {
    this.limit = limit;
    this.sink = sink;
    this.pushDelayMs = pushDelayMs;
    this.load();
  }

  /** (Re)read persisted marks, e.g. after another tab wrote them. */
  load() {
    let marks: Record<string, string> = {};
    try {
      const raw = this.storage?.getItem(this.key);
      const parsed = raw ? JSON.parse(raw) : null;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        for (const [k, v] of Object.entries(parsed)) if (typeof v === "string") marks[k] = v;
      }
    } catch {
      marks = {};
    }
    try {
      this.storage?.removeItem(this.key + LEGACY_WATERMARK_SUFFIX);
    } catch {
      // ignore
    }
    this.marks = marks;
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

  /** Whether a session has agent activity newer than what the user saw. Sessions without any mark are not unread. */
  isUnread(s: Summary): boolean {
    if (s.live && s.status.toLowerCase() === "running") return false;
    if (s.live && s.awaitingJobs) return false;
    const markMs = later(this.effectiveMs(s.sessionId), parseTimestamp(s.readThrough));
    if (markMs === null) return false;
    const activity = recencyStamp(s);
    return activity !== null && activity.ms > markMs;
  }

  unreadCount(sessions: readonly Summary[]): number {
    let n = 0;
    for (const s of sessions) if (this.isUnread(s)) n++;
    return n;
  }

  /** Whether everything through `timestamp` has been seen for a session. */
  hasSeen(sessionId: string, timestamp: string): boolean {
    const markMs = this.effectiveMs(sessionId);
    const ms = parseTimestamp(timestamp);
    return markMs !== null && ms !== null && ms <= markMs;
  }

  // MARK: syncing

  /**
   * Record the daemon's marks from a fresh list, and push local marks the
   * daemon is behind on. Never creates a mark.
   */
  noteSummaries(rows: readonly ReadRow[]) {
    let changed = false;
    for (const { session: s, project } of rows) {
      if (this.noteServer(s.sessionId, s.readThrough)) changed = true;
      const local = this.marks[s.sessionId];
      if (local !== undefined) this.enqueue(project, s.sessionId, local);
    }
    if (changed) this.changed();
  }

  // MARK: marking

  /** Everything up to `timestamp` (a daemon event stamp) is seen; never moves a mark back. */
  markRead(project: string, sessionId: string, timestamp: string) {
    if (!this.apply(sessionId, timestamp)) return;
    this.persist();
    this.enqueue(project, sessionId, timestamp);
  }

  /** Mark a listed session read at its last reported activity ("I know, stop nagging"). */
  markSummaryRead(row: ReadRow) {
    this.markAllRead([row]);
  }

  /** Mark a whole list read in one write. */
  markAllRead(rows: readonly ReadRow[]) {
    const applied: ReadRow[] = [];
    for (const row of rows) if (this.apply(row.session.sessionId, stamp(row.session))) applied.push(row);
    if (!applied.length) return;
    this.persist();
    for (const row of applied) this.enqueue(row.project, row.session.sessionId, stamp(row.session));
  }

  /**
   * Push pending marks now (view closing, page hidden). Failures keep the
   * local marks; the next list re-pushes them.
   */
  async flush(): Promise<void> {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    const batches = [...this.pending];
    this.pending.clear();
    await Promise.all(batches.map(([project, marks]) => this.send(project, marks)));
  }

  private async send(project: string, batch: Map<string, string>) {
    if (!this.sink) return;
    const marks = [...batch].map(([sessionId, readThrough]) => ({ sessionId, readThrough }));
    for (const m of marks) this.inflight.set(m.sessionId, m.readThrough);
    let result: readonly ReadMark[] | null = null;
    try {
      result = await this.sink(project, marks);
    } catch {
      // Offline or an older daemon: the local mark stands and is retried.
    } finally {
      for (const m of marks) if (this.inflight.get(m.sessionId) === m.readThrough) this.inflight.delete(m.sessionId);
    }
    if (!result) return;
    let changed = false;
    for (const m of result) if (this.noteServer(m.sessionId, m.readThrough)) changed = true;
    if (changed) this.changed();
  }

  /** Queue a push unless the daemon already has (or is being sent) this mark. */
  private enqueue(project: string, sessionId: string, timestamp: string) {
    if (!this.sink) return;
    const ms = parseTimestamp(timestamp);
    if (ms === null) return;
    const known = later(parseOrNull(this.server.get(sessionId)), parseOrNull(this.inflight.get(sessionId)));
    if (known !== null && ms <= known) return;
    let batch = this.pending.get(project);
    if (!batch) this.pending.set(project, (batch = new Map()));
    const queued = parseOrNull(batch.get(sessionId));
    if (queued !== null && ms <= queued) return;
    batch.set(sessionId, timestamp);
    this.timer ??= setTimeout(() => void this.flush(), this.pushDelayMs);
  }

  private noteServer(sessionId: string, readThrough: string): boolean {
    const ms = parseTimestamp(readThrough);
    if (!sessionId || ms === null) return false;
    const prior = parseOrNull(this.server.get(sessionId));
    if (prior !== null && ms <= prior) return false;
    this.server.set(sessionId, readThrough);
    return true;
  }

  private effectiveMs(sessionId: string): number | null {
    return later(parseOrNull(this.marks[sessionId]), parseOrNull(this.server.get(sessionId)));
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

function parseOrNull(value: string | undefined): number | null {
  return value === undefined ? null : parseTimestamp(value);
}

function later(a: number | null, b: number | null): number | null {
  if (a === null) return b;
  if (b === null) return a;
  return Math.max(a, b);
}
