// The command palette (Ctrl/Cmd-K): fuzzy-jump to any session, task,
// project, page, or file path, and run every registered action.
import { useQueries } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { client } from "../../api/client";
import { queryKeys, useProjects, useSessionFeed } from "../../api/queries";
import { invokeAction, shortcutLabel, useActions } from "../../app/actions";
import { track, useTrackedView } from "../../app/analytics";
import { useScope } from "../../app/scope";
import { lastViewedProject } from "../../app/memory";
import { historyTargets, relativeTime } from "../sessions/feed";
import { useReadMarks } from "../sessions/unread";
import { useLoopSessionIds } from "../workloop/hooks";
import { paletteResults, PALETTE_PREFIXES, parseQuery, type PaletteItem, type PaletteKind } from "./items";
import { closePalette, useOverlays } from "./state";

const KIND_LABEL: Record<PaletteKind, string> = {
  session: "Session",
  task: "Task",
  project: "Project",
  page: "Go to",
  action: "Action",
  file: "File",
};

function Highlight({ text, indices }: { text: string; indices: readonly number[] | undefined }) {
  if (!indices?.length) return <>{text}</>;
  const set = new Set(indices);
  const out: ReactNode[] = [];
  let run = "";
  let marked = false;
  const flush = (key: number) => {
    if (!run) return;
    out.push(marked ? <mark key={key}>{run}</mark> : <span key={key}>{run}</span>);
    run = "";
  };
  for (let i = 0; i < text.length; i++) {
    const m = set.has(i);
    if (m !== marked) {
      flush(i);
      marked = m;
    }
    run += text[i];
  }
  flush(text.length);
  return <>{out}</>;
}

export function CommandPalette() {
  const { palette } = useOverlays();
  const ref = useRef<HTMLDialogElement>(null);
  useTrackedView("palette", palette.open);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (palette.open && !d.open) d.showModal();
    if (!palette.open && d.open) d.close();
  }, [palette.open]);
  return (
    <dialog
      ref={ref}
      className="dialog palette"
      aria-label="Command palette"
      onCancel={(e) => {
        e.preventDefault();
        closePalette();
      }}
      onClick={(e) => {
        // A click on the backdrop (the dialog box itself, outside its content) closes.
        if (e.target === e.currentTarget) closePalette();
      }}
    >
      {palette.open && <PaletteBody key={palette.seq} initialQuery={palette.query} />}
    </dialog>
  );
}

