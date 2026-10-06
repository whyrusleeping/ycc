// Quick capture reducer and the app action registry / shortcut matching.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import { EventSchema, type Event } from "../src/gen/ycc/v1/ycc_pb";
import { INITIAL_CAPTURE, captureLogText, captureOutcome, captureReducer, type CaptureAction } from "../src/features/backlog/capture";
import { isEditableTarget, listActions, matchShortcut, registerAction, runAction } from "../src/app/actions";

function ev(type: string, data: unknown): Event {
  return create(EventSchema, { actor: "capture", type, dataJson: JSON.stringify(data) });
}

function run(actions: CaptureAction[]) {
  return actions.reduce(captureReducer, INITIAL_CAPTURE);
}

describe("quick capture", () => {
  it("reads the terminal capture_result outcomes", () => {
    expect(captureOutcome(ev("capture_result", { task_id: "0042", title: "Retry", question: "" }))).toEqual({
      kind: "created",
      taskId: "0042",
      title: "Retry",
    });
    expect(captureOutcome(ev("capture_result", { task_id: "", title: "", question: "Which endpoint?" }))).toEqual({
      kind: "question",
      question: "Which endpoint?",
    });
    expect(captureOutcome(ev("capture_result", { error: "unknown project" }))).toEqual({ kind: "error", message: "unknown project" });
    expect(captureOutcome(ev("capture_result", {}))?.kind).toBe("error");
    expect(captureOutcome(ev("tool_call", { name: "Read" }))).toBeNull();
  });

  it("summarizes progress events in one line", () => {
    expect(captureLogText(ev("tool_call", { name: "list_backlog", args: { x: 1 } }))).toBe('list_backlog({"x":1})');
    expect(captureLogText(ev("tool_result", { result: "a\n  b" }))).toBe("→ a b");
    expect(captureLogText(ev("model_turn", { text: "" }))).toBeNull();
    expect(captureLogText(ev("usage", { input: 1 }))).toBeNull();
  });

  it("creates a task: log, then the created outcome", () => {
    const s = run([
      { type: "submit", description: "Retry fetch on 5xx" },
      { type: "event", event: ev("tool_call", { name: "list_backlog", args: "{}" }) },
      { type: "event", event: ev("capture_result", { task_id: "0042", title: "Retry fetch" }) },
    ]);
    expect(s.busy).toBe(false);
    expect(s.stage).toBe("created");
    expect(s.created).toEqual({ taskId: "0042", title: "Retry fetch" });
    expect(s.log.map((l) => [l.who, l.text])).toEqual([
      ["you", "Retry fetch on 5xx"],
      ["agent", "list_backlog({})"],
    ]);
  });

  it("asks one question and keeps the description for the follow-up", () => {
    const asked = run([
      { type: "submit", description: "make it faster" },
      { type: "event", event: ev("capture_result", { question: "Which page?" }) },
    ]);
    expect(asked).toMatchObject({ stage: "question", question: "Which page?", busy: false, description: "make it faster" });
    const answered = captureReducer(asked, { type: "answer", answer: "the backlog" });
    expect(answered).toMatchObject({ busy: true, stage: "question", description: "make it faster", question: "Which page?" });
    expect(answered.log.at(-1)).toMatchObject({ who: "you", text: "the backlog" });
  });

  it("surfaces errors without losing the description", () => {
    const failed = run([
      { type: "submit", description: "x" },
      { type: "event", event: ev("capture_result", { error: "model unavailable" }) },
    ]);
    expect(failed).toMatchObject({ busy: false, stage: "describe", error: "model unavailable", description: "x" });
    expect(captureReducer(run([{ type: "submit", description: "y" }]), { type: "ended" }).error).toMatch(/without a result/);
    expect(captureReducer(run([{ type: "submit", description: "y" }]), { type: "failed", message: "network" })).toMatchObject({
      busy: false,
      error: "network",
      description: "y",
    });
    // A stream end after the result changes nothing.
    const done = run([
      { type: "submit", description: "z" },
      { type: "event", event: ev("capture_result", { task_id: "1" }) },
      { type: "ended" },
    ]);
    expect(done.error).toBeNull();
  });
});

describe("app actions", () => {
  const key = (code: string, mods: Partial<{ alt: boolean; shift: boolean; ctrl: boolean; meta: boolean }> = {}) => ({
    code,
    key: "",
    altKey: !!mods.alt,
    shiftKey: !!mods.shift,
    ctrlKey: !!mods.ctrl,
    metaKey: !!mods.meta,
  });

  it("matches shortcuts exactly", () => {
    const s = { code: "KeyN", alt: true, label: "Alt+N" };
    expect(matchShortcut(key("KeyN", { alt: true }), s)).toBe(true);
    expect(matchShortcut(key("KeyN"), s)).toBe(false);
    expect(matchShortcut(key("KeyN", { alt: true, shift: true }), s)).toBe(false);
    expect(matchShortcut(key("KeyN", { alt: true, ctrl: true }), s)).toBe(false);
    expect(matchShortcut({ ...key("KeyN", { alt: true }), isComposing: true }, s)).toBe(false);
    const mod = { code: "KeyK", mod: true, label: "Ctrl+K" };
    expect(matchShortcut(key("KeyK", { meta: true }), mod)).toBe(true);
    expect(matchShortcut(key("KeyK", { ctrl: true }), mod)).toBe(true);
  });

  it("registers, replaces, runs, and unregisters actions", () => {
    const first = vi.fn();
    const second = vi.fn();
    const off1 = registerAction({ id: "t.one", title: "One", run: first });
    const off2 = registerAction({ id: "t.one", title: "One again", run: second });
    expect(listActions().filter((a) => a.id === "t.one")).toHaveLength(1);
    expect(runAction("t.one")).toBe(true);
    expect(second).toHaveBeenCalledOnce();
    expect(first).not.toHaveBeenCalled();
    off1(); // stale unregister of a replaced action is a no-op
    expect(listActions().some((a) => a.id === "t.one")).toBe(true);
    off2();
    expect(runAction("t.one")).toBe(false);
  });

  it("treats only text entry as editable", () => {
    const el = (tagName: string, extra: Record<string, unknown> = {}) =>
      ({ tagName, closest: () => null, isContentEditable: false, ...extra }) as unknown as EventTarget;
    expect(isEditableTarget(el("TEXTAREA"))).toBe(true);
    expect(isEditableTarget(el("INPUT", { type: "search" }))).toBe(true);
    expect(isEditableTarget(el("INPUT", { type: "checkbox" }))).toBe(false);
    expect(isEditableTarget(el("DIV", { isContentEditable: true }))).toBe(true);
    expect(isEditableTarget(el("BUTTON"))).toBe(false);
    expect(isEditableTarget(null)).toBe(false);
  });
});
