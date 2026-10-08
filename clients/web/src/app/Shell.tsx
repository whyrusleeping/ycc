// The desktop shell: sidebar (project switcher, session list, navigation),
// main pane (the routed surface), and the closable, resizable inspector.
import { Link, NavLink, Outlet, useLocation, useNavigate, useParams } from "react-router";
import { useEffect, useMemo, useRef } from "react";
import { useSessionFeed, useWorkLoops, useWorkstreams } from "../api/queries";
import { InspectorPane } from "../features/inspector/InspectorPane";
import { useInspector } from "../features/inspector/inspector";
import { SessionList } from "../features/sessions/SessionList";
import { reconnectActiveSessions } from "../features/session/useSession";
import { Toasts, toast } from "../ui/toast";
import { PROJECT_SECTIONS, paths, sectionPath } from "./paths";
import { useScope } from "./scope";
import { authStore, getToken } from "../api/auth";
import { useQueryClient } from "@tanstack/react-query";
import { track } from "./analytics";
import { registerAction, shortcutLabel, useAction, useActionShortcuts, type AppAction, type Shortcut } from "./actions";
import { CAPTURE_SHORTCUT_LABEL, CaptureDialog, openCapture } from "../features/backlog/CaptureDialog";
import { IS_MAC } from "./platform";
import { canStart, canStop, currentSessionId, loopIndicator, loopState } from "../features/workloop/model";
import { useLoopWatcher } from "../features/workloop/hooks";
import { loopIntentKey } from "../features/workloop/WorkLoopPage";
import { workstreamIndicator } from "../features/workstreams/model";
import { workstreamsIntentKey } from "../features/workstreams/WorkstreamsPage";
import { requestIntent } from "./intents";
import { MenuButton } from "../ui/Menu";
import { Icon, type IconName } from "../ui/icons";
import { gitSyncBadge } from "../features/projects/model";
import { ProjectDialogs, openAddProject, openRemoveProject, openRenameProject } from "../features/projects/ProjectDialogs";
import { AnthropicLoginDialog, openAnthropicLogin } from "../features/settings/AnthropicLogin";
import { SETTINGS_INTENT } from "../features/settings/SettingsPage";
import { CommandPalette } from "../features/palette/Palette";
import { HelpOverlay } from "../features/palette/HelpOverlay";
import { openHelp, togglePalette } from "../features/palette/state";
import { useReadMarks } from "../features/sessions/unread";
import { activitySuffix, projectActivity } from "../features/sessions/activity";
import { adjacentSession, nextNeedsAnswer } from "../features/sessions/navigation";
import { useSessionWatch } from "../features/notify/useSessionWatch";
import { NotificationPrompt, enableFromGesture, sendTestNotification } from "../features/notify/NotifyControls";
import { setNotificationsEnabled, useNotifyState } from "../features/notify/notifier";
import { backlogIntentKey } from "../features/backlog/BacklogPage";
import { memoryIntentKey } from "../features/memory/MemoryPage";
import type { FeedRow } from "../features/sessions/feed";

/** Shell-owned shortcuts (Alt chords leave text fields alone on macOS, where Option types characters). */
const SHORTCUTS = {
  palette: { key: "k", mod: true, inDialog: true },
  help: { key: "?" },
  newSession: { code: "KeyN", alt: true, shift: true, inEditable: !IS_MAC },
  nextSession: { code: "ArrowDown", alt: true, inEditable: !IS_MAC },
  prevSession: { code: "ArrowUp", alt: true, inEditable: !IS_MAC },
  needsAnswer: { code: "KeyA", alt: true, inEditable: !IS_MAC },
  composer: { code: "KeyI", alt: true, inEditable: !IS_MAC },
  inspector: { code: "Backslash", alt: true, inEditable: !IS_MAC },
} satisfies Record<string, Shortcut>;

const SECTION_ICONS: Record<(typeof PROJECT_SECTIONS)[number]["key"], IconName> = {
  backlog: "backlog",
  loop: "loop",
  workstreams: "workstreams",
  usage: "usage",
  files: "files",
  memory: "memory",
};

const PALETTE_LABEL = shortcutLabel(SHORTCUTS.palette);
const NEW_SESSION_LABEL = shortcutLabel(SHORTCUTS.newSession);

/** Focus the pending question's answer box, else the message composer. */
function focusComposer(): boolean {
  const el =
    document.querySelector<HTMLElement>(".answer-panel textarea:not([disabled])") ??
    document.querySelector<HTMLElement>(".composer textarea:not([disabled])") ??
    document.querySelector<HTMLElement>(".new-session-composer textarea:not([disabled])");
  if (!el) return false;
  el.focus();
  return true;
}

