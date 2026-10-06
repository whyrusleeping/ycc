// Routed main-pane surfaces.
import { Link, Navigate, useLocation, useNavigate, useParams } from "react-router";
import { useEffect, useMemo, type ReactNode } from "react";
import { useProjects, useSessionFeed, useWorkLoops, useWorkstreams } from "../api/queries";
import { errorMessage } from "../api/client";
import { BacklogPage } from "../features/backlog/BacklogPage";
import { projectChoices } from "../features/newSession/model";
import { SessionList } from "../features/sessions/SessionList";
import { SessionView } from "../features/session/SessionView";
import { displayTitle, taskIds } from "../features/sessions/feed";
import { NewSessionPage as NewSession } from "../features/newSession/NewSessionPage";
import { LoopStateBadge, WorkLoopPage } from "../features/workloop/WorkLoopPage";
import { bannerLine, loopState } from "../features/workloop/model";
import { WorkstreamsPage } from "../features/workstreams/WorkstreamsPage";
import { workstreamIndicator } from "../features/workstreams/model";
import { FilesPage } from "../features/files/FilesPage";
import { MemoryPage, PlansPage } from "../features/memory/MemoryPage";
import { ProjectsPage } from "../features/projects/ProjectsPage";
import { lastViewedProject } from "./memory";
import { PROJECT_SECTIONS, paths } from "./paths";
import { useScope } from "./scope";

/** `/`: the daemon-wide Recent sessions page. */
export function HomePage() {
  const { setScope } = useScope();
  useEffect(() => setScope(null), [setScope]);
  return (
    <div className="page">
      <header className="page-head">
        <h1>Recent sessions</h1>
        <Link to={paths.newSession(null)} className="btn primary">
          + New session
        </Link>
      </header>
      <SessionList scope={null} variant="page" />
    </div>
  );
}

/** `/p/:project`: one project's sessions. */
export function ProjectPage() {
  const { project = "" } = useParams();
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  return (
    <div className="page">
      <header className="page-head">
        <h1>{project}</h1>
        <Link to={paths.newSession(project)} className="btn primary">
          + New session
        </Link>
      </header>
      <SessionList scope={project} variant="page" />
    </div>
  );
}

/** `/p/:project/s/:sessionId` and `/s/:sessionId`. */
export function SessionPage() {
  const { project = "", sessionId = "" } = useParams();
  const { feed } = useSessionFeed(null);
  const summary = feed?.rows.find((r) => r.session.sessionId === sessionId)?.session;
  const title = summary ? displayTitle(summary) : sessionId;
  useEffect(() => lastViewedProject.set(project), [project]);
  useEffect(() => {
    document.title = `${title} · ycc`;
    return () => {
      document.title = "ycc";
    };
  }, [title]);
  const focus = useMemo(() => (summary ? taskIds(summary) : []), [summary]);
  return (
    <SessionView key={`${project}\u0000${sessionId}`} project={project} sessionId={sessionId} title={title} focusTasks={focus} />
  );
}

/** `/new` and `/p/:project/new`: start a session (asks for the project when unscoped). */
export function NewSessionPage() {
  const { project } = useParams();
  useEffect(() => {
    document.title = "New session · ycc";
    return () => {
      document.title = "ycc";
    };
  }, []);
  return <NewSession key={project ?? ""} routeProject={project ?? null} />;
}

function useDocumentTitle(title: string) {
  useEffect(() => {
    document.title = `${title} · ycc`;
    return () => {
      document.title = "ycc";
    };
  }, [title]);
}

/**
 * `/p/:project/backlog[/:taskId]`, and the unscoped `/backlog[/:taskId]`,
 * which asks for a project (last viewed first) unless there is only one.
 */
export function BacklogRoutePage() {
  const { project, taskId } = useParams();
  if (project !== undefined) return <ScopedBacklog project={project} taskId={taskId ?? null} />;
  return <UnscopedBacklog taskId={taskId ?? null} />;
}

