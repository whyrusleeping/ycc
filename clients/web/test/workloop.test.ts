// Work-loop logic: lifecycle state, digest sections, pricing honesty, the
// resource envelope, finish announcements, loop-owned sessions, and the
// sidebar indicator. Mirrors YccKit's WorkLoopModelTests where they overlap.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  GetBudgetResponseSchema,
  WorkLoopDigestTaskSchema,
  WorkLoopInfoSchema,
  WorkLoopSessionSchema,
  WorkLoopSlotSchema,
  type WorkLoopInfo,
} from "../src/gen/ycc/v1/ycc_pb";
import {
  ambientLoops,
  bannerLine,
  budgetLine,
  canStart,
  canStop,
  currentSessionId,
  digestSections,
  digestTotalsLine,
  finishAnnouncement,
  formatCost,
  isActive,
  isFailureOutcome,
  isParallel,
  loopIndicator,
  loopSessionIds,
  loopState,
  parallelToSend,
  pollInterval,
  recentCompleted,
  resourceEnvelopeLines,
  slotsLine,
  slotStateText,
  stateTitle,
  stopConfirmBody,
  summaryLine,
  totalsLine,
  uncommittedInReviewLine,
  waitingLine,
} from "../src/features/workloop/model";

const loop = (extra: MessageInitShape<typeof WorkLoopInfoSchema> = {}): WorkLoopInfo =>
  create(WorkLoopInfoSchema, { loopId: "loop_1", project: "gamma", state: "running", ...extra });
const task = (id: string) => create(WorkLoopDigestTaskSchema, { id, title: `Task ${id}` });
const sess = (sessionId: string) => create(WorkLoopSessionSchema, { sessionId });

describe("loop state", () => {
  it("parses lifecycle values, missing and unknown snapshots", () => {
    expect(loopState(null)).toBe("none");
    expect(loopState(loop({ state: "RUNNING" }))).toBe("running");
    expect(loopState(loop({ state: "waiting" }))).toBe("waiting");
    expect(loopState(loop({ state: "stopping" }))).toBe("stopping");
    expect(loopState(loop({ state: "finished" }))).toBe("finished");
    expect(loopState(loop({ state: "exploded" }))).toBe("unknown");
    expect(stateTitle("none")).toBe("Not running");
  });

  it("gates start/stop and polling on the lifecycle", () => {
    expect(canStart("none") && canStart("finished")).toBe(true);
    expect(canStart("running") || canStart("stopping") || canStart("unknown")).toBe(false);
    expect(canStop("running") && canStop("waiting")).toBe(true);
    expect(canStop("stopping") || canStop("finished")).toBe(false);
    expect(isActive("stopping")).toBe(true);
    expect(pollInterval(loop())).toBe(3_000);
    expect(pollInterval(loop({ state: "finished" }))).toBe(30_000);
    expect(pollInterval(null)).toBe(30_000);
  });
});

