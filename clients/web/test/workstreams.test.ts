// Workstream logic: lifecycle and queue parsing, badges and tones, which
// actions a row offers, grouping, the sidebar indicator, merge-all, and the
// spawn request. Mirrors YccKit's WorkstreamsModelTests where they overlap.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { BacklogTaskSummarySchema, WorkstreamInfoSchema, type WorkstreamInfo } from "../src/gen/ycc/v1/ycc_pb";
import {
  badgeTitle,
  badgeTone,
  branchLabel,
  buildSpawnRequest,
  canRetry,
  canSpawn,
  commitSummary,
  groupWorkstreams,
  integrationState,
  isDiscardable,
  isGateEligible,
  isMergeable,
  mergeAllMessage,
  mergeAllReady,
  pollInterval,
  spawnableTasks,
  statusReason,
  taskPrompt,
  workstreamIndicator,
  workstreamStatus,
} from "../src/features/workstreams/model";

const ws = (id: string, extra: MessageInitShape<typeof WorkstreamInfoSchema> = {}): WorkstreamInfo =>
  create(WorkstreamInfoSchema, { id, project: "alpha", branch: `ycc/ws/${id}`, status: "active", ...extra });

describe("status parsing and permissions", () => {
  it("parses lifecycle and queue states", () => {
    expect(workstreamStatus(ws("a", { status: "NEEDS_ATTENTION" }))).toBe("needs_attention");
    expect(workstreamStatus(ws("a", { status: "weird" }))).toBe("unknown");
    expect(integrationState(ws("a"))).toBe("idle");
    expect(integrationState(ws("a", { integrationState: "Queued" }))).toBe("queued");
    expect(integrationState(ws("a", { integrationState: "spinning" }))).toBe("unknown");
  });

  it("allows preview/merge in flight and discard for stale too", () => {
    for (const s of ["active", "ready", "needs_attention"] as const) {
      expect(isMergeable(s) && isDiscardable(s)).toBe(true);
    }
    expect(isMergeable("stale")).toBe(false);
    expect(isDiscardable("stale")).toBe(true);
    expect(isMergeable("merged") || isDiscardable("discarded")).toBe(false);
  });

  it("offers retry for needs-attention rows and idle auto-mode ready rows", () => {
    expect(canRetry(ws("a", { status: "needs_attention", integrationMode: "gate" }))).toBe(true);
    expect(canRetry(ws("a", { status: "ready", integrationMode: "auto" }))).toBe(true);
    expect(canRetry(ws("a", { status: "ready", integrationMode: "auto", integrationState: "queued" }))).toBe(false);
    expect(canRetry(ws("a", { status: "ready", integrationMode: "gate" }))).toBe(false);
    expect(canRetry(ws("a", { status: "active", integrationMode: "auto" }))).toBe(false);
  });
});

describe("badges", () => {
  it("lets terminal and attention states win, then queue state, then Gated", () => {
    expect(badgeTitle(ws("a"))).toBe("Working");
    expect(badgeTitle(ws("a", { status: "ready", integrationState: "integrating" }))).toBe("Integrating");
    expect(badgeTitle(ws("a", { status: "active", integrationState: "queued" }))).toBe("Queued");
    expect(badgeTitle(ws("a", { status: "ready", integrationMode: "gate" }))).toBe("Gated");
    expect(badgeTitle(ws("a", { status: "ready", integrationMode: "auto" }))).toBe("Ready");
    expect(badgeTitle(ws("a", { status: "needs_attention", integrationState: "queued" }))).toBe("Needs attention");
    expect(badgeTitle(ws("a", { status: "merged" }))).toBe("Merged");
    expect(badgeTitle(ws("a", { status: "stale" }))).toBe("Stale");
  });

  it("maps tones", () => {
    expect(badgeTone(ws("a"))).toBe("live");
    expect(badgeTone(ws("a", { status: "ready", integrationState: "queued" }))).toBe("queue");
    expect(badgeTone(ws("a", { status: "ready" }))).toBe("ready");
    expect(badgeTone(ws("a", { status: "needs_attention" }))).toBe("attention");
    expect(badgeTone(ws("a", { status: "merged" }))).toBe("done");
    expect(badgeTone(ws("a", { status: "discarded" }))).toBe("muted");
    expect(badgeTone(ws("a", { status: "stale" }))).toBe("warn");
  });

  it("labels commits, reasons, and branches", () => {
    expect(commitSummary(ws("a"))).toBe("no commits");
    expect(commitSummary(ws("a", { commitCount: 1n }))).toBe("1 commit");
    expect(commitSummary(ws("a", { commitCount: 3n }))).toBe("3 commits");
    expect(statusReason(ws("a", { statusReason: "  " }))).toBeNull();
    expect(statusReason(ws("a", { statusReason: "verify failed" }))).toBe("verify failed");
    expect(branchLabel(ws("a", { branch: "" }))).toBe("a");
  });
});

