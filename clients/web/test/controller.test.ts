// SessionController against a fake client: resubscribe from the cursor with
// backoff, persisted-only sessions, paging, and durable question dismissal.
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import {
  EventSchema,
  GetSessionViewPageResponseSchema,
  GetSessionViewResponseSchema,
  ListSessionsResponseSchema,
  SessionInfoSchema,
  SessionPresentationRowSchema,
  SessionViewStateSchema,
  SessionViewUpdateSchema,
  type SessionViewUpdate,
} from "../src/gen/ycc/v1/ycc_pb";
import { SessionController, type SessionApi } from "../src/features/session/controller";

function protoRow(seq: number, text: string) {
  return create(SessionPresentationRowSchema, {
    id: `seq-${seq}`,
    positionSeq: BigInt(seq),
    updatedSeq: BigInt(seq),
    events: [create(EventSchema, { seq: BigInt(seq), actor: "coordinator", type: "model_turn", dataJson: JSON.stringify({ text }) })],
  });
}

function viewState(through: number, extra: Record<string, unknown> = {}) {
  return create(SessionViewStateSchema, { indexedThroughSeq: BigInt(through), phase: "running", rolloverAvailable: true, ...extra });
}

type Script = { updates: SessionViewUpdate[]; end?: "error" | "close" | "hang"; error?: ConnectError };

function fakeApi(opts: { live: boolean; scripts: Script[]; snapshotThrough?: number; earlierCursor?: string }) {
  const subscribeCalls: bigint[] = [];
  let i = 0;
  const api = {
    getSessionView: vi.fn(async () =>
      create(GetSessionViewResponseSchema, {
        state: viewState(opts.snapshotThrough ?? 5),
        rows: [protoRow(opts.snapshotThrough ?? 5, "snapshot")],
        earlierCursor: opts.earlierCursor ?? "",
      }),
    ),
    listSessions: vi.fn(async () =>
      create(ListSessionsResponseSchema, {
        sessions: opts.live ? [create(SessionInfoSchema, { sessionId: "s1", status: "running" })] : [],
      }),
    ),
    subscribeSessionView: vi.fn((req: { fromSeq?: bigint }, callOpts?: { signal?: AbortSignal }) => {
      subscribeCalls.push(req.fromSeq ?? 0n);
      const script = opts.scripts[Math.min(i++, opts.scripts.length - 1)];
      return (async function* () {
        for (const u of script.updates) yield u;
        if (script.end === "error") throw script.error ?? new ConnectError("dropped", Code.Unavailable);
        if (script.end === "hang") {
          await new Promise((_, reject) =>
            callOpts?.signal?.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled))),
          );
        }
      })();
    }),
    getSessionViewPage: vi.fn(async () =>
      create(GetSessionViewPageResponseSchema, { rows: [protoRow(1, "earliest")], indexedThroughSeq: 5n, earlierCursor: "" }),
    ),
    getSessionViewDetail: vi.fn(),
    sendInput: vi.fn(async () => ({})),
    answerQuestion: vi.fn(async () => ({})),
    answerQuestions: vi.fn(async () => ({})),
    interrupt: vi.fn(async () => ({})),
    resume: vi.fn(async () => ({})),
    stopSession: vi.fn(async () => ({})),
  };
  return { api: api as unknown as SessionApi, raw: api, subscribeCalls };
}

const hooks = () => ({ onUnauthorized: vi.fn(), onError: vi.fn(), onInfo: vi.fn() });

async function until(pred: () => boolean, ms = 2000) {
  const start = Date.now();
  while (!pred()) {
    if (Date.now() - start > ms) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 2));
  }
}

const fast = { backoffInitialMs: 1, backoffMaxMs: 4, publishDelayMs: 0 };

