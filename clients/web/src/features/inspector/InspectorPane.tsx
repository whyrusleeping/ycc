// Inspector pane contents for each InspectorItem kind.
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { client, errorMessage } from "../../api/client";
import { useSessionController, useSessionSnapshot } from "../session/useSession";
import { RowBody, rowTitle } from "../session/RowView";
import { InspectorResizer, useInspector, type InspectorItem } from "./inspector";
import { PictureDetail } from "../attachments/SessionPicture";
import { DiffView } from "../code/CodeBlock";
import { FileRef } from "../files/FileRef";
import { CopyButton } from "../../ui/CopyButton";
import { reportPresentation } from "../session/report";
import { SessionSettingsPanel } from "../session/SessionSettings";
import { TaskDetailView } from "../backlog/TaskDetail";

export function InspectorPane() {
  const { item, close } = useInspector();
  if (!item) return null;
  return (
    <aside className="inspector" aria-label="Inspector">
      <InspectorResizer />
      <div className="inspector-inner">
        <header className="inspector-head">
          <span className="inspector-title">{inspectorTitle(item)}</span>
          <button type="button" className="btn ghost small" onClick={close} aria-label="Close inspector">
            ×
          </button>
        </header>
        <div className="inspector-body">
          {item.kind === "row" && <RowInspector item={item} />}
          {item.kind === "workingChanges" && <WorkingChanges item={item} />}
          {item.kind === "commit" && <CommitDiff item={item} />}
          {item.kind === "picture" && (
            <PictureDetail
              key={item.attachmentId}
              session={item}
              picture={{ attachmentId: item.attachmentId, filename: item.filename, mediaType: item.mediaType }}
            />
          )}
          {item.kind === "task" && (
            <TaskDetailView key={`${item.project}\u0000${item.taskId}`} project={item.project} taskId={item.taskId} variant="inspector" />
          )}
          {item.kind === "sessionSettings" && (
            <SessionSettingsPanel key={`${item.project}\u0000${item.sessionId}`} project={item.project} sessionId={item.sessionId} />
          )}
        </div>
      </div>
    </aside>
  );
}

function inspectorTitle(item: InspectorItem): string {
  switch (item.kind) {
    case "row":
      return "Detail";
    case "workingChanges":
      return item.taskId ? `Working changes · task ${item.taskId}` : "Working changes";
    case "commit":
      return `Commit ${item.sha.slice(0, 12)}`;
    case "picture":
      return item.filename || "Picture";
    case "sessionSettings":
      return "Session settings";
    case "task":
      return `Task ${item.taskId}`;
  }
}

function RowInspector({ item }: { item: Extract<InspectorItem, { kind: "row" }> }) {
  const controller = useSessionController(item.project, item.sessionId);
  const snap = useSessionSnapshot(controller);
  const index = snap.rows.findIndex((r) => r.id === item.rowId);
  const row = index >= 0 ? snap.rows[index] : undefined;
  const needsDetail = row?.detailAvailable ?? false;
  useEffect(() => {
    // Opening a row in the inspector is an explicit request for all of it.
    if (needsDetail) void controller.loadDetail(item.rowId);
  }, [controller, item.rowId, needsDetail]);
  if (!row) return <p className="muted">This row is no longer loaded.</p>;
  return (
    <div className="inspector-row">
      <div className="inspector-subtitle">{rowTitle(row, reportPresentation(snap.rows, index))}</div>
      {snap.loadingDetail.has(row.id) && <p className="muted">Loading full detail…</p>}
      <RowBody row={row} full session={item} />
    </div>
  );
}

interface ReviewVerdict {
  rowId: string;
  verdict: string;
  heading: string;
  snapshot: string;
}

/** Loaded review rows of a session, newest first. */
function useSessionReviews(project: string, sessionId: string): ReviewVerdict[] {
  const controller = useSessionController(project, sessionId);
  const snap = useSessionSnapshot(controller);
  const out: ReviewVerdict[] = [];
  for (const r of snap.rows) {
    if (r.kind.type === "review") {
      out.push({ rowId: r.id, verdict: r.kind.verdict, heading: r.kind.text, snapshot: r.kind.reviewedSnapshot });
    }
  }
  return out.reverse();
}

