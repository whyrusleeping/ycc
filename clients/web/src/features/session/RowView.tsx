// One transcript row. Model and user turns are prominent; tool, reasoning,
// review, and system detail fold; a question and its answer render as one
// exchange. Every payload renders as text (React escapes it) — never as HTML.
import { memo, type ReactNode } from "react";
import type { TranscriptRow } from "./projection";
import type { SessionController } from "./controller";
import { useInspector } from "../inspector/inspector";

function actorLabel(actor: string): string {
  if (!actor || actor === "coordinator") return "";
  return actor;
}

export function rowTitle(row: TranscriptRow): string {
  const k = row.kind;
  switch (k.type) {
    case "user":
      return "You";
    case "model":
    case "liveTail":
      return actorLabel(row.actor) || "Agent";
    case "report":
      return "Final report";
    case "thinking":
      return "Reasoning";
    case "tool":
      return `Tool · ${k.name}`;
    case "question":
      return "Question";
    case "assumption":
      return "Assumed answer";
    case "system":
      return k.text;
    case "commit":
      return k.text;
    case "review":
      return k.text;
  }
}

/** The row's full textual content (inspector / expanded body). */
export function RowBody({ row, full = false }: { row: TranscriptRow; full?: boolean }) {
  const k = row.kind;
  switch (k.type) {
    case "user":
      return (
        <div className="text">
          {k.text}
          {k.pictures.length > 0 && (
            <div className="pictures">
              {k.pictures.map((p, i) => (
                <span key={i} className="tag">
                  🖼 {p.filename || p.mediaType || "image"}
                </span>
              ))}
            </div>
          )}
        </div>
      );
    case "model":
    case "report":
    case "thinking":
    case "liveTail":
      return <div className="text">{k.text}</div>;
    case "tool":
      return (
        <div className="tool-body">
          {k.args && (
            <>
              <div className="label">Arguments</div>
              <pre className={full ? "" : "clamp"}>{prettyJson(k.args)}</pre>
            </>
          )}
          <div className="label">{k.status === "running" ? "Running…" : k.status === "error" ? "Error" : "Result"}</div>
          {k.output && <pre className={full ? "" : "clamp"}>{k.output}</pre>}
        </div>
      );
    case "question":
      return <QuestionBody prompt={k.prompt} options={k.options} answer={k.answer} />;
    case "assumption":
      return (
        <div>
          {k.questions.map((q, i) => (
            <QuestionBody key={i} prompt={q.prompt} options={q.options} answer={null} />
          ))}
          <div className="answer">
            <span className="label">Assumed:</span> {k.response ?? "(pending)"}
          </div>
        </div>
      );
    case "review":
      return <div className="text">{k.summary || k.text}</div>;
    case "system":
    case "commit":
      return <div className="text">{k.text}</div>;
  }
}

function QuestionBody({ prompt, options, answer }: { prompt: string; options: string[]; answer: string | null }) {
  return (
    <div className="question-body">
      <div className="text">{prompt}</div>
      {options.length > 0 && (
        <ul className="options">
          {options.map((o, i) => (
            <li key={i} className={answer === o ? "chosen" : ""}>
              {o}
            </li>
          ))}
        </ul>
      )}
      {answer !== null && (
        <div className="answer">
          <span className="label">Answer:</span> {answer}
        </div>
      )}
    </div>
  );
}

