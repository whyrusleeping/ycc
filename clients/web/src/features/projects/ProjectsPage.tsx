// `/projects`: the registered projects with their git sync state, and the
// add / rename / remove actions (removal only deregisters).
import { Link, useNavigate } from "react-router";
import { useProjects } from "../../api/queries";
import { errorMessage } from "../../api/client";
import { paths } from "../../app/paths";
import type { ProjectInfo } from "../../gen/ycc/v1/ycc_pb";
import { MenuButton } from "../../ui/Menu";
import { gitSyncBadge, gitSyncTitle } from "./model";
import { openAddProject, openRemoveProject, openRenameProject } from "./ProjectDialogs";

export function GitBadge({ project }: { project: ProjectInfo }) {
  const badge = gitSyncBadge(project.git);
  if (!badge) return null;
  return (
    <span className="git-badge" title={gitSyncTitle(project.git)} aria-label={`Git: ${gitSyncTitle(project.git)}`}>
      {badge}
    </span>
  );
}

export function ProjectsPage() {
  const projects = useProjects();
  const navigate = useNavigate();
  const list = projects.data ?? [];
  return (
    <div className="page projects-page">
      <header className="page-head">
        <h1>Projects</h1>
        <button type="button" className="btn primary" onClick={() => openAddProject()}>
          + Add project…
        </button>
      </header>
      {projects.isPending ? (
        <p className="muted">Loading…</p>
      ) : projects.isError ? (
        <p className="error">{errorMessage(projects.error)}</p>
      ) : list.length === 0 ? (
        <div className="empty-state">
          <p>No projects are registered; sessions use the daemon’s startup workspace.</p>
          <button type="button" className="btn primary" onClick={() => openAddProject()}>
            Add a project…
          </button>
        </div>
      ) : (
        <ul className="project-list">
          {list.map((p) => (
            <li key={p.name} className="project-row">
              <div className="project-main">
                <div className="project-name-line">
                  <Link to={paths.project(p.name)} className="project-name">
                    {p.name}
                  </Link>
                  {p.git?.branch && (
                    <span className="mono small muted" title="Current branch">
                      {p.git.branch}
                    </span>
                  )}
                  <GitBadge project={p} />
                  {p.needsOnboarding && <span className="tag warn">needs onboarding</span>}
                </div>
                <div className="mono small muted project-path">{p.path}</div>
              </div>
              <nav className="project-links" aria-label={`${p.name} surfaces`}>
                <Link to={paths.backlog(p.name)} className="btn ghost small">
                  Backlog
                </Link>
                <Link to={paths.files(p.name)} className="btn ghost small">
                  Files
                </Link>
                <Link to={paths.memory(p.name)} className="btn ghost small">
                  Memory
                </Link>
              </nav>
              <MenuButton
                label="⋯"
                ariaLabel={`Actions for ${p.name}`}
                items={[
                  { label: "New session…", onSelect: () => navigate(paths.newSession(p.name)) },
                  { label: "Rename…", onSelect: () => openRenameProject(p.name) },
                  {
                    label: "Remove from ycc…",
                    danger: true,
                    title: "Deregister the project; nothing on disk is deleted",
                    onSelect: () => openRemoveProject(p.name),
                  },
                ]}
              />
            </li>
          ))}
        </ul>
      )}
      <p className="muted small projects-foot">
        Removing a project only deregisters it from ycc — its directory, files, and session logs are never deleted.
      </p>
    </div>
  );
}
