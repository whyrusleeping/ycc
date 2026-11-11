// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QuickRepliesForm } from "../src/features/quickReplies/QuickRepliesEditor";

const rpc = vi.hoisted(() => ({ setQuickReplies: vi.fn() }));
vi.mock("../src/api/client", () => ({ client: rpc, errorMessage: () => "Save failed", isUnauthorized: () => false }));
vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn(), submit: vi.fn(), error: vi.fn() } }));
vi.mock("../src/ui/toast", () => ({ toast: vi.fn() }));

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

const defaults = [
  { id: "go_ahead", label: "Go ahead", text: "Go ahead", icon: "check" },
  { id: "continue", label: "Continue", text: "Continue", icon: "" },
  { id: "commit_push", label: "Commit & push", text: "Commit and push", icon: "upload" },
] as never[];

const type = (field: HTMLInputElement | HTMLTextAreaElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(field), "value")!.set!.call(field, value);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });

const button = (label: string) =>
  [...container.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label)!;

it("saves the daemon-wide list edited, reordered and trimmed, keeping each default's id and icon", async () => {
  rpc.setQuickReplies.mockImplementation(async (req: { replies: unknown[] }) => ({ replies: req.replies }));
  const qc = new QueryClient();
  await act(() =>
    root.render(
      <QueryClientProvider client={qc}>
        <QuickRepliesForm scope={{ global: true }} initial={defaults} emptyText="none" />
      </QueryClientProvider>,
    ),
  );
  expect(button("Save").disabled).toBe(true); // nothing to save yet
  const label = container.querySelectorAll<HTMLInputElement>("input")[1];
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(label, "Keep going");
    label.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(() => button("Move Keep going up").click());
  await act(() => button("Remove Commit & push").click());
  await act(async () => button("Save").click());
  expect(rpc.setQuickReplies).toHaveBeenCalledWith({
    global: true,
    project: "",
    replies: [
      { id: "continue", label: "Keep going", text: "Continue", icon: "" },
      { id: "go_ahead", label: "Go ahead", text: "Go ahead", icon: "check" },
    ],
  });
  expect(button("Save").disabled).toBe(true); // saved state is the new baseline
});

it("fills a new reply's message from its label until the message is edited", async () => {
  const qc = new QueryClient();
  await act(() =>
    root.render(
      <QueryClientProvider client={qc}>
        <QuickRepliesForm scope={{ global: true }} initial={[]} emptyText="none" />
      </QueryClientProvider>,
    ),
  );
  await act(() => button("+ Add reply").click());
  const label = container.querySelector<HTMLInputElement>("input")!;
  const message = container.querySelector<HTMLTextAreaElement>("textarea")!;
  await type(label, "D");
  await type(label, "Deploy");
  expect(message.value).toBe("Deploy");
  await type(message, "Deploy to prod");
  await type(label, "Ship");
  expect(message.value).toBe("Deploy to prod");
});
