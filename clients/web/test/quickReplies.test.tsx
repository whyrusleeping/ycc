// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { QuickReplySchema } from "../src/gen/ycc/v1/ycc_pb";
import type { SessionSnapshot } from "../src/features/session/controller";

const track = vi.hoisted(() => ({ action: vi.fn() }));
vi.mock("../src/app/analytics", () => ({ track }));

const { Composer } = await import("../src/features/session/Composer");
const { quickRepliesAllowed } = await import("../src/features/session/SessionView");

const replies = [
  create(QuickReplySchema, { id: "go_ahead", label: "Go ahead", text: "Go ahead" }),
  create(QuickReplySchema, { label: "Deploy", text: "Deploy to prod following the runbook" }),
];

let container: HTMLDivElement;
let root: Root;
const onSend = vi.fn();

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  onSend.mockReset();
  track.action.mockReset();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function render(props: { quickReplies?: typeof replies; disabled?: boolean; sessionKey?: string } = {}) {
  await act(() =>
    root.render(
      <Composer
        sessionKey={props.sessionKey ?? "p\u0000s"}
        placeholder=""
        onSend={onSend}
        disabled={props.disabled}
        sendAttrs={{ phase: "idle" }}
        quickReplies={props.quickReplies}
      />,
    ),
  );
}
const chips = () => [...container.querySelectorAll<HTMLButtonElement>(".quick-replies .chip")].map((b) => b.textContent);

it("sends a quick reply's text in one click and records only its kind", async () => {
  await render({ quickReplies: replies });
  expect(chips()).toEqual(["Go ahead", "Deploy"]);
  await act(() => container.querySelectorAll<HTMLButtonElement>(".quick-replies .chip")[1].click());
  expect(onSend).toHaveBeenCalledWith("Deploy to prod following the runbook", []);
  expect(track.action).toHaveBeenCalledWith("composer.quick_reply", "click", { phase: "idle", kind: "project" });
  expect(JSON.stringify(track.action.mock.calls)).not.toContain("runbook");
  // The chips go away at once, so a double click can't send the reply twice.
  expect(chips()).toEqual([]);
  expect(onSend).toHaveBeenCalledTimes(1);
});

it("hides quick replies once the user types, when disabled, or when none are offered", async () => {
  await render({ quickReplies: replies, sessionKey: "typing" });
  const area = container.querySelector("textarea")!;
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "no wait");
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(chips()).toEqual([]);

  await render({ quickReplies: replies, disabled: true, sessionKey: "disabled" });
  expect(chips()).toEqual([]);
  await render({ sessionKey: "none" });
  expect(chips()).toEqual([]);
});

it("offers quick replies only when no turn is running and no question is pending", () => {
  const snap = (s: Partial<SessionSnapshot>) => ({ mode: "live", phase: { kind: "idle" }, pendingQuestion: null, ...s }) as SessionSnapshot;
  expect(quickRepliesAllowed(snap({}))).toBe(true);
  expect(quickRepliesAllowed(snap({ phase: { kind: "paused" } } as Partial<SessionSnapshot>))).toBe(true);
  expect(quickRepliesAllowed(snap({ mode: "persisted", phase: { kind: "running" } } as Partial<SessionSnapshot>))).toBe(true);
  expect(quickRepliesAllowed(snap({ phase: { kind: "running" } } as Partial<SessionSnapshot>))).toBe(false);
  expect(quickRepliesAllowed(snap({ pendingQuestion: {} } as Partial<SessionSnapshot>))).toBe(false);
});
