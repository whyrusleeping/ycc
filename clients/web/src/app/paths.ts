// URL paths for every surface. Routes are real paths so history, reload, tabs,
// and bookmarks work; build links only through these helpers.
const enc = encodeURIComponent;

/** Project-scoped sidebar surfaces (the backlog is live; later phases fill in the rest). */
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
  settings: () => "/settings",
};
