// The History surface (`/p/<project>/history[?branch=<name>]`): one local
// branch's git log, newest first, paged with ListCommits. The picker lists
// local branches (workstream branches grouped and marked); commits a ycc
// session recorded link to that session and its task; a row opens the
// commit's diff in the inspector. An absent `branch` is the checked-out HEAD.
import { keepPreviousData, useInfiniteQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import type { CommitSummary } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { Icon } from "../../ui/icons";
import { useInspector } from "../inspector/inspector";
import { TaskLink } from "../backlog/parts";
import { relativeTime } from "../sessions/feed";
import {
  COMMITS_PAGE,
  branchOptionLabel,
  branchParam,
  commitStats,
  commitTimeTitle,
  flattenCommits,
  groupBranches,
  isMerge,
  pickerValue,
  shortSha,
} from "./model";

function codeOf(err: unknown): Code | null {
  return err ? ConnectError.from(err).code : null;
}

export function HistoryPage({ project }: { project: string }) {
  const [params, setParams] = useSearchParams();
  const branch = params.get("branch") ?? "";
  const inspector = useInspector();
  const q = useInfiniteQuery({
    queryKey: queryKeys.commits(project, branch),
    initialPageParam: "",
    // The cursor pins the branch tip, so it only ever pairs with this branch.
    queryFn: ({ pageParam, signal }) => client.listCommits({ project, branch, limit: COMMITS_PAGE, cursor: pageParam }, { signal }),
    getNextPageParam: (last) => last.nextCursor || undefined,
    // Keep the picker (and the old list, dimmed) while another branch loads.
    placeholderData: keepPreviousData,
    staleTime: 30_000,
    retry: (count, err) => {
      const code = codeOf(err);
      if (code === Code.FailedPrecondition || code === Code.InvalidArgument || code === Code.Unauthenticated) return false;
      return count < 2;
    },
  });
  const pages = q.data?.pages;
  const first = pages?.[0];
  const commits = useMemo(() => flattenCommits(pages ?? []), [pages]);
  const branches = useMemo(() => first?.branches ?? [], [first]);
  const groups = useMemo(() => groupBranches(branches), [branches]);
  // While another branch loads, the old page's resolved branch is stale: HEAD is the current branch.
  const head = q.isPlaceholderData ? (branches.find((b) => b.current)?.name ?? "") : (first?.branch ?? "");
  const value = pickerValue(branch, head);
  const selected = branches.find((b) => b.name === value);
  const notGit = codeOf(q.error) === Code.FailedPrecondition && !q.data;
  const openSha = inspector.item?.kind === "commit" && inspector.item.project === project ? inspector.item.sha : null;

  const choose = (choice: string) => {
    const next = new URLSearchParams(params);
    const param = branchParam(choice, branches);
    if (param) next.set("branch", param);
    else next.delete("branch");
    track.action("history.branch", "click", { workstream: !!branches.find((b) => b.name === choice)?.workstreamId });
    setParams(next);
  };
  const openCommit = (c: CommitSummary) => {
    track.action("history.open_commit", "click");
    inspector.open({ kind: "commit", project, sha: c.sha });
  };

  return (
    <div className="page history-page">
      <header className="page-head">
        <h1>
          History {project && <span className="muted">· {project}</span>}
        </h1>
        <div className="page-actions">
          {branches.length > 0 && (
            <label className="chip-select history-branch">
              <Icon name="workstreams" size={14} />
              <select value={value} onChange={(e) => choose(e.target.value)} aria-label="Branch">
                {value === "" && <option value="">HEAD (detached)</option>}
                {value !== "" && !selected && <option value={value}>{value}</option>}
                {groups.local.map((b) => (
                  <option key={b.name} value={b.name}>
                    {branchOptionLabel(b)}
                  </option>
                ))}
                {groups.workstreams.length > 0 && (
                  <optgroup label="Workstreams">
                    {groups.workstreams.map((b) => (
                      <option key={b.name} value={b.name}>
                        {branchOptionLabel(b)}
                      </option>
                    ))}
                  </optgroup>
                )}
              </select>
            </label>
          )}
          {selected?.workstreamId && (
            <span className="history-branch-notes">
              <Link to={paths.workstreams(project)} className="tag" title={`Workstream ${selected.workstreamId}`} data-track="history.workstream">
                workstream
              </Link>
              {selected.taskId && <TaskLink project={project} id={selected.taskId} className="task-chip" />}
            </span>
          )}
          <button
            type="button"
            className="btn ghost small"
            onClick={() => void q.refetch()}
            disabled={q.isFetching}
            data-track="history.refresh"
            title="Refresh"
            aria-label="Refresh history"
          >
            <Icon name="refresh" size={14} />
          </button>
        </div>
      </header>
      {q.isPending ? (
        <p className="muted">Loading history…</p>
      ) : notGit ? (
        <div className="loop-empty">
          <h2>No git history</h2>
          <p className="muted">{project ? `${project} is` : "This project is"} not a git repository.</p>
        </div>
      ) : q.isError && !q.data ? (
        <div className="loop-empty">
          <p className="error">{errorMessage(q.error, "Couldn’t load the history.")}</p>
          {branch && (
            <p>
              <Link to={paths.history(project)} className="btn small" data-track="history.show_head">
                Show the checked-out branch
              </Link>
            </p>
          )}
        </div>
      ) : commits.length === 0 ? (
        <div className="loop-empty">
          <h2>No commits yet</h2>
          <p className="muted">{value ? `${value} has no commits.` : "The checked-out HEAD has no commits."}</p>
        </div>
      ) : (
        <>
          {q.isRefetchError && <p className="warn small">Couldn’t refresh: {errorMessage(q.error)}</p>}
          <ul className={`commit-list${q.isPlaceholderData ? " stale" : ""}`} aria-label={`Commits on ${value || "HEAD"}`} aria-busy={q.isPlaceholderData}>
            {commits.map((c) => (
              <CommitRow key={c.sha} project={project} commit={c} selected={openSha === c.sha} onOpen={() => openCommit(c)} />
            ))}
          </ul>
          {q.hasNextPage && (
            <div className="commit-more">
              <button
                type="button"
                className="btn ghost small"
                disabled={q.isFetchingNextPage}
                data-track="history.load_more"
                onClick={() => void q.fetchNextPage()}
              >
                {q.isFetchingNextPage ? "Loading…" : "Load older commits"}
              </button>
              {q.isFetchNextPageError && <span className="error small">{errorMessage(q.error)}</span>}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function CommitRow({ project, commit: c, selected, onOpen }: { project: string; commit: CommitSummary; selected: boolean; onOpen: () => void }) {
  const stats = commitStats(c);
  const when = c.authoredAt || c.committedAt;
  return (
    <li className={`commit-row${selected ? " selected" : ""}`} onClick={onOpen}>
      <div className="commit-main">
        <button
          type="button"
          className="commit-subject"
          title={c.subject}
          aria-current={selected || undefined}
          onClick={(e) => {
            e.stopPropagation();
            onOpen();
          }}
        >
          {c.subject || <span className="muted">(no subject)</span>}
        </button>
        {isMerge(c) && <span className="tag">merge</span>}
      </div>
      <div className="commit-meta muted small">
        <span className="mono commit-sha">{shortSha(c.sha)}</span>
        {c.authorName && <span title={c.authorEmail || undefined}>{c.authorName}</span>}
        {when && <span title={commitTimeTitle(c) || undefined}>{relativeTime(when)}</span>}
        {stats && (
          <span className="mono commit-stats">
            <span className="add">{stats.additions}</span> <span className="del">{stats.deletions}</span> <span>{stats.files}</span>
          </span>
        )}
        {(c.sessionId || c.taskId) && (
          <span className="commit-links" onClick={(e) => e.stopPropagation()}>
            {c.taskId && <TaskLink project={project} id={c.taskId} className="task-chip" />}
            {c.sessionId && (
              <Link
                to={paths.session(project, c.sessionId)}
                className="commit-session"
                title={`Made in session ${c.sessionTitle || c.sessionId}`}
                data-track="history.open_session"
              >
                {c.sessionTitle || c.sessionId}
              </Link>
            )}
          </span>
        )}
      </div>
    </li>
  );
}
