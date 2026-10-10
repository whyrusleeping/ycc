import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { SessionSummarySchema, type SessionSummary } from "../src/gen/ycc/v1/ycc_pb";
import { filterSessions, isInboxSession, SESSION_FILTERS } from "../src/features/sessions/attention";
import { detectEvents } from "../src/features/notify/policy";
import { paletteResults, type PaletteSources } from "../src/features/palette/items";
import type { FeedRow } from "../src/features/sessions/feed";
import { adjacentSession } from "../src/features/sessions/navigation";

function row(id: string, extra: Partial<SessionSummary> = {}): FeedRow {
  return { project: "work", session: { ...create(SessionSummarySchema, { sessionId: id, title: id, status: "idle" }), ...extra } };
}
const ids = (rows: FeedRow[]) => rows.map((r) => r.session.sessionId);
const loops = new Set(["old-loop"]);
const rows = [
  row("manual", { origin: "user" }),
  row("groom", { origin: "memory_groom" }),
  row("loop", { origin: "work_loop" }),
  row("engaged", { origin: "work_loop", humanParticipated: true }),
  row("flagged", { origin: "memory_groom", followUp: true }),
  row("asks", { origin: "automation", live: true, waitingInput: true }),
  row("old-loop"),
  row("unknown"),
];

describe("personal session inbox", () => {
  it("suppresses known routine automation without guessing unknown legacy origins", () => {
    expect(ids(filterSessions(rows, "inbox", "", () => true, loops))).toEqual(["manual", "engaged", "flagged", "asks", "unknown"]);
    expect(ids(filterSessions(rows, "unread", "", () => true, loops))).toEqual(["manual", "engaged", "flagged", "asks", "unknown"]);
    expect(ids(filterSessions(rows, "all", "", () => true, loops))).toEqual(ids(rows));
    expect(isInboxSession(row("memory-groom-looking title", { mode: "pm" }).session)).toBe(true);
    expect(isInboxSession(row("manual", { origin: "user" }).session, true)).toBe(true);
  });

  it("always surfaces questions despite filters or search, and retains explicitly opened automatic sessions", () => {
    for (const filter of SESSION_FILTERS) {
      expect(ids(filterSessions(rows, filter.value, "no match", () => false, loops))).toEqual(["asks"]);
    }
    expect(ids(filterSessions(rows, "inbox", "", () => false, loops, "groom"))).toContain("groom");
    expect(ids(filterSessions(rows, "unread", "manual", (r) => r.session.sessionId === "manual", loops))).toEqual(["manual", "asks"]);
  });

  it("keeps an opened unread row as the next/previous navigation anchor", () => {
    const ordered = [row("A"), row("B"), row("C")];
    const visible = filterSessions(ordered, "unread", "", (r) => r.session.sessionId !== "B", loops, "B");
    expect(ids(visible)).toEqual(["A", "B", "C"]);
    expect(adjacentSession(visible, "B", 1)?.session.sessionId).toBe("C");
    expect(adjacentSession(visible, "B", -1)?.session.sessionId).toBe("A");
  });

  it("searches titles, projects and task IDs without changing the history", () => {
    const tasks = [row("task session", { origin: "user", focusTasks: ["0123"] })];
    expect(ids(filterSessions(tasks, "inbox", "0123", () => false, loops))).toEqual(["task session"]);
    expect(ids(filterSessions(tasks, "inbox", "WORK", () => false, loops))).toEqual(["task session"]);
    expect(rows).toHaveLength(8);
  });

  it("suppresses automatic completion notifications but never pending human questions", () => {
    const base = row("groom", { origin: "memory_groom", lastActivity: "2026-10-10T10:00:00Z", live: true, status: "running" });
    const baseline = detectEvents(null, [base]).state;
    const done = row("groom", { origin: "memory_groom", lastActivity: "2026-10-10T10:01:00Z" });
    expect(detectEvents(baseline, [done]).events).toEqual([]);
    const asks = row("groom", { ...done.session, live: true, waitingInput: true });
    expect(detectEvents(baseline, [asks]).events.map((e) => e.kind)).toEqual(["question"]);
    const engaged = row("groom", { ...done.session, humanParticipated: true });
    expect(detectEvents(baseline, [engaged]).events.map((e) => e.kind)).toEqual(["done"]);
  });

  it("keeps automatic sessions out of default palette suggestions, but allows explicit history search", () => {
    const source: PaletteSources = {
      rows, activeSessionId: null, isUnread: () => true, relativeTime: () => "", tasks: [], projects: [], actions: [], target: null,
      shortcutLabel: () => undefined, loopSessionIds: loops,
    };
    expect(paletteResults(source, "").some((r) => r.item.id === "session:work:groom")).toBe(false);
    expect(paletteResults(source, "groom").some((r) => r.item.id === "session:work:groom")).toBe(true);
  });
});
