// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider, skipToken, useQuery } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { queryKeys } from "../src/api/queries";
import { SessionList } from "../src/features/sessions/SessionList";
import { buildFeed, type HistoryLoad } from "../src/features/sessions/feed";
import { SessionSummarySchema, SetSessionFollowUpResponseSchema } from "../src/gen/ycc/v1/ycc_pb";

const { rpc, toast } = vi.hoisted(() => ({ rpc: vi.fn(), toast: vi.fn() }));
vi.mock("../src/api/client", () => ({
  client: { setSessionFollowUp: rpc },
  errorMessage: (_err: unknown, fallback: string) => fallback,
}));
vi.mock("../src/ui/toast", () => ({ toast }));
vi.mock("../src/features/workloop/hooks", () => ({ useLoopSessionIds: () => new Set() }));
vi.mock("../src/features/session/useSession", () => ({ requestReopen: vi.fn() }));
vi.mock("../src/api/queries", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../src/api/queries")>();
  return {
    ...actual,
    // Use the real feed cache and mutation, without project discovery or polling.
    useSessionFeed: (scope: string | null) => {
      const { data } = useQuery<HistoryLoad[]>({ queryKey: actual.queryKeys.sessionFeed(["work"]), queryFn: skipToken });
      return { feed: data && buildFeed(data, scope), isLoading: false, loadOlder: vi.fn(), loadingOlder: false };
    },
  };
});

let container: HTMLDivElement;
let root: Root;
let qc: QueryClient;
const key = queryKeys.sessionFeed(["work"]);
const serverAt = "2026-07-04T17:10:32.643Z";

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  rpc.mockReset();
  toast.mockReset();
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(() => root.unmount());
  qc.clear();
  container.remove();
  vi.unstubAllGlobals();
});

async function render(variant: "page" | "sidebar", flagged = false) {
  qc.setQueryData<HistoryLoad[]>(key, [{
    project: "work",
    sessions: [
      create(SessionSummarySchema, { sessionId: "one", title: "First session", followUp: flagged, followUpAt: flagged ? serverAt : "" }),
      create(SessionSummarySchema, { sessionId: "two", title: "Second session" }),
    ],
    pinned: [],
    nextCursor: "",
  }]);
  await act(() => root.render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><SessionList scope="work" variant={variant} /></MemoryRouter>
    </QueryClientProvider>,
  ));
}

function button(track: string): HTMLButtonElement {
  return container.querySelector<HTMLButtonElement>(`[data-track="${track}"]`)!;
}

async function flush() {
  await new Promise((resolve) => setTimeout(resolve, 10));
}

async function click(target: HTMLButtonElement) {
  await act(async () => { target.click(); await flush(); });
}

function cachedFirst() {
  return qc.getQueryData<HistoryLoad[]>(key)![0].sessions[0];
}

describe("session follow-up", () => {
  it.each(["page", "sidebar"] as const)("flags, filters, and manually clears in the %s list", async (variant) => {
    let resolve!: (value: ReturnType<typeof create<typeof SetSessionFollowUpResponseSchema>>) => void;
    rpc.mockReturnValueOnce(new Promise((done) => { resolve = done; }));
    await render(variant);
    expect(container.querySelector('[data-track="sessions.follow_up_filter"]') !== null).toBe(variant === "page");
    const flag = button("sessions.follow_up");
    expect(flag.getAttribute("aria-label")).toBe("Flag First session for follow-up");
    await click(flag);
    expect(rpc).toHaveBeenCalledWith({ project: "work", sessionId: "one", followUp: true });
    expect(cachedFirst().followUp).toBe(true);
    expect(flag.getAttribute("aria-pressed")).toBe("true");
    expect(flag.disabled).toBe(true);
    await click(flag);
    expect(rpc).toHaveBeenCalledTimes(1);

    await act(async () => { resolve(create(SetSessionFollowUpResponseSchema, { followUp: true, followUpAt: serverAt })); await flush(); });
    expect(cachedFirst().followUpAt).toBe(serverAt);
    expect(flag.disabled).toBe(false);
    expect(flag.title).toContain(new Date(serverAt).toLocaleString());
    expect(flag.getAttribute("aria-label")).toBe("Clear follow-up on First session");

    const filter = button("sessions.follow_up_filter");
    expect(filter.textContent).toBe("Follow-up (1)");
    await click(filter);
    expect(filter.getAttribute("aria-pressed")).toBe("true");
    expect(container.querySelectorAll(".session-row")).toHaveLength(1);
    expect(container.querySelector(".session-row-link")?.textContent).toBe("First session");
    expect(rpc).toHaveBeenCalledTimes(1); // Viewing/filtering never clears a bookmark.

    rpc.mockResolvedValueOnce(create(SetSessionFollowUpResponseSchema));
    await click(button("sessions.follow_up"));
    expect(rpc).toHaveBeenLastCalledWith({ project: "work", sessionId: "one", followUp: false });
    expect(cachedFirst()).toMatchObject({ followUp: false, followUpAt: "" });
    expect(container.textContent).toContain("No sessions flagged for follow-up.");
    expect(button("sessions.follow_up_filter").textContent).toBe("Follow-up (0)");
    expect(container.querySelector('[data-track="sessions.start_first"]')).toBeNull();
    await click(filter);
    expect(container.querySelectorAll(".session-row")).toHaveLength(2);
    expect(container.querySelector('[data-track="sessions.follow_up_filter"]') !== null).toBe(variant === "page");
  });

  it.each([false, true])("rolls back a failed toggle (previously flagged: %s) without undoing unrelated changes", async (flagged) => {
    let reject!: (err: Error) => void;
    rpc.mockReturnValueOnce(new Promise((_done, fail) => { reject = fail; }));
    await render("page", flagged);
    await click(button("sessions.follow_up"));
    expect(cachedFirst().followUp).toBe(!flagged);
    await act(async () => {
      qc.setQueryData<HistoryLoad[]>(key, (loads) => loads!.map((load) => ({
        ...load, sessions: load.sessions.map((s) => ({ ...s, title: `${s.title} updated` })),
      })));
      reject(new Error("offline"));
      await flush();
    });
    expect(cachedFirst()).toMatchObject({ followUp: flagged, followUpAt: flagged ? serverAt : "", title: "First session updated" });
    expect(button("sessions.follow_up").getAttribute("aria-pressed")).toBe(String(flagged));
    expect(toast).toHaveBeenCalledWith("Couldn’t update follow-up.", "error", { op: "sessions.follow_up", err: expect.any(Error) });
  });
});
