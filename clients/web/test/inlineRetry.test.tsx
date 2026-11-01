// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SessionController, type SessionApi, type SessionSnapshot } from "../src/features/session/controller";
import { Transcript } from "../src/features/session/Transcript";

vi.mock("../src/features/inspector/inspector", () => ({ useInspector: () => ({ open: vi.fn() }) }));

let container: HTMLDivElement;
let root: Root;
let controller: SessionController;
const resume = vi.fn();
const sendInput = vi.fn();
const onError = vi.fn();

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  resume.mockReset();
  sendInput.mockReset();
  onError.mockReset();
  controller = new SessionController({ resume, sendInput } as unknown as SessionApi, "project", "session", {
    onUnauthorized: vi.fn(), onError,
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  controller.dispose();
  container.remove();
  vi.unstubAllGlobals();
});

function event(seq: number, type = "session_error", actor = "coordinator") {
  controller.projection.apply({
    seq, ts: "", type, actor, transient: false,
    dataJson: JSON.stringify({ msg: "Server unavailable", retryable: true, status: "done", label: "background job" }),
  });
}
async function render(extra: Partial<SessionSnapshot> = {}) {
  await act(() => root.render(<Transcript controller={controller} snap={{
    ...controller.getSnapshot(), rows: controller.projection.rows, phase: controller.projection.phase,
    installed: true, mode: "live", conn: "streaming", ...extra,
  }} onEditFailed={vi.fn()} />));
}
const retryButton = () => container.querySelector<HTMLButtonElement>('[data-track="session.retry"]');

it("offers Retry only beside the latest coordinator error, even with identical earlier errors and newer job/subagent notices", async () => {
  event(1);
  event(2);
  event(3, "session_error", "agent:1");
  event(4, "job_finished");
  await render();
  expect(container.querySelectorAll('[data-track="session.retry"]')).toHaveLength(1);
  expect(retryButton()!.closest("[data-row-id]")!.getAttribute("data-row-id")).toBe(controller.projection.rows[1].id);
  expect(retryButton()!.parentElement!.textContent).toContain("Session error: Server unavailable");
});

it("resumes without resending input, disables Retry while pending, and restores it after an action failure", async () => {
  event(1);
  let reject!: (error: Error) => void;
  resume.mockImplementation(() => new Promise((_, fail) => { reject = fail; }));
  await render();
  await act(() => retryButton()!.click());
  expect(resume).toHaveBeenCalledExactlyOnceWith({ sessionId: "session" });
  expect(sendInput).not.toHaveBeenCalled();
  await render();
  expect(retryButton()!.disabled).toBe(true);
  expect(retryButton()!.textContent).toBe("Retrying…");
  await act(() => retryButton()!.click());
  expect(resume).toHaveBeenCalledTimes(1);
  await act(async () => reject(new Error("Cannot resume")));
  await render();
  expect(onError.mock.calls[0][0]).toContain("Retry failed: Cannot resume");
  expect(retryButton()!.disabled).toBe(false);
});

it.each<Partial<SessionSnapshot>>([
  { phase: { kind: "running" } },
  { phase: { kind: "idle" } },
  { phase: { kind: "error", message: "Switch model", retryable: false } },
  { mode: "persisted", conn: "finished" },
  { conn: "failed" },
])("does not offer Retry when the current session cannot retry: %j", async (state) => {
  event(1);
  await render(state);
  expect(retryButton()).toBeNull();
});

it("does not attach session Retry to a subagent error", async () => {
  event(1, "session_error", "agent:1");
  await render();
  expect(retryButton()).toBeNull();
});
