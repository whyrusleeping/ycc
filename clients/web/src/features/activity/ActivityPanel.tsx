// The session Activity panel (an inspector item): the session's coordinator,
// subagents, and background jobs as a tree and a swimlane timeline, plus the
// selected node's detail — an agent's own transcript, rounds, and report, or a
// shell job's output. Polls GetSessionActivity while anything runs.
import { useQuery } from "@tanstack/react-query";
import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { client, errorMessage } from "../../api/client";
import type { ActivityNode, GetSessionActivityResponse } from "../../gen/ycc/v1/ycc_pb";
import { InspectorStackScope } from "../inspector/inspector";
import { FileLinksProvider } from "../files/FileRef";
import { useSessionFileLinks } from "../files/links";
import { Markdown } from "../markdown/Markdown";
import type { SessionController } from "../session/controller";
import { RowView } from "../session/RowView";
import { ActivityHead } from "../session/Transcript";
import { activitySummary, foldedTailStart, transcriptBlocks } from "../session/blocks";
import { reportPresentation } from "../session/report";
import { useSessionController, useSessionSnapshot } from "../session/useSession";
import { Icon } from "../../ui/icons";
import { JobOutput } from "./JobOutput";
import {
  badgeText,
  buildTree,
  compactTokens,
  elapsedMs,
  formatDuration,
  isJob,
  ms,
  shouldPoll,
  statusOf,
  timeRange,
  ROOT_ID,
  type TimeRange,
} from "./model";
import { StatusDot } from "./StatusDot";
import { Timeline } from "./Timeline";

const POLL_MS = 2000;

/** The node each session's panel last showed, so Back from a pushed row restores it. */
const lastSelected = new Map<string, string>();

export function ActivityPanel({ project, sessionId }: { project: string; sessionId: string }) {
  const controller = useSessionController(project, sessionId);
  // Only the durable cursor: streaming text must not re-render the overview.
  const cursor = useSyncExternalStore(controller.subscribe, () => controller.getSnapshot().cursor);
  const q = useQuery({
    queryKey: ["sessionActivity", project, sessionId],
    queryFn: ({ signal }) => client.getSessionActivity({ project, sessionId }, { signal }),
    refetchInterval: (query) => (shouldPoll(query.state.data) ? POLL_MS : false),
  });
  const polling = shouldPoll(q.data);
  // Not polling: a new durable event may have started or finished something.
  const { refetch } = q;
  const seenCursor = useRef(cursor);
  useEffect(() => {
    if (seenCursor.current === cursor) return;
    seenCursor.current = cursor;
    if (!polling) void refetch();
  }, [cursor, polling, refetch]);

  const selKey = `${project}\u0000${sessionId}`;
  const [selected, setSelectedState] = useState<string | null>(() => lastSelected.get(selKey) ?? null);
  const select = useCallback(
    (id: string) => {
      lastSelected.set(selKey, id);
      setSelectedState(id);
    },
    [selKey],
  );

  const now = useNow(polling);
  const data = q.data;
  const range = useMemo(() => (data ? timeRange(data, now) : null), [data, now]);

  if (q.isPending) return <p className="muted pad">Loading activity…</p>;
  if (q.isError) return <p className="error pad">{errorMessage(q.error)}</p>;
  const nodes = q.data.nodes;
  const node = nodes.find((n) => n.id === selected) ?? null;
  return (
    <div className="activity-panel">
      <div className="activity-overview">
        <Summary data={q.data} fetching={q.isFetching} onRefresh={() => void q.refetch()} />
        <ActivityTree nodes={nodes} range={range} now={now} selected={selected} onSelect={select} />
        {range && <Timeline nodes={nodes} range={range} selected={selected} onSelect={select} />}
      </div>
      <div className={`activity-detail${node && isJob(node) ? " job" : ""}`}>
        {node ? (
          <NodeDetail key={node.id} node={node} nodes={nodes} range={range} now={now} live={q.data.live} controller={controller} />
        ) : (
          <p className="muted activity-hint">Select an agent or job to see what it did.</p>
        )}
      </div>
    </div>
  );
}

/** Wall-clock time, ticking once a second while something runs. */
function useNow(ticking: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    setNow(Date.now());
    if (!ticking) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [ticking]);
  return now;
}