function PaletteBody({ initialQuery }: { initialQuery: string }) {
  const [query, setQuery] = useState(initialQuery);
  const [selected, setSelected] = useState(0);
  const list = useRef<HTMLUListElement>(null);
  const navigate = useNavigate();
  const { project, sessionId } = useParams();
  const { scope } = useScope();
  const target = project ?? scope ?? lastViewedProject.get();
  const { feed } = useSessionFeed(null);
  const projects = useProjects();
  const actions = useActions();
  const marks = useReadMarks();
  const loopSessionIds = useLoopSessionIds();
  const mode = parseQuery(query).mode;
  // Tasks of every project (ListBacklog per workspace), loaded while the palette is open.
  const targets = useMemo(() => (projects.data ? historyTargets(projects.data) : []), [projects.data]);
  const backlogs = useQueries({
    queries: targets.map((p) => ({
      queryKey: queryKeys.backlog(p),
      queryFn: async ({ signal }: { signal: AbortSignal }) => (await client.listBacklog({ project: p }, { signal })).tasks,
    })),
  });
  const tasksKey = backlogs.map((b) => b.dataUpdatedAt).join(",");
  const tasks = useMemo(
    () => targets.map((p, i) => ({ project: p, tasks: backlogs[i]?.data ?? [] })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [targets, tasksKey],
  );
  const now = Date.now();
  const results = useMemo(
    () =>
      paletteResults(
        {
          rows: feed?.rows ?? [],
          activeSessionId: sessionId ?? null,
          isUnread: (row) => row.session.sessionId !== sessionId && marks.isUnread(row.session),
          relativeTime: (iso) => relativeTime(iso, now),
          tasks,
          projects: projects.data ?? [],
          actions,
          target: target || null,
          shortcutLabel: (a) => (a.shortcut ? shortcutLabel(a.shortcut) : undefined),
          loopSessionIds,
        },
        query,
      ),
    // `now` only refreshes relative times; recomputing per render is not needed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [feed, sessionId, marks.revision, tasks, projects.data, actions, target, query, loopSessionIds],
  );
  const sel = Math.min(selected, Math.max(0, results.length - 1));
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>(`[data-index="${sel}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const choose = (item: PaletteItem) => {
    closePalette();
    const action = item.kind === "action" ? actions.find((a) => `action:${a.id}` === item.id) : undefined;
    // Jumps record only the kind of target, never which one.
    if (!action) track.action("palette.jump", "palette", { kind: item.kind, mode, query: query.trim() ? "typed" : "empty" });
    // Let the palette's dialog close before an action opens its own.
    setTimeout(() => {
      if (item.to) navigate(item.to);
      else if (action) invokeAction(action, "palette");
      else item.run?.();
    }, 0);
  };
  const loadingTasks = (mode === "tasks" || query.trim() !== "") && backlogs.some((b) => b.isPending);

  return (
    <div className="palette-body">
      <input
        className="palette-input"
        autoFocus
        type="text"
        role="combobox"
        aria-expanded="true"
        aria-controls="palette-results"
        aria-activedescendant={results.length ? `palette-opt-${sel}` : undefined}
        aria-label="Search sessions, tasks, pages, and actions"
        placeholder="Jump to a session, task, page… or run an action"
        value={query}
        spellCheck={false}
        autoComplete="off"
        onChange={(e) => {
          setQuery(e.target.value);
          setSelected(0);
        }}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing) return;
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setSelected((s) => Math.min(results.length - 1, Math.min(s, results.length - 1) + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setSelected((s) => Math.max(0, Math.min(s, results.length - 1) - 1));
          } else if (e.key === "Enter") {
            e.preventDefault();
            const r = results[sel];
            if (r) choose(r.item);
          }
        }}
      />
      <ul className="palette-results" id="palette-results" role="listbox" ref={list} aria-label="Results">
        {results.map((r, i) => (
          <li
            key={r.item.id}
            id={`palette-opt-${i}`}
            data-index={i}
            role="option"
            aria-selected={i === sel}
            className={`palette-row kind-${r.item.kind}${i === sel ? " selected" : ""}`}
            onMouseMove={() => {
              if (i !== sel) setSelected(i);
            }}
            onMouseDown={(e) => e.preventDefault()}
            onClick={() => choose(r.item)}
          >
            <span className="palette-kind">{KIND_LABEL[r.item.kind]}</span>
            <span className="palette-title">
              {r.item.marker === "needs" && (
                <span className="needs-marker" aria-label="needs answer">
                  ●
                </span>
              )}
              {r.item.marker === "unread" && <span className="unread-dot static" aria-label="unread" />}
              <span className="palette-text">
                <Highlight text={r.item.title} indices={r.match?.indices} />
              </span>
            </span>
            {r.item.detail && <span className="palette-detail">{r.item.detail}</span>}
            {r.item.shortcut && <kbd className="palette-kbd">{r.item.shortcut}</kbd>}
          </li>
        ))}
        {results.length === 0 && <li className="palette-empty muted">{loadingTasks ? "Loading tasks…" : "No matches"}</li>}
      </ul>
      <div className="palette-foot muted small">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> move · <kbd>Enter</kbd> open · <kbd>Esc</kbd> close
        </span>
        <span>
          {PALETTE_PREFIXES.map((p) => (
            <span key={p.prefix} className="palette-prefix">
              <kbd>{p.prefix}</kbd> {p.label}
            </span>
          ))}
        </span>
      </div>
    </div>
  );
}
