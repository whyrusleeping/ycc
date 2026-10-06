// Project management dialogs, opened from anywhere (sidebar menu, projects
// page, new-session picker, command palette): add a project through a
// server-side directory picker (ListDir), rename one, or remove one (which
// only deregisters it). One host component in the shell renders whichever is
// open; mutations refresh every project-keyed cache and keep the current
// route, sidebar scope, and last-viewed project pointing at the right name.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useReducer, useRef, useState, useSyncExternalStore, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useProjects } from "../../api/queries";
import { lastViewedProject } from "../../app/memory";
import { paths } from "../../app/paths";
import { useScope } from "../../app/scope";
import type { ProjectInfo } from "../../gen/ycc/v1/ycc_pb";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import {
  canGoUp,
  childDir,
  derivedName,
  dirCrumbs,
  dirPickerReducer,
  initialDirPicker,
  isPlausiblePath,
  renamedRoute,
  routeUnderProject,
} from "./model";

type DialogState =
  | { kind: "add"; onAdded?: (p: ProjectInfo) => void; navigate: boolean }
  | { kind: "rename"; name: string }
  | { kind: "remove"; name: string }
  | null;

let state: DialogState = null;
const listeners = new Set<() => void>();
function set(next: DialogState) {
  state = next;
  for (const l of listeners) l();
}

/**
 * Open the add-project dialog. `onAdded` receives the registered project
 * (e.g. the new-session page selects it); without it the app opens the new
 * project.
 */
export function openAddProject(onAdded?: (p: ProjectInfo) => void) {
  set({ kind: "add", onAdded, navigate: !onAdded });
}
export function openRenameProject(name: string) {
  set({ kind: "rename", name });
}
export function openRemoveProject(name: string) {
  set({ kind: "remove", name });
}

function useDialogState(): DialogState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
  );
}

function expireOn(err: unknown): boolean {
  if (isUnauthorized(err)) {
    authStore.expire();
    return true;
  }
  return false;
}

/**
 * Everything keyed by a project name may now be stale. The project list
 * refreshes first, so the per-project queries that refresh next are the new
 * names' (a stale name's query would fail as an unknown project).
 */
async function invalidateProjectCaches(qc: ReturnType<typeof useQueryClient>) {
  await qc.invalidateQueries({ queryKey: queryKeys.projects });
  void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
  void qc.invalidateQueries({ queryKey: queryKeys.workstreamsAll });
  void qc.invalidateQueries({ queryKey: queryKeys.workLoopAll });
}

export function ProjectDialogs() {
  const d = useDialogState();
  const close = useCallback(() => set(null), []);
  if (!d) return null;
  if (d.kind === "add") return <AddProjectDialog onAdded={d.onAdded} navigateTo={d.navigate} onClose={close} />;
  if (d.kind === "rename") return <RenameProjectDialog key={d.name} name={d.name} onClose={close} />;
  return <RemoveProjectDialog key={d.name} name={d.name} onClose={close} />;
}