describe("digest and lines", () => {
  it("orders digest sections and drops empty ones", () => {
    const l = loop({ created: [task("9")], completed: [task("1"), task("2")], unfinished: [task("5")], blocked: [task("3")] });
    expect(digestSections(l).map((s) => s.title)).toEqual(["Completed", "Blocked", "Unfinished", "Created, still open"]);
    expect(digestSections(loop())).toEqual([]);
    const withUncommitted = loop({ completed: [task("1")], uncommittedInReview: [task("7")] });
    expect(digestSections(withUncommitted).map((s) => s.key)).toEqual(["completed", "uncommittedInReview"]);
  });

  it("counts every task that reached done, live sessions and other commits in the summary", () => {
    const status = (id: string, s: string) => create(WorkLoopDigestTaskSchema, { id, title: `Task ${id}`, status: s });
    const slot = (sessionId: string, state: string) => create(WorkLoopSlotSchema, { sessionId, state });
    const l = loop({
      sessionsRun: 11,
      parallel: 4,
      slots: [slot("s_a", "working"), slot("s_b", "waiting")],
      completed: [status("0001", "done"), status("0002", "done")],
      blocked: [status("0003", "blocked")],
      created: [status("0010", "done"), status("0011", "todo"), status("0012", "done"), status("0013", "proposed")],
      manualLandings: [{ sha: "abc", subject: "x" }],
    });
    expect(summaryLine(l)).toBe("11 sessions finished, 2 live · 4 completed, 1 blocked, 2 created still open, 1 other commit");
    // The slots line beside it already names the live sessions.
    expect(bannerLine(l)).toBe(
      "Running · 1/4 working, 1 waiting · 11 sessions finished · 4 completed, 1 blocked, 2 created still open, 1 other commit",
    );
    const sections = digestSections(l);
    expect(sections[0].rows.map((t) => t.id)).toEqual(["0001", "0002", "0010", "0012"]);
    expect(sections.find((s) => s.key === "created")?.rows.map((t) => t.id)).toEqual(["0011", "0013"]);
    expect(recentCompleted(l)).toHaveLength(4);
    expect(summaryLine(loop({ sessionsRun: 1 }))).toBe("1 session");
    expect(summaryLine(loop({ slots: [slot("s_a", "working")] }))).toBe("1 live session");
    // A slot still starting has no session yet.
    expect(summaryLine(loop({ sessionsRun: 11, slots: [slot("s_a", "working"), slot("", "working")] }))).toBe(
      "11 sessions finished, 1 live",
    );
    // A finished (restored) loop's slots are history, not live sessions.
    expect(summaryLine(loop({ state: "finished", sessionsRun: 11, slots: [slot("s_old", "waiting")] }))).toBe("11 sessions");
    expect(summaryLine(loop({ sessionsRun: 2, manualLandings: [{ sha: "a" }, { sha: "b" }] }))).toBe("2 sessions · 2 other commits");
  });

  it("omits usage for digest rows no session worked", () => {
    expect(digestTotalsLine(task("0011"))).toBeNull();
    expect(digestTotalsLine(create(WorkLoopDigestTaskSchema, { id: "1", attempts: 1 }))).toBe("0 tokens · unpriced");
    expect(digestTotalsLine(create(WorkLoopDigestTaskSchema, { id: "1", tokens: 1_200n, priceStatus: "partial", cost: 0.5 }))).toBe(
      "1.2K tokens · ≈$0.5000 (partial)",
    );
  });

  it("summarizes uncommitted in-review work", () => {
    expect(uncommittedInReviewLine([], 0)).toBe("");
    expect(uncommittedInReviewLine([task("7")], 1)).toBe("1 in-review task has uncommitted changes in 1 file");
    expect(uncommittedInReviewLine([task("7"), task("8")], 5)).toBe("2 in-review tasks have uncommitted changes in 5 files");
  });

  it("never presents incomplete pricing as exact", () => {
    expect(formatCost(1.5, "priced")).toBe("$1.5000");
    expect(formatCost(1.5, "partial")).toBe("≈$1.5000 (partial)");
    expect(formatCost(1.5, "unpriced")).toBe("unpriced");
    expect(formatCost(1.5, "")).toBe("unpriced");
    expect(totalsLine(12_345n, 0, "unpriced")).toBe("12.3K tokens · unpriced");
    expect(totalsLine(0n, 0, "")).toBe("0 tokens · unpriced");
  });

  it("renders zero limits as explicitly unbounded", () => {
    const lines = resourceEnvelopeLines(
      loop({ resourceEnvelopeCaptured: true, sessionTokenLimit: 200_000n, loopCostLimit: 5, loopTimeLimitSecs: 3_600n, costLimitsPricedOnly: true }),
    );
    expect(lines[0]).toBe("Session: 200K tokens · cost unbounded · wall time unbounded");
    expect(lines[1]).toBe("Loop: tokens unbounded · $5.00 priced cost · 1h 0m wall time");
    expect(lines[2]).toContain("priced models only");
    expect(resourceEnvelopeLines(loop())).toEqual(["Resource envelope unavailable for this pre-upgrade snapshot."]);
    expect(budgetLine(create(GetBudgetResponseSchema, { sessionCost: 2, loopTokens: 1_000_000n }))).toBe(
      "Caps: $2.00 per session · 1M tokens per loop.",
    );
    expect(budgetLine(create(GetBudgetResponseSchema, { sessionCost: 2, sessionTokens: 500_000n }))).toBe(
      "Caps: $2.00 or 500K tokens per session · none per loop.",
    );
    expect(budgetLine(create(GetBudgetResponseSchema, {}))).toBe("No budget caps set.");
  });
});

