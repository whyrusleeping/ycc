// The session list: the daemon-wide Recent feed (or one project's sessions),
// needs-answer sessions pinned on top, then most recent first.
import { Link } from "react-router";
import { useEffect, useState } from "react";
import { useProjects, useSessionFeed, useSetFollowUp } from "../../api/queries";
import { errorMessage } from "../../api/client";
import { paths } from "../../app/paths";
import { lastViewedProject } from "../../app/memory";
import { hitKindLabel, hitPath, indexingNote, searchableQuery, searchProject } from "./messageHits";
import { Snippet, useMessageSearch } from "./messageSearch";
import { TaskPreviewLink } from "../backlog/parts";
import { useLoopSessionIds } from "../workloop/hooks";
import { useReadMarks } from "./unread";
import { filterSessions, isInboxSession, SESSION_FILTERS, type SessionFilter } from "./attention";
import { setSidebarFilter, useSidebarFilter } from "./listFilter";
import { Icon } from "../../ui/icons";
import type { SessionSearchHit, SessionSummary } from "../../gen/ycc/v1/ycc_pb";
import {
  compactAge,
  compactState,
  displayProject,
  displayTitle,
  formatSessionDuration,
  lifecycleLabel,
  lifecycleTone,
  metadataItems,
  needsAnswer,
  onlyFollowUp,
  relativeTime,
  sections,
  sessionDurationMs,
  taskChipLabels,
  taskIds,
  type FeedRow,
} from "./feed";

/** Re-render periodically so relative times stay fresh. */
function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}

export function SessionRowView({
  row,
  showProject,
  active,
  now,
  variant,
  loopOwned = false,
  unread = false,
  onMarkRead,
  onToggleFollowUp,
  followUpPending = false,
}: {
  row: FeedRow;
  showProject: boolean;
  active: boolean;
  now: number;
  variant: "sidebar" | "page";
  /** Started by a work loop (marked like iOS's "via loop"). */
  loopOwned?: boolean;
  /** Agent activity this browser hasn't shown yet. */
  unread?: boolean;
  onMarkRead?: () => void;
  onToggleFollowUp: () => void;
  followUpPending?: boolean;
}) {
  const s = row.session;
  if (variant === "sidebar") {
    return (
      <SidebarRow
        row={row}
        showProject={showProject}
        active={active}
        now={now}
        loopOwned={loopOwned}
        unread={unread}
        onMarkRead={onMarkRead}
        onToggleFollowUp={onToggleFollowUp}
        followUpPending={followUpPending}
      />
    );
  }
  const lifecycle = lifecycleLabel(s);
  const project = displayProject(row);
  const chips = taskChipLabels(s);
  const when = relativeTime(s.lastActivity || s.startedAt, now);
  const ids = taskIds(s);
  // The row is one stretched link (its ::after covers the row) so the focus
  // task chips can be links of their own without nesting anchors.
  return (
    <div className={`session-row variant-${variant}${active ? " active" : ""}${needsAnswer(s) ? " needs" : ""}${unread ? " unread" : ""}${lifecycleTone(s) === "running" ? " running" : ""}`}>
      <div className="session-row-title">
        {unread && (
          <button
            type="button"
            className="unread-dot"
            title="New agent activity — click to mark read"
            aria-label={`Unread: mark ${displayTitle(s)} read`}
            data-track="sessions.mark_read"
            onClick={onMarkRead}
          />
        )}
        <FollowUpFlag s={s} pending={followUpPending} onToggle={onToggleFollowUp} />
        <Link
          to={paths.session(row.project, s.sessionId)}
          className="session-row-link title-text"
          title={displayTitle(s)}
          data-track="sessions.open"
          aria-current={active ? "page" : undefined}
        >
          {displayTitle(s)}
        </Link>
      </div>
      <div className="session-row-meta">
        {lifecycle && <span className={`badge badge-${lifecycleTone(s)}`}>{lifecycle}</span>}
        {loopOwned && (
          <span className="tag loop-tag" title="Started by the work loop">
            loop
          </span>
        )}
        {s.origin === "watch" && (
          <span className="tag loop-tag" title="A watch run (listed while it works or asks)">
            watch
          </span>
        )}
        <WorkstreamMark row={row} className="row-ws" />
        {showProject && project && <span className="project">{project}</span>}
        {chips.map((c) =>
          ids.includes(c) ? (
            <TaskPreviewLink key={c} project={row.project} id={c} className="chip task-chip" title={`Open task ${c}`} data-track="sessions.task_chip" />
          ) : (
            <span key={c} className="chip" title={ids.slice(2).join(", ")}>
              {c}
            </span>
          ),
        )}
        {variant === "page" && metadataItems(s).map((m) => <span key={m}>{m}</span>)}
        {isWorking(s) ? <RunTime s={s} now={now} /> : when && <span className="when">{when}</span>}
      </div>
    </div>
  );
}

