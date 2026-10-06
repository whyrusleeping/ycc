// URL paths for every surface. Routes are real paths so history, reload, tabs,
// and bookmarks work; build links only through these helpers.
const enc = encodeURIComponent;

/** Project-scoped sidebar surfaces (usage arrives in a later phase). */
export const PROJECT_SECTIONS = [
  { key: "backlog", label: "Backlog" },
  { key: "loop", label: "Work loop" },
  { key: "workstreams", label: "Workstreams" },
  { key: "usage", label: "Usage" },
  { key: "files", label: "Files" },
  { key: "memory", label: "Memory & plans" },
] as const;

export type ProjectSection = (typeof PROJECT_SECTIONS)[number]["key"];

export const paths = {
  home: () => "/",
  project: (project: string) => `/p/${enc(project)}`,
  session: (project: string, sessionId: string) =>
    project ? `/p/${enc(project)}/s/${enc(sessionId)}` : `/s/${enc(sessionId)}`,
  newSession: (project?: string | null) => (project ? `/p/${enc(project)}/new` : "/new"),
  section: (project: string, section: ProjectSection) => `/p/${enc(project)}/${section}`,
  /** The backlog browser; unscoped ("" / null) asks for a project (or uses the sole/default one). */
  backlog: (project?: string | null) => (project ? `/p/${enc(project)}/backlog` : "/backlog"),
  task: (project: string, id: string) =>
    project ? `/p/${enc(project)}/backlog/${enc(id)}` : `/backlog/${enc(id)}`,
  /** The work loop; unscoped asks for a project (or uses the sole/default one). */
  loop: (project?: string | null) => (project ? `/p/${enc(project)}/loop` : "/loop"),
  /** Parallel workstreams; unscoped asks for a project (or uses the sole/default one). */
  workstreams: (project?: string | null) => (project ? `/p/${enc(project)}/workstreams` : "/workstreams"),
  /**
   * The file browser at a root-relative path ("" is the root). `session`
   * resolves it against that session's live worktree; `lines` targets a line
   * range (as a `#L12-L20` fragment). Unscoped asks for a project (unless a
   * session names the worktree: sessions of the daemon's default workspace).
   */
  files: (project?: string | null, path = "", opts: { session?: string; lines?: { start: number; end: number } | null } = {}) => {
    const rel = path
      .split("/")
      .filter(Boolean)
      .map(enc)
      .join("/");
    let out = project ? `/p/${enc(project)}/files${rel ? `/${rel}` : ""}` : `/files${rel ? `/${rel}` : ""}`;
    if (opts.session) out += `?session=${enc(opts.session)}`;
    if (opts.lines) out += `#L${opts.lines.start}${opts.lines.end > opts.lines.start ? `-L${opts.lines.end}` : ""}`;
    return out;
  },
  /** Project memory; unscoped asks for a project. */
  memory: (project?: string | null) => (project ? `/p/${enc(project)}/memory` : "/memory"),
  /** The plan library, or one plan. */
  plans: (project: string, name?: string) =>
    `/p/${enc(project)}/plans${name ? `/${enc(name)}` : ""}`,
  /** Registered projects: add, rename, remove. */
  projects: () => "/projects",
  settings: () => "/settings",
};

/** Sections that ask which project when opened unscoped (the rest need one). */
export const UNSCOPED_SECTIONS: readonly ProjectSection[] = ["backlog", "loop", "workstreams", "files", "memory"];

/** The unscoped (or scoped) route of a section that has one. */
export function sectionPath(section: ProjectSection, project: string | null): string | null {
  if (project) return paths.section(project, section);
  switch (section) {
    case "backlog":
      return paths.backlog(null);
    case "loop":
      return paths.loop(null);
    case "workstreams":
      return paths.workstreams(null);
    case "files":
      return paths.files(null);
    case "memory":
      return paths.memory(null);
    default:
      return null;
  }
}
