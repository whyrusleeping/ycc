// Activity panel logic: tree nesting, timeline geometry, and the job-output
// follow buffer.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ActivityNodeSchema, type ActivityNode } from "../src/gen/ycc/v1/ycc_pb";
import { barGeometry, buildTree, elapsedMs, JobOutputBuffer, laneIntervals, MIN_BAR_PX, timeRange } from "../src/features/activity/model";

const node = (init: MessageInitShape<typeof ActivityNodeSchema>): ActivityNode => create(ActivityNodeSchema, init);
const T0 = Date.parse("2026-10-21T10:00:00Z");
const at = (s: number) => new Date(T0 + s * 1000).toISOString();

describe("buildTree", () => {
  it("nests jobs under the actor that started them and never drops a node", () => {
    const rows = buildTree([
      node({ id: "coordinator", kind: "coordinator", actor: "coordinator" }),
      node({ id: "agent:a1", parentId: "coordinator", actor: "agent:a1" }),
      node({ id: "job_1", parentId: "coordinator" }),
      node({ id: "job_2", parentId: "agent:a1" }),
      node({ id: "job_3", parentId: "agent:gone" }), // unknown parent → root
      node({ id: "x", parentId: "y", actor: "x" }), // a parent cycle
      node({ id: "y", parentId: "x", actor: "y" }),
    ]);
    expect(rows.map((r) => [r.node.id, r.depth])).toEqual([
      ["coordinator", 0],
      ["agent:a1", 1],
      ["job_2", 2],
      ["job_1", 1],
      ["job_3", 1],
      ["x", 1],
      ["y", 2],
    ]);
  });
});

describe("timeline geometry", () => {
  const nodes = [
    node({ id: "coordinator", actor: "coordinator", status: "idle", startedAt: at(0) }),
    node({
      id: "agent:a1",
      actor: "agent:a1",
      status: "running",
      startedAt: at(10),
      spans: [
        { round: 1, status: "done", startedAt: at(10), finishedAt: at(20) },
        { round: 2, status: "running", startedAt: at(30) },
      ],
    }),
    node({ id: "job_1", status: "lost", startedAt: at(5) }), // in flight when the session stopped
  ];

  it("runs the axis to now while something runs, else to the latest finish", () => {
    const now = T0 + 100_000;
    expect(timeRange({ nodes, live: true }, now)).toEqual({ start: T0, end: now, live: true });
    // Not live: nothing is really running, so the axis ends at the last known time.
    expect(timeRange({ nodes, live: false }, now)).toEqual({ start: T0, end: T0 + 30_000, live: false });
  });

  it("extends running rounds to now and keeps tiny bars visible inside the track", () => {
    const now = T0 + 100_000;
    const range = timeRange({ nodes, live: true }, now)!;
    const bars = barGeometry(laneIntervals(nodes[1], range), range, 1000);
    expect(bars.map((b) => [b.round, b.status, b.open, Math.round(b.x), Math.round(b.w)])).toEqual([
      [1, "done", false, 100, 100],
      [2, "running", true, 300, 700],
    ]);
    const job = laneIntervals(nodes[2], range);
    expect(job[0]).toMatchObject({ status: "lost", unknownEnd: true, end: range.end });
    // A 1 ms round at the very end of the axis still gets a hoverable bar within the track.
    const tiny = barGeometry([{ start: now - 1, end: now, status: "done", open: false, unknownEnd: false, round: 1 }], range, 1000)[0];
    expect(tiny.w).toBe(MIN_BAR_PX);
    expect(tiny.x + tiny.w).toBeLessThanOrEqual(1000);
  });

  it("ends a finished coordinator-only session at the coordinator's last activity", () => {
    // No subagents or jobs: only the root's lastActivityAt says how long the session ran.
    const root = node({ id: "coordinator", actor: "coordinator", status: "idle", startedAt: at(0), lastActivityAt: at(600) });
    const range = timeRange({ nodes: [root], live: false }, T0 + 3_600_000)!;
    expect(range).toEqual({ start: T0, end: T0 + 600_000, live: false });
    expect(laneIntervals(root, range)[0]).toMatchObject({ start: T0, end: T0 + 600_000, open: false });
    expect(elapsedMs(root, range, T0 + 3_600_000)).toBe(600_000);
  });
});

describe("JobOutputBuffer", () => {
  const chunk = (bytes: number[], start: number, retainedStart = 0) => ({
    data: new Uint8Array(bytes),
    start: BigInt(start),
    end: BigInt(start + bytes.length),
    retainedStart: BigInt(retainedStart),
  });

  it("keeps a multi-byte character split across reads intact", () => {
    const bytes = [...new TextEncoder().encode("é✓\n")]; // 2 + 3 + 1 bytes
    const buf = new JobOutputBuffer();
    buf.apply(chunk(bytes.slice(0, 1), 0));
    buf.apply(chunk(bytes.slice(1, 3), 1));
    buf.apply(chunk(bytes.slice(3), 3));
    expect(buf.text).toBe("é✓\n");
    expect(buf.cursor).toBe(6n);
  });

  it("marks bytes dropped between reads and resumes at the new start", () => {
    const enc = (s: string) => [...new TextEncoder().encode(s)];
    const buf = new JobOutputBuffer();
    buf.apply(chunk(enc("one\n"), 100, 40)); // tail read: 40 bytes were already gone
    buf.apply(chunk(enc("three\n"), 110, 110)); // bytes 104..110 dropped before this read
    expect(buf.droppedBefore).toBe(40n);
    expect(buf.droppedWhileFollowing).toBe(6n);
    expect(buf.text).toBe("one\n[… 6 bytes dropped …]\nthree\n");
    expect(buf.cursor).toBe(116n);
  });
});
