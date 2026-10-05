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
  canStart,
  initialMode,
  initialProject,
  modelChoices,
  projectChoices,
  suggestedPresets,
  withMode,
  type NewSessionDraft,
} from "../src/features/newSession/model";

const project = (name: string, needsOnboarding?: boolean) =>
  create(ProjectInfoSchema, { name, path: `/w/${name}`, ...(needsOnboarding === undefined ? {} : { needsOnboarding }) });
const projects = [project("alpha", false), project("beta", false), project("gamma", true)];
const presets = [
  create(PresetSchema, { name: "onboard", mode: "pm", openingPrompt: "ONBOARD" }),
  create(PresetSchema, { name: "spec-doctor", mode: "pm", openingPrompt: "DOCTOR" }),
];
const draft = (over: Partial<NewSessionDraft> = {}): NewSessionDraft => ({
  project: "alpha",
  mode: "chat",
  model: "",
  prompt: "",
  preset: "",
  pictures: [],
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

  it("remembers a still-valid mode, else the first", () => {
    const modes = [{ name: "chat" }, { name: "work" }];
    expect(initialMode(modes, "work")).toBe("work");
    expect(initialMode(modes, "gone")).toBe("chat");
    expect(initialMode([], null)).toBe("");
  });

  it("offers onboarding only to projects that need it (or report nothing)", () => {
    expect(suggestedPresets(presets, projects, "alpha").map((p) => p.name)).toEqual(["spec-doctor"]);
    expect(suggestedPresets(presets, projects, "gamma").map((p) => p.name)).toEqual(["onboard", "spec-doctor"]);
    expect(suggestedPresets(presets, [project("old")], "old").map((p) => p.name)).toEqual(["onboard", "spec-doctor"]);
    expect(suggestedPresets(presets, projects, "").map((p) => p.name)).toEqual(["spec-doctor"]);
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