describe("outcomes and announcements", () => {
  it("separates failures from normal ends", () => {
    expect(isFailureOutcome("loop complete: no ready tasks remain")).toBe(false);
    expect(isFailureOutcome("loop stopped: requested")).toBe(false);
    expect(isFailureOutcome("loop stopped: budget reached (1.2M tokens, cap 1M)")).toBe(false);
    expect(isFailureOutcome("loop stopped: session failed (invalid_request)")).toBe(true);
    expect(isFailureOutcome("loop stopped: refresh token expired")).toBe(true);
    expect(isFailureOutcome("")).toBe(false);
  });

  it("announces an observed active → finished transition only", () => {
    const running = loop();
    const done = loop({ state: "finished", outcome: "loop complete: no ready tasks remain" });
    expect(finishAnnouncement(running, done)).toEqual({
      text: "Work loop in gamma finished: loop complete: no ready tasks remain",
      failure: false,
    });
    // A historical completion seen on first load, or a refresh of a finished loop.
    expect(finishAnnouncement(null, done)).toBeNull();
    expect(finishAnnouncement(done, done)).toBeNull();
    // A different (older) loop finishing is not this loop's transition.
    expect(finishAnnouncement(loop({ loopId: "loop_0" }), done)).toBeNull();
    // Start that came back already finished (startup failure, 0419).
    const failed = loop({ state: "finished", outcome: "loop stopped: anthropic refresh token expired" });
    expect(finishAnnouncement(null, failed, true)).toEqual({
      text: "Work loop in gamma finished: loop stopped: anthropic refresh token expired",
      failure: true,
    });
    expect(finishAnnouncement(running, running)).toBeNull();
  });
});

describe("loop-owned sessions and the sidebar indicator", () => {
  it("collects finished and current session ids", () => {
    const l = loop({ sessions: [sess("s_a"), sess(""), sess("s_b")], currentSessionId: " s_c " });
    expect([...loopSessionIds(l)].sort()).toEqual(["s_a", "s_b", "s_c"]);
    expect(currentSessionId(l)).toBe("s_c");
    expect(loopSessionIds(null).size).toBe(0);
  });

  it("shows only live loops", () => {
    expect(loopIndicator([null, loop({ state: "finished" })])).toBeNull();
    expect(loopIndicator([loop()])).toMatchObject({ label: "running", tone: "live" });
    expect(loopIndicator([loop({ state: "waiting", waitKind: "overloaded" })])).toMatchObject({ label: "waiting", tone: "warn" });
    const two = loopIndicator([loop(), loop({ project: "beta", state: "stopping" })]);
    expect(two?.label).toBe("2 running");
    expect(two?.title).toBe("gamma: Running · 0 sessions\nbeta: Stopping…");
  });
});

describe("waiting line", () => {
  it("names each wait kind", () => {
    expect(waitingLine(loop({ state: "waiting", waitKind: "rate_limit" }))).toBe("Waiting for provider (rate_limit)");
    expect(waitingLine(loop({ state: "waiting", waitKind: "no_progress" }))).toBe("Waiting: last session made no progress");
    expect(waitingLine(loop({ state: "waiting", waitKind: "sessions_waiting" }))).toBe(
      "Waiting: loop sessions are waiting on background jobs",
    );
  });
});