/** Work in flight, whose row shows how long it has run rather than "now". */
function isWorking(s: SessionSummary): boolean {
  const tone = lifecycleTone(s);
  return tone === "running" || tone === "background";
}

/** How long a session has run since it started, with the start in the tooltip. */
export function RunTime({ s, now, className = "when" }: { s: SessionSummary; now: number; className?: string }) {
  const ran = sessionDurationMs(s, now);
  if (ran === null) return null;
  const text = formatSessionDuration(ran);
  return (
    <span className={className} title={`Started ${new Date(s.startedAt).toLocaleString()} · ${text} so far`}>
      {text}
    </span>
  );
}

/** A workstream session's branch marker; it links to the project's workstreams. */
function WorkstreamMark({ row, className }: { row: FeedRow; className: string }) {
  if (!row.session.workstreamId) return null;
  return (
    <Link
      to={paths.workstreams(row.project || null)}
      className={className}
      title={`Runs in workstream ${row.session.workstreamId} (its own worktree and branch) — open workstreams`}
      aria-label="Workstream session: open workstreams"
      data-track="sessions.workstream"
    >
      <Icon name="workstreams" size={11} />
    </Link>
  );
}

function FollowUpFlag({ s, pending, onToggle }: { s: SessionSummary; pending: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      className={`follow-up-flag${s.followUp ? " flagged" : ""}`}
      aria-pressed={s.followUp}
      aria-label={s.followUp ? `Clear follow-up on ${displayTitle(s)}` : `Flag ${displayTitle(s)} for follow-up`}
      title={s.followUp ? `Flagged for follow-up${s.followUpAt ? ` since ${new Date(s.followUpAt).toLocaleString()}` : ""} — click to clear` : "Flag for follow-up"}
      data-track="sessions.follow_up"
      disabled={pending}
      onClick={onToggle}
    >
      ⚑
    </button>
  );
}

/**
 * A sidebar row, laid out like the iOS inbox so a column of them scans:
 *
 *     ▪ project · 0431 0432              ⚑ 13m
 *       Title of the session, wrapping to at
 *       most two lines
 *
 * The gutter holds the one attention mark (needs-answer bell, else the unread
 * dot), the where/when line is quiet mono with the age (or a running/error
 * state) right-aligned, and a row with nothing to say about where moves its
 * age onto the title line.
 */
function SidebarRow({
  row,
  showProject,
  active,
  now,
  loopOwned,
  unread,
  onMarkRead,
  onToggleFollowUp,
  followUpPending,
}: {
  row: FeedRow;
  showProject: boolean;
  active: boolean;
  now: number;
  loopOwned: boolean;
  unread: boolean;
  onMarkRead?: () => void;
  onToggleFollowUp: () => void;
  followUpPending: boolean;
}) {
  const s = row.session;
  const project = showProject ? displayProject(row) : "";
  const chips = taskChipLabels(s);
  const ids = taskIds(s);
  const state = compactState(s);
  const age = compactAge(s.lastActivity || s.startedAt, now);
  const asks = needsAnswer(s);
  const watchRun = s.origin === "watch";
  const hasLead = !!project || chips.length > 0 || loopOwned || watchRun || !!s.workstreamId || !!s.forkedFrom;
  const trail = (
    <span className="row-trail">
      <FollowUpFlag s={s} pending={followUpPending} onToggle={onToggleFollowUp} />
      {state ? (
        <>
          <span className={`row-state tone-${state.tone}`}>{state.label}</span>
          {isWorking(s) && <RunTime s={s} now={now} />}
        </>
      ) : (
        age && <span className="when" title={new Date(s.lastActivity || s.startedAt).toLocaleString()}>{age}</span>
      )}
    </span>
  );
  return (
    <div
      className={`session-row variant-sidebar${active ? " active" : ""}${asks ? " needs" : ""}${unread ? " unread" : ""}${state?.tone === "running" ? " running" : ""}`}
    >
      <span className="row-gutter">
        {asks ? (
          <span className="row-needs" title="Needs your answer" aria-label="Needs your answer" role="img">
            <Icon name="bell" size={12} />
          </span>
        ) : (
          unread && (
            <button
              type="button"
              className="unread-dot"
              title="New agent activity — click to mark read"
              aria-label={`Unread: mark ${displayTitle(s)} read`}
              data-track="sessions.mark_read"
              onClick={onMarkRead}
            />
          )
        )}
      </span>
      <div className="row-main">
        {hasLead && (
          <div className="row-head">
            <span className="row-where">
              <WorkstreamMark row={row} className="row-ws" />
              {project && <span className="project">{project}</span>}
              {chips.map((c) =>
                ids.includes(c) ? (
                  <TaskPreviewLink key={c} project={row.project} id={c} className="row-task" title={`Open task ${c}`} data-track="sessions.task_chip" />
                ) : (
                  <span key={c} className="row-task more" title={ids.slice(2).join(", ")}>
                    {c}
                  </span>
                ),
              )}
              {loopOwned && <span className="row-loop" title="Started by the work loop">loop</span>}
              {watchRun && <span className="row-loop" title="A watch run (listed while it works or asks)">watch</span>}
              {s.forkedFrom && (
                <span className="row-loop" title={`Forked from ${s.forkedFrom}`}>
                  fork
                </span>
              )}
            </span>
            {trail}
          </div>
        )}
        <div className="row-title">
          <Link
            to={paths.session(row.project, s.sessionId)}
            className="session-row-link title-text"
            title={displayTitle(s)}
            data-track="sessions.open"
            aria-current={active ? "page" : undefined}
          >
            {displayTitle(s)}
          </Link>
          {!hasLead && trail}
        </div>
      </div>
    </div>
  );
}

