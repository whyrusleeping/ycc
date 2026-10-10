// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ModelEditorDialog } from "../src/features/settings/ModelEditor";
import { TierEditorDialog } from "../src/features/settings/TierEditor";

const rpc = vi.hoisted(() => ({ upsertModel: vi.fn(), upsertReviewTier: vi.fn() }));
vi.mock("../src/api/client", () => ({ client: rpc, errorMessage: () => "Save failed", isUnauthorized: () => false }));
vi.mock("../src/app/analytics", () => ({ useFlow: () => {}, useTrackedView: () => {}, track: { action: vi.fn(), submit: vi.fn(), error: vi.fn() } }));
vi.mock("../src/ui/toast", () => ({ toast: vi.fn() }));

let container: HTMLDivElement;
let root: Root;
let qc: QueryClient;
beforeAll(() => {
  Object.defineProperties(HTMLDialogElement.prototype, {
    showModal: { configurable: true, value(this: HTMLDialogElement) { this.open = true; } },
    close: { configurable: true, value(this: HTMLDialogElement) { this.open = false; } },
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
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(() => root.unmount());
  qc.clear();
  container.remove();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

async function setValue(selector: string, value: string) {
  const field = container.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector)!;
  const proto = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(() => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(field, value);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function escape(dialog: HTMLDialogElement) {
  await act(() => { dialog.dispatchEvent(new Event("cancel", { cancelable: true })); });
}
async function renderEditor(kind: "model" | "tier", onClose: () => void) {
  await act(() => root.render(
    <QueryClientProvider client={qc}>
      {kind === "model"
        ? <ModelEditorDialog target={{ kind: "new" }} existingNames={[]} onClose={onClose} />
        : <TierEditorDialog draft={{ name: "self", strategy: "self", description: "Original", prompt: "", slots: [], existing: true, builtin: false }} modelNames={[]} tierNames={["self"]} onClose={onClose} />}
    </QueryClientProvider>,
  ));
  if (kind === "model") {
    await setValue(".model-editor input", "my-model");
    await setValue("#model-id", "claude-sonnet");
    await setValue("input[name=key_env]", "ANTHROPIC_API_KEY");
  } else await setValue("textarea", "My edited description");
}

describe.each(["model", "tier"] as const)("%s editor draft protection", (kind) => {
  it("guards Cancel, header close and Escape; cancelling discard retains edits", async () => {
    const onClose = vi.fn();
    await renderEditor(kind, onClose);
    const modal = container.querySelector<HTMLDialogElement>("dialog.modal")!;
    const confirm = container.querySelector<HTMLDialogElement>("dialog:not(.modal)")!;
    const cancel = Array.from(container.querySelectorAll<HTMLButtonElement>(".editor-actions button")).find((b) => b.textContent === "Cancel")!;
    const close = container.querySelector<HTMLButtonElement>('[aria-label="Close"]')!;
    for (const request of [() => cancel.click(), () => close.click(), () => modal.dispatchEvent(new Event("cancel", { cancelable: true }))]) {
      await act(request);
      expect(confirm.open).toBe(true);
      expect(onClose).not.toHaveBeenCalled();
      await escape(confirm);
      expect(confirm.open).toBe(false);
      expect(kind === "model" ? container.querySelector<HTMLInputElement>("#model-id")!.value : container.querySelector("textarea")!.value).toBe(kind === "model" ? "claude-sonnet" : "My edited description");
    }
    await escape(modal);
    await act(() => { confirm.querySelector<HTMLButtonElement>("button.danger")!.click(); });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("does not close during saving and keeps the draft on failure", async () => {
    let reject!: (err: Error) => void;
    const pending = new Promise((_, r) => { reject = r; });
    const save = kind === "model" ? rpc.upsertModel : rpc.upsertReviewTier;
    save.mockReturnValueOnce(pending);
    const onClose = vi.fn();
    await renderEditor(kind, onClose);
    await act(() => { container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
    expect(save).toHaveBeenCalledOnce();
    await escape(container.querySelector<HTMLDialogElement>("dialog.modal")!);
    await act(() => { container.querySelector<HTMLButtonElement>('[aria-label="Close"]')!.click(); });
    expect(onClose).not.toHaveBeenCalled();
    expect(container.querySelector<HTMLDialogElement>("dialog:not(.modal)")!.open).toBe(false);
    expect(Array.from(container.querySelectorAll<HTMLButtonElement>(".editor-actions button")).find((b) => b.textContent === "Cancel")!.disabled).toBe(true);
    await act(async () => {
      reject(new Error("failed"));
      await pending.catch(() => {});
    });
    await escape(container.querySelector<HTMLDialogElement>("dialog.modal")!);
    expect(container.querySelector<HTMLDialogElement>("dialog:not(.modal)")!.open).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });
});