function ScopedBacklog({ project, taskId }: { project: string; taskId: string | null }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(taskId ? `Task ${taskId} · ${project}` : `Backlog · ${project}`);
  return <BacklogPage key={project} project={project} taskId={taskId} />;
}

function UnscopedBacklog({ taskId }: { taskId: string | null }) {
  useDocumentTitle(taskId ? `Task ${taskId}` : "Backlog");
  return (
    <ProjectChoice
      title="Backlog"
      question="Which project’s backlog?"
      target={(name) => (taskId ? paths.task(name, taskId) : paths.backlog(name))}
      // No registered project: the daemon's own workspace ("" resolves it).
      fallback={() => <BacklogPage project="" taskId={taskId} />}
    />
  );
}

/**
 * An unscoped project surface asks which project (last viewed first), goes
 * straight to the sole project, and renders `fallback` (the daemon's own
 * workspace) when none is registered.
 */
function ProjectChoice({
  title,
  question,
  target,
  fallback,
  extra,
}: {
  title: string;
  question: string;
  target: (project: string) => string;
  fallback: () => ReactNode;
  extra?: (project: string) => ReactNode;
}) {
  const projects = useProjects();
  const navigate = useNavigate();
  const list = projects.data ?? [];
  const choices = useMemo(() => projectChoices(list, lastViewedProject.get()), [list]);
  if (projects.isPending) return <div className="page muted">Loading…</div>;
  if (projects.isError) return <div className="page error">{errorMessage(projects.error)}</div>;
  if (list.length === 0) return <>{fallback()}</>;
  if (list.length === 1) return <Navigate replace to={target(list[0].name)} />;
  const last = lastViewedProject.get();
  const headingId = `ask-${title.toLowerCase().replace(/[^a-z]+/g, "-")}-project`;
  return (
    <div className="page">
      <header className="page-head">
        <h1>{title}</h1>
      </header>
      <section className="project-ask" aria-labelledby={headingId}>
        <h2 id={headingId}>{question}</h2>
        <div className="choice-grid">
          {choices.map((name, i) => {
            const info = list.find((p) => p.name === name);
            return (
              <button
                key={name}
                type="button"
                className="choice-card"
                autoFocus={i === 0}
                onClick={() => navigate(target(name))}
              >
                <span className="choice-title">{name}</span>
                {info?.path && <span className="choice-sub mono">{info.path}</span>}
                {extra?.(name)}
                {i === 0 && name === last && <span className="tag">last viewed</span>}
              </button>
            );
          })}
        </div>
      </section>
    </div>
  );
}

/** `/p/:project/loop` and the unscoped `/loop` (asks which project). */
export function WorkLoopRoutePage() {
  const { project } = useParams();
  if (project !== undefined) return <ScopedLoop project={project} />;
  return <UnscopedLoop />;
}

function ScopedLoop({ project }: { project: string }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(`Work loop · ${project}`);
  return <WorkLoopPage key={project} project={project} />;
}

function LoopChoiceState({ project }: { project: string }) {
  const loops = useWorkLoops();
  const loop = loops.find((l) => l.project === project)?.loop;
  const state = loopState(loop);
  if (state === "none") return <span className="choice-sub muted">No loop yet</span>;
  return (
    <span className="choice-sub">
      <LoopStateBadge state={state} /> <span className="muted">{bannerLine(loop)}</span>
    </span>
  );
}

function UnscopedLoop() {
  useDocumentTitle("Work loop");
  return (
    <ProjectChoice
      title="Work loop"
      question="Which project’s work loop?"
      target={(name) => paths.loop(name)}
      fallback={() => <WorkLoopPage project="" />}
      extra={(name) => <LoopChoiceState project={name} />}
    />
  );
}

/** `/p/:project/workstreams` and the unscoped `/workstreams` (asks which project). */
export function WorkstreamsRoutePage() {
  const { project } = useParams();
  if (project !== undefined) return <ScopedWorkstreams project={project} />;
  return <UnscopedWorkstreams />;
}

function ScopedWorkstreams({ project }: { project: string }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(`Workstreams · ${project}`);
  return <WorkstreamsPage key={project} project={project} />;
}

