// @vitest-environment jsdom
// New Session's "Separate workstream" toggle: on, the same choices go to
// SpawnWorkstream (with the optional task and base) and the page opens the
// workstream's session; off, StartSession is unchanged.
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  BacklogTaskSummarySchema,
  ListModelsResponseSchema,
  ListModesResponseSchema,
  ModeSchema,
  ModelInfoSchema,
  ProjectInfoSchema,
  WorkstreamInfoSchema,
} from "../src/gen/ycc/v1/ycc_pb";

const enabledModels = [create(ModelInfoSchema, { name: "big" }), create(ModelInfoSchema, { name: "fast", thinkingLevels: ["low", "medium", "high"] })];
let catalog: ReturnType<typeof modelCatalog>;
const modelCatalog = () => create(ListModelsResponseSchema, {
  coordinator: "big",
  models: [...enabledModels, create(ModelInfoSchema, { name: "unavailable", disabled: true })],
});

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
  const projects = [create(ProjectInfoSchema, { name: "alpha", path: "/w/alpha", needsOnboarding: false })];
  const modes = create(ListModesResponseSchema, {
    modes: [create(ModeSchema, { name: "chat", title: "Chat" }), create(ModeSchema, { name: "work", title: "Work" })],
  });
  const tasks = [create(BacklogTaskSummarySchema, { id: "0042", title: "Fix it", status: "todo", priority: 2, ready: true })];
  return {
    ...actual,
    useProjects: () => ({ data: projects }),
    useModes: () => ({ data: modes, isPending: false }),
    useModels: () => ({ data: catalog }),
    useBacklog: (_project: string, enabled: boolean) => ({ data: enabled ? tasks : undefined, isPending: !enabled }),
  };
});

const { NewSessionPage } = await import("../src/features/newSession/NewSessionPage");

let container: HTMLDivElement;
let root: Root;
let location = "";

