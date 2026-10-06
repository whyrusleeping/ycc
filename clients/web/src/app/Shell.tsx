// The desktop shell: sidebar (project switcher, session list, navigation),
// main pane (the routed surface), and the closable, resizable inspector.
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { useEffect, useMemo } from "react";
import { useSessionFeed, useWorkLoops, useWorkstreams } from "../api/queries";
import { InspectorPane } from "../features/inspector/InspectorPane";
import { useInspector } from "../features/inspector/inspector";
import { SessionList } from "../features/sessions/SessionList";
import { reconnectActiveSessions } from "../features/session/useSession";
import { Toasts } from "../ui/toast";
import { PROJECT_SECTIONS, paths, sectionPath } from "./paths";
import { useScope } from "./scope";
import { authStore, getToken, setToken } from "../api/auth";
import { useQueryClient } from "@tanstack/react-query";
import { useAction, useActionShortcuts, type AppAction } from "./actions";
import { CAPTURE_SHORTCUT_LABEL, CaptureDialog, IS_MAC, openCapture } from "../features/backlog/CaptureDialog";
import { canStart, canStop, loopIndicator, loopState } from "../features/workloop/model";
import { useLoopWatcher } from "../features/workloop/hooks";
import { loopIntentKey } from "../features/workloop/WorkLoopPage";
import { workstreamIndicator } from "../features/workstreams/model";
import { workstreamsIntentKey } from "../features/workstreams/WorkstreamsPage";
import { requestIntent } from "./intents";

