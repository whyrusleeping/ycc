// Unread tracking: a port of YccKit's SessionReadStoreTests (baselining on
// first sighting, going unread on later daemon activity, clearing when read)
// plus localStorage persistence and cross-tab reload.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { SessionSummarySchema, type SessionSummary } from "../src/gen/ycc/v1/ycc_pb";
import { parseTimestamp, SessionReadStore, type KeyValueStorage } from "../src/features/sessions/readStore";

class MemStorage implements KeyValueStorage {
  data = new Map<string, string>();
  getItem(k: string) {
    return this.data.get(k) ?? null;
  }
  setItem(k: string, v: string) {
    this.data.set(k, v);
  }
  removeItem(k: string) {
    this.data.delete(k);
  }
}

const makeStore = (limit = 600) => new SessionReadStore(null, "marks", limit);

function session(
  id: string,
  { lastActivity = "2026-08-06T10:00:00Z", startedAt = "2026-08-06T09:00:00Z", status = "idle", live = false, awaitingJobs = false } = {},
): SessionSummary {
  return create(SessionSummarySchema, { sessionId: id, lastActivity, startedAt, status, live, awaitingJobs });
}

describe("SessionReadStore (iOS parity)", () => {
  it("unknown session is not unread", () => {
    expect(makeStore().isUnread(session("a"))).toBe(false);
  });

  it("first sighting baselines as read", () => {
    const store = makeStore();
    store.noteSeen([session("a")]);
    expect(store.isUnread(session("a"))).toBe(false);
  });

  it("activity after first sighting is unread", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z" });
    expect(store.isUnread(progressed)).toBe(true);
    expect(store.unreadCount([progressed])).toBe(1);
  });

  it("noteSeen does not rebaseline known sessions", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z" });
    store.noteSeen([progressed]);
    expect(store.isUnread(progressed)).toBe(true);
  });

  it("a running live session is never unread; finished, it is", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", status: "running", live: true }))).toBe(false);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", status: "idle", live: true }))).toBe(true);
  });

  it("an idle session still awaiting delegated jobs is not unread yet", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", live: true, awaitingJobs: true }))).toBe(false);
  });

  it("a new session after the watermark is unread, and markRead clears it", () => {
    const store = makeStore();
    const existing = session("existing", { lastActivity: "2026-08-06T10:00:00Z" });
    store.noteSeen([existing]);
    const fresh = session("new", { lastActivity: "2026-08-06T11:00:00Z" });
    store.noteSeen([existing, fresh]);
    expect(store.isUnread(fresh)).toBe(true);
    store.markSummaryRead(fresh);
    expect(store.isUnread(fresh)).toBe(false);
  });

  it("a new running session waits until it stops to become unread", () => {
    const store = makeStore();
    const existing = session("existing", { lastActivity: "2026-08-06T10:00:00Z" });
    store.noteSeen([existing]);
    const running = session("new", { lastActivity: "2026-08-06T11:00:00Z", status: "running", live: true });
    store.noteSeen([existing, running]);
    expect(store.isUnread(running)).toBe(false);
    expect(store.isUnread(session("new", { lastActivity: "2026-08-06T11:00:00Z", status: "idle", live: true }))).toBe(true);
  });

  it("back-catalogue first seen at or before the watermark is read", () => {
    const store = makeStore();
    const existing = session("existing", { lastActivity: "2026-08-06T10:00:00Z" });
    store.noteSeen([existing]);
    const backCatalogue = session("old", { lastActivity: "2026-08-05T10:00:00Z" });
    const tied = session("tied", { lastActivity: "2026-08-06T10:00:00Z" });
    store.noteSeen([existing, backCatalogue, tied]);
    expect(store.isUnread(backCatalogue)).toBe(false);
    expect(store.isUnread(tied)).toBe(false);
  });

  it("a fresh browser's first list is all read", () => {
    const store = makeStore();
    const first = [session("older", { lastActivity: "2026-08-05T10:00:00Z" }), session("newer", { lastActivity: "2026-08-06T10:00:00Z" })];
    store.noteSeen(first);
    expect(store.unreadCount(first)).toBe(0);
  });

  it("markRead through an event timestamp clears", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z" });
    expect(store.isUnread(progressed)).toBe(true);
    // Event stamps carry nanoseconds; summaries are millisecond-truncated.
    store.markRead("a", "2026-08-06T10:05:00.000123456Z");
    expect(store.isUnread(progressed)).toBe(false);
  });

  it("markRead never moves the mark backwards", () => {
    const store = makeStore();
    store.markRead("a", "2026-08-06T10:05:00Z");
    store.markRead("a", "2026-08-06T10:01:00Z");
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z" }))).toBe(false);
    expect(store.hasSeen("a", "2026-08-06T10:04:00Z")).toBe(true);
    expect(store.hasSeen("a", "2026-08-06T10:06:00Z")).toBe(false);
  });

  it("marking a row read uses its last activity", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z" });
    store.markSummaryRead(progressed);
    expect(store.isUnread(progressed)).toBe(false);
  });

  it("mark all read clears every row", () => {
    const store = makeStore();
    store.noteSeen([session("a"), session("b")]);
    const progressed = [session("a", { lastActivity: "2026-08-06T11:00:00Z" }), session("b", { lastActivity: "2026-08-06T12:00:00Z" })];
    expect(store.unreadCount(progressed)).toBe(2);
    store.markAllRead(progressed);
    expect(store.unreadCount(progressed)).toBe(0);
  });

  it("fractional-second stamps compare", () => {
    const store = makeStore();
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00.120Z" })]);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00.120Z" }))).toBe(false);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00.500Z" }))).toBe(true);
  });

  it("parses offsets and long fractions", () => {
    expect(parseTimestamp("2026-08-06T12:00:00.123456789+02:00")).toBe(Date.parse("2026-08-06T10:00:00.123Z"));
    expect(parseTimestamp("")).toBeNull();
    expect(parseTimestamp("nope")).toBeNull();
  });

  it("marks persist across store instances (another tab, a reload)", () => {
    const storage = new MemStorage();
    const store = new SessionReadStore(storage, "marks");
    store.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const reloaded = new SessionReadStore(storage, "marks");
    expect(reloaded.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z" }))).toBe(true);
    expect(reloaded.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00Z" }))).toBe(false);
    // A second tab marks it read; this one reloads on the storage event.
    reloaded.markRead("a", "2026-08-06T10:05:00Z");
    let notified = 0;
    store.subscribe(() => notified++);
    store.load();
    expect(notified).toBe(1);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z" }))).toBe(false);
  });

  it("the watermark persists across instances", () => {
    const storage = new MemStorage();
    const original = new SessionReadStore(storage, "marks");
    original.noteSeen([session("a", { lastActivity: "2026-08-06T10:00:00Z" })]);
    const progressed = session("a", { lastActivity: "2026-08-06T12:00:00Z" });
    original.noteSeen([progressed]);
    const reloaded = new SessionReadStore(storage, "marks");
    const backCatalogue = session("b", { lastActivity: "2026-08-06T11:00:00Z" });
    reloaded.noteSeen([progressed, backCatalogue]);
    expect(reloaded.isUnread(backCatalogue)).toBe(false);
  });

  it("marks without a watermark derive one", () => {
    const storage = new MemStorage();
    storage.setItem("marks", JSON.stringify({ known: "2026-08-06T10:00:00Z" }));
    const upgraded = new SessionReadStore(storage, "marks");
    expect(storage.getItem("marks.watermark")).toBe("2026-08-06T10:00:00Z");
    const fresh = session("new", { lastActivity: "2026-08-06T11:00:00Z" });
    upgraded.noteSeen([fresh]);
    expect(upgraded.isUnread(fresh)).toBe(true);
  });

  it("corrupt storage reads as empty", () => {
    const storage = new MemStorage();
    storage.setItem("marks", "{not json");
    const store = new SessionReadStore(storage, "marks");
    expect(store.isUnread(session("a"))).toBe(false);
    store.noteSeen([session("a")]);
    expect(JSON.parse(storage.getItem("marks")!)).toEqual({ a: "2026-08-06T10:00:00Z" });
  });

  it("eviction keeps the most recent marks", () => {
    const store = makeStore(2);
    store.noteSeen([
      session("old", { lastActivity: "2026-08-01T10:00:00Z" }),
      session("mid", { lastActivity: "2026-08-05T10:00:00Z" }),
      session("new", { lastActivity: "2026-08-06T10:00:00Z" }),
    ]);
    expect(store.isUnread(session("old", { lastActivity: "2026-08-07T10:00:00Z" }))).toBe(false);
    expect(store.isUnread(session("new", { lastActivity: "2026-08-07T10:00:00Z" }))).toBe(true);
  });
});