function Where() {
  location = useLocation().pathname;
  return null;
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  client.startSession.mockReset().mockResolvedValue({ sessionId: "s_plain" });
  client.spawnWorkstream
    .mockReset()
    .mockResolvedValue({ workstream: create(WorkstreamInfoSchema, { id: "ws_1", project: "alpha", sessionId: "s_ws" }) });
  track.submit.mockReset();
  catalog = modelCatalog();
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

async function render() {
  const qc = new QueryClient();
  await act(() =>
    root.render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/p/alpha/new"]}>
          <Routes>
            <Route path="*" element={<><NewSessionPage routeProject="alpha" /><Where /></>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
}

async function type(el: HTMLTextAreaElement | HTMLInputElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(() => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function choose(select: HTMLSelectElement, value: string) {
  await act(() => {
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

const toggle = () => container.querySelector<HTMLInputElement>(".chip-toggle input")!;
const submit = () => act(async () => container.querySelector<HTMLButtonElement>("button[type=submit]")!.click());
const advanced = () => container.querySelector<HTMLDetailsElement>("details.new-session-advanced")!;
const summary = () => advanced().querySelector("summary")!;
const policy = () => advanced().querySelector<HTMLSelectElement>('select[aria-label="Subagent model policy"]')!;
const subagentOptions = () => Array.from(advanced().querySelectorAll<HTMLLabelElement>('[aria-label="Allowed subagent models"] label'));
const pickSubagent = (name: string) => act(() => subagentOptions().find((label) => label.textContent === name)!.click());

it("spawns a workstream with the same session choices and opens its session", async () => {
  await render();
  expect(advanced().open).toBe(false);
  await act(() => summary().click());
  expect(advanced().open).toBe(true);
  await choose(policy(), "custom");
  expect(subagentOptions().map((label) => label.textContent)).toEqual(["big", "fast"]);
  await pickSubagent("big");
  await act(() => summary().click());
  expect(advanced().open).toBe(false);
  expect(summary().textContent).toContain("Subagents: 1 selected");
  const reasoning = () => container.querySelector<HTMLSelectElement>('select[aria-label="Coordinator reasoning for this session"]')!;
  await choose(reasoning(), "max");
  await choose(container.querySelector<HTMLSelectElement>('select[aria-label="Coordinator model for this session"]')!, "fast");
  // The picked level moves to the nearest one the new model supports.
  expect(reasoning().value).toBe("high");
  expect(Array.from(reasoning().options).map((o) => o.value)).toEqual(["", "low", "medium", "high"]);
  await type(container.querySelector("textarea")!, "  look at this ");
  expect(toggle().checked).toBe(false);
  await act(() => toggle().click());
  await choose(container.querySelector<HTMLSelectElement>('select[aria-label="Backlog task for the workstream"]')!, "0042");
  await type(container.querySelector<HTMLInputElement>(".chip-input")!, "main");
  expect(container.querySelector("button[type=submit]")!.textContent).toBe("Start workstream");
  await submit();

  expect(client.startSession).not.toHaveBeenCalled();
  expect(client.spawnWorkstream).toHaveBeenCalledWith({
    project: "alpha",
    mode: "chat",
    prompt: "look at this",
    coordinatorModel: "fast",
    coordinatorThinking: "high",
    subagentModels: { names: ["big"] },
    preset: "",
    images: [],
    taskId: "0042",
    baseRef: "main",
  });
  expect(location).toBe("/p/alpha/s/s_ws");
  expect(track.submit).toHaveBeenCalledWith("new_session", expect.objectContaining({ workstream: true, mode: "chat" }));
});

it("starts a plain session while the toggle is off", async () => {
  await render();
  await act(() => summary().click());
  expect(policy().value).toBe("default");
  await choose(policy(), "custom");
  await pickSubagent("fast");
  await choose(policy(), "default");
  await type(container.querySelector("textarea")!, "hello");
  await submit();
  expect(client.spawnWorkstream).not.toHaveBeenCalled();
  expect(client.startSession).toHaveBeenCalledWith(expect.objectContaining({ project: "alpha", prompt: "hello", coordinatorThinking: "" }));
  expect(client.startSession.mock.calls[0][0]).not.toHaveProperty("subagentModels");
  expect(container.querySelector(".workstream-options")).toBeNull();
  expect(location).toBe("/p/alpha/s/s_plain");
  expect(track.submit).toHaveBeenCalledWith("new_session", expect.objectContaining({ workstream: false }));
});

it("starts a plain session with no subagents without changing preferences or the next draft", async () => {
  await render();
  await act(() => summary().click());
  await choose(policy(), "custom");
  await pickSubagent("fast");
  await pickSubagent("fast");
  expect(summary().textContent).toContain("Subagents: none");
  expect(localStorage.length).toBe(0);
  await type(container.querySelector("textarea")!, "no subagents");
  await submit();
  expect(client.startSession).toHaveBeenNthCalledWith(1, expect.objectContaining({ subagentModels: { names: [] } }));

  // The real route unmounts the form when it opens the started session.
  await act(() => root.unmount());
  root = createRoot(container);
  await render();
  expect(policy().value).toBe("default");
  await type(container.querySelector("textarea")!, "next draft");
  await submit();
  expect(client.startSession).toHaveBeenCalledTimes(2);
  expect(client.startSession.mock.calls[1][0]).not.toHaveProperty("subagentModels");
});

it("removes unavailable models from a kept selection without reverting to default", async () => {
  await render();
  await act(() => summary().click());
  await choose(policy(), "custom");
  await pickSubagent("fast");
  catalog = create(ListModelsResponseSchema, { coordinator: "big", models: [enabledModels[0]] });
  await render();
  expect(policy().value).toBe("custom");
  expect(summary().textContent).toContain("Subagents: none");
  await type(container.querySelector("textarea")!, "no more fast");
  await submit();
  expect(client.startSession).toHaveBeenCalledWith(expect.objectContaining({ subagentModels: { names: [] } }));
});
