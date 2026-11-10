// @vitest-environment jsdom
// New session starts chat by default with no mode or preset grid in the main
// flow; only a project that needs onboarding is offered the onboarding preset,
// and a kept draft carrying another mode and preset still starts with them.
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  ListModelsResponseSchema,
  ListModesResponseSchema,
  ModeSchema,
  ModelInfoSchema,
  PresetSchema,
  ProjectInfoSchema,
} from "../src/gen/ycc/v1/ycc_pb";

const { client, track } = vi.hoisted(() => ({
  client: { startSession: vi.fn(), spawnWorkstream: vi.fn() },
  track: { submit: vi.fn(), action: vi.fn(), error: vi.fn() },
}));
vi.mock("../src/api/client", () => ({
  client,
  errorMessage: (_err: unknown, fallback = "failed") => fallback,
  isUnauthorized: () => false,
}));
vi.mock("../src/app/analytics", () => ({ track, useFlow: () => {} }));
vi.mock("../src/api/queries", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../src/api/queries")>();
  const projects = [
    create(ProjectInfoSchema, { name: "alpha", path: "/w/alpha", needsOnboarding: false }),
    create(ProjectInfoSchema, { name: "fresh", path: "/w/fresh", needsOnboarding: true }),
  ];
  // Work listed first: the default comes from default_mode, not list order.
  const modes = create(ListModesResponseSchema, {
    modes: [
      create(ModeSchema, { name: "work", title: "Work on backlog" }),
      create(ModeSchema, { name: "chat", title: "Chat" }),
      create(ModeSchema, { name: "pm", title: "Project onboarding" }),
    ],
    presets: [
      create(PresetSchema, { name: "onboard", title: "Onboard this project", mode: "pm", openingPrompt: "ONBOARD", placement: "onboarding" }),
      create(PresetSchema, { name: "spec-doctor", title: "Check docs against code", mode: "pm", openingPrompt: "DOCTOR", placement: "project" }),
    ],
    defaultMode: "chat",
  });
  const models = create(ListModelsResponseSchema, { coordinator: "big", models: [create(ModelInfoSchema, { name: "big" })] });
  return {
    ...actual,
    useProjects: () => ({ data: projects }),
    useModes: () => ({ data: modes, isPending: false }),
    useModels: () => ({ data: models }),
    useBacklog: () => ({ data: undefined, isPending: true }),
  };
});

const { NewSessionPage } = await import("../src/features/newSession/NewSessionPage");

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  client.startSession.mockReset().mockResolvedValue({ sessionId: "s_1" });
  localStorage.clear();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function render(project: string) {
  await act(() =>
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={[`/p/${project}/new`]}>
          <Routes>
            <Route path="*" element={<NewSessionPage routeProject={project} />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
}

async function type(value: string) {
  const el = container.querySelector("textarea")!;
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

const submit = () => act(() => container.querySelector<HTMLButtonElement>("button[type=submit]")!.click());
const advanced = () => container.querySelector<HTMLDetailsElement>("details.new-session-advanced")!;
const presetTitles = () => Array.from(container.querySelectorAll(".preset-card .choice-title")).map((e) => e.textContent);

it("starts chat by default, with mode choice folded away and no suggestions for an onboarded project", async () => {
  await render("alpha");
  expect(presetTitles()).toEqual([]);
  expect(advanced().open).toBe(false);
  expect(container.textContent).not.toContain("Mode: ");
  await type("hello");
  await submit();
  expect(client.startSession).toHaveBeenCalledWith(expect.objectContaining({ project: "alpha", mode: "chat", preset: "", prompt: "hello" }));
});

it("offers only onboarding to a project that needs it, and a kept draft keeps its mode and preset", async () => {
  await render("fresh");
  expect(presetTitles()).toEqual(["Onboard this project"]);
  await act(() => container.querySelector<HTMLButtonElement>(".preset-card")!.click());
  // Applying the preset selects pm, which shows the (otherwise folded) mode choice.
  expect(advanced().open).toBe(true);
  expect(container.querySelector(".mode-card.selected")!.textContent).toContain("Project onboarding");

  // Leave and come back: the tab-local draft still carries mode and preset.
  await act(() => root.unmount());
  root = createRoot(container);
  await render("fresh");
  expect(advanced().open).toBe(true);
  await submit();
  expect(client.startSession).toHaveBeenCalledWith(
    expect.objectContaining({ project: "fresh", mode: "pm", preset: "onboard", prompt: "ONBOARD" }),
  );
});
