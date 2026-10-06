// Work-loop logic: lifecycle state, digest sections and summary lines, the
// resource envelope, finish announcements, loop-owned sessions, and the
// sidebar indicator. Mirrors YccKit's WorkLoopModelTests where they overlap.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  GetBudgetResponseSchema,
  WorkLoopDigestTaskSchema,
  WorkLoopInfoSchema,
  WorkLoopSessionSchema,
  type WorkLoopInfo,
} from "../src/gen/ycc/v1/ycc_pb";
import {
  bannerLine,
  budgetLines,
  canStart,
  canStop,
  currentSessionId,
  digestSections,
  durationText,
  finishAnnouncement,
  formatCost,
  isActive,
  isFailureOutcome,
  loopIndicator,
  loopSessionIds,
  loopState,
  pollInterval,
  resourceEnvelopeLines,
  stateTitle,
  summaryLine,
  totalsLine,
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
    expect(digestSections(l).map((s) => s.title)).toEqual(["Completed", "Blocked", "Unfinished", "Created"]);
    expect(digestSections(loop())).toEqual([]);
  });

  it("summarizes sessions and digest counts", () => {
    expect(summaryLine(loop({ sessionsRun: 1 }))).toBe("1 session");
    expect(summaryLine(loop({ sessionsRun: 3, completed: [task("1"), task("2")], blocked: [task("3")] }))).toBe(
      "3 sessions · 2 completed, 1 blocked",
    );
  });

  it("never presents incomplete pricing as exact", () => {
    expect(formatCost(1.5, "priced")).toBe("$1.5000");
    expect(formatCost(1.5, "partial")).toBe("≈$1.5000 (partial)");
    expect(formatCost(1.5, "unpriced")).toBe("unpriced");
    expect(formatCost(1.5, "")).toBe("unpriced");
    expect(totalsLine(12_345n, 0, "unpriced")).toBe("12.3K tokens · unpriced");
    expect(totalsLine(0n, 0, "")).toBe("0 tokens · unpriced");
  });

  it("formats durations", () => {
    expect(durationText(0n)).toBeNull();
    expect(durationText(45n)).toBe("45s");
    expect(durationText(200n)).toBe("3m 20s");
    expect(durationText(7_500n)).toBe("2h 5m");
  });

  it("renders zero limits as explicitly unbounded", () => {
    const lines = resourceEnvelopeLines(
      loop({ resourceEnvelopeCaptured: true, sessionTokenLimit: 200_000n, loopCostLimit: 5, loopTimeLimitSecs: 3_600n, costLimitsPricedOnly: true }),
    );
    expect(lines[0]).toBe("Session: 200K tokens · cost unbounded · wall time unbounded");
    expect(lines[1]).toBe("Loop: tokens unbounded · $5.00 priced cost · 1h 0m wall time");
    expect(lines[2]).toContain("priced models only");
    expect(resourceEnvelopeLines(loop())).toEqual(["Resource envelope unavailable for this pre-upgrade snapshot."]);
    expect(budgetLines(create(GetBudgetResponseSchema, { sessionCost: 2, loopTokens: 1_000_000n }))).toEqual([
      "Per session: tokens unbounded · $2.00 priced cost",
      "Whole loop: 1M tokens · cost unbounded",
    ]);
  });

  it("describes waiting and banner states", () => {
    expect(waitingLine(loop({ state: "waiting", waitKind: "rate_limit" }))).toBe("Waiting for provider (rate_limit)");
    expect(waitingLine(loop({ state: "waiting", resumeAt: "2026-10-06T03:15:00Z" }))).toMatch(/^Waiting for provider — resumes /);
    expect(bannerLine(null)).toBe("Not running");
    expect(bannerLine(loop({ sessionsRun: 2 }))).toBe("Running · 2 sessions");
    expect(bannerLine(loop({ state: "stopping" }))).toBe("Stopping…");
    expect(bannerLine(loop({ state: "finished", outcome: "loop complete: no ready tasks remain" }))).toBe(
      "loop complete: no ready tasks remain",
    );
    expect(bannerLine(loop({ state: "finished", sessionsRun: 1 }))).toBe("Finished · 1 session");
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
