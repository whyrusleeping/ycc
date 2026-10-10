// Session-list merge, ordering, sectioning, and labels.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ProjectInfoSchema, SessionSummarySchema, type SessionSummary } from "../src/gen/ycc/v1/ycc_pb";
import {
  buildFeed,
  compactTokenCount,
  displayTitle,
  historyTargets,
  lifecycleLabel,
  mergePage,
  onlyFollowUp,
  sections,
  withFollowUp,
  type HistoryLoad,
} from "../src/features/sessions/feed";

function s(id: string, minute: number, extra: MessageInitShape<typeof SessionSummarySchema> = {}): SessionSummary {
  const ts = `2026-01-01T00:${String(minute).padStart(2, "0")}:00.000Z`;
  return create(SessionSummarySchema, { sessionId: id, lastActivity: ts, startedAt: ts, status: "idle", ...extra });
}

const ids = (rows: { session: SessionSummary }[]) => rows.map((r) => r.session.sessionId);

describe("feed", () => {
  it("dedupes project aliases that share a workspace", () => {
    const projects = [
      create(ProjectInfoSchema, { name: "a", path: "/w/a" }),
      create(ProjectInfoSchema, { name: "alias", path: "/w/a" }),
      create(ProjectInfoSchema, { name: "b", path: "/w/b" }),
    ];
    expect(historyTargets(projects)).toEqual(["a", "b"]);
    expect(historyTargets([])).toEqual([""]);
  });

  it("merges projects most-recent first and routes rows to their project", () => {
    const loads: HistoryLoad[] = [
      { project: "a", sessions: [s("a2", 50), s("a1", 10)], pinned: [], nextCursor: "" },
      { project: "b", sessions: [s("b1", 30)], pinned: [], nextCursor: "" },
    ];
    const feed = buildFeed(loads, null);
    expect(ids(feed.rows)).toEqual(["a2", "b1", "a1"]);
    expect(feed.rows.map((r) => r.project)).toEqual(["a", "b", "a"]);
    expect(feed.hasMore).toBe(false);
  });

  it("holds back rows older than a truncated project's frontier, but shows pinned live rows", () => {
    const loads: HistoryLoad[] = [
      { project: "a", sessions: [s("a2", 50), s("a1", 40)], pinned: [], nextCursor: "more" },
      { project: "b", sessions: [s("b2", 45), s("b1", 5)], pinned: [s("live", 1, { live: true })], nextCursor: "" },
    ];
    const feed = buildFeed(loads, null);
    expect(ids(feed.rows)).toEqual(["a2", "b2", "a1", "live"]);
    expect(feed.hasMore).toBe(true);
    // Paging a past its frontier releases the held-back row.
    loads[0] = mergePage(loads[0], [s("a0", 2)], "");
    expect(ids(buildFeed(loads, null).rows)).toEqual(["a2", "b2", "a1", "b1", "a0", "live"]);
  });

  it("scopes to one project including its pinned rows", () => {
    const loads: HistoryLoad[] = [
      { project: "a", sessions: [s("a1", 10)], pinned: [s("a-live", 1, { live: true })], nextCursor: "x" },
      { project: "b", sessions: [s("b1", 30)], pinned: [], nextCursor: "" },
    ];
    const feed = buildFeed(loads, "a");
    expect(ids(feed.rows)).toEqual(["a1", "a-live"]);
    expect(feed.hasMore).toBe(true);
  });

  it("updates follow-up in history and pinned rows without touching other projects or rows", () => {
    const row = s("same", 10);
    const other = s("other", 20);
    const loads: HistoryLoad[] = [
      { project: "a", sessions: [row, other], pinned: [row], nextCursor: "more" },
      { project: "b", sessions: [row], pinned: [row], nextCursor: "" },
    ];
    const flagged = withFollowUp(loads, "a", "same", true, "2026-01-02T00:00:00Z");
    expect(flagged[0].sessions[0]).toMatchObject({ followUp: true, followUpAt: "2026-01-02T00:00:00Z" });
    expect(flagged[0].pinned[0]).toEqual(flagged[0].sessions[0]);
    expect(flagged[0].sessions[1]).toBe(other);
    expect(flagged[1]).toBe(loads[1]);
    expect(row.followUp).toBe(false);
    const cleared = withFollowUp(flagged, "a", "same", false, "");
    expect(cleared[0].sessions[0]).toMatchObject({ followUp: false, followUpAt: "" });
    expect(cleared[0].pinned[0]).toMatchObject({ followUp: false, followUpAt: "" });
  });

  it("filters only flagged rows while retaining needs-answer sectioning", () => {
    const rows = buildFeed([{
      project: "a",
      sessions: [s("new", 50), s("flagged", 30, { followUp: true })],
      pinned: [s("asks", 1, { followUp: true, live: true, waitingInput: true })],
      nextCursor: "",
    }], null).rows;
    expect(ids(onlyFollowUp(rows))).toEqual(["flagged", "asks"]);
    expect(sections(onlyFollowUp(rows)).map((g) => [g.kind, ids(g.rows)])).toEqual([
      ["needsAnswer", ["asks"]], ["all", ["flagged"]],
    ]);
  });

  it("reports partial and total failures", () => {
    const ok: HistoryLoad = { project: "a", sessions: [s("a1", 1)], pinned: [], nextCursor: "" };
    const bad: HistoryLoad = { project: "b", sessions: [], pinned: [], nextCursor: "", error: "boom" };
    expect(buildFeed([ok, bad], null).warning).toContain("b");
    expect(buildFeed([bad], null).error).toBe("boom");
  });

  it("pins needs-answer sessions in their own section", () => {
    const rows = buildFeed(
      [
        {
          project: "a",
          sessions: [s("new", 50), s("asks", 10, { live: true, waitingInput: true }), s("old", 5)],
          pinned: [],
          nextCursor: "",
        },
      ],
      null,
    ).rows;
    const out = sections(rows);
    expect(out.map((x) => [x.kind, x.title, ids(x.rows)])).toEqual([
      ["needsAnswer", "Needs answer", ["asks"]],
      ["all", "Recent", ["new", "old"]],
    ]);
  });
});

describe("labels", () => {
  it("strips task boilerplate from titles and falls back to mode + id", () => {
    expect(displayTitle(s("x", 1, { title: "Work on task 0198: fix it", focusTasks: ["0198"] }))).toBe("fix it");
    expect(displayTitle(s("x", 1, { title: "[0198] fix it", focusTasks: ["0198"] }))).toBe("fix it");
    expect(displayTitle(s("abcdef123456", 1, { title: "", mode: "work" }))).toBe("work · abcdef12");
  });

  it("labels attention-worthy lifecycle states", () => {
    expect(lifecycleLabel(s("x", 1, { live: true, waitingInput: true, status: "running" }))).toBe("waiting");
    expect(lifecycleLabel(s("x", 1, { live: true, awaitingJobs: true }))).toBe("background");
    expect(lifecycleLabel(s("x", 1, { status: "idle" }))).toBeNull();
    expect(lifecycleLabel(s("x", 1, { status: "error" }))).toBe("error");
  });

  it("formats compact token counts", () => {
    expect(compactTokenCount(999)).toBe("999");
    expect(compactTokenCount(12_345)).toBe("12.3K");
    expect(compactTokenCount(999_600)).toBe("1M");
    expect(compactTokenCount(0)).toBeNull();
  });
});