function ProjectSwitcher() {
  const { scope, setScope } = useScope();
  const { projects } = useSessionFeed(scope);
  const navigate = useNavigate();
  const location = useLocation();
  const names = (projects ?? []).map((p) => p.name);
  // A remembered scope whose project was removed falls back to Recent.
  useEffect(() => {
    if (scope && projects && !names.includes(scope)) setScope(null);
  }, [scope, projects, names, setScope]);
  return (
    <label className="project-switcher">
      <span className="sr-only">Project</span>
      <select
        value={scope ?? ""}
        onChange={(e) => {
          const next = e.target.value || null;
          setScope(next);
          // On a list page, follow the scope; elsewhere keep the current view.
          const onList = location.pathname === "/" || /^\/p\/[^/]+\/?$/.test(location.pathname);
          if (onList) navigate(next ? paths.project(next) : paths.home());
          // On the backlog, show the new project's backlog (or ask which one).
          else if (/^(\/p\/[^/]+)?\/backlog(\/|$)/.test(location.pathname)) navigate(paths.backlog(next));
          else if (/^(\/p\/[^/]+)?\/loop\/?$/.test(location.pathname)) navigate(paths.loop(next));
          else if (/^(\/p\/[^/]+)?\/workstreams\/?$/.test(location.pathname)) navigate(paths.workstreams(next));
        }}
      >
        <option value="">All projects · Recent</option>
        {names.map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </select>
    </label>
  );
}

function Sidebar() {
  const { scope } = useScope();
  const { sessionId } = useParams();
  const { refresh, isFetching } = useSessionFeed(scope);
  const qc = useQueryClient();
  const loops = useWorkLoops();
  const workstreams = useWorkstreams(scope ?? "");
  // Scoped: that project's activity; Recent: every project's.
  const loopBadge = loopIndicator(loops.filter((l) => scope === null || l.project === scope).map((l) => l.loop));
  const wsBadge = workstreamIndicator(workstreams.data);
  return (
    <nav className="sidebar" aria-label="Sidebar">
      <div className="sidebar-top">
        <Link to={paths.home()} className="brand">
          ycc
        </Link>
        <Link to={paths.newSession(scope)} className="btn primary small" title="Start a new session">
          + New session
        </Link>
      </div>
      <ProjectSwitcher />
      <div className="sidebar-heading">
        <NavLink to={scope ? paths.project(scope) : paths.home()} end className="sidebar-heading-link">
          {scope ? "Sessions" : "Recent sessions"}
        </NavLink>
        <button
          type="button"
          className="btn ghost small"
          onClick={refresh}
          disabled={isFetching}
          title="Refresh"
          aria-label="Refresh sessions"
        >
          ↻
        </button>
      </div>
      <div className="sidebar-list">
        <SessionList scope={scope} activeSessionId={sessionId} variant="sidebar" />
      </div>
      <div className="sidebar-nav">
        {PROJECT_SECTIONS.map((s) => {
          // Backlog, work loop, and workstreams ask for a project when unscoped; other sections need one.
          const to = sectionPath(s.key, scope);
          const badge = s.key === "loop" ? loopBadge : s.key === "workstreams" ? wsBadge : null;
          return to ? (
            <NavLink key={s.key} to={to} className="nav-item">
              <span>{s.label}</span>
              {badge && (
                <span className={`nav-badge tone-${badge.tone}`} title={badge.title} aria-label={`${s.label}: ${badge.title}`}>
                  {badge.label}
                </span>
              )}
            </NavLink>
          ) : (
            <span key={s.key} className="nav-item disabled" title="Choose a project first">
              {s.label}
            </span>
          );
        })}
        <NavLink to={paths.settings()} className="nav-item">
          Settings
        </NavLink>
        {getToken() && (
          <button
            type="button"
            className="nav-item link"
            onClick={() => {
              setToken(null);
              qc.clear();
              authStore.setStatus({ kind: "needsToken", note: "Signed out." });
            }}
          >
            Sign out
          </button>
        )}
      </div>
    </nav>
  );
}

/** App-wide actions the shell owns (the command palette lists the same registry). */
function useShellActions() {
  const { scope } = useScope();
  const { project } = useParams();
  const navigate = useNavigate();
  const target = project ?? scope;
  const loops = useWorkLoops();
  const capture = useMemo<AppAction>(
    () => ({
      id: "backlog.capture",
      title: "Quick capture a backlog item…",
      group: "Backlog",
      // Option+letter types characters on macOS: leave text fields alone there.
      shortcut: { code: "KeyN", alt: true, label: CAPTURE_SHORTCUT_LABEL, inEditable: !IS_MAC },
      run: () => openCapture(target ?? null),
    }),
    [target],
  );
  useAction(capture);
  // Work-loop and workstream actions need a project: the route's, else the sidebar scope.
  const state = loopState(target ? loops.find((l) => l.project === target)?.loop : null);
  const loopStartable = !!target && canStart(state);
  const loopStoppable = !!target && canStop(state);
  const startLoop = useMemo<AppAction | null>(
    () =>
      target && loopStartable
        ? {
            id: "loop.start",
            title: `Start the work loop in ${target}…`,
            group: "Work loop",
            run: () => {
              requestIntent(loopIntentKey(target), "start");
              navigate(paths.loop(target));
            },
          }
        : null,
    [target, loopStartable, navigate],
  );
  const stopLoop = useMemo<AppAction | null>(
    () =>
      target && loopStoppable
        ? {
            id: "loop.stop",
            title: `Stop the work loop in ${target}…`,
            group: "Work loop",
            run: () => {
              requestIntent(loopIntentKey(target), "stop");
              navigate(paths.loop(target));
            },
          }
        : null,
    [target, loopStoppable, navigate],
  );
  const spawn = useMemo<AppAction | null>(
    () =>
      target
        ? {
            id: "workstreams.spawn",
            title: `Spawn a workstream in ${target}…`,
            group: "Workstreams",
            run: () => {
              requestIntent(workstreamsIntentKey(target), "spawn");
              navigate(paths.workstreams(target));
            },
          }
        : null,
    [target, navigate],
  );
  useAction(startLoop);
  useAction(stopLoop);
  useAction(spawn);
  useActionShortcuts();
}

export function Shell() {
  const inspector = useInspector();
  useShellActions();
  useLoopWatcher();
  useEffect(() => {
    // A file dropped outside a drop zone must not navigate the tab to it.
    const guard = (e: DragEvent) => {
      if (Array.from(e.dataTransfer?.types ?? []).includes("Files")) e.preventDefault();
    };
    window.addEventListener("dragover", guard);
    window.addEventListener("drop", guard);
    return () => {
      window.removeEventListener("dragover", guard);
      window.removeEventListener("drop", guard);
    };
  }, []);
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") reconnectActiveSessions();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, []);
  return (
    <div
      className={`shell${inspector.item ? " with-inspector" : ""}`}
      style={inspector.item ? { gridTemplateColumns: `var(--sidebar-width) minmax(0, 1fr) ${inspector.width}px` } : undefined}
    >
      <Sidebar />
      <main className="main">
        <Outlet />
      </main>
      <InspectorPane />
      <CaptureDialog />
      <Toasts />
    </div>
  );
}
