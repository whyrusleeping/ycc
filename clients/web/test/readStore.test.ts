// Unread tracking: effective marks combine this browser's local marks with the
// daemon's shared readThrough; local marks persist in localStorage, sync across
// tabs, and are pushed to the daemon (coalesced per project, retried by the
// next list).
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { SessionSummarySchema, type SessionSummary } from "../src/gen/ycc/v1/ycc_pb";
import { parseTimestamp, SessionReadStore, type KeyValueStorage, type ReadMark, type ReadStoreOptions } from "../src/features/sessions/readStore";

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

/** A fake MarkSessionsRead: records calls and echoes the stamps back (or fails). */
class FakeDaemon {
  calls: { project: string; marks: ReadMark[] }[] = [];
  fail = false;
  sink = async (project: string, marks: ReadMark[]) => {
    this.calls.push({ project, marks });
    if (this.fail) throw new Error("offline");
    return marks;
  };
}

const makeStore = (opts: ReadStoreOptions = {}, storage: KeyValueStorage | null = null) =>
  new SessionReadStore(storage, "marks", { pushDelayMs: 60_000, ...opts });

function session(
  id: string,
  { lastActivity = "2026-08-06T10:00:00Z", startedAt = "2026-08-06T09:00:00Z", status = "idle", live = false, awaitingJobs = false, readThrough = "" } = {},
): SessionSummary {
  return create(SessionSummarySchema, { sessionId: id, lastActivity, startedAt, status, live, awaitingJobs, readThrough });
}

const row = (s: SessionSummary, project = "p") => ({ session: s, project });

describe("SessionReadStore", () => {
  it("a session without any mark is read", () => {
    const store = makeStore();
    store.noteSummaries([row(session("a"))]);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-07T10:00:00Z" }))).toBe(false);
  });

  it("activity after the daemon's readThrough is unread", () => {
    const store = makeStore();
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z" }))).toBe(true);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00Z", readThrough: "2026-08-06T10:00:00Z" }))).toBe(false);
  });

  it("the effective mark is the later of local and daemon marks", () => {
    const store = makeStore();
    store.markRead("p", "a", "2026-08-06T10:02:00Z");
    // Daemon behind local: local wins.
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:01:00Z", readThrough: "2026-08-06T10:00:00Z" }))).toBe(false);
    // Daemon ahead of local (read on another client): daemon wins.
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:05:00Z" }))).toBe(false);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z" }))).toBe(true);
    // hasSeen follows daemon marks recorded from a list.
    expect(store.hasSeen("a", "2026-08-06T10:04:00Z")).toBe(false);
    store.noteSummaries([row(session("a", { readThrough: "2026-08-06T10:05:00Z" }))]);
    expect(store.hasSeen("a", "2026-08-06T10:04:00Z")).toBe(true);
  });

  it("a running live session is never unread; finished, it is", () => {
    const store = makeStore();
    const readThrough = "2026-08-06T10:00:00Z";
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", status: "running", live: true, readThrough }))).toBe(false);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z", status: "idle", live: true, readThrough }))).toBe(true);
  });

  it("an idle session still awaiting delegated jobs is not unread yet", () => {
    const store = makeStore();
    const s = session("a", { lastActivity: "2026-08-06T10:05:00Z", live: true, awaitingJobs: true, readThrough: "2026-08-06T10:00:00Z" });
    expect(store.isUnread(s)).toBe(false);
  });

  it("markRead through an event timestamp clears", () => {
    const store = makeStore();
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z" });
    expect(store.isUnread(progressed)).toBe(true);
    // Event stamps carry nanoseconds; summaries are millisecond-truncated.
    store.markRead("p", "a", "2026-08-06T10:05:00.000123456Z");
    expect(store.isUnread(progressed)).toBe(false);
  });

  it("markRead never moves the mark backwards", () => {
    const store = makeStore();
    store.markRead("p", "a", "2026-08-06T10:05:00Z");
    store.markRead("p", "a", "2026-08-06T10:01:00Z");
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:05:00Z" }))).toBe(false);
    expect(store.hasSeen("a", "2026-08-06T10:04:00Z")).toBe(true);
    expect(store.hasSeen("a", "2026-08-06T10:06:00Z")).toBe(false);
  });

  it("marking rows read uses their last activity", () => {
    const store = makeStore();
    const readThrough = "2026-08-06T10:00:00Z";
    const progressed = [session("a", { lastActivity: "2026-08-06T11:00:00Z", readThrough }), session("b", { lastActivity: "2026-08-06T12:00:00Z", readThrough })];
    expect(store.unreadCount(progressed)).toBe(2);
    store.markSummaryRead(row(progressed[0]));
    expect(store.unreadCount(progressed)).toBe(1);
    store.markAllRead(progressed.map((s) => row(s)));
    expect(store.unreadCount(progressed)).toBe(0);
  });

  it("fractional-second stamps compare", () => {
    const store = makeStore();
    const readThrough = "2026-08-06T10:00:00.120Z";
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00.120Z", readThrough }))).toBe(false);
    expect(store.isUnread(session("a", { lastActivity: "2026-08-06T10:00:00.500Z", readThrough }))).toBe(true);
  });

  it("parses offsets and long fractions", () => {
    expect(parseTimestamp("2026-08-06T12:00:00.123456789+02:00")).toBe(Date.parse("2026-08-06T10:00:00.123Z"));
    expect(parseTimestamp("")).toBeNull();
    expect(parseTimestamp("nope")).toBeNull();
  });
});

