// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MenuButton } from "../src/ui/Menu";

vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn() } }));

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
  vi.unstubAllGlobals();
});

async function key(target: Element, value: string) {
  await act(() => { target.dispatchEvent(new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true })); });
}

it("focuses enabled items, wraps with arrows, supports Home/End, and restores the trigger on Escape", async () => {
  const select = vi.fn();
  await act(() => root.render(<StrictMode><MenuButton label="Actions" ariaLabel="Actions" items={[
    { label: "Unavailable", disabled: true, onSelect: select },
    { label: "First", onSelect: select },
    { label: "Middle", onSelect: select },
    { label: "Last", onSelect: select },
  ]} /></StrictMode>));
  const trigger = container.querySelector<HTMLButtonElement>("[aria-haspopup]")!;
  trigger.focus();
  await key(trigger, "ArrowDown");
  const items = container.querySelectorAll<HTMLButtonElement>("[role=menuitem]");
  expect(document.activeElement).toBe(items[1]);
  expect(trigger.getAttribute("aria-controls")).toBe(container.querySelector("[role=menu]")!.id);
  await key(items[1], "ArrowDown");
  expect(document.activeElement).toBe(items[2]);
  await key(items[2], "End");
  expect(document.activeElement).toBe(items[3]);
  await key(items[3], "ArrowDown");
  expect(document.activeElement).toBe(items[1]);
  await key(items[1], "ArrowUp");
  expect(document.activeElement).toBe(items[3]);
  await key(items[3], "Home");
  expect(document.activeElement).toBe(items[1]);
  expect(select).not.toHaveBeenCalled();
  await key(items[1], "Escape");
  expect(container.querySelector("[role=menu]")).toBeNull();
  expect(document.activeElement).toBe(trigger);
  expect(trigger.getAttribute("aria-expanded")).toBe("false");

  await key(trigger, "ArrowUp");
  expect(document.activeElement?.textContent).toBe("Last");
  await act(() => (document.activeElement as HTMLButtonElement).click());
  expect(select).toHaveBeenCalledOnce();
  expect(document.activeElement).toBe(trigger);
});

it("focuses the first item on click and lets Tab or an outside click dismiss without stealing focus", async () => {
  await act(() => root.render(<><MenuButton label="Actions" ariaLabel="Actions" items={[
    { label: "First", onSelect: () => {} },
  ]} /><button id="outside">Outside</button></>));
  const trigger = container.querySelector<HTMLButtonElement>("[aria-haspopup]")!;
  const outside = container.querySelector<HTMLButtonElement>("#outside")!;
  await act(() => trigger.click());
  expect(document.activeElement?.textContent).toBe("First");
  await key(document.activeElement!, "Tab");
  expect(container.querySelector("[role=menu]")).toBeNull();
  await act(() => trigger.click());
  await act(() => {
    outside.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    outside.focus();
  });
  expect(container.querySelector("[role=menu]")).toBeNull();
  expect(document.activeElement).toBe(outside);
});
