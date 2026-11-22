// Command palette entries (pure): sessions from the Recent feed, full-text
// message hits, backlog tasks, projects, pages, a file path, and every
// registered action, plus the query prefixes that narrow to one kind and the
// empty-query default list.
import type { BacklogTaskSummary, ProjectInfo, SessionSearchHit, SessionSearchSpan } from "../../gen/ycc/v1/ycc_pb";
import type { AppAction } from "../../app/actions";
import { PROJECT_SECTIONS, paths, sectionPath } from "../../app/paths";
import { SETTINGS_SECTIONS } from "../settings/sections";
import { statusLabel } from "../backlog/model";
import { displayProject, displayTitle, lifecycleLabel, needsAnswer, taskIds, type FeedRow } from "../sessions/feed";
import { isInboxSession } from "../sessions/attention";
import { visualOrder } from "../sessions/navigation";
import { hitKindLabel, hitPath, snippetText } from "../sessions/messageHits";
import { rank, type FuzzyMatch } from "./fuzzy";

export type PaletteKind = "session" | "message" | "task" | "project" | "page" | "action" | "file";

export interface PaletteItem {
  id: string;
  kind: PaletteKind;
  /** Shown and matched (highlighted). */
  title: string;
  /** Matched too, at a discount (project, ids, keywords); not shown. */
  secondary: string;
  /** Right-hand detail, e.g. "alpha · 5m ago". */
  detail?: string;
  /** Message hits show their snippet (matched terms highlighted) as the title. */
  snippet?: readonly SessionSearchSpan[];
  /** A marker before the title. */
  marker?: "needs" | "unread" | null;
  /** Keyboard shortcut label. */
  shortcut?: string;
  /** Navigate here… */
  to?: string;
  /** …or run this. */
  run?: () => void;
  boost?: number;
}

export type PaletteMode = "all" | "actions" | "sessions" | "tasks";

export const PALETTE_PREFIXES: { prefix: string; mode: PaletteMode; label: string }[] = [
  { prefix: ">", mode: "actions", label: "actions" },
  { prefix: "@", mode: "sessions", label: "sessions" },
  { prefix: "#", mode: "tasks", label: "tasks" },
];

export function parseQuery(raw: string): { mode: PaletteMode; text: string } {
  const t = raw.trimStart();
  for (const p of PALETTE_PREFIXES) if (t.startsWith(p.prefix)) return { mode: p.mode, text: t.slice(p.prefix.length).trim() };
  return { mode: "all", text: raw.trim() };
}

export interface PaletteSources {
  rows: readonly FeedRow[];
  activeSessionId: string | null;
  isUnread: (row: FeedRow) => boolean;
  relativeTime: (iso: string) => string;
  tasks: readonly { project: string; tasks: readonly BacklogTaskSummary[] }[];
  projects: readonly ProjectInfo[];
  actions: readonly AppAction[];
  /** The route's (or the sidebar's) project for project pages. */
  target: string | null;
  shortcutLabel: (a: AppAction) => string | undefined;
  loopSessionIds?: ReadonlySet<string>;
  /** SearchSessions hits for the current query in one project. */
  messages?: { project: string; hits: readonly SessionSearchHit[] };
}

export function sessionItems(src: PaletteSources, personalOnly = false): PaletteItem[] {
  const rows = personalOnly ? src.rows.filter((r) => isInboxSession(r.session, src.loopSessionIds?.has(r.session.sessionId))) : src.rows;
  return visualOrder(rows).map((row) => {
    const s = row.session;
    const project = displayProject(row);
    const waiting = needsAnswer(s);
    const unread = !waiting && isInboxSession(s, src.loopSessionIds?.has(s.sessionId)) && src.isUnread(row);
    const when = src.relativeTime(s.lastActivity || s.startedAt);
    const life = lifecycleLabel(s);
    return {
      id: `session:${row.project}:${s.sessionId}`,
      kind: "session",
      title: displayTitle(s),
      secondary: [project, s.sessionId, s.mode, ...taskIds(s)].filter(Boolean).join(" "),
      detail: [project, life, when].filter(Boolean).join(" · "),
      marker: waiting ? "needs" : unread ? "unread" : null,
      to: paths.session(row.project, s.sessionId),
      boost: (waiting ? 3 : unread ? 1 : 0) + (s.sessionId === src.activeSessionId ? -4 : 0),
    } satisfies PaletteItem;
  });
}