describe("SessionReadStore daemon sync", () => {
  it("listing never creates or pushes marks", async () => {
    const storage = new MemStorage();
    const daemon = new FakeDaemon();
    const store = makeStore({ sink: daemon.sink }, storage);
    store.noteSummaries([row(session("a")), row(session("b", { readThrough: "2026-08-06T09:30:00Z" }))]);
    await store.flush();
    expect(daemon.calls).toEqual([]);
    expect(storage.getItem("marks")).toBeNull();
  });

  it("markRead pushes grouped by project and coalesced", async () => {
    const daemon = new FakeDaemon();
    const store = makeStore({ sink: daemon.sink });
    store.markRead("alpha", "a", "2026-08-06T10:00:00Z");
    store.markRead("alpha", "a", "2026-08-06T10:00:01Z");
    store.markRead("alpha", "b", "2026-08-06T10:00:02Z");
    store.markAllRead([row(session("c", { lastActivity: "2026-08-06T10:00:03Z" }), "beta")]);
    expect(daemon.calls).toEqual([]);
    await store.flush();
    expect(daemon.calls).toEqual([
      { project: "alpha", marks: [{ sessionId: "a", readThrough: "2026-08-06T10:00:01Z" }, { sessionId: "b", readThrough: "2026-08-06T10:00:02Z" }] },
      { project: "beta", marks: [{ sessionId: "c", readThrough: "2026-08-06T10:00:03Z" }] },
    ]);
    // Acknowledged marks are not pushed again by the next list.
    store.noteSummaries([row(session("a"), "alpha")]);
    await store.flush();
    expect(daemon.calls).toHaveLength(2);
  });

  it("pushes after the coalescing delay without an explicit flush", async () => {
    const daemon = new FakeDaemon();
    const store = makeStore({ sink: daemon.sink, pushDelayMs: 5 });
    store.markRead("p", "a", "2026-08-06T10:00:00Z");
    store.markRead("p", "a", "2026-08-06T10:00:01Z");
    await new Promise((r) => setTimeout(r, 30));
    expect(daemon.calls).toEqual([{ project: "p", marks: [{ sessionId: "a", readThrough: "2026-08-06T10:00:01Z" }] }]);
  });

  it("a failed push keeps the local mark and the next list retries it", async () => {
    const daemon = new FakeDaemon();
    daemon.fail = true;
    const store = makeStore({ sink: daemon.sink });
    store.markRead("p", "a", "2026-08-06T10:05:00Z");
    await store.flush();
    expect(daemon.calls).toHaveLength(1);
    const listed = session("a", { lastActivity: "2026-08-06T10:05:00Z", readThrough: "2026-08-06T10:00:00Z" });
    expect(store.isUnread(listed)).toBe(false);
    daemon.fail = false;
    store.noteSummaries([row(listed)]);
    await store.flush();
    expect(daemon.calls[1]).toEqual({ project: "p", marks: [{ sessionId: "a", readThrough: "2026-08-06T10:05:00Z" }] });
  });

  it("migrates stored local marks the daemon is behind on, and only those", async () => {
    const storage = new MemStorage();
    storage.setItem("marks", JSON.stringify({ ahead: "2026-08-06T10:00:00Z", behind: "2026-08-06T10:00:00Z", fresh: "2026-08-06T10:00:00Z" }));
    const daemon = new FakeDaemon();
    const store = makeStore({ sink: daemon.sink }, storage);
    store.noteSummaries([
      row(session("ahead", { readThrough: "2026-08-06T09:00:00Z" })),
      row(session("behind", { readThrough: "2026-08-06T10:00:00Z" })),
      row(session("fresh")), // project without read state yet
    ]);
    await store.flush();
    expect(daemon.calls).toEqual([
      {
        project: "p",
        marks: [
          { sessionId: "ahead", readThrough: "2026-08-06T10:00:00Z" },
          { sessionId: "fresh", readThrough: "2026-08-06T10:00:00Z" },
        ],
      },
    ]);
  });

  it("the daemon's response is recorded as its mark", async () => {
    const daemon = new FakeDaemon();
    const store = makeStore({ sink: async (project, marks) => (await daemon.sink(project, marks)).map((m) => ({ ...m, readThrough: "2026-08-06T11:00:00Z" })) });
    store.markRead("p", "a", "2026-08-06T10:00:00Z");
    await store.flush();
    expect(store.hasSeen("a", "2026-08-06T10:30:00Z")).toBe(true);
  });
});