describe("lists and indicators", () => {
  it("groups in-flight rows (attention first, newest first) and finished history", () => {
    const rows = [
      ws("old", { createdAt: "2026-10-01T00:00:00Z" }),
      ws("merged", { status: "merged", createdAt: "2026-10-03T00:00:00Z" }),
      ws("new", { createdAt: "2026-10-04T00:00:00Z" }),
      ws("bad", { status: "needs_attention", createdAt: "2026-10-02T00:00:00Z" }),
      ws("gone", { status: "discarded", createdAt: "2026-10-05T00:00:00Z" }),
    ];
    const g = groupWorkstreams(rows);
    expect(g.inFlight.map((w) => w.id)).toEqual(["bad", "new", "old"]);
    expect(g.finished.map((w) => w.id)).toEqual(["gone", "merged"]);
  });

  it("polls quickly only while something can change by itself", () => {
    expect(pollInterval(undefined)).toBe(15_000);
    expect(pollInterval([ws("a", { status: "needs_attention" })])).toBe(15_000);
    expect(pollInterval([ws("a")])).toBe(3_000);
    expect(pollInterval([ws("a", { status: "ready", integrationState: "integrating" })])).toBe(3_000);
  });

  it("summarizes in-flight work for the sidebar", () => {
    expect(workstreamIndicator(undefined)).toBeNull();
    expect(workstreamIndicator([ws("a", { status: "merged" })])).toBeNull();
    expect(workstreamIndicator([ws("a"), ws("b", { status: "ready", integrationMode: "gate" })])).toEqual({
      label: "2",
      title: "2 in flight · 1 working",
      tone: "live",
    });
    expect(workstreamIndicator([ws("a", { status: "ready" })])?.tone).toBe("idle");
    expect(workstreamIndicator([ws("a"), ws("b", { status: "needs_attention" })])).toEqual({
      label: "1!",
      title: "2 in flight · 1 working · 1 needs attention",
      tone: "attention",
    });
  });
});

describe("merge all ready", () => {
  const gate = (id: string) => ws(id, { status: "ready", integrationMode: "gate" });

  it("merges only gate-mode ready rows, in order", async () => {
    expect(isGateEligible(gate("a"))).toBe(true);
    expect(isGateEligible(ws("x", { status: "ready", integrationMode: "auto" }))).toBe(false);
    const calls: string[] = [];
    const summary = await mergeAllReady(
      [gate("a"), ws("x", { status: "ready", integrationMode: "manual" }), gate("b")],
      async (id) => {
        calls.push(id);
        return { merged: true, needsAccept: false, conflicts: [] };
      },
      String,
    );
    expect(calls).toEqual(["a", "b"]);
    expect(summary).toEqual({ merged: 2, error: null });
    expect(mergeAllMessage(summary)).toBe("Merged 2 workstreams");
  });

  it("stops at the first conflict, review gate, or error with an honest count", async () => {
    const conflict = await mergeAllReady(
      [gate("a"), gate("b"), gate("c")],
      async (id) => (id === "b" ? { merged: false, needsAccept: false, conflicts: ["x.go", "y.go"] } : { merged: true, needsAccept: false, conflicts: [] }),
      String,
    );
    expect(conflict).toEqual({ merged: 1, error: "b conflicts: x.go, y.go" });
    expect(mergeAllMessage(conflict)).toBe("Merged 1 workstream, then failed: b conflicts: x.go, y.go");
    const gated = await mergeAllReady([gate("a")], async () => ({ merged: false, needsAccept: true, conflicts: [] }), String);
    expect(gated.error).toBe("a still needs review");
    const thrown = await mergeAllReady(
      [gate("a")],
      async () => {
        throw new Error("base is dirty");
      },
      (e) => (e as Error).message,
    );
    expect(thrown).toEqual({ merged: 0, error: "base is dirty" });
  });
});

describe("spawn", () => {
  it("needs a project and a task or a prompt; trims the request", () => {
    expect(canSpawn("alpha", { taskId: "", prompt: "  ", baseRef: "" })).toBe(false);
    expect(canSpawn("", { taskId: "0001", prompt: "", baseRef: "" })).toBe(false);
    expect(canSpawn("alpha", { taskId: "0001", prompt: "", baseRef: "" })).toBe(true);
    expect(canSpawn("alpha", { taskId: "", prompt: "fix it", baseRef: "" })).toBe(true);
    expect(buildSpawnRequest("alpha", { taskId: " 0001 ", prompt: " do it\n", baseRef: " main " })).toEqual({
      project: "alpha",
      taskId: "0001",
      prompt: "do it",
      baseRef: "main",
    });
  });

  it("offers actionable tasks by priority then id, with the TUI's default prompt", () => {
    const t = (id: string, status: string, priority: number, ready = true) =>
      create(BacklogTaskSummarySchema, { id, title: `T${id}`, status, priority, ready });
    const offered = spawnableTasks([t("0003", "todo", 2), t("0001", "todo", 3), t("0002", "in_progress", 2), t("0004", "proposed", 1), t("0005", "todo", 1, false), t("0006", "done", 1)]);
    expect(offered.map((x) => x.id)).toEqual(["0002", "0003", "0001"]);
    expect(taskPrompt(offered[0])).toBe("Work on backlog task 0002: T0002");
  });
});