function prettyJson(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

function toolSummary(args: string): string {
  try {
    const v: unknown = JSON.parse(args);
    if (v && typeof v === "object") {
      for (const key of ["command", "file_path", "path", "pattern", "query", "url", "description"]) {
        const x = (v as Record<string, unknown>)[key];
        if (typeof x === "string" && x) return x.split("\n")[0].slice(0, 160);
      }
    }
  } catch {
    // not JSON
  }
  return args.split("\n")[0].slice(0, 160);
}

interface RowViewProps {
  row: TranscriptRow;
  controller: SessionController;
  loadingDetail: boolean;
}

export const RowView = memo(function RowView({ row, controller, loadingDetail }: RowViewProps) {
  const inspector = useInspector();
  const openInInspector = () =>
    inspector.open({ kind: "row", project: controller.project, sessionId: controller.sessionId, rowId: row.id });
  const k = row.kind;
  const actor = actorLabel(row.actor);

  switch (k.type) {
    case "user":
      return (
        <div className="row turn user">
          <div className="turn-head">
            <span className="who">You</span>
            {row.userInputStatus === "queued" && <span className="tag">queued</span>}
          </div>
          <RowBody row={row} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "model":
    case "liveTail":
      return (
        <div className={`row turn agent${k.type === "liveTail" ? " streaming" : ""}`}>
          <div className="turn-head">
            <span className="who">{actor || "Agent"}</span>
            {k.type === "liveTail" && <span className="tag live">streaming</span>}
          </div>
          <RowBody row={row} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "report":
      return (
        <div className="row turn report">
          <div className="turn-head">
            <span className="who">Final report</span>
          </div>
          <RowBody row={row} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "question":
      return (
        <div className={`row question${k.answer === null ? " open" : ""}`}>
          <div className="turn-head">
            <span className="who">Question{actor ? ` · ${actor}` : ""}</span>
            {k.answer === null && <span className="tag warn">waiting for an answer</span>}
          </div>
          <RowBody row={row} />
        </div>
      );
    case "system":
      return (
        <div className="row system">
          {actor && <span className="actor">{actor}</span>}
          <span className="text">{k.text}</span>
        </div>
      );
    case "commit":
      return (
        <div className="row system commit">
          <span className="text">{k.text}</span>
          {k.sha && (
            <button
              type="button"
              className="link"
              onClick={() => inspector.open({ kind: "commit", project: controller.project, sha: k.sha })}
            >
              View diff
            </button>
          )}
        </div>
      );
    default: {
      // Folded detail: reasoning, tools, reviews, assumptions.
      let summary: ReactNode;
      let cls = "";
      if (k.type === "thinking") {
        summary = <span className="sum-title">Reasoning</span>;
        cls = "thinking";
      } else if (k.type === "tool") {
        summary = (
          <>
            <span className="sum-title">{k.name}</span>
            <span className="sum-text">{toolSummary(k.args)}</span>
            <span className={`tag tool-${k.status}`}>{k.status}</span>
          </>
        );
        cls = "tool";
      } else if (k.type === "review") {
        summary = (
          <>
            <span className={`tag verdict-${k.verdict}`}>{k.verdict.toUpperCase()}</span>
            <span className="sum-text">{k.text}</span>
          </>
        );
        cls = "review";
      } else if (k.type === "assumption") {
        summary = (
          <>
            <span className="sum-title">Assumed</span>
            <span className="sum-text">{k.questions[0]?.prompt ?? ""}</span>
          </>
        );
        cls = "assumption";
      }
      return (
        <details
          className={`row fold ${cls}`}
          onToggle={(e) => {
            if ((e.currentTarget as HTMLDetailsElement).open && row.detailAvailable) void controller.loadDetail(row.id);
          }}
        >
          <summary>
            {actor && <span className="actor">{actor}</span>}
            {summary}
          </summary>
          <div className="fold-body">
            <RowBody row={row} />
            {loadingDetail && <p className="muted">Loading full detail…</p>}
            <div className="row-actions">
              <button type="button" className="link" onClick={openInInspector}>
                Open in inspector
              </button>
              {k.type === "review" && (
                <button
                  type="button"
                  className="link"
                  onClick={() =>
                    inspector.open({
                      kind: "workingChanges",
                      project: controller.project,
                      sessionId: controller.sessionId,
                      taskId: k.task,
                      knownSnapshotId: k.reviewedSnapshot,
                    })
                  }
                >
                  View working changes
                </button>
              )}
            </div>
          </div>
        </details>
      );
    }
  }
});

function DetailNote({
  row,
  controller,
  loading,
}: {
  row: TranscriptRow;
  controller: SessionController;
  loading: boolean;
}) {
  if (!row.detailAvailable) return null;
  return (
    <div className="detail-note">
      <span className="muted">Abbreviated.</span>{" "}
      <button type="button" className="link" disabled={loading} onClick={() => void controller.loadDetail(row.id)}>
        {loading ? "Loading…" : "Show full text"}
      </button>
    </div>
  );
}