describe("SessionReadStore persistence", () => {
  it("marks persist across store instances (another tab, a reload)", () => {
    const storage = new MemStorage();
    const store = makeStore({}, storage);
    const progressed = session("a", { lastActivity: "2026-08-06T10:05:00Z" });
    store.markRead("p", "a", "2026-08-06T10:00:00Z");
    const reloaded = makeStore({}, storage);
    expect(reloaded.isUnread(progressed)).toBe(true);
    // A second tab marks it read; this one reloads on the storage event.
    reloaded.markRead("p", "a", "2026-08-06T10:05:00Z");
    let notified = 0;
    store.subscribe(() => notified++);
    store.load();
    expect(notified).toBe(1);
    expect(store.isUnread(progressed)).toBe(false);
  });

  it("the old per-browser watermark is ignored and removed", () => {
    const storage = new MemStorage();
    storage.setItem("marks.watermark", "2026-08-06T10:00:00Z");
    const store = makeStore({}, storage);
    expect(storage.getItem("marks.watermark")).toBeNull();
    store.noteSummaries([row(session("new", { lastActivity: "2026-08-06T11:00:00Z" }))]);
    expect(store.isUnread(session("new", { lastActivity: "2026-08-06T11:00:00Z" }))).toBe(false);
  });

  it("corrupt storage reads as empty", () => {
    const storage = new MemStorage();
    storage.setItem("marks", "{not json");
    const store = makeStore({}, storage);
    expect(store.isUnread(session("a"))).toBe(false);
    store.markRead("p", "a", "2026-08-06T10:00:00Z");
    expect(JSON.parse(storage.getItem("marks")!)).toEqual({ a: "2026-08-06T10:00:00Z" });
  });

  it("eviction keeps the most recent marks", () => {
    const store = makeStore({ limit: 2 });
    store.markRead("p", "old", "2026-08-01T10:00:00Z");
    store.markRead("p", "mid", "2026-08-05T10:00:00Z");
    store.markRead("p", "new", "2026-08-06T10:00:00Z");
    expect(store.isUnread(session("old", { lastActivity: "2026-08-07T10:00:00Z" }))).toBe(false);
    expect(store.isUnread(session("new", { lastActivity: "2026-08-07T10:00:00Z" }))).toBe(true);
  });
});
