// Notifications: which list changes are events, when to show one, the
// shared dedupe ledger, and the tab title count. Also session-list keyboard
// navigation, per-project attention counts, and toast collapsing.
import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SessionSummarySchema, type SessionSummary } from "../src/gen/ycc/v1/ycc_pb";
import { claimNotification, detectEvents, notificationText, shouldNotify, titleWithCount, NOTIFIED_KEY, type NotifyContext } from "../src/features/notify/policy";
import type { KeyValueStorage } from "../src/features/sessions/readStore";
import { adjacentSession, nextNeedsAnswer } from "../src/features/sessions/navigation";
import { activityDescription, projectActivity } from "../src/features/sessions/activity";
import { currentToasts, dismissToast, toast } from "../src/ui/toast";

const ts = (m: number) => `2026-01-01T00:${String(m).padStart(2, "0")}:00.000Z`;
function s(id: string, minute: number, extra: Record<string, unknown> = {}): SessionSummary {
  return create(SessionSummarySchema, { sessionId: id, lastActivity: ts(minute), startedAt: ts(0), status: "idle", ...extra });
}
const rows = (...ss: SessionSummary[]) => ss.map((session) => ({ session, project: "alpha" }));
const waiting = { live: true, waitingInput: true, status: "running" };

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

describe("detectEvents", () => {
  it("baselines on the first poll", () => {
    const { events } = detectEvents(null, rows(s("a", 1, waiting), s("b", 2, { status: "error" })));
    expect(events).toEqual([]);
  });

  it("reports a new question once", () => {
    const first = detectEvents(null, rows(s("a", 1, { live: true, status: "running" })));
    const second = detectEvents(first.state, rows(s("a", 2, waiting)));
    expect(second.events.map((e) => [e.kind, e.key])).toEqual([["question", `question:a:${ts(2)}`]]);
    // Still waiting on the next poll: nothing new.
    expect(detectEvents(second.state, rows(s("a", 2, waiting))).events).toEqual([]);
  });

  it("reports activity that came to rest as done, and errors", () => {
    const first = detectEvents(null, rows(s("a", 1, { live: true }), s("b", 1, { live: true, status: "running" }), s("c", 1)));
    const second = detectEvents(
      first.state,
      rows(s("a", 3, { live: true }), s("b", 3, { live: true, status: "error" }), s("c", 1)),
    );
    expect(second.events.map((e) => `${e.kind}:${e.sessionId}`)).toEqual(["done:a", "error:b"]);
  });

  it("does not report a session still working or idle awaiting its jobs", () => {
    const first = detectEvents(null, rows(s("a", 1, { live: true, status: "running" }), s("b", 1, { live: true })));
    const second = detectEvents(first.state, rows(s("a", 3, { live: true, status: "running" }), s("b", 3, { live: true, awaitingJobs: true })));
    expect(second.events).toEqual([]);
  });

  it("treats a newly listed session as news only when newer than everything seen", () => {
    const first = detectEvents(null, rows(s("a", 5)));
    // An older page loaded later (back-catalogue), and a brand-new session that asks at once.
    const second = detectEvents(first.state, rows(s("a", 5), s("old", 1, waiting), s("new", 6, waiting)));
    expect(second.events.map((e) => `${e.kind}:${e.sessionId}`)).toEqual(["question:new"]);
  });

  it("keeps sessions missing from a partial poll", () => {
    const first = detectEvents(null, rows(s("a", 1, { live: true, status: "running" })));
    const partial = detectEvents(first.state, []);
    expect(detectEvents(partial.state, rows(s("a", 2, waiting))).events).toHaveLength(1);
  });
});

describe("shouldNotify", () => {
  const event = { kind: "question" as const, sessionId: "a", project: "alpha", key: "k", activity: ts(2) };
  const ctx = (over: Partial<NotifyContext> = {}): NotifyContext => ({ enabled: true, away: true, activeSessionId: null, seen: () => false, ...over });
  it("needs notifications on", () => {
    expect(shouldNotify(event, ctx({ enabled: false }))).toBe(false);
  });
  it("skips the session the user is looking at, but not when the tab is in the background", () => {
    expect(shouldNotify(event, ctx({ away: false, activeSessionId: "a" }))).toBe(false);
    expect(shouldNotify(event, ctx({ away: true, activeSessionId: "a" }))).toBe(true);
    expect(shouldNotify(event, ctx({ away: false, activeSessionId: "b" }))).toBe(true);
  });
  it("skips finished/error activity already read (e.g. in another tab) but never a question", () => {
    expect(shouldNotify({ ...event, kind: "done" }, ctx({ seen: () => true }))).toBe(false);
    expect(shouldNotify({ ...event, kind: "error" }, ctx({ seen: () => true }))).toBe(false);
    expect(shouldNotify(event, ctx({ seen: () => true }))).toBe(true);
  });
});

