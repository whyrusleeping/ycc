// @vitest-environment jsdom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BacklogTaskSummarySchema } from "../src/gen/ycc/v1/ycc_pb";
import { applyMention, matchTasks, mentionAt } from "../src/features/session/mentions";

vi.mock("../src/app/analytics", () => ({ track: { action: vi.fn() } }));
const { Composer } = await import("../src/features/session/Composer");

const task = (id: string, title = `Task ${id}`, status = "todo") => create(BacklogTaskSummarySchema, { id, title, status });
const tasks = [task("0061"), task("0610"), task("0613", "Graceful shutdown", "in_progress"), task("0006"), task("0600", "Old", "done")];

describe("mentionAt", () => {
  it("finds the #digits token at or inside the caret", () => {
    expect(mentionAt("see #61", 7)).toEqual({ start: 4, end: 7, digits: "61" });
    expect(mentionAt("#0613 later", 3)).toEqual({ start: 0, end: 5, digits: "0613" });
    expect(mentionAt("(#6", 3)).toEqual({ start: 1, end: 3, digits: "6" });
  });
  it("ignores a bare #, a mid-word #, and digits running into letters", () => {
    expect(mentionAt("see #", 5)).toBeNull();
    expect(mentionAt("a#6", 3)).toBeNull();
    expect(mentionAt("##6", 3)).toBeNull();
    expect(mentionAt("#6a", 2)).toBeNull();
    expect(mentionAt("#61 x", 5)).toBeNull();
  });
});

describe("matchTasks", () => {
  it("matches padded and unpadded prefixes, exact id first, then newest", () => {
    expect(matchTasks(tasks, "61").map((t) => t.id)).toEqual(["0061", "0613", "0610"]);
    expect(matchTasks(tasks, "6").map((t) => t.id)).toEqual(["0006", "0613", "0610", "0600", "0061"]);
    expect(matchTasks(tasks, "06").map((t) => t.id)).toEqual(["0613", "0610", "0600"]);
    expect(matchTasks(tasks, "613").map((t) => t.id)).toEqual(["0613"]);
    expect(matchTasks(tasks, "006").map((t) => t.id)).toEqual(["0061"]);
    expect(matchTasks(tasks, "9")).toEqual([]);
  });
});

describe("applyMention", () => {
  it("writes the padded id with one following space", () => {
    expect(applyMention("fix #61", { start: 4, end: 7, digits: "61" }, "0613")).toEqual({ text: "fix #0613 ", caret: 10 });
    expect(applyMention("#6 now", { start: 0, end: 2, digits: "6" }, "0613")).toEqual({ text: "#0613 now", caret: 6 });
  });
});

describe("Composer task mentions", () => {
  let container: HTMLDivElement;
  let root: Root;
  const onSend = vi.fn();
  const onWanted = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    onSend.mockReset();
    onWanted.mockReset();
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  });
  afterEach(async () => {
    await act(() => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  });

  const render = (list = tasks) =>
    act(() =>
      root.render(<Composer sessionKey={`p\u0000${Math.random()}`} placeholder="" onSend={onSend} taskMentions={{ tasks: list, onWanted }} />),
    );
  const area = () => container.querySelector("textarea")!;
  const options = () => [...container.querySelectorAll(".task-mention")].map((o) => o.textContent);
  const type = async (value: string) => {
    const el = area();
    el.focus();
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(el, value);
    el.setSelectionRange(value.length, value.length);
    await act(() => el.dispatchEvent(new Event("input", { bubbles: true })));
  };
  const key = (k: string) => act(() => area().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));

  it("offers matching tasks with titles and inserts the chosen id on Enter without sending", async () => {
    await render();
    await type("look at #61");
    expect(onWanted).toHaveBeenCalled();
    expect(options()).toEqual(["#0061Task 0061Todo", "#0613Graceful shutdownIn progress", "#0610Task 0610Todo"]);
    await key("ArrowDown");
    expect(container.querySelector(".task-mention.cursor")?.textContent).toContain("Graceful shutdown");
    await key("Enter");
    expect(onSend).not.toHaveBeenCalled();
    expect(area().value).toBe("look at #0613 ");
    expect(options()).toEqual([]);
    await key("Enter");
    expect(onSend).toHaveBeenCalledWith("look at #0613", []);
  });

  it("Escape dismisses the popover and Enter sends again", async () => {
    await render();
    await type("#613");
    expect(options()).toHaveLength(1);
    await key("Escape");
    expect(options()).toEqual([]);
    await key("Enter");
    expect(onSend).toHaveBeenCalledWith("#613", []);
  });

  it("shows a loading line until the backlog arrives", async () => {
    await act(() => root.render(<Composer sessionKey="p\u0000l" placeholder="" onSend={onSend} taskMentions={{ tasks: undefined, onWanted }} />));
    await type("#6");
    expect(container.querySelector(".task-mentions")?.textContent).toBe("Loading tasks…");
  });
});