export function messageItems(src: PaletteSources): PaletteItem[] {
  const m = src.messages;
  if (!m) return [];
  return m.hits.flatMap((hit) => {
    const s = hit.session;
    if (!s) return [];
    const when = src.relativeTime(hit.timestamp || s.lastActivity || s.startedAt);
    return [
      {
        id: `message:${m.project}:${s.sessionId}`,
        kind: "message",
        title: snippetText(hit.snippet),
        secondary: displayTitle(s),
        // Who said it and when stay visible; the session title truncates.
        detail: [hitKindLabel(hit.kind), when, displayTitle(s)].filter(Boolean).join(" · "),
        snippet: hit.snippet,
        to: hitPath(m.project, hit),
      } satisfies PaletteItem,
    ];
  });
}

export function taskItems(src: PaletteSources): PaletteItem[] {
  const out: PaletteItem[] = [];
  const many = src.tasks.length > 1;
  for (const { project, tasks } of src.tasks) {
    for (const t of tasks) {
      const done = t.status === "done";
      out.push({
        id: `task:${project}:${t.id}`,
        kind: "task",
        title: `${t.id} ${t.title}`,
        secondary: `${project} ${t.status}`,
        detail: [many ? project : "", statusLabel(t.status)].filter(Boolean).join(" · "),
        to: paths.task(project, t.id),
        boost: done ? -6 : t.status === "in_progress" ? 1 : 0,
      });
    }
  }
  return out;
}

export function projectItems(src: PaletteSources): PaletteItem[] {
  const out: PaletteItem[] = [];
  for (const p of src.projects) {
    out.push({ id: `project:${p.name}`, kind: "project", title: p.name, secondary: `project ${p.path}`, detail: p.path, to: paths.project(p.name), boost: 2 });
    for (const s of PROJECT_SECTIONS) {
      const to = sectionPath(s.key, p.name);
      if (to) out.push({ id: `project:${p.name}:${s.key}`, kind: "page", title: `${p.name} › ${s.label}`, secondary: "project page", to, boost: -1 });
    }
    out.push({ id: `project:${p.name}:plans`, kind: "page", title: `${p.name} › Plans`, secondary: "plan library", to: paths.plans(p.name), boost: -1 });
    if (p.sessionCoordination) {
      out.push({ id: `project:${p.name}:live`, kind: "page", title: `${p.name} › Live sessions`, secondary: "notices notice board coordination", to: paths.coordination(p.name), boost: -1 });
    }
  }
  return out;
}

export function pageItems(src: PaletteSources): PaletteItem[] {
  const t = src.target;
  const out: PaletteItem[] = [{ id: "page:home", kind: "page", title: "Recent sessions", secondary: "home all projects feed", to: paths.home() }];
  for (const s of PROJECT_SECTIONS) {
    const to = sectionPath(s.key, t);
    if (to) out.push({ id: `page:${s.key}`, kind: "page", title: s.label, secondary: t ? `${t} page` : "page", detail: t ?? undefined, to });
  }
  if (t) out.push({ id: "page:plans", kind: "page", title: "Plans", secondary: "plan library", detail: t, to: paths.plans(t) });
  out.push({ id: "page:projects", kind: "page", title: "Projects", secondary: "manage registered projects", to: paths.projects() });
  out.push({ id: "page:settings", kind: "page", title: "Settings", secondary: "preferences configuration", to: paths.settings() });
  for (const s of SETTINGS_SECTIONS) {
    out.push({ id: `page:settings:${s.id}`, kind: "page", title: `Settings › ${s.title}`, secondary: "settings", to: paths.settings(s.id), boost: -1 });
  }
  return out.map((i) => ({ ...i, boost: (i.boost ?? 0) + 3 }));
}