describe("slots", () => {
  it("counts working slots out of the parallelism, and waiting sessions", () => {
    const slot = (sessionId: string, state: string) => create(WorkLoopSlotSchema, { sessionId, state });
    const l = loop({ parallel: 3, slots: [slot("s_a", "working"), slot("", "working"), slot("s_w", "waiting")] });
    expect(slotsLine(l)).toBe("2/3 working, 1 waiting");
    expect(bannerLine(l)).toBe("Running · 2/3 working, 1 waiting");
    expect([...loopSessionIds(l)].sort()).toEqual(["s_a", "s_w"]);
    expect(slotsLine(loop())).toBe("");
  });

  it("tells a parallel loop apart and times each slot in its state", () => {
    const now = Date.parse("2026-11-25T10:00:00Z");
    expect(isParallel(loop({ parallel: 1, slots: [create(WorkLoopSlotSchema, { state: "working" })] }))).toBe(false);
    expect(isParallel(loop({ parallel: 3 }))).toBe(true);
    const at = (state: string, since: string) => create(WorkLoopSlotSchema, { sessionId: "s", state, since });
    expect(slotStateText(at("waiting", "2026-11-25T06:40:00Z"), now)).toBe("waiting 3h 20m");
    expect(slotStateText(at("working", "2026-11-25T09:58:30Z"), now)).toBe("working 1m 30s");
    expect(slotStateText(at("working", ""), now)).toBe("working");
  });

  it("states in the stop confirmation which sessions finish and which keep running", () => {
    const slot = (state: string) => create(WorkLoopSlotSchema, { sessionId: "s", state });
    expect(stopConfirmBody(loop({ parallel: 4, slots: [slot("working"), slot("working"), slot("working"), slot("waiting")] }))).toBe(
      "3 working sessions finish their work. 1 session waiting on its background jobs keeps running. The daemon will not pick another backlog task.",
    );
    expect(stopConfirmBody(loop({ parallel: 1, slots: [slot("working")] }))).toBe(
      "1 working session finishes its work. The daemon will not pick another backlog task.",
    );
    expect(
      stopConfirmBody(loop({ state: "waiting", waitKind: "sessions_waiting", parallel: 2, slots: [slot("waiting"), slot("waiting")] })),
    ).toBe("2 sessions waiting on their background jobs keep running. The daemon will not pick another backlog task.");
    expect(stopConfirmBody(loop({ state: "waiting", waitKind: "rate_limit", parallel: 2 }))).toBe(
      "The loop is waiting for the provider; stopping ends the wait at once. The daemon will not pick another backlog task.",
    );
    // A session the loop let wait that a job woke works, but a stop leaves it running.
    const woken = create(WorkLoopSlotSchema, { sessionId: "w", state: "working", detached: true });
    expect(stopConfirmBody(loop({ parallel: 2, slots: [slot("working"), woken, slot("waiting")] }))).toBe(
      "1 working session finishes its work. 2 sessions let wait on background jobs keep running (1 working again). The daemon will not pick another backlog task.",
    );
    // Snapshots without slots (older daemons) keep the one-session wording.
    expect(stopConfirmBody(loop())).toBe("The current session finishes its work; the daemon will not pick another backlog task.");
  });

  it("sends 0 for the project default and clamps explicit counts", () => {
    expect(parallelToSend(null, 2)).toBe(0);
    expect(parallelToSend(2, 2)).toBe(0);
    expect(parallelToSend(1, 2)).toBe(1);
    expect(parallelToSend(4, 0)).toBe(4);
    expect(parallelToSend(40, 1)).toBe(16);
  });
});

describe("ambient sidebar status", () => {
  const now = Date.parse("2026-10-11T12:00:00Z");
  const finished = (loopId: string, finishedAt: string, sessionsRun = 2) =>
    loop({ loopId, state: "finished", finishedAt, sessionsRun });

  it("shows live loops and recently finished ones that ran, until dismissed", () => {
    const shown = ambientLoops(
      [
        { project: "live", loop: loop({ loopId: "live" }) },
        { project: "recent", loop: finished("recent", "2026-10-11T02:00:00Z") },
        { project: "stale", loop: finished("stale", "2026-10-10T11:00:00Z") },
        { project: "legacy", loop: finished("legacy", "") },
        { project: "empty", loop: finished("empty", "2026-10-11T11:00:00Z", 0) },
        { project: "gone", loop: finished("gone", "2026-10-11T11:00:00Z") },
        { project: "none", loop: null },
      ],
      now,
      new Set(["gone", "live"]),
    );
    expect(shown.map((l) => l.project)).toEqual(["live", "recent"]);
  });

  it("orders completed tasks by the last session that worked on them", () => {
    const l = loop({
      completed: [task("0001"), task("0002"), task("0003")],
      sessions: [{ sessionId: "a", focus: "0002" }, { sessionId: "b", focus: "0001" }, { sessionId: "c", focus: "0002" }],
    });
    expect(recentCompleted(l).map((t) => t.id)).toEqual(["0002", "0001", "0003"]);
  });
});
