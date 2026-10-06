// The desktop shell: sidebar (project switcher, session list, navigation),
// main pane (the routed surface), and the closable, resizable inspector.
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { useEffect, useMemo } from "react";
import { useSessionFeed } from "../api/queries";
import { InspectorPane } from "../features/inspector/InspectorPane";
import { useInspector } from "../features/inspector/inspector";
import { SessionList } from "../features/sessions/SessionList";
import { reconnectActiveSessions } from "../features/session/useSession";
import { Toasts } from "../ui/toast";
import { PROJECT_SECTIONS, paths } from "./paths";
import { useScope } from "./scope";
import { authStore, getToken, setToken } from "../api/auth";
import { useQueryClient } from "@tanstack/react-query";
import { useAction, useActionShortcuts, type AppAction } from "./actions";
import { CAPTURE_SHORTCUT_LABEL, CaptureDialog, IS_MAC, openCapture } from "../features/backlog/CaptureDialog";

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
          // The backlog asks for a project when unscoped; other sections need one.
          const to = scope ? paths.section(scope, s.key) : s.key === "backlog" ? paths.backlog(null) : null;
          return to ? (
            <NavLink key={s.key} to={to} className="nav-item">
              {s.label}
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
  const target = project ?? scope;
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
  useActionShortcuts();
}

export function Shell() {
  const inspector = useInspector();
  useShellActions();
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
