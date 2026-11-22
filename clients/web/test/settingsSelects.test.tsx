// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ListModelsResponseSchema, ListReviewTiersResponseSchema } from "../src/gen/ycc/v1/ycc_pb";
import { SettingsPage } from "../src/features/settings/SettingsPage";
import { SessionSettingsPanel } from "../src/features/session/SessionSettings";

const rpc = vi.hoisted(() => ({
  setRoleConfig: vi.fn(), setReviewDefault: vi.fn(), setThinking: vi.fn(), invalidateQueries: vi.fn(),
}));
let models = create(ListModelsResponseSchema);
let tiers = create(ListReviewTiersResponseSchema);
vi.mock("../src/api/client", () => ({
  client: rpc,
  isUnauthorized: () => false,
  errorMessage: (error: unknown) => error instanceof Error ? error.message : "Error",
}));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: rpc.invalidateQueries }),
  useQuery: () => ({ data: { rows: [] }, isPending: false, isError: false }),
}));
vi.mock("../src/api/queries", () => ({
  queryKeys: { models: (id: string) => ["models", id], sessionUsage: (project: string) => ["usage", project], reviewTiers: ["tiers"] },
  useModels: () => ({ data: models, isPending: false, isError: false }),
  useReviewTiers: () => ({ data: tiers, isPending: false, isError: false }),
  useHealth: () => ({ data: [], isPending: false, isError: false }),
  useModes: () => ({ isPending: true }),
  useBudget: () => ({ isPending: true }),
  useQuickReplies: () => ({ isPending: true }),
  useProjects: () => ({ isPending: true }),
}));
vi.mock("../src/features/session/useSession", () => ({
  useSessionController: () => ({}),
  useSessionSnapshot: () => ({ mode: "live", conn: "ready", contextTokens: null, rolloverAvailable: false, phase: { kind: "idle" }, control: null }),
}));
vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn(), error: vi.fn() } }));
vi.mock("../src/app/intents", () => ({ useIntent: () => {} }));
vi.mock("../src/features/settings/ModelEditor", () => ({ ModelEditorDialog: () => null }));
vi.mock("../src/features/settings/TierEditor", () => ({ TierEditorDialog: () => null }));
vi.mock("../src/features/settings/AnthropicLogin", () => ({ openAnthropicLogin: vi.fn(), openOpenAILogin: vi.fn() }));
vi.mock("../src/features/notify/NotifyControls", () => ({ NotificationControls: () => null }));
vi.mock("../src/features/health/HealthPanel", () => ({ HealthPanel: () => null }));

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.clearAllMocks();
  rpc.invalidateQueries.mockResolvedValue(undefined);
  rpc.setRoleConfig.mockResolvedValue({});
  rpc.setReviewDefault.mockResolvedValue({});
  models = create(ListModelsResponseSchema, {
    coordinator: "first", implementer: "first", reviewers: ["first"],
    models: [{ name: "first", backend: "test" }, { name: "second", backend: "test" }, { name: "third", backend: "test" }],
  });
  tiers = create(ListReviewTiersResponseSchema, { defaultTier: "standard", tiers: [{ name: "standard" }, { name: "comprehensive" }, { name: "self" }] });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});
