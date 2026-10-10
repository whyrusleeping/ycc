// @vitest-environment jsdom
// Native dialog opening can override React's commit-time autoFocus.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Modal } from "../src/ui/Modal";

vi.mock("../src/app/analytics", () => ({
  useFlow: () => {},
  useTrackedView: () => {},
}));

let container: HTMLDivElement;
let root: Root;

// jsdom has no native modal implementation. Model the opening focus steps
// so autofocus before showModal cannot accidentally pass this regression.
beforeAll(() => {
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: {
      configurable: true,
      value(this: HTMLDialogElement) {
        this.open = true;
        this.querySelector<HTMLElement>("button, select, textarea, input")?.focus();
      },
    },
    close: {
      configurable: true,
      value(this: HTMLDialogElement) { this.open = false; },
    },
  });
});

afterAll(() => {
  Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
  Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
});

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function renderForm(open: boolean, multipleProjects: boolean, description = true) {
  await act(() => root.render(
    <StrictMode>
      <Modal open={open} onClose={() => {}} title="Capture a backlog item">
        {multipleProjects && <select aria-label="Project"><option>First</option><option>Second</option></select>}
        {description ? <textarea autoFocus aria-label="Describe the task" /> : <button>Capture another</button>}
      </Modal>
    </StrictMode>,
  ));
}

function expectDescriptionFocus() {
  expect(container.querySelector("dialog")?.open).toBe(true);
  expect(document.activeElement).toBe(container.querySelector("textarea"));
}

describe("Modal focus", () => {
  it.each([false, true])("focuses the description on opening and reopening (multiple projects: %s)", async (multipleProjects) => {
    await renderForm(false, multipleProjects);
    await renderForm(true, multipleProjects);
    expectDescriptionFocus();

    await renderForm(false, multipleProjects);
    expect(container.querySelector("dialog")?.open).toBe(false);
    await renderForm(true, multipleProjects);
    expectDescriptionFocus();
  });

  it("focuses a remounted description while already open", async () => {
    await renderForm(true, true, false);
    await renderForm(true, true);
    expectDescriptionFocus();
  });

  it("keeps the close button focused when there is no autofocus child", async () => {
    await renderForm(true, true, false);
    expect(document.activeElement).toBe(container.querySelector('[aria-label="Close"]'));
  });
});
