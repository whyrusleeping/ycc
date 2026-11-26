// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { client } from "../src/api/client";
import { queryKeys } from "../src/api/queries";
import { WorkLoopPage } from "../src/features/workloop/WorkLoopPage";
import { GetWorkLoopResponseSchema, ResizeWorkLoopResponseSchema, WorkLoopInfoSchema } from "../src/gen/ycc/v1/ycc_pb";
import { toast } from "../src/ui/toast";

vi.mock("../src/api/client", async (original) => ({
  ...(await original<typeof import("../src/api/client")>()),
  client: { getWorkLoop: vi.fn(), resizeWorkLoop: vi.fn() },
}));
vi.mock("../src/ui/toast", () => ({ toast: vi.fn() }));

const project = "gamma";
const queryKey = queryKeys.workLoop(project);
const loop = (state: string, parallel = 2, loopId = "loop_1") => create(WorkLoopInfoSchema, {
  project, loopId, state, parallel,
});
let container: HTMLDivElement;
let root: Root;
let qc: QueryClient;

// jsdom doesn't implement modal dialogs.
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: { configurable: true, value(this: HTMLDialogElement) { this.open = true; } },
    close: { configurable: true, value(this: HTMLDialogElement) { this.open = false; } },
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
});

afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  qc.clear();
  Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
  vi.resetAllMocks();
  vi.unstubAllGlobals();
});

async function render(current = loop("running")) {
  qc.setQueryData(queryKey, current);
  await act(() => root.render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><WorkLoopPage project={project} /></MemoryRouter>
    </QueryClientProvider>,
  ));
}

const resizeButton = () => container.querySelector<HTMLButtonElement>("button[data-track='loop.resize']");
const select = () => container.querySelector<HTMLSelectElement>("#loop-resize-slots")!;
const submit = () => container.querySelector<HTMLButtonElement>("dialog[open] button[type=submit]")!;
const choose = (value: string) => act(() => {
  select().value = value;
  select().dispatchEvent(new Event("change", { bubbles: true }));
});
// React Query notifies observers on a timer.
const notify = () => new Promise((resolve) => setTimeout(resolve, 20));

it("sends the chosen target and installs the snapshot without dropping working sessions", async () => {
  await render();
  await act(() => resizeButton()!.click());
  expect(select().value).toBe("2");
  expect(Array.from(select().options, (o) => Number(o.value))).toEqual(Array.from({ length: 16 }, (_, i) => i + 1));
  expect(submit().disabled).toBe(true);
  await choose("1");

  const response = create(ResizeWorkLoopResponseSchema, { loop: {
    project, loopId: "loop_1", state: "running", parallel: 1,
    slots: [{ state: "working", sessionId: "s_a" }, { state: "working", sessionId: "s_b" }],
  } });
  let complete!: (resp: typeof response) => void;
  vi.mocked(client.resizeWorkLoop).mockReturnValue(new Promise((resolve) => { complete = resolve; }));
  await act(() => submit().click());
  expect(client.resizeWorkLoop).toHaveBeenCalledExactlyOnceWith({ project, parallel: 1, loopId: "loop_1" });
  expect(select().disabled).toBe(true);
  expect(submit().disabled).toBe(true);
  expect(resizeButton()?.disabled).toBe(true);

  await act(async () => { complete(response); await notify(); });
  expect(qc.getQueryData(queryKey)).toEqual(response.loop);
  expect(container.querySelector("dialog[open]")).toBeNull();
  expect(container.textContent).toContain("2 working (target 1), 0 waiting");
  expect(container.querySelectorAll(".loop-slot")).toHaveLength(2);
});

it("can resize while waiting, but refetches rather than applying a failed request", async () => {
  const current = loop("waiting");
  await render(current);
  vi.mocked(client.resizeWorkLoop).mockRejectedValue(new Error("Resize unavailable"));
  vi.mocked(client.getWorkLoop).mockResolvedValue(create(GetWorkLoopResponseSchema, { loop: current }));
  await act(() => resizeButton()!.click());
  await choose("16");
  await act(async () => { submit().click(); await notify(); });
  expect(client.resizeWorkLoop).toHaveBeenCalledExactlyOnceWith({ project, parallel: 16, loopId: current.loopId });
  expect(client.getWorkLoop).toHaveBeenCalledTimes(1);
  expect(qc.getQueryData(queryKey)).toEqual(current);
  expect(toast).toHaveBeenCalledWith("Couldn’t resize the work loop: Resize unavailable", "error", expect.any(Object));
  expect(select().value).toBe("16");
  expect(submit().disabled).toBe(false);
});

it("refreshes after the server rejects a resize for a loop replaced between polls", async () => {
  const displayed = loop("running");
  const replacement = loop("running", 7, "loop_2");
  await render(displayed);
  vi.mocked(client.resizeWorkLoop).mockRejectedValue(new ConnectError("Work loop changed", Code.FailedPrecondition));
  vi.mocked(client.getWorkLoop).mockResolvedValue(create(GetWorkLoopResponseSchema, { loop: replacement }));
  await act(() => resizeButton()!.click());
  await choose("4");
  await act(async () => { submit().click(); await notify(); });
  expect(client.resizeWorkLoop).toHaveBeenCalledExactlyOnceWith({ project, parallel: 4, loopId: displayed.loopId });
  expect(qc.getQueryData(queryKey)).toEqual(replacement);
  expect(container.querySelector("dialog[open]")).toBeNull();
});

it("hides the control when stopping or finished, and invalidates an open choice when the loop is replaced", async () => {
  await render(loop("stopping"));
  expect(resizeButton()).toBeNull();
  await act(async () => { qc.setQueryData(queryKey, loop("finished")); await notify(); });
  expect(resizeButton()).toBeNull();
  await act(async () => { qc.setQueryData(queryKey, loop("running")); await notify(); });
  await act(() => resizeButton()!.click());
  await choose("4");
  await act(async () => { qc.setQueryData(queryKey, loop("running", 7, "loop_2")); await notify(); });
  expect(container.querySelector("dialog[open]")).toBeNull();
  expect(client.resizeWorkLoop).not.toHaveBeenCalled();
  await act(() => resizeButton()!.click());
  expect(select().value).toBe("7");
  expect(submit().disabled).toBe(true);
});