async function change(select: HTMLSelectElement, value: string) {
  await act(() => {
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
function button(label: string) {
  return Array.from(container.querySelectorAll("button")).find((b) => b.textContent === label || b.getAttribute("aria-label") === label)!;
}
async function render(session: boolean) {
  await act(() => root.render(<MemoryRouter>{session
    ? <SessionSettingsPanel project="project" sessionId="session" />
    : <SettingsPage />}</MemoryRouter>));
}

it.each([false, true])("keeps model browsing local and focused, applying only the final choice (session: %s)", async (session) => {
  await render(session);
  const select = container.querySelector<HTMLSelectElement>(`[aria-label="${session ? "Coordinator model" : "Default coordinator model"}"]`)!;
  select.focus();
  await change(select, "second");
  await change(select, "third");
  expect(document.activeElement).toBe(select);
  expect(select.disabled).toBe(false);
  expect(rpc.setRoleConfig).not.toHaveBeenCalled();
  const apply = button("Apply coordinator model");
  expect(apply.disabled).toBe(false);
  await act(async () => { apply.click(); });
  expect(rpc.setRoleConfig).toHaveBeenCalledExactlyOnceWith({ sessionId: session ? "session" : "", coordinator: "third" });
  expect(rpc.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["models", session ? "session" : ""] });
});

it.each([false, true])("keeps a failed model draft and announces the error (session: %s)", async (session) => {
  rpc.setRoleConfig.mockRejectedValueOnce(new Error("Cannot use that model"));
  await render(session);
  const select = container.querySelector<HTMLSelectElement>(`[aria-label="${session ? "Implementer model" : "Default implementer model"}"]`)!;
  await change(select, "second");
  await act(async () => { button("Apply implementer model").click(); });
  expect(container.querySelector('[role="alert"]')?.textContent).toBe("Cannot use that model");
  expect(select.value).toBe("second");
  expect(button("Apply implementer model").disabled).toBe(false);
});

it("applies the default tier only after explicit confirmation, retaining a failed selection", async () => {
  await render(false);
  const select = container.querySelector<HTMLSelectElement>('[aria-label="Default review tier"]')!;
  select.focus();
  await change(select, "comprehensive");
  await change(select, "self");
  expect(document.activeElement).toBe(select);
  expect(rpc.setReviewDefault).not.toHaveBeenCalled();
  rpc.setReviewDefault.mockRejectedValueOnce(new Error("Tier unavailable"));
  await act(async () => { button("Apply default tier").click(); });
  expect(rpc.setReviewDefault).toHaveBeenCalledExactlyOnceWith({ name: "self" });
  expect(select.value).toBe("self");
  expect(container.querySelector('[role="alert"]')?.textContent).toBe("Tier unavailable");
});

it("applies a session reasoning level once, to that session only, keeping focus while saving", async () => {
  let resolve!: (value: object) => void;
  rpc.setThinking.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  await render(true);
  const select = container.querySelector<HTMLSelectElement>('[aria-label="Implementer reasoning"]')!;
  select.focus();
  await change(select, "high");
  expect(select.disabled).toBe(false);
  expect(select.getAttribute("aria-disabled")).toBe("true");
  expect(document.activeElement).toBe(select);
  await change(select, "max");
  expect(rpc.setThinking).toHaveBeenCalledExactlyOnceWith({ sessionId: "session", role: "implementer", level: "high" });
  await act(async () => resolve({}));
  expect(select.getAttribute("aria-disabled")).toBe("false");
  expect(document.activeElement).toBe(select);
});

it.each([false, true])("offers only the role model's thinking levels (session: %s)", async (session) => {
  models.models[0].thinkingLevels = ["low", "medium", "high"];
  models.models[1].thinkingLevels = ["off", "high"];
  models.reviewers = ["first", "second"];
  await render(session);
  const options = (label: string) =>
    Array.from(container.querySelector<HTMLSelectElement>(`[aria-label="${label}"]`)!.options).map((o) => o.value);
  const coordinator = session ? "Coordinator reasoning" : "Main agent reasoning";
  expect(options(coordinator)).toEqual(["low", "medium", "high"]);
  expect(options("Reviewers reasoning")).toEqual(["off", "low", "medium", "high"]);
});

it("saves a daemon reasoning level once while keeping the select usable", async () => {
  let resolve!: (value: object) => void;
  rpc.setThinking.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  await render(false);
  const select = container.querySelector<HTMLSelectElement>('[aria-label="Main agent reasoning"]')!;
  select.focus();
  await change(select, "high");
  expect(select.disabled).toBe(false);
  expect(select.getAttribute("aria-disabled")).toBe("true");
  expect(document.activeElement).toBe(select);
  await change(select, "max");
  expect(rpc.setThinking).toHaveBeenCalledExactlyOnceWith({ sessionId: "", role: "coordinator", level: "high" });
  await act(async () => resolve({}));
  expect(select.getAttribute("aria-disabled")).toBe("false");
});

it.each([false, true])("holds a role's reasoning while a different model is chosen but not applied (session: %s)", async (session) => {
  await render(session);
  const model = container.querySelector<HTMLSelectElement>(`[aria-label="${session ? "Coordinator model" : "Default coordinator model"}"]`)!;
  const reasoning = container.querySelector<HTMLSelectElement>(`[aria-label="${session ? "Coordinator reasoning" : "Main agent reasoning"}"]`)!;
  expect(reasoning.disabled).toBe(false);
  await change(model, "second");
  expect(reasoning.disabled).toBe(true);
  expect(container.textContent).toContain("apply the model first");
  await change(model, "first");
  expect(reasoning.disabled).toBe(false);
});

it("holds the session reviewers' reasoning while a reviewer change is unapplied", async () => {
  await render(true);
  const reasoning = container.querySelector<HTMLSelectElement>('[aria-label="Reviewers reasoning"]')!;
  const second = Array.from(container.querySelectorAll<HTMLLabelElement>(".settings-reviewers label")).find((l) => l.textContent?.includes("second"))!;
  await act(() => second.querySelector("input")!.click());
  expect(reasoning.disabled).toBe(true);
});

it("holds every default reasoning picker while a role assignment is saving", async () => {
  let resolve!: (value: object) => void;
  rpc.setRoleConfig.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  await render(false);
  const reviewers = container.querySelector<HTMLSelectElement>('[aria-label="Reviewers reasoning"]')!;
  const second = Array.from(container.querySelectorAll<HTMLLabelElement>(".settings-reviewers label")).find((l) => l.textContent?.includes("second"))!;
  await act(() => second.querySelector("input")!.click());
  expect(rpc.setRoleConfig).toHaveBeenCalledExactlyOnceWith({ sessionId: "", reviewers: ["first", "second"] });
  for (const label of ["Main agent reasoning", "Reviewers reasoning", "Implementer reasoning"]) {
    expect(container.querySelector<HTMLSelectElement>(`[aria-label="${label}"]`)!.disabled).toBe(true);
  }
  await act(async () => resolve({}));
  expect(reviewers.disabled).toBe(false);
});
