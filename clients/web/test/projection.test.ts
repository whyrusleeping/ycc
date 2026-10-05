// Presentation-API reducer semantics: snapshot install, row upsert/delete with
// version guards, earlier-page merges, detail installs, transient tails, and
// pending-question state.
import { describe, expect, it } from "vitest";
import { SessionProjection } from "../src/features/session/projection";
import type { WireEvent, WireRow, WireState } from "../src/features/session/wire";

function ev(seq: number, type: string, data: Record<string, unknown> = {}, actor = "coordinator"): WireEvent {
  return { seq, ts: `2026-01-01T00:00:${String(seq).padStart(2, "0")}Z`, actor, type, dataJson: JSON.stringify(data), transient: false };
}

function delta(text: string, actor = "coordinator"): WireEvent {
  return { seq: 0, ts: "", actor, type: "turn_delta", dataJson: JSON.stringify({ text }), transient: true };
}

function row(id: string, position: number, updated: number, events: WireEvent[], hasDetail = false): WireRow {
  return { id, positionSeq: position, updatedSeq: updated, events, hasDetail };
}

function turn(seq: number, text: string, actor = "coordinator"): WireRow {
  return row(`seq-${seq}`, seq, seq, [ev(seq, "model_turn", { text }, actor)]);
}

function state(through: number, extra: Partial<WireState> = {}): WireState {
  return {
    indexedThroughSeq: through,
    lastEventTimestamp: "",
    phase: "running",
    errorMessage: "",
    errorRetryable: false,
    coordinatorModel: "",
    contextTokens: 0,
    hasContextTokens: false,
    rolloverAvailable: true,
    pendingQuestions: [],
    pendingRowId: "",
    pendingQuestionsTruncated: false,
    pauseRequested: false,
    awaitingJobs: false,
    ...extra,
  };
}

function texts(p: SessionProjection): string[] {
  return p.rows.map((r) => ("text" in r.kind ? r.kind.text : r.kind.type));
}

describe("indexed snapshot and updates", () => {
  it("installs a snapshot and sets the cursor from indexed_through_seq", () => {
    const p = new SessionProjection();
    p.installIndexed(state(12, { phase: "idle", coordinatorModel: "claude" }), [turn(10, "a"), turn(12, "b")]);
    expect(texts(p)).toEqual(["a", "b"]);
    expect(p.lastPersistedSeq).toBe(12);
    expect(p.phase.kind).toBe("idle");
    expect(p.coordinatorModel).toBe("claude");
    expect(p.rows[1].seq).toBe(12);
  });

  it("upserts in place, appends newer rows, and deletes tombstoned rows", () => {
    const p = new SessionProjection();
    const call = row("tool-1", 3, 3, [ev(3, "tool_call", { id: "1", name: "Read", args: "{}" })]);
    p.installIndexed(state(4), [turn(2, "hi"), call, turn(4, "doing")]);
    const done = row("tool-1", 3, 5, [
      ev(3, "tool_call", { id: "1", name: "Read", args: "{}" }),
      ev(5, "tool_result", { id: "1", name: "Read", result: "ok" }),
    ]);
    p.applyIndexed(state(6), [done, turn(6, "next")], ["seq-2"]);
    expect(p.rows.map((r) => r.id)).toEqual(["tool-1", "seq-4", "seq-6"]);
    const tool = p.rows[0].kind;
    expect(tool.type === "tool" && tool.status).toBe("ok");
    expect(p.lastPersistedSeq).toBe(6);
  });

  it("ignores updates at or below the cursor and stale row versions", () => {
    const p = new SessionProjection();
    p.installIndexed(state(5), [row("r", 5, 5, [ev(5, "model_turn", { text: "current" })])]);
    p.applyIndexed(state(5), [turn(9, "replayed")], []);
    expect(texts(p)).toEqual(["current"]);
    p.applyIndexed(state(7), [row("r", 5, 4, [ev(4, "model_turn", { text: "stale" })])], []);
    expect(texts(p)).toEqual(["current"]);
    expect(p.lastPersistedSeq).toBe(7);
  });

  it("keeps a deleted row deleted even if an older version is upserted later", () => {
    const p = new SessionProjection();
    p.installIndexed(state(3), [turn(3, "x")]);
    p.applyIndexed(state(8), [], ["seq-3"]);
    p.applyIndexed(state(9), [row("seq-3", 3, 7, [ev(3, "model_turn", { text: "old" })])], []);
    expect(p.rows).toEqual([]);
  });
});

describe("earlier pages", () => {
  it("prepends an earlier page in order", () => {
    const p = new SessionProjection();
    p.installIndexed(state(20), [turn(19, "c"), turn(20, "d")]);
    p.prependIndexed([turn(10, "a"), turn(11, "b")], 20);
    expect(texts(p)).toEqual(["a", "b", "c", "d"]);
  });

  it("does not resurrect rows deleted after the page was captured", () => {
    const p = new SessionProjection();
    p.installIndexed(state(20), [turn(20, "d")]);
    p.applyIndexed(state(21), [], ["seq-10"]);
    p.prependIndexed([turn(10, "gone"), turn(11, "b")], 20);
    expect(texts(p)).toEqual(["b", "d"]);
  });

  it("installs the buffered live version of an old row instead of the page's stale copy", () => {
    const p = new SessionProjection();
    p.installIndexed(state(20), [turn(20, "d")]);
    // A live edit to an old, not-yet-loaded tool row arrives first.
    const live = row("tool-9", 9, 21, [
      ev(9, "tool_call", { id: "9", name: "Bash", args: "{}" }),
      ev(21, "tool_result", { id: "9", name: "Bash", result: "new" }),
    ]);
    p.applyIndexed(state(21), [live], []);
    expect(p.rows.map((r) => r.id)).toEqual(["seq-20"]);
    const stale = row("tool-9", 9, 9, [ev(9, "tool_call", { id: "9", name: "Bash", args: "{}" })]);
    p.prependIndexed([stale], 20);
    expect(p.rows.map((r) => r.id)).toEqual(["tool-9", "seq-20"]);
    const k = p.rows[0].kind;
    expect(k.type === "tool" && k.output).toBe("new");
  });

  it("skips page rows newer than the page's own indexed_through_seq", () => {
    const p = new SessionProjection();
    p.installIndexed(state(20), [turn(20, "d")]);
    p.prependIndexed([row("seq-5", 5, 25, [ev(5, "model_turn", { text: "future" })])], 20);
    expect(texts(p)).toEqual(["d"]);
  });
});

