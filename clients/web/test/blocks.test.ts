// Transcript display blocks: long tool/reasoning runs fold into one activity
// block, repeated identical notices collapse, and search can still reach a
// hidden row.
import { describe, expect, it } from "vitest";
import type { RowKind, TranscriptRow } from "../src/features/session/projection";
import {
  ACTIVITY_FOLD_MIN,
  ACTIVITY_TAIL,
  activitySummary,
  blockHides,
  compactDuration,
  foldedTailStart,
  transcriptBlocks,
} from "../src/features/session/blocks";

let n = 0;
function row(kind: RowKind, opts: { actor?: string; ts?: string } = {}): TranscriptRow {
  n++;
  return { id: `r${n}`, kind, seq: n, updatedSeq: n, actor: opts.actor ?? "coordinator", ts: opts.ts ?? "", detailAvailable: false };
}
const tool = (name: string, status: "ok" | "error" | "running" = "ok", ts = "") =>
  row({ type: "tool", name, status, args: "{}", output: "" }, { ts });
const thinking = () => row({ type: "thinking", text: "hmm" });
const note = (text: string, actor = "coordinator") => row({ type: "system", text }, { actor });
const model = () => row({ type: "model", text: "done" });

describe("transcript blocks", () => {
  it("short runs stay as individual rows", () => {
    const rows = [model(), ...Array.from({ length: ACTIVITY_FOLD_MIN - 1 }, () => tool("Read")), model()];
    const blocks = transcriptBlocks(rows);
    expect(blocks.every((b) => b.type === "row")).toBe(true);
    expect(blocks).toHaveLength(rows.length);
  });

  it("a long run of tools and reasoning folds into one activity block", () => {
    const run = [thinking(), ...Array.from({ length: ACTIVITY_FOLD_MIN }, () => tool("Bash"))];
    const rows = [model(), ...run, model()];
    const blocks = transcriptBlocks(rows);
    expect(blocks).toEqual([
      { type: "row", index: 0, repeat: 1 },
      { type: "activity", start: 1, end: 1 + run.length, key: run[0].id },
      { type: "row", index: rows.length - 1, repeat: 1 },
    ]);
    const b = blocks[1] as { start: number; end: number };
    expect(foldedTailStart(b)).toBe(b.end - ACTIVITY_TAIL);
    // Only rows before the visible tail count as hidden.
    expect(blockHides(rows, b, run[0].id)).toBe(true);
    expect(blockHides(rows, b, run.at(-1)!.id)).toBe(false);
    expect(blockHides(rows, b, null)).toBe(false);
  });

  it("a system notice breaks an activity run", () => {
    const half = Math.ceil(ACTIVITY_FOLD_MIN / 2);
    const rows = [...Array.from({ length: half }, () => tool("Read")), note("x"), ...Array.from({ length: half }, () => tool("Read"))];
    expect(transcriptBlocks(rows).some((b) => b.type === "activity")).toBe(false);
  });

  it("identical consecutive notices collapse with a count", () => {
    const rows = [note("job claimed"), note("job claimed"), note("job claimed"), note("job claimed", "agent:a"), note("other")];
    expect(transcriptBlocks(rows)).toEqual([
      { type: "row", index: 0, repeat: 3 },
      { type: "row", index: 3, repeat: 1 },
      { type: "row", index: 4, repeat: 1 },
    ]);
  });

  it("a notice run hiding the search's current row is not merged", () => {
    const rows = [note("job claimed"), note("job claimed")];
    expect(transcriptBlocks(rows, rows[1].id)).toEqual([
      { type: "row", index: 0, repeat: 1 },
      { type: "row", index: 1, repeat: 1 },
    ]);
  });
});

describe("activity summary", () => {
  it("counts tools by frequency, reasoning, failures, and wall time", () => {
    const rows = [
      tool("Read", "ok", "2026-10-07T10:00:00Z"),
      thinking(),
      tool("Bash", "error"),
      tool("Bash"),
      tool("Edit", "running", "2026-10-07T10:04:12Z"),
    ];
    expect(activitySummary(rows, 0, rows.length)).toEqual({
      steps: 5,
      tools: [
        { name: "Bash", count: 2 },
        { name: "Read", count: 1 },
        { name: "Edit", count: 1 },
      ],
      reasoning: 1,
      failed: 1,
      running: 1,
      durationMs: 252_000,
    });
  });

  it("formats durations compactly", () => {
    expect(compactDuration(null)).toBeNull();
    expect(compactDuration(400)).toBeNull();
    expect(compactDuration(45_000)).toBe("45s");
    expect(compactDuration(252_000)).toBe("4m 12s");
    expect(compactDuration(3_780_000)).toBe("1h 03m");
  });
});
