// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router";
import { expect, it, vi } from "vitest";
import { InspectorProvider } from "../src/features/inspector/inspector";
import { Markdown } from "../src/features/markdown/Markdown";

const { useTask } = vi.hoisted(() => ({ useTask: vi.fn((project: string, id: string) => ({
  data: { title: `${project} task ${id}`, status: "in_progress" },
})) }));
vi.mock("../src/api/queries", async (original) => ({ ...await original(), useTask }));

it("previews a scoped mention on hover or focus, not on transcript render", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  try {
    await act(() => root.render(<MemoryRouter><InspectorProvider><Markdown project="alpha" text={
      "See **#42** and `#0100`, not file#1234 or #12abc or `let x = '#1234'`. [#8888](https://example.com)"
    } /></InspectorProvider></MemoryRouter>));
    const links = host.querySelectorAll("a");
    expect([...links].map((l) => l.textContent)).toEqual(["#42", "#0100", "#8888"]);
    expect(links[0].pathname).toBe("/p/alpha/backlog/0042");
    expect(useTask).not.toHaveBeenCalled();
    await act(() => {
      links[0].dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
      vi.advanceTimersByTime(300);
    });
    const tip = document.querySelector('[role="tooltip"]')!;
    expect(tip.textContent).toContain("In progress");
    expect(tip.textContent).toContain("alpha task 0042");
    expect(links[0].getAttribute("aria-describedby")).toBe(tip.id);
    await act(() => {
      links[0].dispatchEvent(new MouseEvent("mouseout", { bubbles: true }));
      vi.advanceTimersByTime(300);
    });
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
    await act(() => links[1].focus());
    expect(document.querySelector('[role="tooltip"]')?.textContent).toContain("alpha task 0100");
    await act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    expect(document.querySelector('[role="tooltip"]')).toBeNull();
    expect(document.activeElement).toBe(links[1]);
  } finally {
    await act(() => root.unmount());
    host.remove();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  }
});