describe("detail", () => {
  it("replaces an abbreviated row with its detail and keeps it across snapshots of the same version", () => {
    const p = new SessionProjection();
    const short = row("seq-4", 4, 4, [ev(4, "model_turn", { text: "abbrev…" })], true);
    p.installIndexed(state(4), [short]);
    expect(p.rows[0].detailAvailable).toBe(true);
    p.installIndexedDetail(row("seq-4", 4, 4, [ev(4, "model_turn", { text: "the full text" })]));
    expect(texts(p)).toEqual(["the full text"]);
    expect(p.rows[0].detailAvailable).toBe(false);
    p.installIndexed(state(4), [short]);
    expect(texts(p)).toEqual(["the full text"]);
  });

  it("ignores detail older than the loaded row", () => {
    const p = new SessionProjection();
    p.installIndexed(state(6), [row("tool-1", 3, 6, [ev(3, "tool_call", { id: "1", name: "x" }), ev(6, "tool_result", { id: "1", result: "r" })], true)]);
    p.installIndexedDetail(row("tool-1", 3, 3, [ev(3, "tool_call", { id: "1", name: "x" })]));
    const k = p.rows[0].kind;
    expect(k.type === "tool" && k.status).toBe("ok");
  });
});

describe("transient live tails", () => {
  it("never advance the cursor and are retired by the actor's durable turn", () => {
    const p = new SessionProjection();
    p.installIndexed(state(5), [turn(5, "a")]);
    p.apply(delta("draft"));
    p.apply(delta("other", "implementer"));
    expect(p.lastPersistedSeq).toBe(5);
    expect(p.liveTails.map((t) => t.actor)).toEqual(["coordinator", "implementer"]);
    p.apply(delta("draft more"));
    expect(p.liveTails.map((t) => t.actor)).toEqual(["coordinator", "implementer"]);
    expect(texts(p).slice(-2)).toEqual(["draft more", "other"]);
    p.applyIndexed(state(6), [turn(6, "final")], []);
    expect(p.liveTails.map((t) => t.actor)).toEqual(["implementer"]);
    expect(p.lastPersistedSeq).toBe(6);
  });

  it("are all cleared when the session goes idle without awaiting jobs", () => {
    const p = new SessionProjection();
    p.installIndexed(state(1), []);
    p.apply(delta("x", "implementer"));
    p.applyIndexed(state(2, { phase: "idle", awaitingJobs: true }), [], []);
    expect(p.liveTails).toHaveLength(1);
    p.applyIndexed(state(3, { phase: "idle" }), [], []);
    expect(p.liveTails).toHaveLength(0);
  });

  it("are dropped by a fresh snapshot", () => {
    const p = new SessionProjection();
    p.apply(delta("x"));
    p.installIndexed(state(1), []);
    expect(p.liveTails).toHaveLength(0);
  });
});

describe("pending questions", () => {
  const ask = row("seq-7", 7, 7, [ev(7, "question_asked", { questions: [{ question: "A?", options: ["x", "y"] }, { question: "B?" }] })]);

  it("come from durable state and close when another client answers", () => {
    const p = new SessionProjection();
    p.installIndexed(
      state(7, { pendingRowId: "seq-7", pendingQuestions: [{ prompt: "A?", options: ["x", "y"] }, { prompt: "B?", options: [] }] }),
      [ask],
    );
    expect(p.pendingQuestion?.questions.map((q) => q.prompt)).toEqual(["A?", "B?"]);
    expect(p.pendingQuestion?.rowId).toBe("seq-7");
    const answered = row("seq-7", 7, 8, [...ask.events, ev(8, "question_answered", { answers: ["x", "z"] })]);
    p.applyIndexed(state(8), [answered], []);
    expect(p.pendingQuestion).toBeNull();
    const k = p.rows[0].kind;
    expect(k.type === "question" && k.answer).toBe("x; z");
  });

  it("withholds a truncated batch until the row detail restores it", () => {
    const p = new SessionProjection();
    p.installIndexed(state(9, { pendingRowId: "seq-7", pendingQuestionsTruncated: true, pendingQuestions: [{ prompt: "A?", options: [] }] }), []);
    expect(p.pendingQuestion).toBeNull();
    expect(p.awaitsAnswer).toBe(true);
    expect(p.needsPendingDetail("seq-7")).toBe(true);
    p.installIndexedDetail(ask);
    expect(p.pendingQuestion?.questions).toHaveLength(2);
    expect(p.rows).toHaveLength(0); // the out-of-page row is not inserted
  });
});
