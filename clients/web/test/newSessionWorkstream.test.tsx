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
  const models = create(ListModelsResponseSchema, {
    coordinator: "big",
    models: [create(ModelInfoSchema, { name: "big" }), create(ModelInfoSchema, { name: "fast" })],
  });
  const tasks = [create(BacklogTaskSummarySchema, { id: "0042", title: "Fix it", status: "todo", priority: 2, ready: true })];
  return {
    ...actual,
    useProjects: () => ({ data: projects }),
    useModes: () => ({ data: modes, isPending: false }),
    useModels: () => ({ data: models }),
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
const submit = () => act(() => container.querySelector<HTMLButtonElement>("button[type=submit]")!.click());

it("spawns a workstream with the same session choices and opens its session", async () => {
  await render();
  await act(() => container.querySelector<HTMLButtonElement>(".mode-card")!.click()); // Chat
  await choose(container.querySelector<HTMLSelectElement>('select[aria-label="Coordinator model for this session"]')!, "fast");
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
  await type(container.querySelector("textarea")!, "hello");
  await submit();
  expect(client.spawnWorkstream).not.toHaveBeenCalled();
  expect(client.startSession).toHaveBeenCalledWith(expect.objectContaining({ project: "alpha", prompt: "hello" }));
  expect(container.querySelector(".workstream-options")).toBeNull();
  expect(location).toBe("/p/alpha/s/s_plain");
  expect(track.submit).toHaveBeenCalledWith("new_session", expect.objectContaining({ workstream: false }));
});
