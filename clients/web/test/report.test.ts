// A session_idle report that is the agent's plain reply (the daemon deleted
// the duplicate model row) reads as an agent turn; a control-tool report keeps
// its Finished/Blocked card.
import { describe, expect, it } from "vitest";
import { decodeRow, type TranscriptRow } from "../src/features/session/projection";
import { reportPresentation } from "../src/features/session/report";
import type { WireEvent } from "../src/features/session/wire";

let seq = 0;
function ev(type: string, data: Record<string, unknown>, actor = "coordinator"): WireEvent {
  seq++;
  return { seq, ts: "", actor, type, dataJson: JSON.stringify(data), transient: false };
}
function indexed(...events: WireEvent[]): TranscriptRow {
  const first = events[0];
  return decodeRow({ id: `seq-${first.seq}`, positionSeq: first.seq, updatedSeq: events.at(-1)!.seq, events, hasDetail: false })!;
}

describe("report presentation", () => {
  it("a chat reply (model row folded into the report) is a reply", () => {
    const rows = [indexed(ev("user_input", { text: "hi" })), indexed(ev("session_idle", { report: "Hello! **Markdown** here." }))];
    expect(rows[1].kind.type).toBe("report");
    expect(reportPresentation(rows, 1)).toBe("reply");
  });

  it("a reply after ordinary tool calls is still a reply", () => {
    const call = ev("tool_call", { id: "c1", name: "Bash", args: '{"command":"ls"}' });
    const rows = [
      indexed(ev("user_input", { text: "list" })),
      indexed(call, ev("tool_result", { id: "c1", name: "Bash", result: "a\nb" })),
      indexed(ev("session_idle", { report: "Two files." })),
    ];
    expect(reportPresentation(rows, 2)).toBe("reply");
  });

  it("finish / request_integration / report_blocked reports keep their card", () => {
    for (const [tool, want] of [
      ["finish", "finished"],
      ["request_integration", "finished"],
      ["report_blocked", "blocked"],
    ] as const) {
      const call = ev("tool_call", { id: `x${seq}`, name: tool, args: '{"report":"done"}' });
      const rows = [
        indexed(ev("model_turn", { text: "Wrapping up." })),
        indexed(call),
        indexed(ev("thinking", { text: "…" })),
        indexed(ev("subagent_finished", { role: "implementer" }, "implementer")),
        indexed(ev("session_idle", { report: "## Summary\n- did things" })),
      ];
      expect(reportPresentation(rows, rows.length - 1), tool).toBe(want);
    }
  });

  it("an unloaded predecessor defaults to a reply", () => {
    const rows = [indexed(ev("session_idle", { report: "x" }))];
    expect(reportPresentation(rows, 0)).toBe("reply");
  });
});
