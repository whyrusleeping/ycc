// New-session logic: project prompt ordering, defaults, presets, start gate,
// and the StartSession request.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  ListModelsResponseSchema,
  ModelInfoSchema,
  PresetSchema,
  ProjectInfoSchema,
} from "../src/gen/ycc/v1/ycc_pb";
import {
  applyPreset,
  buildStartRequest,
  buildWorkstreamRequest,
  canStart,
  initialMode,
  initialProject,
  modelChoices,
  placedPresets,
  presetActionLabel,
  projectChoices,
  suggestedPresets,
  withMode,
  type NewSessionDraft,
} from "../src/features/newSession/model";

const project = (name: string, needsOnboarding?: boolean) =>
  create(ProjectInfoSchema, { name, path: `/w/${name}`, ...(needsOnboarding === undefined ? {} : { needsOnboarding }) });
const projects = [project("alpha", false), project("beta", false), project("gamma", true)];
const presets = [
  create(PresetSchema, { name: "onboard", title: "Onboard this project", mode: "pm", openingPrompt: "ONBOARD", placement: "onboarding" }),
  create(PresetSchema, { name: "spec-doctor", title: "Check docs against code", mode: "pm", openingPrompt: "DOCTOR", placement: "project" }),
  create(PresetSchema, { name: "backlog-groom", mode: "pm", openingPrompt: "GROOM", placement: "backlog" }),
];
const draft = (over: Partial<NewSessionDraft> = {}): NewSessionDraft => ({
  project: "alpha",
  mode: "chat",
  model: "",
  prompt: "",
  preset: "",
  pictures: [],
  workstream: false,
  taskId: "",
  baseRef: "",
  ...over,
});
const gateOpts = { projectsRegistered: 3, defaultDisabled: false, starting: false, loadingPictures: false };

