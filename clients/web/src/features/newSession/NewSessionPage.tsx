// The new-session page: which project (asked when starting from the unscoped
// Recent feed, last-viewed project first), mode with descriptions, preset
// suggestions that adopt a mode and seed the prompt, an optional per-session
// coordinator model, and a multiline prompt with pictures → StartSession.
// On success it navigates straight into the live session.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { Link, useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useModels, useModes, useProjects } from "../../api/queries";
import { paths } from "../../app/paths";
import { track, useFlow, type Via } from "../../app/analytics";
import { lastMode, lastViewedProject } from "../../app/memory";
import { openAddProject } from "../projects/ProjectDialogs";
import { AttachButton, PictureStrip, filesFrom, loadPictures, revokePictures, useDropZone } from "../attachments/pictures";
import { MAX_PICTURES } from "../attachments/attachments";
import type { Preset } from "../../gen/ycc/v1/ycc_pb";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { NewSessionDraftStore } from "./drafts";
import {
  applyPreset,
  buildStartRequest,
  canStart,
  initialMode,
  initialProject,
  modelChoices,
  projectChoices,
  promptIsOptional,
  presetNeedsConfirmation,
  suggestedPresets,
  withMode,
  type NewSessionDraft,
} from "./model";

/** The project picker's "Add project…" entry (never a project name: names cannot hold NUL). */
const ADD_PROJECT = "\u0000add";
const draftStore = new NewSessionDraftStore(loadPictures, revokePictures);

