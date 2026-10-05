// Inspector pane contents for each InspectorItem kind.
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { client, errorMessage } from "../../api/client";
import { useSessionController, useSessionSnapshot } from "../session/useSession";
import { RowBody, rowTitle } from "../session/RowView";
import { InspectorResizer, useInspector, type InspectorItem } from "./inspector";
import { PictureDetail } from "../attachments/SessionPicture";
import { SessionSettingsPanel } from "../session/SessionSettings";

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
  }
}

function RowInspector({ item }: { item: Extract<InspectorItem, { kind: "row" }> }) {
  const controller = useSessionController(item.project, item.sessionId);
  const snap = useSessionSnapshot(controller);
  const row = snap.rows.find((r) => r.id === item.rowId);
  const needsDetail = row?.detailAvailable ?? false;
  useEffect(() => {
    // Opening a row in the inspector is an explicit request for all of it.
    if (needsDetail) void controller.loadDetail(item.rowId);
  }, [controller, item.rowId, needsDetail]);
  if (!row) return <p className="muted">This row is no longer loaded.</p>;
  return (
    <div className="inspector-row">
      <div className="inspector-subtitle">{rowTitle(row)}</div>
      {snap.loadingDetail.has(row.id) && <p className="muted">Loading full detail…</p>}
      <RowBody row={row} full session={item} />
    </div>
  );
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
  if (q.isPending) return <p className="muted">Loading…</p>;
  if (q.isError) return <p className="error">{errorMessage(q.error)}</p>;
  const r = q.data;
  return (
    <div className="diff-view">
      <div className="diff-meta">
        {item.knownSnapshotId && (
          <span className={r.changedSinceKnown ? "tag warn" : "tag"}>
            {r.changedSinceKnown ? "Changed since review" : "Unchanged since review"}
          </span>
        )}
        <span>{r.scope}</span>
        <span>snapshot {r.snapshotId.slice(0, 12)}</span>
        <span>
          +{String(r.additions)} −{String(r.deletions)} in {r.pathsTotal} {r.pathsTotal === 1 ? "path" : "paths"}
        </span>
        {r.excludedDirtyPaths > 0 && <span>{r.excludedDirtyPaths} unrelated dirty paths excluded</span>}
        {r.truncated && <span className="tag warn">truncated</span>}
        <button type="button" className="btn ghost small" onClick={() => void q.refetch()} disabled={q.isFetching}>
          Refresh
        </button>
      </div>
      {r.paths.length > 0 && (
        <ul className="path-list">
          {r.paths.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      <pre className="diff">{r.diff || "No changes."}</pre>
    </div>
  );
}

function CommitDiff({ item }: { item: Extract<InspectorItem, { kind: "commit" }> }) {
  const q = useQuery({
    queryKey: ["commitDiff", item.project, item.sha],
    queryFn: ({ signal }) => client.getCommitDiff({ project: item.project, sha: item.sha }, { signal }),
    staleTime: Infinity,
  });
  if (q.isPending) return <p className="muted">Loading…</p>;
  if (q.isError) return <p className="error">{errorMessage(q.error)}</p>;
  return (
    <div className="diff-view">
      {q.data.truncated && <p className="tag warn">Diff truncated</p>}
      <pre className="diff">{q.data.diff}</pre>
    </div>
  );
}