function WorkstreamsChoiceState({ project }: { project: string }) {
  const all = useWorkstreams("");
  const indicator = workstreamIndicator(all.data?.filter((w) => w.project === project));
  return <span className={`choice-sub${indicator ? "" : " muted"}`}>{indicator ? indicator.title : "None in flight"}</span>;
}

function UnscopedWorkstreams() {
  useDocumentTitle("Workstreams");
  return (
    <ProjectChoice
      title="Workstreams"
      question="Which project’s workstreams?"
      target={(name) => paths.workstreams(name)}
      fallback={() => <WorkstreamsPage project="" />}
      extra={(name) => <WorkstreamsChoiceState project={name} />}
    />
  );
}

/**
 * `/p/:project/files/*` and the unscoped `/files/*` (asks which project,
 * unless `?session=` names the worktree of a default-workspace session).
 */
export function FilesRoutePage() {
  const params = useParams();
  const splat = params["*"] ?? "";
  if (params.project !== undefined) return <ScopedFiles project={params.project} splat={splat} />;
  return <UnscopedFiles splat={splat} />;
}

function ScopedFiles({ project, splat }: { project: string; splat: string }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(`${splat ? splat.split("/").filter(Boolean).pop() : "Files"} · ${project}`);
  return <FilesPage key={project} project={project} splat={splat} />;
}

function UnscopedFiles({ splat }: { splat: string }) {
  const location = useLocation();
  useDocumentTitle("Files");
  if (new URLSearchParams(location.search).get("session")) return <FilesPage project="" splat={splat} />;
  return (
    <ProjectChoice
      title="Files"
      question="Which project’s files?"
      target={(name) => `${paths.files(name, splat)}${location.hash}`}
      fallback={() => <FilesPage project="" splat={splat} />}
    />
  );
}

/** `/p/:project/memory` and the unscoped `/memory` (asks which project). */
export function MemoryRoutePage() {
  const { project } = useParams();
  if (project !== undefined) return <ScopedMemory project={project} />;
  return <UnscopedMemory />;
}

function ScopedMemory({ project }: { project: string }) {
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(`Memory · ${project}`);
  return <MemoryPage key={project} project={project} />;
}

function UnscopedMemory() {
  useDocumentTitle("Memory & plans");
  return (
    <ProjectChoice
      title="Memory & plans"
      question="Which project’s memory?"
      target={(name) => paths.memory(name)}
      fallback={() => <MemoryPage project="" />}
    />
  );
}

/** `/p/:project/plans[/:name]`: the plan library. */
export function PlansRoutePage() {
  const { project = "", name = "" } = useParams();
  const { setScope } = useScope();
  useEffect(() => setScope(project), [project, setScope]);
  useEffect(() => lastViewedProject.set(project), [project]);
  useDocumentTitle(name ? `Plan ${name} · ${project}` : `Plans · ${project}`);
  return <PlansPage key={project} project={project} name={name} />;
}

/** `/projects`: add, rename, and remove registered projects. */
export function ProjectsRoutePage() {
  useDocumentTitle("Projects");
  return <ProjectsPage />;
}

/** A project surface that a later phase fills in. */
export function SectionPlaceholder({ section }: { section: string }) {
  const { project = "" } = useParams();
  const label = PROJECT_SECTIONS.find((s) => s.key === section)?.label ?? section;
  return (
    <div className="page placeholder">
      <h1>
        {label} <span className="muted">· {project}</span>
      </h1>
      <p className="muted">This view arrives in a later phase of the desktop web client.</p>
    </div>
  );
}

export function SettingsPlaceholder() {
  return (
    <div className="page placeholder">
      <h1>Settings</h1>
      <p className="muted">Model, role, and review settings arrive in a later phase of the desktop web client.</p>
    </div>
  );
}

export function NotFoundPage() {
  return (
    <div className="page placeholder">
      <h1>Not found</h1>
      <p>
        <Link to={paths.home()}>Back to recent sessions</Link>
      </p>
    </div>
  );
}