describe("SessionController", () => {
  it("resubscribes from the last indexed_through_seq after a drop; transients never move it", async () => {
    const transient = create(SessionViewUpdateSchema, {
      transientEvent: create(EventSchema, { seq: 0n, transient: true, actor: "coordinator", type: "turn_delta", dataJson: '{"text":"…"}' }),
    });
    const durable = create(SessionViewUpdateSchema, { seq: 7n, state: viewState(7), upsertedRows: [protoRow(7, "seven")] });
    const keepalive = create(SessionViewUpdateSchema, {});
    const { api, subscribeCalls } = fakeApi({
      live: true,
      scripts: [
        { updates: [transient, durable, keepalive, transient], end: "error" },
        { updates: [], end: "hang" },
      ],
    });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => subscribeCalls.length >= 2);
    expect(subscribeCalls).toEqual([5n, 7n]);
    await until(() => c.getSnapshot().conn === "streaming");
    const snap = c.getSnapshot();
    expect(snap.cursor).toBe(7);
    // The reconnect dropped the stale live tail but kept durable rows.
    expect(snap.rows.map((r) => r.id)).toEqual(["seq-5", "seq-7"]);
    c.dispose();
  });

  it("renders a persisted-only session as a finite read-only view", async () => {
    const { api, raw } = fakeApi({ live: false, scripts: [{ updates: [] }] });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "finished");
    expect(c.getSnapshot().mode).toBe("persisted");
    expect(raw.subscribeSessionView).not.toHaveBeenCalled();
    c.dispose();
  });

  it("falls back to read-only when the stream reports the session missing", async () => {
    const { api } = fakeApi({
      live: true,
      scripts: [{ updates: [], end: "error", error: new ConnectError("no session", Code.NotFound) }],
    });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "finished");
    expect(c.getSnapshot().mode).toBe("persisted");
    c.dispose();
  });

  it("routes a 401 to the unauthorized hook", async () => {
    const { api } = fakeApi({
      live: true,
      scripts: [{ updates: [], end: "error", error: new ConnectError("bad token", Code.Unauthenticated) }],
    });
    const h = hooks();
    const c = new SessionController(api, "p", "s1", h, fast);
    c.start();
    await until(() => h.onUnauthorized.mock.calls.length > 0);
    c.dispose();
  });

  it("prepends an earlier page", async () => {
    const { api } = fakeApi({ live: false, scripts: [{ updates: [] }], earlierCursor: "cur" });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().installed);
    expect(c.getSnapshot().hasEarlier).toBe(true);
    await c.loadEarlier();
    const snap = c.getSnapshot();
    expect(snap.rows.map((r) => r.id)).toEqual(["seq-1", "seq-5"]);
    expect(snap.hasEarlier).toBe(false);
    expect(snap.earlierRevision).toBe(1);
    c.dispose();
  });

  it("keeps the answer panel until durable state closes the question", async () => {
    const asked = create(SessionViewUpdateSchema, {
      seq: 6n,
      state: viewState(6, { pendingRowId: "seq-6", pendingQuestions: [{ prompt: "Ship?", options: ["yes", "no"] }] }),
      upsertedRows: [
        create(SessionPresentationRowSchema, {
          id: "seq-6",
          positionSeq: 6n,
          updatedSeq: 6n,
          events: [create(EventSchema, { seq: 6n, type: "question_asked", dataJson: '{"question":"Ship?","options":["yes","no"]}' })],
        }),
      ],
    });
    let release: (u: SessionViewUpdate) => void = () => {};
    const answered = new Promise<SessionViewUpdate>((r) => (release = r));
    const { api, raw } = fakeApi({ live: true, scripts: [{ updates: [asked], end: "hang" }] });
    raw.subscribeSessionView.mockImplementationOnce(() =>
      (async function* () {
        yield asked;
        yield await answered;
        await new Promise(() => {});
      })(),
    );
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().pendingQuestion !== null);
    expect(await c.answerOption(0)).toBe(true);
    expect(raw.answerQuestion).toHaveBeenCalledWith({ sessionId: "s1", text: "", optionIndex: 0 });
    let snap = c.getSnapshot();
    expect(snap.pendingQuestion?.rowId).toBe("seq-6");
    expect(snap.answeredRowId).toBe("seq-6");
    release(create(SessionViewUpdateSchema, { seq: 7n, state: viewState(7) }));
    await until(() => c.getSnapshot().pendingQuestion === null);
    snap = c.getSnapshot();
    expect(snap.answeredRowId).toBeNull();
    c.dispose();
  });

  it("retires a provisional message when its durable echo arrives", async () => {
    let push: (u: SessionViewUpdate) => void = () => {};
    const { api, raw } = fakeApi({ live: true, scripts: [{ updates: [], end: "hang" }] });
    raw.subscribeSessionView.mockImplementationOnce(() =>
      (async function* () {
        for (;;) yield await new Promise<SessionViewUpdate>((r) => (push = r));
      })(),
    );
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "streaming");
    await c.send("  hello  ");
    expect(raw.sendInput).toHaveBeenCalledWith({ sessionId: "s1", text: "hello" });
    expect(c.getSnapshot().pendingMessages.map((m) => m.status)).toEqual(["sent"]);
    push(
      create(SessionViewUpdateSchema, {
        seq: 6n,
        state: viewState(6),
        upsertedRows: [
          create(SessionPresentationRowSchema, {
            id: "seq-6",
            positionSeq: 6n,
            updatedSeq: 6n,
            events: [create(EventSchema, { seq: 6n, actor: "user", type: "user_input", dataJson: '{"text":"hello","queued":true}' })],
          }),
        ],
      }),
    );
    await until(() => c.getSnapshot().pendingMessages.length === 0);
    const user = c.getSnapshot().rows.find((r) => r.id === "seq-6");
    expect(user?.userInputStatus).toBe("queued");
    c.dispose();
  });
});