/** Unread test for list rows: the open session is being read right now. */
function useRowUnread(): (row: FeedRow) => boolean {
  const marks = useReadMarks();
  const { sessionId } = useParams();
  return (row) => row.session.sessionId !== sessionId && marks.isUnread(row.session);
}

function ProjectSwitcher() {
  const { scope, setScope } = useScope();
  const { projects } = useSessionFeed(scope);
  const { feed: all } = useSessionFeed(null);
  const isUnread = useRowUnread();
  const activity = projectActivity(all?.rows ?? [], isUnread);
  const navigate = useNavigate();
  const location = useLocation();
  const names = (projects ?? []).map((p) => p.name);
  // A remembered scope whose project was removed falls back to Recent.
  useEffect(() => {
    if (scope && projects && !names.includes(scope)) setScope(null);
  }, [scope, projects, names, setScope]);
  return (
    <div className="project-switcher-row">
      <label className="project-switcher">
        <span className="sr-only">Project</span>
        <select
          value={scope ?? ""}
          onChange={(e) => {
            const next = e.target.value || null;
            track.action("sidebar.scope", "click", { to: next ? "project" : "all" });
            setScope(next);
            // On a list page, follow the scope; elsewhere keep the current view.
            const onList = location.pathname === "/" || /^\/p\/[^/]+\/?$/.test(location.pathname);
            if (onList) navigate(next ? paths.project(next) : paths.home());
            // On the backlog, show the new project's backlog (or ask which one).
            else if (/^(\/p\/[^/]+)?\/backlog(\/|$)/.test(location.pathname)) navigate(paths.backlog(next));
            else if (/^(\/p\/[^/]+)?\/loop\/?$/.test(location.pathname)) navigate(paths.loop(next));
            else if (/^(\/p\/[^/]+)?\/workstreams\/?$/.test(location.pathname)) navigate(paths.workstreams(next));
            else if (/^(\/p\/[^/]+)?\/files(\/|$)/.test(location.pathname)) navigate(paths.files(next));
            else if (/^(\/p\/[^/]+)?\/(memory|plans)(\/|$)/.test(location.pathname)) navigate(paths.memory(next));
            else if (/^(\/p\/[^/]+)?\/usage\/?$/.test(location.pathname)) navigate(paths.usage(next, location.search));
          }}
        >
          {/* The closed select is narrow: with counts, drop "· Recent". */}
          <option value="">{activitySuffix(activity.total) ? `All projects${activitySuffix(activity.total)}` : "All projects · Recent"}</option>
          {(projects ?? []).map((p) => {
            // The quiet git sync badge (ahead/behind, dirty, unfetched), as on iOS,
            // after the attention counts (waiting for an answer, unread).
            const badge = gitSyncBadge(p.git);
            const label = `${p.name}${activitySuffix(activity.byProject.get(p.name))}`;
            return (
              <option key={p.name} value={p.name}>
                {badge ? `${label}  ${badge}` : label}
              </option>
            );
          })}
        </select>
      </label>
      <MenuButton
        label="⋯"
        ariaLabel="Project actions"
        className="btn ghost small project-menu-btn"
        items={[
          { id: "projects.add", label: "Add project…", onSelect: () => openAddProject() },
          !!scope && { id: "projects.rename", label: `Rename ${scope}…`, onSelect: () => openRenameProject(scope) },
          !!scope && {
            id: "projects.remove",
            label: `Remove ${scope} from ycc…`,
            danger: true,
            title: "Deregister the project; nothing on disk is deleted",
            onSelect: () => openRemoveProject(scope),
          },
          { id: "projects.manage", label: "Manage projects", onSelect: () => navigate(paths.projects()) },
        ]}
      />
    </div>
  );
}