/** The list filter; the sidebar shows it as its heading. */
export function SessionFilterSelect({
  value,
  onChange,
  className,
}: {
  value: SessionFilter;
  onChange: (filter: SessionFilter) => void;
  className?: string;
}) {
  return (
    <select
      aria-label="Filter sessions"
      className={className}
      value={value}
      data-track="sessions.filter"
      onChange={(e) => onChange(e.target.value as SessionFilter)}
    >
      {SESSION_FILTERS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
    </select>
  );
}

/**
 * Full-text message hits for the list's search (SearchSessions over one
 * project's whole history, beyond the loaded rows the title filter sees).
 */
function MessageResults({ scope, query, now }: { scope: string | null; query: string; now: number }) {
  const projects = useProjects();
  // The home feed spans projects; it searches the last one viewed (or the sole one).
  const project = searchProject(scope ?? lastViewedProject.get(), projects.data);
  const search = useMessageSearch(project, query, 30);
  if (!searchableQuery(query) || projects.isPending) return null;
  const resp = search.response;
  const note = indexingNote(resp);
  const hits = resp?.hits.filter((h) => h.session) ?? [];
  return (
    <section className="session-section message-results" aria-busy={search.pending}>
      <h3 className="section-title">{project ? `Messages in ${project}` : "Messages"}</h3>
      {project === null ? (
        <p className="muted pad small">Open a project to search its messages.</p>
      ) : (
        <>
          {note && (
            <p className="muted pad small search-indexing" role="status">
              {note}
            </p>
          )}
          {search.error ? (
            <p className="error pad small">{errorMessage(search.error, "Couldn’t search messages.")}</p>
          ) : !resp ? (
            <p className="muted pad small">Searching messages…</p>
          ) : hits.length === 0 ? (
            <p className="muted pad small">{search.pending ? "Searching messages…" : "No messages match."}</p>
          ) : (
            hits.map((hit) => <MessageHitRow key={hit.session!.sessionId} project={project} hit={hit} now={now} />)
          )}
          {resp?.nextCursor && hits.length > 0 && <p className="muted pad small">Showing the best {hits.length} sessions; refine the search to narrow them.</p>}
        </>
      )}
    </section>
  );
}

function MessageHitRow({ project, hit, now }: { project: string; hit: SessionSearchHit; now: number }) {
  const s = hit.session!;
  const when = relativeTime(hit.timestamp || s.lastActivity || s.startedAt, now);
  return (
    <div className="session-row variant-page message-hit">
      <div className="session-row-title">
        <Link to={hitPath(project, hit)} className="session-row-link title-text" title={displayTitle(s)} data-track="sessions.search_hit">
          {displayTitle(s)}
        </Link>
      </div>
      <div className="message-hit-snippet">
        <span className="hit-kind">{hitKindLabel(hit.kind)}</span>
        <Snippet spans={hit.snippet} />
      </div>
      <div className="session-row-meta">
        {hit.matchCount > 1 && <span>{hit.matchCount} matching messages</span>}
        {when && <span className="when">{when}</span>}
      </div>
    </div>
  );
}

export function SessionList({
  scope,
  activeSessionId,
  variant,
}: {
  scope: string | null;
  activeSessionId?: string;
  variant: "sidebar" | "page";
}) {
  const { feed, isLoading, error, loadOlder, loadingOlder } = useSessionFeed(scope);
  const now = useNow();
  const followUp = useSetFollowUp();
  const [pageFilter, setPageFilter] = useState<SessionFilter>("inbox");
  const sidebarFilter = useSidebarFilter();
  const filter = variant === "sidebar" ? sidebarFilter : pageFilter;
  const setFilter = variant === "sidebar" ? setSidebarFilter : setPageFilter;
  const [query, setQuery] = useState("");
  const followUpOnly = filter === "followup";
  const loopIds = useLoopSessionIds();
  const marks = useReadMarks();
  // The open session is being read right now.
  const isUnread = (row: FeedRow) => row.session.sessionId !== activeSessionId &&
    isInboxSession(row.session, loopIds.has(row.session.sessionId)) && marks.isUnread(row.session);
  if (isLoading) return <p className="muted pad">Loading sessions…</p>;
  if (error) return <p className="error pad">{errorMessage(error, "Couldn’t load sessions.")}</p>;
  if (!feed) return null;
  if (feed.error) return <p className="error pad">{feed.error}</p>;
  const flagged = onlyFollowUp(feed.rows);
  const visibleRows = filterSessions(feed.rows, filter, query, isUnread, loopIds, activeSessionId);
  // While searching, message hits come first; label the title matches below them.
  const searching = variant === "page" && searchableQuery(query) !== "";
  const groups = sections(visibleRows).map((g) => (searching && !g.title ? { ...g, title: "Sessions" } : g));
  const unreadRows = visibleRows.filter(isUnread);
  return (
    <div className={`session-list variant-${variant}`}>
      {/* The sidebar's filter lives in its heading (Shell); flagged rows already have their own section there. */}
      {variant === "page" && (
        <div className="session-list-toolbar">
          <SessionFilterSelect value={filter} onChange={setFilter} />
          <input type="search" className="field" aria-label="Search sessions and messages" placeholder="Search sessions and messages…" value={query} onChange={(e) => setQuery(e.target.value)} />
          <button
            type="button"
            className="btn ghost small follow-up-filter"
            aria-pressed={followUpOnly}
            data-track="sessions.follow_up_filter"
            onClick={() => setFilter(followUpOnly ? "inbox" : "followup")}
          >
            Follow-up ({flagged.length})
          </button>
        </div>
      )}
      {feed.warning && <p className="warn pad small">{feed.warning}</p>}
      {variant === "page" && unreadRows.length > 0 && (
        <div className="unread-bar">
          <span className="unread-dot static" aria-hidden="true" />
          <span>
            {unreadRows.length} {unreadRows.length === 1 ? "session has" : "sessions have"} new agent activity
          </span>
          <button
            type="button"
            className="btn ghost small"
            data-track="sessions.mark_all_read"
            onClick={() => marks.markAllRead(unreadRows)}
          >
            Mark all read
          </button>
        </div>
      )}
      {variant === "page" && <MessageResults scope={scope} query={query} now={now} />}
      {groups.length === 0 && (
        <div className="pad empty-list">
          <p className="muted">{followUpOnly ? "No sessions flagged for follow-up." : feed.rows.length === 0 ? "No sessions yet." : query.trim() ? "No loaded sessions in this view match by title." : "No sessions match this view."}</p>
          {feed.rows.length === 0 && !followUpOnly && variant === "page" && (
            <Link to={paths.newSession(scope)} className="btn primary small" data-track="sessions.start_first">
              Start a session
            </Link>
          )}
        </div>
      )}
      {groups.map((g) => (
        <section key={g.kind} className={`session-section ${g.kind}`}>
          {g.title && (
            <h3 className="section-title">
              {/* Like iOS's section labels: the mark sits in the rows' attention gutter. */}
              {variant === "sidebar" && (
                <span className="row-gutter" aria-hidden="true">
                  {g.kind === "needsAnswer" ? <Icon name="bell" size={12} /> : g.kind === "followUp" ? "⚑" : null}
                </span>
              )}
              {g.title}
            </h3>
          )}
          {g.rows.map((row) => (
            <SessionRowView
              key={`${row.project}\u0000${row.session.sessionId}`}
              row={row}
              showProject={scope === null}
              active={row.session.sessionId === activeSessionId}
              now={now}
              variant={variant}
              loopOwned={loopIds.has(row.session.sessionId)}
              unread={isUnread(row)}
              onMarkRead={() => marks.markSummaryRead(row)}
              onToggleFollowUp={() => void followUp.toggle(row)}
              followUpPending={followUp.isPending(row)}
            />
          ))}
        </section>
      ))}
      {feed.hasMore && (
        <div className="pad">
          <button type="button" className="btn ghost small" disabled={loadingOlder} data-track="sessions.load_older" onClick={() => void loadOlder()}>
            {loadingOlder ? "Loading…" : "Load older sessions"}
          </button>
        </div>
      )}
    </div>
  );
}
