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
  useModes: () => ({ isPending: true }),
  useBudget: () => ({ isPending: true }),
}));
vi.mock("../src/features/session/useSession", () => ({
  useSessionController: () => ({}),
  useSessionSnapshot: () => ({ mode: "live", conn: "ready", contextTokens: null, rolloverAvailable: false, phase: { kind: "idle" }, control: null }),
}));
vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn(), error: vi.fn() } }));
vi.mock("../src/app/intents", () => ({ useIntent: () => {} }));
vi.mock("../src/features/settings/ModelEditor", () => ({ ModelEditorDialog: () => null }));
vi.mock("../src/features/settings/TierEditor", () => ({ TierEditorDialog: () => null }));
vi.mock("../src/features/settings/AnthropicLogin", () => ({ openAnthropicLogin: vi.fn() }));
vi.mock("../src/features/notify/NotifyControls", () => ({ NotificationControls: () => null }));

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
  return Array.from(container.querySelectorAll("button")).find((b) => b.textContent === label)!;
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

it.each([false, true])("keeps reasoning focus while saving and ignores repeated choices (session: %s)", async (session) => {
  let resolve!: (value: object) => void;
  rpc.setThinking.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  await render(session);
  const group = container.querySelector(`[aria-label="${session ? "Reasoning level" : "Coordinator reasoning"}"]`)!;
  const high = Array.from(group.querySelectorAll("button")).find((b) => b.textContent === "High")!;
  high.focus();
  await act(() => high.click());
  expect(high.disabled).toBe(false);
  expect(high.getAttribute("aria-disabled")).toBe("true");
  expect(document.activeElement).toBe(high);
  await act(() => high.click());
  expect(rpc.setThinking).toHaveBeenCalledTimes(1);
  await act(async () => resolve({}));
  expect(high.getAttribute("aria-disabled")).toBe("false");
  expect(document.activeElement).toBe(high);
});