function Sidebar() {
  const { scope } = useScope();
  const location = useLocation();
  const { sessionId } = useParams();
  const { refresh, isFetching, feed } = useSessionFeed(scope);
  const marks = useReadMarks();
  const isUnread = useRowUnread();
  const unreadRows = (feed?.rows ?? []).filter(isUnread);
  const qc = useQueryClient();
  const loops = useWorkLoops();
  const workstreams = useWorkstreams(scope ?? "");
  // Scoped: that project's activity; Recent: every project's.
  const loopBadge = loopIndicator(loops.filter((l) => scope === null || l.project === scope).map((l) => l.loop));
  const wsBadge = workstreamIndicator(workstreams.data);
  return (
    <nav className="sidebar" aria-label="Sidebar">
      <div className="sidebar-top">
        <Link to={paths.home()} className="brand" data-track="sidebar.home">
          <span className="brand-mark" aria-hidden="true">
            y
          </span>
          ycc
        </Link>
        <span className="sidebar-top-actions">
          <Link
            to={paths.newSession(scope)}
            className="btn primary small"
            title={`Start a new session (${NEW_SESSION_LABEL})`}
            data-track="sidebar.new_session"
          >
            <Icon name="plus" size={14} /> New session
          </Link>
        </span>
      </div>
      <button
        type="button"
        className="palette-trigger"
        title={`Command palette: jump to anything, run any action (${PALETTE_LABEL})`}
        aria-label="Open the command palette"
        data-track="sidebar.palette"
        onClick={() => togglePalette()}
      >
        <Icon name="search" size={14} />
        <span className="palette-trigger-text">Jump to…</span>
        <kbd>{PALETTE_LABEL}</kbd>
      </button>
      <ProjectSwitcher />
      <div className="sidebar-heading">
        <NavLink to={scope ? paths.project(scope) : paths.home()} end className="sidebar-heading-link" data-track="sidebar.sessions">
          {scope ? "Sessions" : "Recent sessions"}
        </NavLink>
        <span className="sidebar-heading-actions">
          {unreadRows.length > 0 && (
            <button
              type="button"
              className="btn ghost small mark-all-read"
              data-track="sidebar.mark_all_read"
              onClick={() => marks.markAllRead(unreadRows.map((r) => r.session))}
              title={`Mark all ${unreadRows.length} unread ${unreadRows.length === 1 ? "session" : "sessions"} read`}
            >
              <span className="unread-dot static" aria-hidden="true" /> {unreadRows.length} · Mark read
            </button>
          )}
          <button
            type="button"
            className="btn ghost small"
            onClick={refresh}
            disabled={isFetching}
            data-track="sidebar.refresh"
            title="Refresh"
            aria-label="Refresh sessions"
          >
            <Icon name="refresh" size={13} />
          </button>
        </span>
      </div>
      <div className="sidebar-list">
        <SessionList scope={scope} activeSessionId={sessionId} variant="sidebar" />
      </div>
      <NotificationPrompt />
      <div className="sidebar-nav">
        {PROJECT_SECTIONS.map((s) => {
          // Backlog, work loop, and workstreams ask for a project when unscoped; other sections need one.
          const to = sectionPath(s.key, scope);
          const badge = s.key === "loop" ? loopBadge : s.key === "workstreams" ? wsBadge : null;
          // Memory & plans covers the plan library's routes too.
          const plansActive = s.key === "memory" && /^\/p\/[^/]+\/plans(\/|$)/.test(location.pathname);
          return to ? (
            <NavLink
              key={s.key}
              to={to}
              className={({ isActive }) => `nav-item${isActive || plansActive ? " active" : ""}`}
              data-track={`sidebar.${s.key}`}
            >
              <Icon name={SECTION_ICONS[s.key]} />
              <span className="nav-label">{s.label}</span>
              {badge && (
                <span className={`nav-badge tone-${badge.tone}`} title={badge.title} aria-label={`${s.label}: ${badge.title}`}>
                  {badge.label}
                </span>
              )}
            </NavLink>
          ) : (
            <span key={s.key} className="nav-item disabled" title="Choose a project first">
              <Icon name={SECTION_ICONS[s.key]} />
              <span className="nav-label">{s.label}</span>
            </span>
          );
        })}
        <div className="sidebar-foot">
          <NavLink to={paths.settings()} className="nav-item" data-track="sidebar.settings">
            <Icon name="settings" />
            <span className="nav-label">Settings</span>
          </NavLink>
          <button
            type="button"
            className="btn ghost icon-btn"
            onClick={() => openHelp()}
            data-track="sidebar.help"
            title="Keyboard shortcuts (?)"
            aria-label="Keyboard shortcuts"
          >
            <Icon name="keyboard" />
          </button>
          {getToken() && (
            <button
              type="button"
              className="btn ghost icon-btn"
              title="Forget token and sign out of this daemon"
              aria-label="Forget token"
              data-track="sidebar.sign_out"
              onClick={() => {
                qc.clear();
                authStore.forgetToken();
              }}
            >
              <Icon name="signOut" />
            </button>
          )}
        </div>
      </div>
    </nav>
  );
}