function AddProjectDialog({
  onAdded,
  navigateTo,
  onClose,
}: {
  onAdded?: (p: ProjectInfo) => void;
  navigateTo: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { setScope } = useScope();
  const [picker, dispatch] = useReducer(dirPickerReducer, initialDirPicker);
  const [path, setPath] = useState("");
  const [name, setName] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pathEdited = useRef(false);
  const list = useRef<HTMLUListElement>(null);

  const load = useCallback(async (dir: string, initial = false) => {
    dispatch({ type: "open", path: dir });
    try {
      const response = await client.listDir({ path: dir, suggest: initial });
      dispatch({ type: "loaded", requested: dir, response, initial });
    } catch (err) {
      if (expireOn(err)) return;
      dispatch({ type: "failed", requested: dir, message: errorMessage(err, "Couldn’t list that directory.") });
    }
  }, []);
  // The first listing resolves the daemon user's home directory.
  useEffect(() => {
    void load("", true);
  }, [load]);
  // The path field follows the browser until the user types their own.
  useEffect(() => {
    if (picker.path && !pathEdited.current) setPath(picker.path);
  }, [picker.path]);
  useEffect(() => {
    // Braced: scroll methods return a promise in newer browsers, and an
    // effect must not return anything but a cleanup.
    list.current?.scrollTo({ top: 0 });
  }, [picker.path]);

  const browse = (dir: string) => {
    pathEdited.current = false;
    void load(dir);
  };
  const ok = isPlausiblePath(path) && !submitting;
  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!ok) return;
    setSubmitting(true);
    setError(null);
    try {
      const resp = await client.addProject({ path: path.trim(), name: name.trim() });
      const p = resp.project;
      // List it at once (the sidebar drops a scope it cannot find); the refetch confirms.
      if (p) qc.setQueryData<ProjectInfo[]>(queryKeys.projects, (list) => (list ? [...list.filter((x) => x.name !== p.name), p] : list));
      void invalidateProjectCaches(qc);
      onClose();
      if (!p) return;
      toast(`Added project ${p.name}.`, "info");
      if (onAdded) onAdded(p);
      else if (navigateTo) {
        setScope(p.name);
        navigate(paths.project(p.name));
      }
    } catch (err) {
      if (expireOn(err)) return;
      setError(errorMessage(err, "The project could not be added."));
      setSubmitting(false);
    }
  };

  const loading = picker.loading !== null;
  return (
    <Modal open onClose={onClose} title="Add project" className="add-project-dialog">
      <form className="add-project" onSubmit={(e) => void submit(e)}>
        <p className="muted small">
          Register a workspace directory on the machine the daemon runs on (not this browser’s). Browse to it, or type its
          absolute path.
        </p>
        {picker.suggestions.length > 0 && (
          <section aria-labelledby="ap-suggest">
            <div className="label" id="ap-suggest">
              Suggestions
            </div>
            <div className="suggestions">
              {picker.suggestions.map((s) => (
                <button
                  key={s}
                  type="button"
                  className={`chip suggestion mono${path === s ? " active" : ""}`}
                  title={`Use ${s}`}
                  onClick={() => {
                    pathEdited.current = true;
                    setPath(s);
                  }}
                >
                  {s}
                </button>
              ))}
            </div>
          </section>
        )}
        <section className="dir-picker" aria-label="Browse the daemon host’s directories">
          <div className="dir-picker-head">
            <button
              type="button"
              className="btn ghost small"
              disabled={!canGoUp(picker) || loading}
              onClick={() => browse(picker.parent)}
              aria-label="Parent directory"
              title="Parent directory"
            >
              ↑ Up
            </button>
            <nav className="crumbs mono small" aria-label="Current directory">
              {picker.path ? (
                dirCrumbs(picker.path).map((c, i, all) => (
                  <span key={c.path} className="crumb">
                    {i === all.length - 1 ? (
                      <span aria-current="location">{c.label}</span>
                    ) : (
                      <button type="button" className="link" disabled={loading} onClick={() => browse(c.path)}>
                        {c.label}
                      </button>
                    )}
                    {i > 0 && i < all.length - 1 && <span className="crumb-sep">/</span>}
                  </span>
                ))
              ) : (
                <span className="muted">Server home</span>
              )}
            </nav>
            <button type="button" className="btn ghost small" disabled={loading} onClick={() => browse("")} title="The daemon user’s home directory">
              Home
            </button>
          </div>
          {picker.error && (
            <div className="error small dir-picker-error" role="alert">
              {picker.error}
            </div>
          )}
          <ul className="dir-entries" ref={list} aria-busy={loading}>
            {picker.entries.map((e) => (
              <li key={e.name} className={e.isRegistered ? "registered" : undefined}>
                <button
                  type="button"
                  className="dir-entry-btn"
                  disabled={loading}
                  onClick={() => browse(childDir(picker.path, e.name))}
                  title={e.isRegistered ? "Already a registered project. Opens this directory." : "Open this directory"}
                >
                  <span className={`entry-icon${e.isGitRepo ? " git" : ""}`} aria-hidden>
                    ▸
                  </span>
                  <span className="dir-name">{e.name}</span>
                  {e.isGitRepo && <span className="tag git-tag">git</span>}
                  {e.isRegistered && <span className="tag">registered</span>}
                </button>
              </li>
            ))}
            {!loading && picker.path && picker.entries.length === 0 && <li className="muted small pad">No subdirectories</li>}
            {loading && <li className="muted small pad">Loading…</li>}
          </ul>
        </section>
        <label className="field-label">
          Workspace path
          <input
            type="text"
            className="field mono"
            value={path}
            placeholder="/home/me/code/project"
            spellCheck={false}
            autoComplete="off"
            disabled={submitting}
            onChange={(e) => {
              pathEdited.current = true;
              setPath(e.target.value);
            }}
          />
        </label>
        <label className="field-label">
          Name (optional)
          <input
            type="text"
            className="field"
            value={name}
            placeholder={path ? derivedName(path) || "Derived from the folder name" : "Derived from the folder name"}
            spellCheck={false}
            autoComplete="off"
            disabled={submitting}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <div className="dialog-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={!ok} title={isPlausiblePath(path) ? undefined : "Choose an absolute directory path"}>
            {submitting ? "Adding…" : "Add project"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function RenameProjectDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const { scope, setScope } = useScope();
  const projects = useProjects();
  const [value, setValue] = useState(name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const next = value.trim();
  const taken = next !== name && (projects.data ?? []).some((p) => p.name === next);
  const ok = next !== "" && next !== name && !taken && !busy;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ok) return;
    setBusy(true);
    setError(null);
    try {
      const resp = await client.renameProject({ name, newName: next });
      const renamed = resp.project?.name ?? next;
      // Swap the name in the cached list at once; the refetch confirms.
      qc.setQueryData<ProjectInfo[]>(queryKeys.projects, (list) =>
        list?.map((p) => (p.name === name ? (resp.project ?? { ...p, name: renamed }) : p)),
      );
      void invalidateProjectCaches(qc);
      if (scope === name) setScope(renamed);
      if (lastViewedProject.get() === name) lastViewedProject.set(renamed);
      const route = renamedRoute(location.pathname, name, renamed);
      if (route) navigate(route + location.search + location.hash, { replace: true });
      toast(`Renamed ${name} to ${renamed}.`, "info");
      onClose();
    } catch (err) {
      if (expireOn(err)) return;
      setError(errorMessage(err, "The project could not be renamed."));
      setBusy(false);
    }
  };
  return (
    <Modal open onClose={onClose} title={`Rename ${name}`} className="rename-project-dialog">
      <form onSubmit={(e) => void submit(e)} className="add-project">
        <label className="field-label">
          New name
          <input
            type="text"
            className="field"
            value={value}
            autoFocus
            spellCheck={false}
            autoComplete="off"
            disabled={busy}
            onFocus={(e) => e.currentTarget.select()}
            onChange={(e) => setValue(e.target.value)}
          />
        </label>
        {taken && <p className="error small">Another project is already called {next}.</p>}
        <p className="muted small">The workspace, its sessions, and its workstreams follow the new name; only the label changes.</p>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <div className="dialog-actions">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={!ok}>
            {busy ? "Renaming…" : "Rename"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function RemoveProjectDialog({ name, onClose }: { name: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const { scope, setScope } = useScope();
  const projects = useProjects();
  const path = projects.data?.find((p) => p.name === name)?.path ?? "";
  const busy = useRef(false);
  const remove = async () => {
    if (busy.current) return;
    busy.current = true;
    try {
      await client.removeProject({ name });
      // Drop it from the cached list at once; the refetch confirms.
      qc.setQueryData<ProjectInfo[]>(queryKeys.projects, (list) => list?.filter((p) => p.name !== name));
      void invalidateProjectCaches(qc);
      if (scope === name) setScope(null);
      if (lastViewedProject.get() === name) lastViewedProject.clear();
      if (routeUnderProject(location.pathname, name)) navigate(paths.home(), { replace: true });
      toast(`Removed ${name} from ycc. Its directory was not touched.`, "info");
      onClose();
    } catch (err) {
      busy.current = false;
      if (expireOn(err)) return;
      toast(`Couldn’t remove ${name}: ${errorMessage(err)}`);
      onClose();
    }
  };
  return (
    <ConfirmDialog
      open
      title={`Remove ${name}?`}
      body={`This only deregisters the project from ycc. Nothing is deleted: the directory${path ? ` ${path}` : ""}, its files, git history, backlog, and session logs stay on disk, and you can add it again later. Live sessions keep running.`}
      confirmLabel="Remove project"
      danger
      onCancel={onClose}
      onConfirm={() => void remove()}
    />
  );
}
