// @vitest-environment jsdom
// Workstream sessions carry a branch mark in both session lists, linking to
// their project's workstreams; other sessions have none.
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider, skipToken, useQuery } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { queryKeys } from "../src/api/queries";
import { SessionList } from "../src/features/sessions/SessionList";
import { buildFeed, type HistoryLoad } from "../src/features/sessions/feed";
import { SessionSummarySchema } from "../src/gen/ycc/v1/ycc_pb";

vi.mock("../src/features/workloop/hooks", () => ({ useLoopSessionIds: () => new Set() }));
vi.mock("../src/features/session/useSession", () => ({ requestReopen: vi.fn() }));
vi.mock("../src/api/queries", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../src/api/queries")>();
  return {
    ...actual,
    useSessionFeed: (scope: string | null) => {
      const { data } = useQuery<HistoryLoad[]>({ queryKey: actual.queryKeys.sessionFeed(["work"]), queryFn: skipToken });
      return { feed: data && buildFeed(data, scope), isLoading: false, loadOlder: vi.fn(), loadingOlder: false };
    },
  };
});

let container: HTMLDivElement;
let root: Root;
let qc: QueryClient;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  qc = new QueryClient();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

it.each(["page", "sidebar"] as const)("marks workstream sessions in the %s list", async (variant) => {
  qc.setQueryData<HistoryLoad[]>(queryKeys.sessionFeed(["work"]), [{
    project: "work",
    sessions: [
      create(SessionSummarySchema, { sessionId: "ws", title: "Parallel", workstreamId: "ws_1", origin: "user", humanParticipated: true }),
      create(SessionSummarySchema, { sessionId: "own", title: "Plain", origin: "user", humanParticipated: true }),
    ],
    pinned: [],
    nextCursor: "",
  }]);
  await act(() => root.render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><SessionList scope="work" variant={variant} /></MemoryRouter>
    </QueryClientProvider>,
  ));
  const rows = [...container.querySelectorAll(".session-row")];
  const marks = (title: string) => rows.find((r) => r.textContent?.includes(title))!.querySelectorAll(".row-ws");
  expect(marks("Parallel")).toHaveLength(1);
  expect(marks("Parallel")[0].getAttribute("href")).toBe("/p/work/workstreams");
  expect(marks("Plain")).toHaveLength(0);
});
