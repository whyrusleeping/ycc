// Pure pieces of project management: the git sync badge (a port of YccKit's
// gitSyncBadge), add-project path checks, the server-side directory picker's
// navigation state, and rewriting routes when a project is renamed or removed.
import type { DirEntry, GitStatus, ListDirResponse } from "../../gen/ycc/v1/ycc_pb";

/**
 * The quiet workspace-sync badge project lists show: `↑2 ↓1 ●`. `?` means
 * remote refs have not been fetched successfully yet or the latest fetch
 * failed; a clean checkout in sync has no badge.
 */
export function gitSyncBadge(status: GitStatus | undefined): string | null {
  if (!status) return null;
  const parts: string[] = [];
  if (status.hasUpstream) {
    if (status.ahead > 0) parts.push(`↑${status.ahead}`);
    if (status.behind > 0) parts.push(`↓${status.behind}`);
  }
  if (status.dirty) parts.push("●");
  if (Number(status.lastFetchUnix) === 0 || status.fetchError) parts.push("?");
  return parts.length ? parts.join(" ") : null;
}

/** A tooltip spelling the badge out. */
export function gitSyncTitle(status: GitStatus | undefined): string {
  if (!status) return "Not a git repository (or status unavailable)";
  const out: string[] = [status.branch ? `On ${status.branch}` : "Detached HEAD"];
  if (status.hasUpstream) {
    if (status.ahead > 0) out.push(`${status.ahead} ahead of upstream`);
    if (status.behind > 0) out.push(`${status.behind} behind upstream`);
    if (status.ahead === 0 && status.behind === 0) out.push("in sync with upstream");
  } else {
    out.push("no upstream");
  }
  if (status.dirty) out.push("uncommitted changes");
  if (status.fetchError) out.push(`last fetch failed: ${status.fetchError}`);
  else if (Number(status.lastFetchUnix) === 0) out.push("not fetched yet");
  return out.join(" · ");
}

/**
 * Whether a typed workspace path is plausibly a daemon-host path: absolute and
 * not the filesystem root. The daemon validates for real; this only gates the
 * button.
 */
export function isPlausiblePath(raw: string): boolean {
  const t = raw.trim();
  return t.startsWith("/") && t !== "/" && !/^\/+$/.test(t);
}

/** The name the daemon derives when none is given: the directory's basename. */
export function derivedName(path: string): string {
  const t = path.trim().replace(/\/+$/, "");
  return t.slice(t.lastIndexOf("/") + 1);
}

/** An absolute child directory of `dir`. */
export function childDir(dir: string, name: string): string {
  return dir === "/" || dir === "" ? `/${name}` : `${dir.replace(/\/+$/, "")}/${name}`;
}

/** Clickable segments of an absolute path: `/home/me` → `/`, `/home`, `/home/me`. */
export function dirCrumbs(path: string): { label: string; path: string }[] {
  if (!path.startsWith("/")) return path ? [{ label: path, path }] : [];
  const out = [{ label: "/", path: "/" }];
  const parts = path.split("/").filter(Boolean);
  parts.forEach((p, i) => out.push({ label: p, path: `/${parts.slice(0, i + 1).join("/")}` }));
  return out;
}

/** The directory picker (a port of YccKit's DirectoryBrowserModel). */
export interface DirPickerState {
  /** The resolved absolute directory shown; "" until the first listing arrives. */
  path: string;
  /** Its parent; "" at the filesystem root (no Up). */
  parent: string;
  entries: DirEntry[];
  /** Likely projects (git repos beside registered ones), from the first listing. */
  suggestions: string[];
  /** The directory being opened, or null. */
  loading: string | null;
  error: string | null;
}

export type DirPickerAction =
  | { type: "open"; path: string }
  | { type: "loaded"; requested: string; response: Pick<ListDirResponse, "path" | "parent" | "entries" | "suggestions">; initial: boolean }
  | { type: "failed"; requested: string; message: string };

export const initialDirPicker: DirPickerState = { path: "", parent: "", entries: [], suggestions: [], loading: null, error: null };

export function dirPickerReducer(state: DirPickerState, action: DirPickerAction): DirPickerState {
  switch (action.type) {
    case "open":
      return { ...state, loading: action.path };
    case "loaded": {
      // A response for a directory the user has already left is dropped.
      if (state.loading !== action.requested) return state;
      const r = action.response;
      return {
        path: r.path,
        parent: r.parent,
        entries: [...r.entries],
        suggestions: action.initial ? [...r.suggestions] : state.suggestions,
        loading: null,
        error: null,
      };
    }
    case "failed":
      if (state.loading !== action.requested) return state;
      // A failed load keeps the current listing.
      return { ...state, loading: null, error: action.message };
  }
}

export function canGoUp(state: DirPickerState): boolean {
  return state.parent !== "";
}

/**
 * Rewrite the current route for a renamed project: `/p/old/…` → `/p/new/…`
 * (null when the route is not under that project).
 */
export function renamedRoute(pathname: string, oldName: string, newName: string): string | null {
  const prefix = `/p/${encodeURIComponent(oldName)}`;
  if (pathname !== prefix && !pathname.startsWith(`${prefix}/`)) return null;
  return `/p/${encodeURIComponent(newName)}${pathname.slice(prefix.length)}`;
}

/** Whether the current route belongs to a project (it must leave once the project is removed). */
export function routeUnderProject(pathname: string, name: string): boolean {
  const prefix = `/p/${encodeURIComponent(name)}`;
  return pathname === prefix || pathname.startsWith(`${prefix}/`);
}