/** App-wide actions the shell owns (the command palette lists the same registry). */
function useShellActions() {
  const { scope } = useScope();
  const { project, sessionId } = useParams();
  const navigate = useNavigate();
  const target = project ?? scope;
  // In a session, the captured task opens beside the transcript.
  const inSession = !!sessionId;
  const loops = useWorkLoops();
  const capture = useMemo<AppAction>(
    () => ({
      id: "backlog.capture",
      title: "Quick capture a backlog item…",
      group: "Backlog",
      // Option+letter types characters on macOS: leave text fields alone there.
      shortcut: { code: "KeyN", alt: true, label: CAPTURE_SHORTCUT_LABEL, inEditable: !IS_MAC },
      run: () => openCapture(target ?? null, { beside: inSession }),
    }),
    [target, inSession],
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
  const addProject = useMemo<AppAction>(
    () => ({ id: "projects.add", title: "Add a project…", group: "Projects", run: () => openAddProject() }),
    [],
  );
  const manageProjects = useMemo<AppAction>(
    () => ({ id: "projects.manage", title: "Manage projects", group: "Projects", run: () => navigate(paths.projects()) }),
    [navigate],
  );
  const openFiles = useMemo<AppAction>(
    () => ({
      id: "files.open",
      title: target ? `Browse files in ${target}` : "Browse project files…",
      group: "Files",
      run: () => navigate(paths.files(target ?? null)),
    }),
    [target, navigate],
  );
  const openMemory = useMemo<AppAction>(
    () => ({
      id: "memory.open",
      title: target ? `Open memory & plans for ${target}` : "Open project memory & plans…",
      group: "Memory",
      run: () => navigate(paths.memory(target ?? null)),
    }),
    [target, navigate],
  );
  const openUsage = useMemo<AppAction>(
    () => ({
      id: "usage.open",
      title: target ? `Open usage for ${target}` : "Open usage (all projects)",
      group: "Usage",
      run: () => navigate(paths.usage(target ?? null)),
    }),
    [target, navigate],
  );
  const openUsageAll = useMemo<AppAction | null>(
    () =>
      target
        ? { id: "usage.openAll", title: "Open usage across all projects", group: "Usage", run: () => navigate(paths.usage(null)) }
        : null,
    [target, navigate],
  );
  const openSettings = useMemo<AppAction>(
    () => ({ id: "settings.open", title: "Open settings", group: "Settings", run: () => navigate(paths.settings()) }),
    [navigate],
  );
  const addModel = useMemo<AppAction>(
    () => ({
      id: "settings.addModel",
      title: "Add a model…",
      group: "Settings",
      run: () => {
        requestIntent(SETTINGS_INTENT, "addModel");
        navigate(paths.settings("models"));
      },
    }),
    [navigate],
  );
  const reviewTiers = useMemo<AppAction>(
    () => ({ id: "settings.reviewTiers", title: "Edit review tiers", group: "Settings", run: () => navigate(paths.settings("reviews")) }),
    [navigate],
  );
  const anthropicLogin = useMemo<AppAction>(
    () => ({ id: "settings.anthropicLogin", title: "Connect / reconnect Anthropic…", group: "Settings", run: () => openAnthropicLogin() }),
    [],
  );
  const openPlans = useMemo<AppAction | null>(
    () => (target ? { id: "plans.open", title: `Open the plan library of ${target}`, group: "Memory", run: () => navigate(paths.plans(target)) } : null),
    [target, navigate],
  );
  const groom = useMemo<AppAction | null>(
    () =>
      target
        ? {
            id: "memory.groom",
            title: `Groom memory in ${target} now`,
            group: "Memory",
            keywords: "memory-groom prune",
            run: () => {
              requestIntent(memoryIntentKey(target), "groom");
              navigate(paths.memory(target));
            },
          }
        : null,
    [target, navigate],
  );
  const openBacklog = useMemo<AppAction>(
    () => ({ id: "backlog.open", title: target ? `Open the backlog of ${target}` : "Open a backlog…", group: "Backlog", run: () => navigate(paths.backlog(target ?? null)) }),
    [target, navigate],
  );
  const newTask = useMemo<AppAction | null>(
    () =>
      target
        ? {
            id: "backlog.newTask",
            title: `New task in ${target}…`,
            group: "Backlog",
            keywords: "create add",
            run: () => {
              requestIntent(backlogIntentKey(target), "newTask");
              navigate(paths.backlog(target));
            },
          }
        : null,
    [target, navigate],
  );
  const openLoop = useMemo<AppAction>(
    () => ({ id: "loop.open", title: target ? `Open the work loop of ${target}` : "Open a work loop…", group: "Work loop", run: () => navigate(paths.loop(target ?? null)) }),
    [target, navigate],
  );
  const loopSession = target ? currentSessionId(loops.find((l) => l.project === target)?.loop) : "";
  const followLoop = useMemo<AppAction | null>(
    () =>
      target && loopSession
        ? {
            id: "loop.follow",
            title: `Follow the work loop’s live session in ${target}`,
            group: "Work loop",
            run: () => navigate(paths.session(target, loopSession)),
          }
        : null,
    [target, loopSession, navigate],
  );
  const openWorkstreams = useMemo<AppAction>(
    () => ({
      id: "workstreams.open",
      title: target ? `Open the workstreams of ${target}` : "Open workstreams…",
      group: "Workstreams",
      run: () => navigate(paths.workstreams(target ?? null)),
    }),
    [target, navigate],
  );
  const renameProject = useMemo<AppAction | null>(
    () => (scope ? { id: "projects.rename", title: `Rename project ${scope}…`, group: "Projects", run: () => openRenameProject(scope) } : null),
    [scope],
  );
  const removeProject = useMemo<AppAction | null>(
    () =>
      scope
        ? { id: "projects.remove", title: `Remove ${scope} from ycc…`, group: "Projects", keywords: "deregister delete", run: () => openRemoveProject(scope) }
        : null,
    [scope],
  );
  useAction(openPlans);
  useAction(groom);
  useAction(openBacklog);
  useAction(newTask);
  useAction(openLoop);
  useAction(followLoop);
  useAction(openWorkstreams);
  useAction(renameProject);
  useAction(removeProject);
  useAction(openUsage);
  useAction(openUsageAll);
  useAction(openSettings);
  useAction(addModel);
  useAction(reviewTiers);
  useAction(anthropicLogin);
  useAction(startLoop);
  useAction(stopLoop);
  useAction(spawn);
  useAction(addProject);
  useAction(manageProjects);
  useAction(openFiles);
  useAction(openMemory);
  useActionShortcuts();
}

/** Palette, help, navigation, unread, notification, and account actions. */
function useNavigationActions() {
  const { scope } = useScope();
  const { sessionId } = useParams();
  const navigate = useNavigate();
  const inspector = useInspector();
  const qc = useQueryClient();
  const { feed: scoped, refresh } = useSessionFeed(scope);
  const { feed: all } = useSessionFeed(null);
  const marks = useReadMarks();
  const notify = useNotifyState();
  const active = sessionId ?? null;
  // The latest rows, read when an action runs (keeps the registrations stable).
  const rows = { scoped: scoped?.rows ?? [], all: all?.rows ?? [] };
  const latest = useRef(rows);
  latest.current = rows;
  // The open session at the moment an action runs: read from the URL, which
  // a navigation updates before the shell re-renders (rapid key repeats).
  const activeNow = () => {
    const m = /\/s\/([^/]+)\/?$/.exec(window.location.pathname);
    return m ? decodeURIComponent(m[1]) : null;
  };
  const go = (row: FeedRow | null, none: string) => {
    if (row) navigate(paths.session(row.project, row.session.sessionId));
    else toast(none, "info");
  };
  const fixed = useMemo<AppAction[]>(
    () => [
      { id: "palette.open", title: "Command palette", group: "General", shortcut: SHORTCUTS.palette, run: () => togglePalette() },
      { id: "help.open", title: "Keyboard shortcuts", group: "General", keywords: "help keys", shortcut: SHORTCUTS.help, run: () => openHelp() },
      {
        id: "session.new",
        title: scope ? `New session in ${scope}` : "New session",
        group: "Sessions",
        keywords: "start chat work",
        shortcut: SHORTCUTS.newSession,
        run: () => navigate(paths.newSession(scope)),
      },
      {
        id: "nav.nextSession",
        title: "Next session in the list",
        group: "Navigation",
        shortcut: SHORTCUTS.nextSession,
        run: () => go(adjacentSession(latest.current.scoped, activeNow(), 1), "No next session."),
      },
      {
        id: "nav.prevSession",
        title: "Previous session in the list",
        group: "Navigation",
        shortcut: SHORTCUTS.prevSession,
        run: () => go(adjacentSession(latest.current.scoped, activeNow(), -1), "No previous session."),
      },
      {
        id: "nav.nextNeedsAnswer",
        title: "Next session waiting for an answer",
        group: "Navigation",
        keywords: "question needs answer",
        shortcut: SHORTCUTS.needsAnswer,
        run: () => go(nextNeedsAnswer(latest.current.all, activeNow()), "No other session is waiting for an answer."),
      },
      {
        id: "nav.focusComposer",
        title: "Focus the message box",
        group: "Navigation",
        keywords: "composer input answer type",
        shortcut: SHORTCUTS.composer,
        run: () => {
          if (!focusComposer()) toast("There is no message box on this page.", "info");
        },
      },
      {
        id: "nav.toggleInspector",
        title: "Toggle the inspector pane",
        group: "Navigation",
        keywords: "detail side panel close",
        shortcut: SHORTCUTS.inspector,
        run: () => {
          if (!inspector.toggle()) toast("Nothing to show in the inspector yet.", "info");
        },
      },
      { id: "sessions.refresh", title: "Refresh the session list", group: "Sessions", keywords: "reload", run: refresh },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [scope, navigate, inspector.toggle],
  );
  useEffect(() => {
    const offs = fixed.map((a) => registerAction(a));
    return () => offs.forEach((off) => off());
  }, [fixed]);
  const unreadScoped = rows.scoped.filter((r) => r.session.sessionId !== active && marks.isUnread(r.session)).length;
  const unreadAll = rows.all.filter((r) => r.session.sessionId !== active && marks.isUnread(r.session)).length;
  useAction(
    useMemo<AppAction | null>(
      () =>
        unreadScoped > 0
          ? {
              id: "sessions.markAllRead",
              title: scope ? `Mark all ${scope} sessions read (${unreadScoped})` : `Mark all sessions read (${unreadScoped})`,
              group: "Sessions",
              keywords: "unread clear",
              run: () => marks.markAllRead(latest.current.scoped.map((r) => r.session)),
            }
          : null,
      [unreadScoped, scope, marks],
    ),
  );
  useAction(
    useMemo<AppAction | null>(
      () =>
        scope && unreadAll > unreadScoped
          ? {
              id: "sessions.markAllReadEverywhere",
              title: `Mark sessions in every project read (${unreadAll})`,
              group: "Sessions",
              keywords: "unread clear all projects",
              run: () => marks.markAllRead(latest.current.all.map((r) => r.session)),
            }
          : null,
      [scope, unreadAll, unreadScoped, marks],
    ),
  );
  const notifyAction = useMemo<AppAction | null>(() => {
    if (notify.support === "unsupported") return null;
    if (notify.active)
      return { id: "notify.toggle", title: "Turn desktop notifications off", group: "Notifications", run: () => setNotificationsEnabled(false) };
    if (notify.support === "granted")
      return { id: "notify.toggle", title: "Turn desktop notifications on", group: "Notifications", run: () => setNotificationsEnabled(true) };
    return {
      id: "notify.toggle",
      title: "Enable desktop notifications…",
      group: "Notifications",
      keywords: "alerts permission",
      run: () => {
        // Insecure/denied pages explain why on the settings card.
        if (notify.support === "default") void enableFromGesture();
        else navigate(paths.settings("notifications"));
      },
    };
  }, [notify.support, notify.active, navigate]);
  useAction(notifyAction);
  useAction(
    useMemo<AppAction | null>(
      () => (notify.support === "granted" ? { id: "notify.test", title: "Send a test notification", group: "Notifications", run: sendTestNotification } : null),
      [notify.support],
    ),
  );
  useAction(
    useMemo<AppAction | null>(
      () =>
        getToken()
          ? {
              id: "auth.signOut",
              title: "Forget token and sign out of this daemon",
              group: "General",
              keywords: "logout token",
              run: () => {
                qc.clear();
                authStore.forgetToken();
              },
            }
          : null,
      [qc],
    ),
  );
}

export function Shell() {
  const inspector = useInspector();
  const { sessionId } = useParams();
  const { pathname } = useLocation();
  // The routed surface is the bottom of the analytics view stack.
  useEffect(() => track.route(pathname), [pathname]);
  useShellActions();
  useNavigationActions();
  useLoopWatcher();
  useSessionWatch(sessionId ?? null);
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
      <ProjectDialogs />
      <AnthropicLoginDialog />
      <CommandPalette />
      <HelpOverlay />
      <Toasts />
    </div>
  );
}
