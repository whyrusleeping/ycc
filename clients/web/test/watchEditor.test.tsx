// @vitest-environment jsdom
// The watch editor keeps unsaved edits while the detail page's polled watch
// changes underneath it (a run starting, a new report).
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { WatchSchema, type GetWatchResponse } from "../src/gen/ycc/v1/ycc_pb";
import { WatchesPage } from "../src/features/watches/WatchesPage";

const state = vi.hoisted(() => ({ q: {} as { data: Partial<GetWatchResponse>; isPending: boolean; isFetching: boolean; isError: boolean } }));
vi.mock("../src/api/queries", async (original) => ({
  ...(await original<typeof import("../src/api/queries")>()),
  useWatch: () => state.q,
  useModels: () => ({ data: { models: [] }, isPending: false }),
}));
vi.mock("../src/app/analytics", () => ({ useFlow: () => {}, useTrackedView: () => {}, track: { action: vi.fn(), error: vi.fn(), submit: vi.fn() } }));

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: { configurable: true, value(this: HTMLDialogElement) { this.open = true; } },
    close: { configurable: true, value(this: HTMLDialogElement) { this.open = false; } },
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

it("preserves an unsaved edit when the polled watch state changes", async () => {
  const w = create(WatchSchema, { definition: { name: "disk", brief: "Original brief", intervalSecs: 300n } });
  state.q = { data: { watch: w, reports: [] }, isPending: false, isFetching: false, isError: false };
  const qc = new QueryClient();
  const render = () =>
    root.render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <WatchesPage project="p" name="disk" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  await act(render);
  await act(() => container.querySelector<HTMLButtonElement>('[data-track="watches.edit"]')!.click());
  const brief = container.querySelector<HTMLTextAreaElement>("dialog textarea")!;
  expect(brief.value).toBe("Original brief");
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(brief, "My unsaved brief");
    brief.dispatchEvent(new Event("input", { bubbles: true }));
  });
  // A poll lands: a run started, the definition is unchanged.
  state.q = { ...state.q, data: { ...state.q.data, watch: create(WatchSchema, { ...w, running: true }) } };
  await act(render);
  expect(brief.value).toBe("My unsaved brief");
  qc.clear();
});