describe("notification ledger", () => {
  it("lets one tab claim each key", () => {
    const storage = new MemStorage();
    expect(claimNotification(storage, "question:a:1", 1000)).toBe(true);
    expect(claimNotification(storage, "question:a:1", 2000)).toBe(false); // another tab, or after a reload
    expect(claimNotification(storage, "question:a:2", 2000)).toBe(true);
  });
  it("forgets old keys and survives corrupt storage", () => {
    const storage = new MemStorage();
    claimNotification(storage, "k", 0);
    expect(claimNotification(storage, "k", 4 * 24 * 3600 * 1000)).toBe(true);
    storage.setItem(NOTIFIED_KEY, "garbage");
    expect(claimNotification(storage, "x", 0)).toBe(true);
    expect(claimNotification(null, "x", 0)).toBe(true);
  });
  it("caps the ledger", () => {
    const storage = new MemStorage();
    for (let i = 0; i < 400; i++) claimNotification(storage, `k${i}`, 1000);
    expect(JSON.parse(storage.getItem(NOTIFIED_KEY)!)).toHaveLength(300);
  });
});

describe("notification text and title", () => {
  it("names the event, session, and project", () => {
    expect(notificationText("question", "Fix login", "alpha")).toEqual({ title: "Question: Fix login", body: "The agent is waiting for your answer · alpha" });
    expect(notificationText("done", "Fix login", "").body).toBe("The agent finished and is idle");
  });
  it("prefixes the tab title with the needs-answer count", () => {
    expect(titleWithCount("ycc", 2)).toBe("(2) ycc");
    expect(titleWithCount("Backlog · ycc", 0)).toBe("Backlog · ycc");
  });
});

describe("session navigation", () => {
  const list = rows(s("a", 5), s("b", 4, waiting), s("c", 3), s("d", 2, waiting));
  it("steps through the sidebar's visual order (needs-answer first)", () => {
    // Visual order: b, d (waiting), then a, c.
    expect(adjacentSession(list, null, 1)?.session.sessionId).toBe("b");
    expect(adjacentSession(list, null, -1)?.session.sessionId).toBe("c");
    expect(adjacentSession(list, "d", 1)?.session.sessionId).toBe("a");
    expect(adjacentSession(list, "a", -1)?.session.sessionId).toBe("d");
    expect(adjacentSession(list, "c", 1)).toBeNull();
    expect(adjacentSession([], null, 1)).toBeNull();
  });
  it("jumps to the next waiting session, wrapping", () => {
    expect(nextNeedsAnswer(list, null)?.session.sessionId).toBe("b");
    expect(nextNeedsAnswer(list, "b")?.session.sessionId).toBe("d");
    expect(nextNeedsAnswer(list, "d")?.session.sessionId).toBe("b");
    expect(nextNeedsAnswer(rows(s("b", 4, waiting)), "b")).toBeNull();
    expect(nextNeedsAnswer(rows(s("a", 1)), null)).toBeNull();
  });
  it("counts waiting and unread sessions per project", () => {
    const all = [...list.map((r) => ({ ...r, project: r.session.sessionId === "d" ? "beta" : "alpha" }))];
    const { total, byProject } = projectActivity(all, (r) => r.session.sessionId === "a" || r.session.sessionId === "b");
    expect(total).toEqual({ needsAnswer: 2, unread: 1, active: 0 });
    expect(byProject.get("alpha")).toEqual({ needsAnswer: 1, unread: 1, active: 0 });
    expect(byProject.get("beta")).toEqual({ needsAnswer: 1, unread: 0, active: 0 });
    expect(activityDescription(byProject.get("alpha"))).toBe("1 waiting for an answer, 1 unread");
    expect(activityDescription(undefined)).toBe("");
  });
  it("counts live work in flight as active, as iOS does", () => {
    const live = rows(
      s("run", 6, { live: true, status: "running" }),
      s("pause", 5, { live: true, status: "paused" }),
      s("bg", 4, { live: true, status: "idle", awaitingJobs: true }),
      s("idle", 3, { live: true, status: "idle" }),
      s("log", 2, { status: "running" }), // a persisted log's last status is history
      s("ask", 1, waiting), // waiting counts once, as waiting
    );
    const { total } = projectActivity(live, (r) => r.session.sessionId === "run");
    expect(total).toEqual({ needsAnswer: 1, unread: 1, active: 3 });
    expect(activityDescription(total)).toBe("1 waiting for an answer, 1 unread, 3 active");
  });
});

describe("toasts", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    for (const t of currentToasts()) dismissToast(t.id);
    vi.useRealTimers();
  });
  it("collapses a repeated toast and restarts its timer", () => {
    toast("Couldn’t load sessions.");
    vi.advanceTimersByTime(5000);
    toast("Couldn’t load sessions.");
    toast("Couldn’t load sessions.", "info"); // a different tone is a different toast
    expect(currentToasts().map((t) => [t.text, t.tone, t.count])).toEqual([
      ["Couldn’t load sessions.", "error", 2],
      ["Couldn’t load sessions.", "info", 1],
    ]);
    vi.advanceTimersByTime(4000); // 9s after the first, 4s after the repeat
    expect(currentToasts().some((t) => t.tone === "error")).toBe(true);
    vi.advanceTimersByTime(2500);
    expect(currentToasts()).toEqual([]);
  });
});
