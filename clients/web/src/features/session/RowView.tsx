// One transcript row. Model and user turns are prominent; tool, reasoning,
// review, and system detail fold; a question and its answer render as one
// exchange. Model text renders as markdown (raw HTML disabled, safe link
// schemes only); every other payload renders as text — never as HTML.
import { memo, useEffect, useState, type ReactNode } from "react";
import type { Picture, TranscriptRow } from "./projection";
import type { SessionController } from "./controller";
import { useInspector } from "../inspector/inspector";
import { PictureThumb, type SessionRef } from "../attachments/SessionPicture";
import { Markdown } from "../markdown/Markdown";
import { MetaTags, ToolBody } from "./ToolBody";
import { oneLine, toolPreview } from "./toolPreview";
import { FileRef } from "../files/FileRef";
import type { ReportPresentation } from "./report";
import { useSmoothText } from "./useSmoothText";

function actorLabel(actor: string): string {
  if (!actor || actor === "coordinator") return "";
  return actor;
}

export function rowTitle(row: TranscriptRow, report: ReportPresentation = "finished"): string {
  const k = row.kind;
  switch (k.type) {
    case "user":
      return "You";
    case "model":
    case "liveTail":
      return actorLabel(row.actor) || "Agent";
    case "report":
      return report === "reply" ? actorLabel(row.actor) || "Agent" : report === "blocked" ? "Blocked" : "Finished";
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

/** The row's full content (inspector / expanded body). */
export function RowBody({ row, full = false, session }: { row: TranscriptRow; full?: boolean; session: SessionRef }) {
  const k = row.kind;
  switch (k.type) {
    case "user":
      return (
        <div>
          {k.text && <div className="text">{k.text}</div>}
          {k.pictures.length > 0 && <UserPictures session={session} pictures={k.pictures} />}
        </div>
      );
    case "model":
    case "report":
      return <Markdown text={k.text} />;
    case "liveTail":
      return <LiveTail text={k.text} />;
    case "thinking":
      return <div className="text thinking-text">{k.text}</div>;
    case "tool":
      return <ToolBody name={k.name} args={k.args} output={k.output} status={k.status} full={full} />;
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
      return k.summary ? <Markdown text={k.summary} /> : <div className="text">{k.text}</div>;
    case "system":
    case "commit":
      return <div className="text">{k.text}</div>;
  }
}

function LiveTail({ text }: { text: string }) {
  const smoothed = useSmoothText(text);
  return <Markdown text={smoothed} />;
}

function UserPictures({ session, pictures }: { session: SessionRef; pictures: Picture[] }) {
  const inspector = useInspector();
  return (
    <div className="pictures">
      {pictures.map((p, i) => (
        <PictureThumb
          key={`${p.attachmentId}-${i}`}
          session={session}
          picture={p}
          onOpen={() =>
            inspector.open({
              kind: "picture",
              project: session.project,
              sessionId: session.sessionId,
              attachmentId: p.attachmentId,
              filename: p.filename,
              mediaType: p.mediaType,
            })
          }
        />
      ))}
    </div>
  );
}

function QuestionBody({ prompt, options, answer }: { prompt: string; options: string[]; answer: string | null }) {
  return (
    <div className="question-body">
      <Markdown text={prompt} />
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

export type SearchMark = "match" | "current" | null;

interface RowViewProps {
  row: TranscriptRow;
  controller: SessionController;
  loadingDetail: boolean;
  /** For report rows: reply (an agent turn) or a genuine finished/blocked report. */
  report?: ReportPresentation;
  search?: SearchMark;
  /** System notices: how many identical consecutive notices this row stands for. */
  repeat?: number;
}

export const RowView = memo(function RowView({
  row,
  controller,
  loadingDetail,
  report = "finished",
  search = null,
  repeat = 1,
}: RowViewProps) {
  const inspector = useInspector();
  const openInInspector = () =>
    inspector.open({ kind: "row", project: controller.project, sessionId: controller.sessionId, rowId: row.id });
  const k = row.kind;
  const actor = actorLabel(row.actor);
  const searchCls = search ? ` search-${search}` : "";
  // Folded rows open when search lands on them (and stay open after).
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (search === "current") setOpen(true);
  }, [search]);

  switch (k.type) {
    case "user":
      return (
        <div className={`row turn user${searchCls}`} data-row-id={row.id}>
          <div className="turn-head">
            <span className="who">You</span>
            {row.userInputStatus === "queued" && <span className="tag">queued</span>}
          </div>
          <RowBody row={row} session={controller} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "model":
    case "liveTail":
      return (
        <div className={`row turn agent${k.type === "liveTail" ? " streaming" : ""}${searchCls}`} data-row-id={row.id}>
          <div className="turn-head">
            <span className="who">{actor || "Agent"}</span>
            {k.type === "liveTail" && <span className="tag live">streaming</span>}
          </div>
          <RowBody row={row} session={controller} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "report":
      if (report === "reply") {
        return (
          <div className={`row turn agent${searchCls}`} data-row-id={row.id}>
            <div className="turn-head">
              <span className="who">{actor || "Agent"}</span>
            </div>
            <RowBody row={row} session={controller} />
            <DetailNote row={row} controller={controller} loading={loadingDetail} />
          </div>
        );
      }
      return (
        <div className={`row turn report report-${report}${searchCls}`} data-row-id={row.id}>
          <div className="turn-head">
            <span className="who">{report === "blocked" ? "⚑ Blocked" : "✓ Finished"}</span>
            {actor && <span className="actor">{actor}</span>}
          </div>
          <RowBody row={row} session={controller} />
          <DetailNote row={row} controller={controller} loading={loadingDetail} />
        </div>
      );
    case "question":
      return (
        <div className={`row question${k.answer === null ? " open" : ""}${searchCls}`} data-row-id={row.id}>
          <div className="turn-head">
            <span className="who">Question{actor ? ` · ${actor}` : ""}</span>
            {k.answer === null && <span className="tag warn">waiting for an answer</span>}
          </div>
          <RowBody row={row} session={controller} />
        </div>
      );
    case "system":
      return (
        <div className={`row system${searchCls}`} data-row-id={row.id}>
          {actor && <span className="actor">{actor}</span>}
          <span className="text">{k.text}</span>
          {repeat > 1 && (
            <span className="repeat" title={`${repeat} identical notices in a row`}>
              ×{repeat}
            </span>
          )}
        </div>
      );
    case "commit":
      return (
        <div className={`row system commit${searchCls}`} data-row-id={row.id}>
          <span className="commit-glyph" aria-hidden>
            ⎇
          </span>
          {k.sha ? (
            <button
              type="button"
              className="link commit-link"
              title="View this commit's diff in the inspector"
              onClick={() => inspector.open({ kind: "commit", project: controller.project, sha: k.sha })}
            >
              {k.text}
            </button>
          ) : (
            <span className="text">{k.text}</span>
          )}
        </div>
      );
    default: {
      // Folded detail: reasoning, tools, reviews, assumptions.
      let summary: ReactNode;
      let cls = "";
      if (k.type === "thinking") {
        summary = (
          <>
            <span className="sum-title">Reasoning</span>
            <span className="sum-text">{oneLine(k.text, 140)}</span>
          </>
        );
        cls = "thinking";
      } else if (k.type === "tool") {
        const p = toolPreview(k.name, k.args, k.output, k.status);
        summary = (
          <>
            <span className="tool-glyph" aria-hidden>
              {p.glyph}
            </span>
            <span className="sum-title">{k.name}</span>
            {p.path ? (
              <span className="sum-path">
                <FileRef path={p.path} />
              </span>
            ) : (
              <span className={`sum-text${k.name === "Bash" ? " mono" : ""}`}>{p.summary}</span>
            )}
            <span className="sum-meta">
              <MetaTags meta={p.meta} />
              {k.status === "running" && <span className="tag live">running</span>}
            </span>
          </>
        );
        cls = `tool tool-${k.status}`;
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
          className={`row fold ${cls}${searchCls}`}
          data-row-id={row.id}
          open={open}
          onToggle={(e) => {
            const isOpen = (e.currentTarget as HTMLDetailsElement).open;
            setOpen(isOpen);
            if (isOpen && row.detailAvailable) void controller.loadDetail(row.id);
          }}
        >
          <summary>
            {actor && <span className="actor">{actor}</span>}
            {summary}
          </summary>
          {open && (
            <div className="fold-body">
              <RowBody row={row} session={controller} />
              {loadingDetail && <p className="muted">Loading full detail…</p>}
              {row.detailAvailable && !loadingDetail && (
                <p className="muted small">Abbreviated — opening loads the full detail.</p>
              )}
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
                        verdict: k.verdict,
                        reviewHeading: k.text,
                      })
                    }
                  >
                    View working changes
                  </button>
                )}
              </div>
            </div>
          )}
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
