// Message search helpers (pure): which project to search, the queries worth
// sending, and how a SearchSessions hit is labelled and linked.
import { paths } from "../../app/paths";
import type { ProjectInfo, SearchSessionsResponse, SessionSearchHit, SessionSearchSpan } from "../../gen/ycc/v1/ycc_pb";

/** Shorter queries are not sent: a one-letter prefix matches nearly everything. */
export const MIN_SEARCH_QUERY = 2;

export function searchableQuery(query: string): string {
  const q = query.trim();
  return q.length >= MIN_SEARCH_QUERY ? q : "";
}

/**
 * The project message search covers: the preferred one (the route's or the
 * list's scope, else the last viewed) when it is registered, else the sole
 * registered project, else none.
 */
export function searchProject(preferred: string | null | undefined, projects: readonly ProjectInfo[] | undefined): string | null {
  if (!projects?.length) return null;
  if (preferred && projects.some((p) => p.name === preferred)) return preferred;
  return projects.length === 1 ? projects[0].name : null;
}

export const HIT_KIND_LABEL: Record<string, string> = {
  user: "You",
  assistant: "Agent",
  question: "Question",
  answer: "Answer",
};

export function hitKindLabel(kind: string): string {
  return HIT_KIND_LABEL[kind] ?? kind;
}

/** Where a hit opens: its session, scrolled to the matching row. */
export function hitPath(project: string, hit: SessionSearchHit): string {
  return paths.session(project, hit.session?.sessionId ?? "", hit.rowId);
}

export function snippetText(spans: readonly SessionSearchSpan[]): string {
  return spans.map((s) => s.text).join("");
}

/** Says the index is still building (results so far may be incomplete); null when complete. */
export function indexingNote(resp: SearchSessionsResponse | undefined): string | null {
  if (!resp?.indexing) return null;
  const progress = resp.totalSessions > 0 ? ` (${resp.indexedSessions} of ${resp.totalSessions} sessions)` : "";
  return `Still indexing session history${progress}; results may be incomplete.`;
}