export function actionItems(src: PaletteSources): PaletteItem[] {
  return src.actions
    .filter((a) => a.id !== "palette.open")
    .map((a) => ({
      id: `action:${a.id}`,
      kind: "action",
      title: a.title,
      secondary: [a.group, a.keywords].filter(Boolean).join(" "),
      detail: a.group,
      shortcut: src.shortcutLabel(a),
      run: a.run,
      boost: 4,
    }));
}

/** A path-like query opens that file (a trailing :12 or :12-20 targets lines). */
export function fileItem(text: string, target: string | null): PaletteItem | null {
  const m = /^([\w@.~+-]+(?:\/[\w@.~+-]+)*\/?|[\w@~+-]*\.[\w]+)(?::(\d+)(?:-(\d+))?)?$/.exec(text.trim());
  if (!m || !(m[1].includes("/") || /\.\w+$/.test(m[1]))) return null;
  const path = m[1].replace(/^\.\//, "");
  const start = m[2] ? Number(m[2]) : 0;
  const end = m[3] ? Number(m[3]) : start;
  const lines = start ? { start, end: Math.max(start, end) } : null;
  return {
    id: `file:${path}`,
    kind: "file",
    title: `Open file ${path}${lines ? `:${lines.start}${lines.end > lines.start ? `-${lines.end}` : ""}` : ""}`,
    secondary: path,
    detail: target ?? "choose project",
    to: paths.files(target, path, { lines }),
    boost: -2,
  };
}

const DEFAULT_SESSIONS = 6;
/** Message hits follow this many of the best fuzzy matches. */
const TOP_MATCHES = 5;

/** The palette's results for a query: ranked, or a short default list when empty. */
export function paletteResults(src: PaletteSources, rawQuery: string, limit = 60): { item: PaletteItem; match: FuzzyMatch | null }[] {
  const { mode, text } = parseQuery(rawQuery);
  const sessions = mode === "all" || mode === "sessions" ? sessionItems(src, mode === "all" && !text) : [];
  const tasks = mode === "all" || mode === "tasks" ? taskItems(src) : [];
  const actions = mode === "all" || mode === "actions" ? actionItems(src) : [];
  const pages = mode === "all" ? [...pageItems(src), ...projectItems(src)] : [];
  if (!text) {
    if (mode === "sessions") return sessions.slice(0, limit).map((item) => ({ item, match: null }));
    if (mode === "tasks") return tasks.filter((t) => !t.boost || t.boost > -6).slice(0, limit).map((item) => ({ item, match: null }));
    if (mode === "actions") return actions.map((item) => ({ item, match: null }));
    // Waiting and unread sessions, then the most recent, then every action and the main pages.
    const flagged = sessions.filter((s) => s.marker);
    const recent = sessions.filter((s) => !s.marker && s.boost !== -4).slice(0, Math.max(0, DEFAULT_SESSIONS - flagged.length));
    const main = pageItems(src).filter((p) => !p.id.startsWith("page:settings:"));
    return [...flagged, ...recent, ...actions, ...main].slice(0, limit).map((item) => ({ item, match: null }));
  }
  const file = mode === "all" ? fileItem(text, src.target) : null;
  const messages = mode === "all" || mode === "sessions" ? messageItems(src).slice(0, limit) : [];
  const ranked = rank([...actions, ...sessions, ...pages, ...tasks], text, (i) => ({ title: i.title, secondary: i.secondary, boost: i.boost }), limit - messages.length);
  const fuzzy = ranked.map((r) => ({ item: r.item, match: r.match as FuzzyMatch | null }));
  // Full-text hits are already ranked by the daemon: they follow the best few
  // fuzzy matches rather than sinking below a long tail of weak ones.
  const out = [...fuzzy.slice(0, TOP_MATCHES), ...messages.map((item) => ({ item, match: null })), ...fuzzy.slice(TOP_MATCHES)];
  // A query shaped like a path means that file: offer it first.
  if (file) out.unshift({ item: file, match: null });
  return out;
}
