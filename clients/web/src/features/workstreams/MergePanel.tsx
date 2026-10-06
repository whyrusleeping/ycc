// The merge panel in the inspector: PreviewMerge's integrated diff (or the
// conflicted paths), then an explicit "Accept & merge" (MergeWorkstream with
// accept) — the diff is always shown before the base moves.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { MergeWorkstreamResponse, WorkstreamInfo } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useWorkstreams } from "../../api/queries";
import { DiffView } from "../code/CodeBlock";
import { CopyButton } from "../../ui/CopyButton";
import { toast } from "../../ui/toast";
import { useInspector } from "../inspector/inspector";
import { branchLabel, commitSummary, isMergeable, workstreamStatus } from "./model";

export function MergePanel({ project, workstreamId }: { project: string; workstreamId: string }) {
  const qc = useQueryClient();
  const inspector = useInspector();
  const [merging, setMerging] = useState(false);
  const [result, setResult] = useState<MergeWorkstreamResponse | null>(null);
  // The row as the project's workstreams list sees it (shared, polled cache).
  const list = useWorkstreams(project);
  const ws = list.data?.find((w) => w.id === workstreamId);
  const status = ws ? workstreamStatus(ws) : "unknown";
  const preview = useQuery({
    queryKey: ["mergePreview", workstreamId],
    queryFn: ({ signal }) => client.previewMerge({ workstreamId }, { signal }),
    enabled: result === null,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const accept = async () => {
    if (merging) return;
    setMerging(true);
    try {
      const r = await client.mergeWorkstream({ workstreamId, accept: true });
      setResult(r);
      if (r.merged) {
        qc.setQueryData<WorkstreamInfo[]>(queryKeys.workstreams(project), (rows) =>
          rows?.map((w) => (w.id === workstreamId ? { ...w, status: "merged", integrationState: "" } : w)),
        );
        toast(`Merged ${ws ? branchLabel(ws) : workstreamId}${r.commit ? ` as ${r.commit.slice(0, 12)}` : ""}.`, "info");
      } else if (r.conflicts.length) {
        toast(`Merge blocked: ${r.conflicts.length} conflicted ${r.conflicts.length === 1 ? "path" : "paths"}; the base is untouched.`);
      }
      void qc.invalidateQueries({ queryKey: queryKeys.workstreamsAll });
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      toast(`Couldn’t merge: ${errorMessage(err)}`);
    } finally {
      setMerging(false);
    }
  };

  const base = ws?.baseBranch || "the base branch";
  return (
    <div className="diff-view merge-panel">
      <div className="diff-meta">
        {ws && (
          <>
            <span className="mono">{branchLabel(ws)}</span>
            <span>→ {base}</span>
            <span>{commitSummary(ws)}</span>
          </>
        )}
        {result === null && (
          <button type="button" className="btn ghost small" onClick={() => void preview.refetch()} disabled={preview.isFetching}>
            {preview.isFetching ? "Checking…" : "Re-check"}
          </button>
        )}
      </div>
      {result ? (
        <MergeResult result={result} base={base} onCommit={(sha) => inspector.open({ kind: "commit", project, sha })} />
      ) : preview.isPending ? (
        <p className="muted">Trial-merging onto {base}…</p>
      ) : preview.isError ? (
        <p className="error">{errorMessage(preview.error, "Couldn’t preview the merge.")}</p>
      ) : preview.data.clean && !preview.data.diff.trim() ? (
        <div className="banner warn" role="note">
          Nothing to merge: the branch has no changes against {base}.
        </div>
      ) : preview.data.clean ? (
        <>
          <div className="merge-accept">
            <div>
              <strong>Merges cleanly onto {base}.</strong>
              <div className="muted small">Review the integrated diff, then accept to rebase the branch and fast-forward {base}.</div>
            </div>
            <button
              type="button"
              className="btn primary"
              disabled={merging || (ws !== undefined && !isMergeable(status))}
              onClick={() => void accept()}
            >
              {merging ? "Merging…" : "Accept & merge"}
            </button>
          </div>
          <DiffView diff={preview.data.diff} />
        </>
      ) : (
        <Conflicts paths={preview.data.conflicts} message={`This workstream conflicts with ${base}. Resolve it in the worktree (or retry integration) before merging; nothing was changed.`} />
      )}
    </div>
  );
}

function MergeResult({
  result,
  base,
  onCommit,
}: {
  result: MergeWorkstreamResponse;
  base: string;
  onCommit: (sha: string) => void;
}) {
  if (result.merged) {
    return (
      <div className="banner ok merge-done" role="status">
        <strong>Merged onto {base}.</strong>
        {result.commit && (
          <div className="small">
            Now at{" "}
            <button type="button" className="link mono" onClick={() => onCommit(result.commit)} title="Show the commit">
              {result.commit.slice(0, 12)}
            </button>{" "}
            <CopyButton text={result.commit} title="Copy commit sha" />
          </div>
        )}
        <div className="muted small">The worktree and branch were cleaned up; the session transcript is kept.</div>
      </div>
    );
  }
  if (result.conflicts.length) {
    return <Conflicts paths={result.conflicts} message={`The merge conflicted; ${base} is untouched and the worktree kept.`} />;
  }
  if (result.needsAccept) {
    return (
      <>
        <p className="muted">The daemon still wants a review of this diff.</p>
        <DiffView diff={result.diff} />
      </>
    );
  }
  return <p className="muted">Not merged.</p>;
}

function Conflicts({ paths, message }: { paths: string[]; message: string }) {
  return (
    <div className="merge-conflicts">
      <div className="banner warn" role="alert">
        {message}
      </div>
      <div className="label">Conflicted files</div>
      {paths.length === 0 ? (
        <p className="muted">No paths reported.</p>
      ) : (
        <ul className="path-list">
          {paths.map((p) => (
            <li key={p} className="mono">
              {p}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