export function NewSessionPage({ routeProject }: { routeProject: string | null }) {
  const projects = useProjects();
  const modes = useModes();
  const models = useModels("");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const saved = useSyncExternalStore(draftStore.subscribe, draftStore.getSnapshot);
  const { draft, projectSeeded, starting } = saved;
  const setDraft = draftStore.update;
  const pictures = {
    pictures: draft.pictures,
    loading: saved.pendingPictures > 0,
    error: saved.pictureError,
    full: draft.pictures.length >= MAX_PICTURES,
    add: draftStore.add,
    remove: draftStore.remove,
  };
  const area = useRef<HTMLTextAreaElement>(null);
  const [pendingPreset, setPendingPreset] = useState<Preset | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Leaving without starting records new_session.cancel.
  useFlow("new_session");

  // Seed the project once the list arrives (route project, or the sole one).
  useEffect(() => {
    if (projectSeeded || !projects.data) return;
    draftStore.seedProject(initialProject(routeProject, projects.data));
  }, [projects.data, projectSeeded, routeProject]);

  // Keep a valid mode selected (the remembered one when it still exists).
  useEffect(() => {
    const list = modes.data?.modes;
    if (!list?.length) return;
    setDraft((d) => (d.mode && list.some((m) => m.name === d.mode) ? d : { ...d, mode: initialMode(list, lastMode.get()) }));
  }, [modes.data]);

  const choice = modelChoices(models.data);
  // Never keep an override pointing at a model that is no longer offered.
  useEffect(() => {
    if (draft.model && models.data && !choice.models.some((m) => m.name === draft.model)) {
      setDraft((d) => ({ ...d, model: "" }));
    }
  }, [draft.model, models.data, choice.models]);

  // A start error is about the draft that was sent; editing pictures moves on.
  useEffect(() => setError(null), [pictures.pictures]);

  const projectList = projects.data ?? [];
  const asking = projectSeeded && projectList.length > 1 && !draft.project;
  const choices = useMemo(() => projectChoices(projectList, lastViewedProject.get()), [projectList]);
  const presets = suggestedPresets(modes.data?.presets ?? [], projectList, draft.project);
  const full: NewSessionDraft = { ...draft, pictures: pictures.pictures };
  const gate = canStart(full, {
    projectsRegistered: projectList.length,
    defaultDisabled: choice.defaultDisabled,
    starting,
    loadingPictures: pictures.loading,
  });
  const drop = useDropZone((files) => void pictures.add(files), !asking && !starting);

  useEffect(() => {
    if (!asking) area.current?.focus();
  }, [asking]);

  const start = async (via: Via) => {
    if (!gate.ok || !draftStore.beginStart()) return;
    setError(null);
    try {
      const resp = await client.startSession(buildStartRequest(full));
      track.submit("new_session", {
        via,
        mode: draft.mode,
        preset: !!draft.preset,
        prompt: draft.prompt.trim() !== "",
        pictures: pictures.pictures.length > 0,
        model: draft.model ? "override" : "default",
      });
      lastMode.set(draft.mode);
      if (draft.project) lastViewedProject.set(draft.project);
      draftStore.clear();
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      navigate(paths.session(draft.project, resp.sessionId));
    } catch (err) {
      draftStore.startFailed();
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error("session.start", err);
      setError(errorMessage(err, "The session could not be started."));
    }
  };

  const loadError = projects.error ?? modes.error;
  if (loadError) {
    return (
      <div className="page">
        <h1>New session</h1>
        <p className="error">{errorMessage(loadError, "Couldn’t load the new-session options.")}</p>
      </div>
    );
  }
  if (!projectSeeded || modes.isPending) {
    return (
      <div className="page">
        <h1>New session</h1>
        <p className="muted">Loading…</p>
      </div>
    );
  }

  const modeList = modes.data?.modes ?? [];
  const selectedMode = modeList.find((m) => m.name === draft.mode);

  return (
    <div className={`new-session${drop.dragging ? " dragging" : ""}`} {...drop.handlers}>
      {drop.dragging && <div className="drop-overlay">Drop pictures to attach them to the opening prompt</div>}
      <header className="page-head new-session-head">
        <h1>New session</h1>
        {draft.project && projectList.length > 1 && <span className="muted">in {draft.project}</span>}
      </header>
      <div className="new-session-body">
        {asking ? (
          <section className="project-ask" aria-labelledby="ask-project">
            <h2 id="ask-project">Which project?</h2>
            <div className="choice-grid">
              {choices.map((name, i) => {
                const info = projectList.find((p) => p.name === name);
                return (
                  <button
                    key={name}
                    type="button"
                    className="choice-card"
                    autoFocus={i === 0}
                    data-track="new_session.pick_project"
                    onClick={() => setDraft((d) => ({ ...d, project: name }))}
                  >
                    <span className="choice-title">{name}</span>
                    {info?.path && <span className="choice-sub mono">{info.path}</span>}
                    {i === 0 && name === lastViewedProject.get() && <span className="tag">last viewed</span>}
                  </button>
                );
              })}
              <button
                type="button"
                className="choice-card add-card"
                data-track="new_session.add_project"
                onClick={() => openAddProject((p) => setDraft((d) => ({ ...d, project: p.name, preset: "" })))}
              >
                <span className="choice-title">+ Add project…</span>
                <span className="choice-sub">Register another workspace on the daemon host</span>
              </button>
            </div>
          </section>
        ) : (
          <>
            <section aria-labelledby="mode-heading">
              <h2 id="mode-heading" className="section-label">
                Mode
              </h2>
              <div className="mode-grid" role="group" aria-labelledby="mode-heading">
                {modeList.map((m) => (
                  <button
                    key={m.name}
                    type="button"
                    aria-pressed={draft.mode === m.name}
                    className={`choice-card mode-card${draft.mode === m.name ? " selected" : ""}`}
                    disabled={starting}
                    onClick={() => {
                      track.action("new_session.mode", "click", { mode: m.name });
                      setDraft((d) => withMode(d, m.name, modes.data?.presets ?? []));
                    }}
                  >
                    <span className="choice-title">{m.title || m.name}</span>
                    {m.description && <span className="choice-sub">{m.description}</span>}
                  </button>
                ))}
              </div>
            </section>
            {presets.length > 0 && (
              <section aria-labelledby="presets-heading">
                <h2 id="presets-heading" className="section-label">
                  Suggestions
                </h2>
                <div className="preset-grid">
                  {presets.map((p) => (
                    <button
                      key={p.name}
                      type="button"
                      className={`choice-card preset-card${draft.preset === p.name ? " selected" : ""}`}
                      disabled={starting}
                      onClick={() => {
                        track.action("new_session.preset", "click", { mode: p.mode });
                        if (presetNeedsConfirmation(draft, p, modes.data?.presets ?? [])) {
                          setPendingPreset(p);
                        } else {
                          setDraft((d) => applyPreset(d, p));
                          area.current?.focus();
                        }
                      }}
                    >
                      <span className="choice-title">{p.title || p.name}</span>
                      {p.description && <span className="choice-sub clamp-2">{p.description}</span>}
                      {p.mode && (
                        <span className="tag">{modeList.find((m) => m.name === p.mode)?.title || p.mode}</span>
                      )}
                    </button>
                  ))}
                </div>
              </section>
            )}
          </>
        )}
      </div>
      <form
        className="new-session-composer"
        onSubmit={(e) => {
          e.preventDefault();
          void start("click");
        }}
      >
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <div className="option-chips">
          {projectList.length > 1 && (
            <label className="chip-select">
              <span>Project</span>
              <select
                aria-label="Project"
                value={draft.project}
                disabled={starting}
                onChange={(e) => {
                  const value = e.target.value;
                  if (value === ADD_PROJECT) {
                    openAddProject((p) => setDraft((d) => ({ ...d, project: p.name, preset: "" })));
                    return;
                  }
                  setDraft((d) => ({ ...d, project: value, preset: "" }));
                }}
              >
                {!draft.project && <option value="">Choose…</option>}
                {choices.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
                <option value={ADD_PROJECT}>Add project…</option>
              </select>
            </label>
          )}
          {projectList.length <= 1 && (
            <button
              type="button"
              className="chip add-project-chip"
              disabled={starting}
              title="Register another workspace on the daemon host"
              data-track="new_session.add_project"
              onClick={() => openAddProject((p) => setDraft((d) => ({ ...d, project: p.name, preset: "" })))}
            >
              + Add project…
            </button>
          )}
          {projectList.length === 0 && (
            <span className="chip-note muted" title="No projects are registered; the daemon’s startup workspace is used">
              Default workspace
            </span>
          )}
          {choice.showPicker && (
            <label className="chip-select">
              <span>Model</span>
              <select
                aria-label="Coordinator model for this session"
                value={draft.model}
                disabled={starting}
                onChange={(e) => {
                  track.action("new_session.model", "click", { model: e.target.value ? "override" : "default" });
                  setDraft((d) => ({ ...d, model: e.target.value }));
                }}
                title="Coordinator model for this session only"
              >
                {choice.defaultDisabled ? (
                  <option value="">Choose a model…</option>
                ) : (
                  <option value="">Default{choice.defaultModel ? ` (${choice.defaultModel})` : ""}</option>
                )}
                {choice.models.map((m) => (
                  <option key={m.name} value={m.name}>
                    {m.name} · {m.model || m.backend}
                  </option>
                ))}
              </select>
            </label>
          )}
          {selectedMode && (
            <span className="chip-note muted">
              Mode: <strong>{selectedMode.title || selectedMode.name}</strong>
            </span>
          )}
          {draft.preset && <span className="tag">preset: {draft.preset}</span>}
        </div>
        <PictureStrip pictures={pictures.pictures} onRemove={pictures.remove} disabled={starting} />
        {pictures.error && <p className="error small">{pictures.error}</p>}
        <div className="composer-row">
          <AttachButton
            onFiles={(f) => {
              track.action("new_session.attach_image", "click", { source: "button" });
              void pictures.add(f);
            }}
            disabled={starting || asking}
            full={pictures.full}
          />
          <textarea
            ref={area}
            rows={3}
            value={draft.prompt}
            disabled={starting || asking}
            aria-label="Opening prompt"
            placeholder={
              asking
                ? "Choose a project first…"
                : promptIsOptional(draft.mode)
                  ? "What should the agent do? (optional — it picks the next ready task) · Enter to start, Shift+Enter for a newline"
                  : "What should the agent do? · Enter to start, Shift+Enter for a newline"
            }
            onChange={(e) => setDraft((d) => ({ ...d, prompt: e.target.value }))}
            onPaste={(e) => {
              const files = filesFrom(e.clipboardData).filter((f) => f.type.startsWith("image/"));
              if (!files.length) return;
              e.preventDefault();
              track.action("new_session.attach_image", "keyboard", { source: "paste" });
              void pictures.add(files);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void start("keyboard");
              }
            }}
          />
          <button type="submit" className="btn primary" disabled={!gate.ok} title={gate.reason}>
            {starting ? "Starting…" : "Start"}
          </button>
        </div>
        <div className="new-session-foot muted small">
          {!gate.ok && gate.reason && !starting ? gate.reason : "\u00a0"}
          <Link to={routeProject ? paths.project(routeProject) : paths.home()} className="link cancel-link" data-track="new_session.cancel_link">
            Cancel
          </Link>
        </div>
      </form>
      <ConfirmDialog
        open={pendingPreset !== null}
        title="Replace your opening prompt?"
        body="Applying this suggestion will replace your edited prompt and select its mode. Your pictures will be kept."
        confirmLabel="Replace prompt"
        action="new_session.replace_prompt"
        onCancel={() => setPendingPreset(null)}
        onConfirm={() => {
          if (pendingPreset && !starting) setDraft((d) => applyPreset(d, pendingPreset));
          setPendingPreset(null);
          area.current?.focus();
        }}
      />
    </div>
  );
}
