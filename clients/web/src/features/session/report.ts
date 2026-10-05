// How a session_idle report row presents. The daemon folds a plain final
// reply into the idle report: when the report repeats the last model turn it
// deletes that model row (internal/sessionview), so in a chat — and for any
// plain-text answer in other modes — the report row IS the agent's reply and
// reads as a normal agent turn. A genuine report comes from a control tool
// (finish, request_integration, report_blocked) whose call row immediately
// precedes it; that keeps the distinct "Finished"/"Blocked" card, as on iOS.
import { isSubagentActor, type TranscriptRow } from "./projection";

export type ReportPresentation = "reply" | "finished" | "blocked";

const FINISH_TOOLS = new Set(["finish", "request_integration"]);
const BLOCKED_TOOLS = new Set(["report_blocked"]);

export function reportPresentation(rows: readonly TranscriptRow[], index: number): ReportPresentation {
  const row = rows[index];
  if (!row || row.kind.type !== "report") return "reply";
  for (let j = index - 1; j >= 0; j--) {
    const prev = rows[j];
    // Subagent activity, reasoning, and lifecycle notes can interleave
    // without changing what produced the report.
    if (isSubagentActor(prev.actor)) continue;
    const k = prev.kind;
    if (k.type === "thinking" || k.type === "system" || k.type === "commit") continue;
    if (k.type === "tool") {
      if (BLOCKED_TOOLS.has(k.name)) return "blocked";
      if (FINISH_TOOLS.has(k.name)) return "finished";
    }
    return "reply";
  }
  return "reply";
}
