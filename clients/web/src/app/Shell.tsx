// The desktop shell: sidebar (project switcher, session list, navigation),
// main pane (the routed surface), and the closable, resizable inspector.
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { useEffect } from "react";
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
        {PROJECT_SECTIONS.map((s) =>
          scope ? (
            <NavLink key={s.key} to={paths.section(scope, s.key)} className="nav-item">
              {s.label}
            </NavLink>
          ) : (
            <span key={s.key} className="nav-item disabled" title="Choose a project first">
              {s.label}
            </span>
          ),
        )}
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

export function Shell() {
  const inspector = useInspector();
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
      <Toasts />
    </div>
  );
}
