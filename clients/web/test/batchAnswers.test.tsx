// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AnswerPanel } from "../src/features/session/AnswerPanel";
import type { SessionController } from "../src/features/session/controller";
import type { PendingQuestion } from "../src/features/session/projection";

vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn() } }));

let container: HTMLDivElement;
let root: Root;
const answerBatch = vi.fn();
const controller = { answerBatch } as unknown as SessionController;
const question: PendingQuestion = {
  rowId: "question", prompt: "Pick one", options: ["A", "B"],
  questions: [{ prompt: "Pick one", options: ["A", "B"] }, { prompt: "Explain", options: [] }],
};
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  answerBatch.mockReset();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});
async function text(index: number, value: string) {
  const textarea = container.querySelectorAll("textarea")[index];
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea, value);
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function render(inFlight = false, submitted = false) {
  await act(() => root.render(<AnswerPanel controller={controller} question={question} inFlight={inFlight} submitted={submitted} />));
}

it("requires an answer to every batch question and sends the complete trimmed option/text payload", async () => {
  await render();
  const send = container.querySelector<HTMLButtonElement>(".answer-send")!;
  const options = container.querySelectorAll<HTMLButtonElement>(".answer-options button");
  expect(send.disabled).toBe(true);
  await act(() => options[1].click());
  expect(options[0].getAttribute("aria-pressed")).toBe("false");
  expect(options[1].getAttribute("aria-pressed")).toBe("true");
  expect(send.disabled).toBe(true);
  await text(1, "   ");
  expect(send.disabled).toBe(true);
  await act(() => send.click());
  expect(answerBatch).not.toHaveBeenCalled();
  await text(1, "  Because it works  ");
  expect(send.disabled).toBe(false);
  await act(() => send.click());
  expect(answerBatch).toHaveBeenCalledExactlyOnceWith([
    { text: "", optionIndex: 1 }, { text: "Because it works", optionIndex: -1 },
  ]);

  await render(true);
  expect(send.disabled).toBe(true);
  await render(false, true);
  expect(send.disabled).toBe(true);
});

it("makes text override an option honestly and lets a later option selection replace that text", async () => {
  await render();
  const option = container.querySelector<HTMLButtonElement>(".answer-options button")!;
  await act(() => option.click());
  await text(0, "Other");
  expect(option.getAttribute("aria-pressed")).toBe("false");
  expect(option.classList.contains("selected")).toBe(false);
  await act(() => option.click());
  expect(container.querySelector("textarea")!.value).toBe("");
  expect(option.getAttribute("aria-pressed")).toBe("true");
  await text(1, "Reason");
  await act(() => container.querySelector<HTMLButtonElement>(".answer-send")!.click());
  expect(answerBatch).toHaveBeenCalledExactlyOnceWith([
    { text: "", optionIndex: 0 }, { text: "Reason", optionIndex: -1 },
  ]);
});

it("steps through a batch one question at a time: a pick moves on, Enter sends once complete", async () => {
  await render();
  const shown = () => [...container.querySelectorAll<HTMLElement>(".answer-q")].map((q) => !q.hidden);
  expect(shown()).toEqual([true, false]);
  const panel = container.querySelector<HTMLElement>(".answer-panel")!;
  await act(() => panel.dispatchEvent(new KeyboardEvent("keydown", { key: "2", bubbles: true })));
  expect(shown()).toEqual([false, true]);
  expect(container.querySelectorAll(".answer-step.done").length).toBe(1);
  await text(1, "Why not");
  const textarea = container.querySelectorAll("textarea")[1];
  await act(() => textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  expect(answerBatch).toHaveBeenCalledExactlyOnceWith([
    { text: "", optionIndex: 1 }, { text: "Why not", optionIndex: -1 },
  ]);
});

it("answers a single question with options at once on a click", async () => {
  const answerOption = vi.fn();
  const single: PendingQuestion = { rowId: "q1", prompt: "Pick", options: ["A", "B"], questions: [{ prompt: "Pick", options: ["A", "B"] }] };
  await act(() =>
    root.render(<AnswerPanel controller={{ answerOption } as unknown as SessionController} question={single} inFlight={false} submitted={false} />),
  );
  expect(container.querySelector(".answer-steps")).toBeNull();
  await act(() => container.querySelectorAll<HTMLButtonElement>(".answer-options button")[1].click());
  expect(answerOption).toHaveBeenCalledExactlyOnceWith(1);
});
