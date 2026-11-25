// @vitest-environment jsdom
// An open task pane follows the polled backlog row's holder: a claim shows the
// held chip and drops "actionable"; a release brings "actionable" back.
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { expect, it, vi } from "vitest";
import { client } from "../src/api/client";
import { queryKeys } from "../src/api/queries";
import { TaskDetailView } from "../src/features/backlog/TaskDetail";
import { InspectorProvider } from "../src/features/inspector/inspector";
import {
  BacklogTaskSummarySchema,
  GetTaskResponseSchema,
  ListBacklogResponseSchema,
  ListProjectsResponseSchema,
  TaskDetailSchema,
  TaskHolderSchema,
  type BacklogTaskSummary,
} from "../src/gen/ycc/v1/ycc_pb";

vi.mock("../src/api/client", async (original) => ({
  ...(await original<typeof import("../src/api/client")>()),
  client: { getTask: vi.fn(), listBacklog: vi.fn(), listProjects: vi.fn() },
}));


it("follows the backlog row's holder while the pane stays open", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const fields = { id: "0001", title: "Task 1", status: "todo", ready: true };
  const row = create(BacklogTaskSummarySchema, fields);
  const heldBy = create(TaskHolderSchema, { sessionId: "s_0123456789abcdef", title: "The holder" });
  vi.mocked(client.getTask).mockResolvedValue(create(GetTaskResponseSchema, { task: create(TaskDetailSchema, fields) }));
  vi.mocked(client.listBacklog).mockResolvedValue(create(ListBacklogResponseSchema, { tasks: [row] }));
  vi.mocked(client.listProjects).mockResolvedValue(create(ListProjectsResponseSchema, {}));
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const view = () => ({
    chip: container.querySelector(".held-badge")?.textContent?.trim(),
    actionable: container.querySelector(".tag.ok")?.textContent?.trim(),
  });
  const refetch = async (tasks: BacklogTaskSummary[]) => {
    vi.mocked(client.listBacklog).mockResolvedValue(create(ListBacklogResponseSchema, { tasks }));
    await act(async () => {
      await qc.refetchQueries({ queryKey: queryKeys.backlog("app") });
      // React Query notifies observers on a timer.
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
  };
  try {
    await act(() =>
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <InspectorProvider>
              <TaskDetailView project="app" taskId="0001" variant="pane" />
            </InspectorProvider>
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    await vi.waitFor(() => expect(view()).toEqual({ chip: undefined, actionable: "actionable" }));
    await refetch([create(BacklogTaskSummarySchema, { ...fields, heldBy })]);
    expect(view()).toEqual({ chip: "held by The holder", actionable: undefined });
    await refetch([row]);
    expect(view()).toEqual({ chip: undefined, actionable: "actionable" });
  } finally {
    await act(() => root.unmount());
    container.remove();
    qc.clear();
    vi.unstubAllGlobals();
  }
});