function Summary({ data, fetching, onRefresh }: { data: GetSessionActivityResponse; fetching: boolean; onRefresh: () => void }) {
  const agents = data.nodes.filter((n) => n.id !== ROOT_ID && !isJob(n)).length;
  const jobs = data.nodes.filter(isJob).length;
  const running = data.nodes.filter((n) => n.id !== ROOT_ID && n.status === "running").length;
  return (
    <div className="activity-summary">
      <span>
        {agents} {agents === 1 ? "agent" : "agents"} · {jobs} {jobs === 1 ? "job" : "jobs"}
      </span>
      {running > 0 && <span className="tag live">{running} running</span>}
      {!data.live && <span className="tag" title="The session is not running in the daemon: live output and progress are unavailable">not live</span>}
      <button
        type="button"
        className="btn ghost small icon-btn activity-refresh"
        aria-label="Refresh activity"
        title="Refresh"
        disabled={fetching}
        data-track="activity.refresh"
        onClick={onRefresh}
      >
        <Icon name="refresh" size={14} />
      </button>
    </div>
  );
}

/** Memoized: it re-renders on new activity data, the clock tick, or selection only. */
const ActivityTree = memo(function ActivityTree({
  nodes,
  range,
  now,
  selected,
  onSelect,
}: {
  nodes: readonly ActivityNode[];
  range: TimeRange | null;
  now: number;
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const rows = useMemo(() => buildTree(nodes), [nodes]);
  return (
    <ul className="activity-tree" role="tree" aria-label="Agents and jobs">
      {rows.map(({ node, depth }) => {
        const sel = node.id === selected;
        const elapsed = elapsedMs(node, range, now);
        return (
          <li key={node.id} role="treeitem" aria-level={depth + 1} aria-selected={sel}>
            <button
              type="button"
              className={`act-row${sel ? " selected" : ""}`}
              style={{ paddingLeft: 8 + depth * 18 }}
              data-track="activity.select"
              onClick={() => onSelect(node.id)}
            >
              {depth > 0 && <span className="act-branch" style={{ left: depth * 18 - 6 }} aria-hidden="true" />}
              <StatusDot status={statusOf(node.status)} />
              <span className="act-main">
                <span className="act-line">
                  <span className={`act-label${isJob(node) ? " mono" : ""}`} title={node.label}>
                    {node.label || node.id}
                  </span>
                  <span className="tag act-badge">{badgeText(node)}</span>
                  {node.mutates && !isJob(node) && <span className="tag warn act-badge">writes</span>}
                  {(isJob(node) || node.id === ROOT_ID) && node.status !== "done" && (
                    <span className={`act-status st-${statusOf(node.status)}`}>{node.status}</span>
                  )}
                </span>
                {!isJob(node) && node.id !== ROOT_ID && <AgentProgress node={node} />}
              </span>
              {elapsed !== null && <span className="act-elapsed">{formatDuration(elapsed)}</span>}
            </button>
          </li>
        );
      })}
    </ul>
  );
});

/** An agent row's second line: what it is doing now, or how it ended. */
function AgentProgress({ node }: { node: ActivityNode }) {
  const parts: ReactNode[] = [];
  if (node.status === "running") {
    parts.push(
      <span key="tool" className="act-tool">
        {node.currentTool ? node.currentTool : "thinking"}
      </span>,
    );
  } else {
    parts.push(<span key="s" className={`act-status st-${statusOf(node.status)}`}>{node.status}</span>);
  }
  if (node.turns > 0) parts.push(<span key="turns">{node.turns} {node.turns === 1 ? "turn" : "turns"}</span>);
  if (node.totalTokens > 0n) parts.push(<span key="tok">{compactTokens(node.totalTokens)} tok</span>);
  if (node.spans.length > 1) parts.push(<span key="r">{node.spans.length} rounds</span>);
  return <span className="act-sub">{parts}</span>;
}

function NodeDetail({
  node,
  nodes,
  range,
  now,
  live,
  controller,
}: {
  node: ActivityNode;
  nodes: readonly ActivityNode[];
  range: TimeRange | null;
  now: number;
  live: boolean;
  controller: SessionController;
}) {
  const job = isJob(node);
  const elapsed = elapsedMs(node, range, now);
  const started = ms(node.startedAt);
  const parent = node.parentId ? nodes.find((n) => n.id === node.parentId) : undefined;
  const lastActive = ms(node.lastActivityAt);
  return (
    <div className={`activity-node${job ? " job" : ""}`}>
      <div className="activity-node-head">
        <div className="activity-node-title">
          <StatusDot status={statusOf(node.status)} />
          <span className={job ? "mono" : ""}>{job ? node.id : node.label}</span>
          <span className="tag act-badge">{badgeText(node)}</span>
          {node.mutates && !job && <span className="tag warn act-badge">writes</span>}
        </div>
        <div className="activity-node-meta">
          <span className={`act-status st-${statusOf(node.status)}`}>{node.status || "unknown"}</span>
          {elapsed !== null && <span>{formatDuration(elapsed)}</span>}
          {started !== null && <span title={new Date(started).toLocaleString()}>started {new Date(started).toLocaleTimeString()}</span>}
          {parent && parent.id !== ROOT_ID && <span>under {parent.label}</span>}
          {!job && node.status === "running" && (
            <>
              <span className="act-tool">{node.currentTool || "thinking"}</span>
              {node.turns > 0 && <span>{node.turns} {node.turns === 1 ? "turn" : "turns"}</span>}
              {node.totalTokens > 0n && <span>{compactTokens(node.totalTokens)} tokens</span>}
              {lastActive !== null && <span>active {formatDuration(Math.max(0, now - lastActive))} ago</span>}
            </>
          )}
        </div>
        {job && <pre className="activity-command mono">$ {node.label}</pre>}
      </div>
      {job ? (
        <JobOutput project={controller.project} sessionId={controller.sessionId} jobId={node.jobId || node.id} available={node.outputAvailable} result={node.result} />
      ) : node.id === ROOT_ID ? (
        <p className="muted">
          The coordinator's conversation is this session's main transcript.{live ? "" : " The session is not running in the daemon."}
        </p>
      ) : (
        <AgentDetail node={node} now={now} controller={controller} />
      )}
    </div>
  );
}

function AgentDetail({ node, now, controller }: { node: ActivityNode; now: number; controller: SessionController }) {
  return (
    <>
      {node.spans.length > 0 && (
        <section className="activity-section">
          <div className="label">Rounds</div>
          <table className="activity-rounds">
            <tbody>
              {node.spans.map((s, i) => {
                const start = ms(s.startedAt);
                const end = ms(s.finishedAt) ?? (s.status === "running" ? now : null);
                return (
                  <tr key={i}>
                    <td>#{s.round || i + 1}</td>
                    <td>
                      <span className={`act-status st-${statusOf(s.status)}`}>{s.status}</span>
                    </td>
                    <td>{start !== null && end !== null ? formatDuration(end - start) : "—"}</td>
                    <td className="muted">{s.contextMode}</td>
                    <td className="muted mono">{s.jobId}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}
      {node.result && (
        <section className="activity-section">
          <div className="label">{node.status === "blocked" ? "Blocked report" : "Report"}</div>
          <div className="activity-report">
            <Markdown text={node.result} />
          </div>
        </section>
      )}
      <section className="activity-section">
        <div className="label">Transcript</div>
        <AgentTranscript actor={node.actor} controller={controller} />
      </section>
    </>
  );
}

/** The session transcript filtered to one actor, rendered like the main one. */
function AgentTranscript({ actor, controller }: { actor: string; controller: SessionController }) {
  const snap = useSessionSnapshot(controller);
  const rows = useMemo(() => snap.rows.filter((r) => r.actor === actor), [snap.rows, actor]);
  const blocks = useMemo(() => transcriptBlocks(rows), [rows]);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const links = useSessionFileLinks(controller.project, controller.sessionId, "inspector");
  const items: ReactNode[] = [];
  const renderRow = (i: number, repeat = 1) => {
    const row = rows[i];
    return (
      <RowView
        key={row.id}
        row={row}
        controller={controller}
        loadingDetail={snap.loadingDetail.has(row.id)}
        report={row.kind.type === "report" ? reportPresentation(rows, i) : undefined}
        repeat={repeat}
      />
    );
  };
  for (const b of blocks) {
    if (b.type === "row") {
      items.push(renderRow(b.index, b.repeat));
      continue;
    }
    const open = expanded.has(b.key);
    const tail = foldedTailStart(b);
    items.push(
      <ActivityHead
        key={`activity:${b.key}`}
        summary={activitySummary(rows, b.start, b.end)}
        hidden={tail - b.start}
        open={open}
        onToggle={() =>
          setExpanded((prev) => {
            const next = new Set(prev);
            if (!next.delete(b.key)) next.add(b.key);
            return next;
          })
        }
      />,
    );
    for (let i = open ? b.start : tail; i < b.end; i++) items.push(renderRow(i));
  }
  return (
    <div className="activity-transcript">
      {snap.hasEarlier && (
        <div className="earlier">
          <button type="button" className="btn ghost small" disabled={snap.loadingEarlier} data-track="activity.load_earlier" onClick={() => void controller.loadEarlier()}>
            {snap.loadingEarlier ? "Loading earlier…" : "Load earlier session history"}
          </button>
        </div>
      )}
      {rows.length === 0 && (
        <p className="muted">{snap.hasEarlier ? "None of this agent's events are in the loaded history." : "No transcript events for this agent."}</p>
      )}
      <InspectorStackScope>
        <FileLinksProvider value={links}>{items}</FileLinksProvider>
      </InspectorStackScope>
    </div>
  );
}