function WorkingChanges({ item }: { item: Extract<InspectorItem, { kind: "workingChanges" }> }) {
  const q = useQuery({
    queryKey: ["workingChanges", item.project, item.sessionId, item.taskId ?? "", item.knownSnapshotId ?? ""],
    queryFn: ({ signal }) =>
      client.getWorkingChanges(
        {
          project: item.project,
          sessionId: item.sessionId,
          taskId: item.taskId ?? "",
          knownSnapshotId: item.knownSnapshotId ?? "",
        },
        { signal },
      ),
    refetchOnWindowFocus: false,
  });
  const reviews = useSessionReviews(item.project, item.sessionId);
  if (q.isPending) return <p className="muted">Loading…</p>;
  if (q.isError) return <p className="error">{errorMessage(q.error)}</p>;
  const r = q.data;
  // Verdicts that covered a snapshot: the one this panel was opened from
  // first, then every other loaded review of the session.
  const linked = item.knownSnapshotId
    ? [
        ...reviews.filter((v) => v.snapshot === item.knownSnapshotId).slice(0, 1),
        ...reviews.filter((v) => v.snapshot !== item.knownSnapshotId),
      ]
    : reviews;
  const known = item.knownSnapshotId;
  return (
    <div className="diff-view">
      {known && (
        <div className={`review-link ${r.changedSinceKnown ? "changed" : "same"}`}>
          {item.verdict && <span className={`tag verdict-${item.verdict}`}>{item.verdict.toUpperCase()}</span>}{" "}
          <strong>{r.changedSinceKnown ? "Changed since review" : "Unchanged since review"}</strong>
          <div className="muted small">
            {item.reviewHeading ? `${item.reviewHeading}. ` : ""}
            {r.changedSinceKnown
              ? `The verdict covered snapshot ${known.slice(0, 12)}; the working tree has changed since.`
              : `The working tree still matches reviewed snapshot ${known.slice(0, 12)}.`}
          </div>
        </div>
      )}
      <div className="diff-meta">
        {r.scope && <span>{r.scope}</span>}
        {r.snapshotId && (
          <span>
            snapshot <span className="mono">{r.snapshotId.slice(0, 12)}</span>{" "}
            <CopyButton text={r.snapshotId} title="Copy snapshot id" />
          </span>
        )}
        {r.baseCommit && (
          <span>
            base <span className="mono">{r.baseCommit.slice(0, 12)}</span>
          </span>
        )}
        <span>
          <span className="add">+{String(r.additions)}</span> <span className="del">−{String(r.deletions)}</span> in{" "}
          {r.pathsTotal} {r.pathsTotal === 1 ? "path" : "paths"}
        </span>
        {r.excludedDirtyPaths > 0 && <span>{r.excludedDirtyPaths} unrelated dirty paths excluded</span>}
        {r.truncated && <span className="tag warn">truncated</span>}
        <button type="button" className="btn ghost small" onClick={() => void q.refetch()} disabled={q.isFetching}>
          {q.isFetching ? "Refreshing…" : "Refresh"}
        </button>
      </div>
      {linked.length > 0 && (
        <div className="review-verdicts">
          <div className="label">Review verdicts in this session</div>
          <ul>
            {linked.map((v) => {
              const current = v.snapshot !== "" && v.snapshot === r.snapshotId;
              return (
                <li key={v.rowId}>
                  <span className={`tag verdict-${v.verdict}`}>{v.verdict.toUpperCase()}</span>{" "}
                  <span className="review-heading">{v.heading}</span>{" "}
                  {v.snapshot ? (
                    <span className={current ? "tag verdict-accept" : "tag"}>
                      {current ? "covers the current changes" : `snapshot ${v.snapshot.slice(0, 12)}`}
                    </span>
                  ) : (
                    <span className="tag">no snapshot</span>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {r.paths.length > 0 && (
        <ul className="path-list">
          {r.paths.map((p) => (
            <li key={p}>
              <FileRef path={p} />
            </li>
          ))}
          {r.pathsTotal > r.paths.length && <li className="muted">… {r.pathsTotal - r.paths.length} more</li>}
        </ul>
      )}
      <DiffView diff={r.diff} truncated={Number(r.diffBytes) > new TextEncoder().encode(r.diff).length} />
    </div>
  );
}

function CommitDiff({ item }: { item: Extract<InspectorItem, { kind: "commit" }> }) {
  const q = useQuery({
    queryKey: ["commitDiff", item.project, item.sha],
    queryFn: ({ signal }) => client.getCommitDiff({ project: item.project, sha: item.sha }, { signal }),
    staleTime: Infinity,
  });
  return (
    <div className="diff-view">
      <div className="diff-meta">
        <span>
          commit <span className="mono">{item.sha}</span> <CopyButton text={item.sha} title="Copy commit sha" />
        </span>
      </div>
      {q.isPending && <p className="muted">Loading…</p>}
      {q.isError && <p className="error">{errorMessage(q.error)}</p>}
      {q.isSuccess && <DiffView diff={q.data.diff} truncated={q.data.truncated} />}
    </div>
  );
}
