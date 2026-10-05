// Pure new-session logic (mirrors YccKit NewSessionModel and the iOS landing
// view's project prompt): which project to ask about first, default mode and
// model choices, preset suggestions, start validation, and the StartSession
// request.
import type { ListModelsResponse, ModelInfo, Preset, ProjectInfo } from "../../gen/ycc/v1/ycc_pb";
import { toImageAttachments, type DraftPicture } from "../attachments/attachments";

/**
 * Registered project names for the "which project?" prompt, in the daemon's
 * recency order with the last project the user viewed hoisted to the top.
 */
export function projectChoices(projects: readonly ProjectInfo[], lastViewed: string | null): string[] {
  const names = projects.map((p) => p.name);
  if (!lastViewed || !names.includes(lastViewed)) return names;
  return [lastViewed, ...names.filter((n) => n !== lastViewed)];
}

/**
 * The project preselected on open: the route's (scoped) project when it is
 * registered, else the sole project. An unscoped start with several projects
 * returns "" so the page asks.
 */
export function initialProject(routeProject: string | null, projects: readonly ProjectInfo[]): string {
  if (routeProject && projects.some((p) => p.name === routeProject)) return routeProject;
  if (projects.length === 1) return projects[0].name;
  return "";
}

/** A remembered mode if it still exists, else the daemon's first mode. */
export function initialMode(modes: readonly { name: string }[], remembered: string | null): string {
  if (remembered && modes.some((m) => m.name === remembered)) return remembered;
  return modes[0]?.name ?? "";
}

/**
 * Presets suitable for the selected project. The onboarding card shows only
 * for a project that needs onboarding (or whose daemon omitted the status).
 */
export function suggestedPresets(
  presets: readonly Preset[],
  projects: readonly ProjectInfo[],
  selectedProject: string,
): Preset[] {
  const withoutOnboarding = presets.filter((p) => p.name !== "onboard");
  const project = projects.find((p) => p.name === selectedProject);
  if (!project) return withoutOnboarding;
  if (project.needsOnboarding === undefined) return [...presets];
  return project.needsOnboarding ? [...presets] : withoutOnboarding;
}

/** Plain work mode may start without a prompt (the agent picks a ready task). */
export function promptIsOptional(mode: string): boolean {
  return mode === "work";
}

export interface ModelChoices {
  /** Enabled models a session may pick. */
  models: ModelInfo[];
  /** The configured default coordinator. */
  defaultModel: string;
  /** The default coordinator is disabled: an explicit pick is required. */
  defaultDisabled: boolean;
  /** Worth showing a picker (a real choice exists). */
  showPicker: boolean;
}

export function modelChoices(resp: ListModelsResponse | undefined): ModelChoices {
  const all = resp?.models ?? [];
  const models = all.filter((m) => !m.disabled);
  const defaultModel = resp?.coordinator ?? "";
  const defaultDisabled = all.some((m) => m.name === defaultModel && m.disabled);
  return { models, defaultModel, defaultDisabled, showPicker: models.length > 0 && (models.length > 1 || defaultDisabled) };
}

export interface NewSessionDraft {
  project: string;
  mode: string;
  /** Coordinator override for this session only; "" = configured default. */
  model: string;
  prompt: string;
  /** Name of the applied preset while its mode is still selected. */
  preset: string;
  pictures: readonly DraftPicture[];
}

export interface StartGate {
  ok: boolean;
  /** Why starting is blocked (for a hint), when not ok. */
  reason?: string;
}

export function canStart(
  draft: NewSessionDraft,
  opts: { projectsRegistered: number; defaultDisabled: boolean; starting: boolean; loadingPictures: boolean },
): StartGate {
  if (opts.starting) return { ok: false, reason: "Starting…" };
  if (opts.loadingPictures) return { ok: false, reason: "Attaching pictures…" };
  if (opts.projectsRegistered > 0 && !draft.project) return { ok: false, reason: "Choose a project first." };
  if (!draft.mode) return { ok: false, reason: "Choose a mode." };
  if (opts.defaultDisabled && !draft.model) {
    return { ok: false, reason: "The default coordinator model is disabled; choose a model." };
  }
  if (!promptIsOptional(draft.mode) && !draft.prompt.trim() && draft.pictures.length === 0) {
    return { ok: false, reason: "Describe what the agent should do." };
  }
  return { ok: true };
}

/** Applying a preset adopts its mode and seeds the prompt with its opening prompt. */
export function applyPreset<T extends Pick<NewSessionDraft, "mode" | "prompt" | "preset">>(draft: T, preset: Preset): T {
  return { ...draft, mode: preset.mode || draft.mode, prompt: preset.openingPrompt, preset: preset.name };
}

/** Changing the mode away from an applied preset's mode drops the preset. */
export function withMode<T extends Pick<NewSessionDraft, "mode" | "preset">>(
  draft: T,
  mode: string,
  presets: readonly Preset[],
): T {
  const applied = presets.find((p) => p.name === draft.preset);
  return { ...draft, mode, preset: applied && applied.mode === mode ? draft.preset : "" };
}

/** The StartSession request for a draft. */
export function buildStartRequest(draft: NewSessionDraft) {
  return {
    project: draft.project,
    mode: draft.mode,
    prompt: draft.prompt.trim(),
    coordinatorModel: draft.model,
    preset: draft.preset,
    images: toImageAttachments(draft.pictures),
  };
}