describe("new session", () => {
  it("asks about the last-viewed project first, then daemon order", () => {
    expect(projectChoices(projects, "gamma")).toEqual(["gamma", "alpha", "beta"]);
    expect(projectChoices(projects, "removed")).toEqual(["alpha", "beta", "gamma"]);
    expect(projectChoices(projects, null)).toEqual(["alpha", "beta", "gamma"]);
  });

  it("preselects the scoped or sole project, otherwise asks", () => {
    expect(initialProject("beta", projects)).toBe("beta");
    expect(initialProject("unknown", projects)).toBe("");
    expect(initialProject(null, projects)).toBe("");
    expect(initialProject(null, [project("solo")])).toBe("solo");
    expect(initialProject(null, [])).toBe("");
  });

  it("starts in the daemon's default mode, else its first", () => {
    const modes = [{ name: "work" }, { name: "chat" }];
    expect(initialMode(modes, "chat")).toBe("chat");
    expect(initialMode(modes, "gone")).toBe("work");
    expect(initialMode(modes, undefined)).toBe("work");
    expect(initialMode([], "chat")).toBe("");
  });

  it("suggests only onboarding, and only to projects that need it (or report nothing)", () => {
    expect(suggestedPresets(presets, projects, "alpha")).toEqual([]);
    expect(suggestedPresets(presets, projects, "gamma").map((p) => p.name)).toEqual(["onboard"]);
    expect(suggestedPresets(presets, [project("old")], "old").map((p) => p.name)).toEqual(["onboard"]);
    expect(suggestedPresets(presets, projects, "")).toEqual([]);
  });

  it("places presets by the daemon's placement, not their names", () => {
    expect(placedPresets(presets, ["backlog", "onboarding"]).map((p) => p.name)).toEqual(["backlog-groom", "onboard"]);
    expect(placedPresets(presets, ["onboarding", "project"]).map((p) => p.name)).toEqual(["onboard", "spec-doctor"]);
    expect(placedPresets(presets, ["memory"])).toEqual([]);
    // Onboarding an already-onboarded project re-onboards it.
    expect(presetActionLabel(presets[0], true)).toBe("Onboard this project");
    expect(presetActionLabel(presets[0], undefined)).toBe("Onboard this project");
    expect(presetActionLabel(presets[0], false)).toBe("Re-onboard");
    expect(presetActionLabel(presets[1], false)).toBe("Check docs against code");
    expect(presetActionLabel(presets[2], false)).toBe("backlog-groom");
  });

  it("gates starting on project, prompt (except work mode), and the model", () => {
    expect(canStart(draft({ project: "" }), gateOpts)).toEqual({ ok: false, reason: "Choose a project first." });
    expect(canStart(draft({ project: "" }), { ...gateOpts, projectsRegistered: 0, }).ok).toBe(false);
    expect(canStart(draft({ project: "", prompt: "hi" }), { ...gateOpts, projectsRegistered: 0 }).ok).toBe(true);
    expect(canStart(draft(), gateOpts).ok).toBe(false);
    expect(canStart(draft({ prompt: "  \n" }), gateOpts).ok).toBe(false);
    expect(canStart(draft({ mode: "work" }), gateOpts).ok).toBe(true);
    const pic = { id: "p", data: new Uint8Array([1]), mediaType: "image/png", filename: "a.png", previewUrl: null };
    expect(canStart(draft({ pictures: [pic] }), gateOpts).ok).toBe(true);
    expect(canStart(draft({ prompt: "x" }), { ...gateOpts, defaultDisabled: true }).ok).toBe(false);
    expect(canStart(draft({ prompt: "x", model: "other" }), { ...gateOpts, defaultDisabled: true }).ok).toBe(true);
    expect(canStart(draft({ prompt: "x" }), { ...gateOpts, starting: true }).ok).toBe(false);
    expect(canStart(draft({ prompt: "x" }), { ...gateOpts, loadingPictures: true }).ok).toBe(false);
  });

  it("applies a preset's mode and prompt; leaving its mode drops it", () => {
    const applied = applyPreset(draft({ prompt: "old" }), presets[1]);
    expect(applied).toMatchObject({ mode: "pm", prompt: "DOCTOR", preset: "spec-doctor" });
    expect(withMode(applied, "pm", presets).preset).toBe("spec-doctor");
    expect(withMode(applied, "chat", presets)).toMatchObject({ mode: "chat", preset: "", prompt: "DOCTOR" });
  });

  it("builds the StartSession request", () => {
    const data = new Uint8Array([9]);
    const pic = { id: "p", data, mediaType: "image/jpeg", filename: "a.jpg", previewUrl: "blob:x" };
    expect(buildStartRequest(draft({ prompt: "  do it \n", model: "fast", preset: "spec-doctor", pictures: [pic] }))).toEqual({
      project: "alpha",
      mode: "chat",
      prompt: "do it",
      coordinatorModel: "fast",
      preset: "spec-doctor",
      images: [{ data, mediaType: "image/jpeg", filename: "a.jpg" }],
    });
  });

  it("builds a workstream request with the same session choices plus task and base", () => {
    const data = new Uint8Array([9]);
    const pic = { id: "p", data, mediaType: "image/jpeg", filename: "a.jpg", previewUrl: "blob:x" };
    const d = draft({ workstream: true, prompt: " do it ", model: "fast", preset: "spec-doctor", pictures: [pic], taskId: "0042", baseRef: " main " });
    expect(buildWorkstreamRequest(d)).toEqual({
      ...buildStartRequest(d),
      taskId: "0042",
      baseRef: "main",
    });
    // An unwritten prompt becomes the chosen task's prompt.
    expect(buildWorkstreamRequest(draft({ workstream: true, taskId: "0042" }), { id: "0042", title: "Fix it" }).prompt).toBe(
      "Work on backlog task 0042: Fix it",
    );
    expect(buildWorkstreamRequest(draft({ workstream: true })).prompt).toBe("");
  });

  it("lets a workstream task stand in for the prompt, and needs a project", () => {
    expect(canStart(draft({ workstream: true, taskId: "0042" }), gateOpts).ok).toBe(true);
    expect(canStart(draft({ workstream: true }), gateOpts).ok).toBe(false);
    expect(canStart(draft({ taskId: "0042" }), gateOpts).ok).toBe(false);
    expect(canStart(draft({ workstream: true, project: "", prompt: "hi" }), { ...gateOpts, projectsRegistered: 0 }).ok).toBe(false);
  });

  it("offers enabled models and flags a disabled default", () => {
    const resp = create(ListModelsResponseSchema, {
      coordinator: "big",
      models: [
        create(ModelInfoSchema, { name: "big", disabled: true }),
        create(ModelInfoSchema, { name: "fast" }),
      ],
    });
    const c = modelChoices(resp);
    expect(c.models.map((m) => m.name)).toEqual(["fast"]);
    expect(c.defaultDisabled).toBe(true);
    expect(c.showPicker).toBe(true);
    expect(modelChoices(create(ListModelsResponseSchema, { coordinator: "a", models: [create(ModelInfoSchema, { name: "a" })] })).showPicker).toBe(false);
    expect(modelChoices(undefined).showPicker).toBe(false);
  });
});
