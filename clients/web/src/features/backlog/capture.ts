// Quick capture (CaptureBacklogItem): a pure reducer over the streamed capture
// agent's action log, mirroring the TUI overlay. A description goes in; the
// stream ends with a `capture_result` event that carries the created task,
// one clarifying question (answered by re-invoking with prior_question /
// prior_answer), or an error. The description survives every failure.
import type { Event } from "../../gen/ycc/v1/ycc_pb";

export interface CaptureLine {
  key: number;
  who: "you" | "agent";
  text: string;
}

export interface CaptureState {
  stage: "describe" | "question" | "created";
  busy: boolean;
  /** The description as first submitted (re-sent with the answer). */
  description: string;
  question: string;
  log: CaptureLine[];
  error: string | null;
  created: { taskId: string; title: string } | null;
}

export const INITIAL_CAPTURE: CaptureState = {
  stage: "describe",
  busy: false,
  description: "",
  question: "",
  log: [],
  error: null,
  created: null,
};

export type CaptureAction =
  | { type: "submit"; description: string }
  | { type: "answer"; answer: string }
  | { type: "event"; event: Event }
  | { type: "ended" }
  | { type: "failed"; message: string }
  | { type: "reset" };

function parse(ev: Event): Record<string, unknown> {
  try {
    const v = JSON.parse(ev.dataJson || "{}");
    return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : v === undefined || v === null ? "" : JSON.stringify(v);
}

function oneLine(s: string, max: number): string {
  const flat = s.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}

/** A one-line progress entry for a capture-agent event (null: not shown). */
export function captureLogText(ev: Event): string | null {
  const d = parse(ev);
  switch (ev.type) {
    case "tool_call":
      return `${str(d.name)}(${oneLine(str(d.args), 70)})`;
    case "tool_result": {
      const r = oneLine(str(d.result), 90);
      return r ? `→ ${r}` : null;
    }
    case "model_turn": {
      const t = oneLine(str(d.text), 140);
      return t || null;
    }
    case "session_error":
    case "error": {
      const m = oneLine(str(d.message ?? d.error), 140);
      return m ? `error: ${m}` : null;
    }
    default:
      return null;
  }
}

export type CaptureOutcome =
  | { kind: "created"; taskId: string; title: string }
  | { kind: "question"; question: string }
  | { kind: "error"; message: string };

/** The outcome a terminal `capture_result` event carries (null for others). */
export function captureOutcome(ev: Event): CaptureOutcome | null {
  if (ev.type !== "capture_result") return null;
  const d = parse(ev);
  const error = str(d.error).trim();
  if (error) return { kind: "error", message: error };
  const taskId = str(d.task_id).trim();
  if (taskId) return { kind: "created", taskId, title: str(d.title) };
  const question = str(d.question).trim();
  if (question) return { kind: "question", question };
  return { kind: "error", message: "The capture finished without a result." };
}

let lineKey = 0;
function line(who: CaptureLine["who"], text: string): CaptureLine {
  return { key: ++lineKey, who, text };
}

export function captureReducer(state: CaptureState, action: CaptureAction): CaptureState {
  switch (action.type) {
    case "submit":
      return {
        ...INITIAL_CAPTURE,
        busy: true,
        description: action.description,
        log: [line("you", action.description)],
      };
    case "answer":
      return { ...state, busy: true, error: null, log: [...state.log, line("you", action.answer)] };
    case "event": {
      const outcome = captureOutcome(action.event);
      if (!outcome) {
        const text = captureLogText(action.event);
        return text ? { ...state, log: [...state.log, line("agent", text)] } : state;
      }
      switch (outcome.kind) {
        case "created":
          return { ...state, busy: false, stage: "created", created: { taskId: outcome.taskId, title: outcome.title } };
        case "question":
          // A second question after an answer is shown the same way; the next
          // answer replaces the previous one.
          return { ...state, busy: false, stage: "question", question: outcome.question, error: null };
        case "error":
          return { ...state, busy: false, error: outcome.message };
      }
      return state;
    }
    case "ended":
      return state.busy ? { ...state, busy: false, error: state.error ?? "The capture ended without a result." } : state;
    case "failed":
      return { ...state, busy: false, error: action.message };
    case "reset":
      return INITIAL_CAPTURE;
  }
}
