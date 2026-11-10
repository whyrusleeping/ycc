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
    resumeSession: vi.fn(async () => ({})),
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

  it("replaces every row with a fresh snapshot when the daemon rebuilt its index", async () => {
    const { api, raw } = fakeApi({
      live: true,
      scripts: [
        {
          updates: [
            create(SessionViewUpdateSchema, { seq: 7n, state: viewState(7), upsertedRows: [protoRow(7, "seven")] }),
            create(SessionViewUpdateSchema, { indexRebuilt: true }),
          ],
        },
        { updates: [], end: "hang" },
      ],
    });
    raw.getSessionView.mockImplementationOnce(raw.getSessionView.getMockImplementation()!).mockResolvedValueOnce(
      create(GetSessionViewResponseSchema, {
        state: viewState(4, { indexGeneration: 9n }),
        rows: [protoRow(2, "rebuilt two"), protoRow(4, "rebuilt four")],
      }),
    );
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => raw.subscribeSessionView.mock.calls.length >= 2);
    await until(() => c.getSnapshot().conn === "streaming");
    expect(raw.getSessionView).toHaveBeenCalledTimes(2);
    const calls = raw.subscribeSessionView.mock.calls.map(([req]) => req as { fromSeq: bigint; indexGeneration: bigint });
    expect(calls.map((r) => [r.fromSeq, r.indexGeneration])).toEqual([
      [5n, 0n],
      [4n, 9n],
    ]);
    const snap = c.getSnapshot();
    expect(snap.cursor).toBe(4);
    expect(snap.mode).toBe("live");
    expect(snap.rows.map((r) => r.id)).toEqual(["seq-2", "seq-4"]);
    c.dispose();
  });

  it("drops expanded and in-flight row details from before an index rebuild", async () => {
    const detailed = (seq: number, text: string) => create(SessionPresentationRowSchema, { ...protoRow(seq, text), hasDetail: true });
    let rebuild: () => void = () => {};
    const rebuilt = new Promise<void>((r) => (rebuild = r));
    const { api, raw } = fakeApi({ live: true, scripts: [{ updates: [], end: "hang" }] });
    raw.subscribeSessionView.mockImplementationOnce(() =>
      (async function* () {
        await rebuilt;
        yield create(SessionViewUpdateSchema, { indexRebuilt: true });
      })(),
    );
    raw.getSessionView
      .mockResolvedValueOnce(
        create(GetSessionViewResponseSchema, { state: viewState(5), rows: [detailed(4, "four"), detailed(5, "five")] }),
      )
      .mockResolvedValueOnce(
        create(GetSessionViewResponseSchema, {
          state: viewState(5, { indexGeneration: 9n }),
          rows: [detailed(4, "rebuilt four"), detailed(5, "rebuilt five")],
        }),
      );
    let answerFour: (row: ReturnType<typeof protoRow>) => void = () => {};
    raw.getSessionViewDetail
      .mockResolvedValueOnce({ row: protoRow(5, "old full five") })
      .mockReturnValueOnce(new Promise((r) => (answerFour = (row) => r({ row }))));
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    const texts = () => c.getSnapshot().rows.map((r) => (r.kind as { text?: string }).text);
    c.start();
    await until(() => c.getSnapshot().conn === "streaming");
    await c.loadDetail("seq-5");
    expect(texts()).toEqual(["four", "old full five"]);
    const inFlight = c.loadDetail("seq-4");
    rebuild();
    await until(() => raw.subscribeSessionView.mock.calls.length >= 2 && c.getSnapshot().conn === "streaming");
    expect(texts()).toEqual(["rebuilt four", "rebuilt five"]);
    answerFour(protoRow(4, "old full four"));
    await inFlight;
    expect(texts()).toEqual(["rebuilt four", "rebuilt five"]);
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

  it("stops retrying a repeated server error, shows it, and retries by hand", async () => {
    const broken = new ConnectError("sequence discontinuity at 6", Code.Internal);
    const { api, subscribeCalls } = fakeApi({
      live: true,
      scripts: [
        { updates: [], end: "error", error: broken },
        { updates: [], end: "error", error: broken },
        { updates: [], end: "error", error: broken },
        { updates: [], end: "hang" },
      ],
    });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "failed");
    expect(c.getSnapshot().failure).toBe("sequence discontinuity at 6");
    expect(subscribeCalls).toEqual([5n, 5n, 5n]);
    await new Promise((r) => setTimeout(r, 20));
    expect(subscribeCalls.length).toBe(3);
    c.reconnect();
    await until(() => c.getSnapshot().conn === "streaming");
    expect(subscribeCalls).toEqual([5n, 5n, 5n, 5n]);
    c.dispose();
  });

  it("keeps reconnecting through network drops and server errors that make progress", async () => {
    const internal = () => ({ updates: [], end: "error" as const, error: new ConnectError("blip", Code.Internal) });
    const durable = (seq: number) =>
      create(SessionViewUpdateSchema, { seq: BigInt(seq), state: viewState(seq), upsertedRows: [protoRow(seq, `r${seq}`)] });
    const { api, subscribeCalls } = fakeApi({
      live: true,
      scripts: [
        { updates: [], end: "error", error: new ConnectError("Failed to fetch", Code.Unknown) },
        { updates: [], end: "error" }, // unavailable
        { updates: [], end: "error", error: new ConnectError("Failed to fetch", Code.Unknown) },
        { updates: [], end: "error", error: new ConnectError("deadline", Code.DeadlineExceeded) },
        internal(),
        internal(),
        { updates: [durable(6)], end: "error", error: new ConnectError("blip", Code.Internal) },
        internal(),
        { updates: [], end: "hang" },
      ],
    });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => subscribeCalls.length >= 9 && c.getSnapshot().conn === "streaming");
    expect(subscribeCalls).toEqual([5n, 5n, 5n, 5n, 5n, 5n, 5n, 6n, 6n]);
    expect(c.getSnapshot().cursor).toBe(6);
    c.dispose();
  });

  it("a failed read-only reload retries with a fresh snapshot", async () => {
    const { api, raw } = fakeApi({
      live: true,
      scripts: [{ updates: [create(SessionViewUpdateSchema, {})], end: "error", error: new ConnectError("ended", Code.NotFound) }],
    });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    const broken = new ConnectError("index corrupt", Code.DataLoss);
    raw.getSessionView
      .mockImplementationOnce(raw.getSessionView.getMockImplementation()!)
      .mockRejectedValueOnce(broken)
      .mockRejectedValueOnce(broken)
      .mockRejectedValueOnce(broken);
    c.start();
    await until(() => c.getSnapshot().conn === "failed");
    expect(c.getSnapshot()).toMatchObject({ mode: "persisted", failure: "index corrupt" });
    c.reconnect();
    await until(() => c.getSnapshot().conn === "finished");
    expect(raw.getSessionView).toHaveBeenCalledTimes(5);
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
    expect(raw.sendInput).toHaveBeenCalledWith({ sessionId: "s1", text: "hello", images: [] });
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

  it("a send to a persisted session re-opens it, goes live from its cursor, then delivers", async () => {
    const { api, raw, subscribeCalls } = fakeApi({ live: false, scripts: [{ updates: [], end: "hang" }] });
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "finished");
    expect(c.getSnapshot().mode).toBe("persisted");
    const first = c.send("hello");
    const second = c.send("again");
    expect(c.getSnapshot().reopening).toBe(true);
    await Promise.all([first, second]);
    expect(raw.resumeSession).toHaveBeenCalledTimes(1);
    expect(raw.resumeSession).toHaveBeenCalledWith({ project: "p", sessionId: "s1" });
    expect(raw.resumeSession.mock.invocationCallOrder[0]).toBeLessThan(raw.sendInput.mock.invocationCallOrder[0]);
    expect(raw.sendInput).toHaveBeenCalledTimes(2);
    await until(() => c.getSnapshot().conn === "streaming");
    const snap = c.getSnapshot();
    expect(snap.mode).toBe("live");
    expect(snap.reopening).toBe(false);
    // History painted first; the live stream resumes from its cursor.
    expect(subscribeCalls).toEqual([5n]);
    expect(raw.getSessionView).toHaveBeenCalledTimes(1);
    c.dispose();
  });

  it("a send into a paused session steers it, then continues it", async () => {
    const { api, raw } = fakeApi({ live: true, scripts: [{ updates: [], end: "hang" }] });
    raw.getSessionView.mockResolvedValueOnce(
      create(GetSessionViewResponseSchema, { state: viewState(5, { phase: "paused" }), rows: [protoRow(5, "snapshot")] }),
    );
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().conn === "streaming");
    expect(c.getSnapshot().phase.kind).toBe("paused");
    await c.send("use the other API");
    expect(raw.sendInput).toHaveBeenCalledWith({ sessionId: "s1", text: "use the other API", images: [] });
    expect(raw.resume).toHaveBeenCalledTimes(1);
    expect(raw.resume).toHaveBeenCalledWith({ sessionId: "s1" });
    expect(raw.sendInput.mock.invocationCallOrder[0]).toBeLessThan(raw.resume.mock.invocationCallOrder[0]);
    c.dispose();
  });

  it("keeps the history and fails the message when re-opening fails", async () => {
    const { api, raw } = fakeApi({ live: false, scripts: [{ updates: [] }] });
    raw.resumeSession.mockRejectedValueOnce(new ConnectError("model disabled", Code.FailedPrecondition));
    const h = hooks();
    const c = new SessionController(api, "p", "s1", h, fast);
    c.start();
    await until(() => c.getSnapshot().conn === "finished");
    await c.send("hello");
    expect(h.onError).toHaveBeenCalledWith("Send failed: model disabled", expect.objectContaining({ op: "session.send" }));
    expect(raw.sendInput).not.toHaveBeenCalled();
    const snap = c.getSnapshot();
    expect(snap.mode).toBe("persisted");
    expect(snap.rows.length).toBe(1);
    expect(snap.pendingMessages.map((m) => m.status)).toEqual(["failed"]);
    c.dispose();
  });

  it("sends pictures with the message and retires the bubble on the echo with pictures", async () => {
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
    const pic = { id: "pic-1", data: new Uint8Array([1, 2, 3]), mediaType: "image/png", filename: "a.png", previewUrl: null };
    await c.send("", [pic]);
    expect(raw.sendInput).toHaveBeenCalledWith({
      sessionId: "s1",
      text: "",
      images: [{ data: pic.data, mediaType: "image/png", filename: "a.png" }],
    });
    expect(c.getSnapshot().pendingMessages[0].pictures).toEqual([pic]);
    push(
      create(SessionViewUpdateSchema, {
        seq: 6n,
        state: viewState(6),
        upsertedRows: [
          create(SessionPresentationRowSchema, {
            id: "seq-6",
            positionSeq: 6n,
            updatedSeq: 6n,
            events: [
              create(EventSchema, {
                seq: 6n,
                actor: "user",
                type: "user_input",
                dataJson: JSON.stringify({ text: "", images: [{ attachment_id: "a_1", media_type: "image/png", filename: "a.png" }] }),
              }),
            ],
          }),
        ],
      }),
    );
    await until(() => c.getSnapshot().pendingMessages.length === 0);
    c.dispose();
  });

  it("pictures never take the answer path; a failed send returns text and pictures for editing", async () => {
    const asked = create(SessionViewUpdateSchema, {
      seq: 6n,
      state: viewState(6, { pendingRowId: "seq-6", pendingQuestions: [{ prompt: "Ship?", options: [] }] }),
      upsertedRows: [
        create(SessionPresentationRowSchema, {
          id: "seq-6",
          positionSeq: 6n,
          updatedSeq: 6n,
          events: [create(EventSchema, { seq: 6n, type: "question_asked", dataJson: '{"question":"Ship?"}' })],
        }),
      ],
    });
    const { api, raw } = fakeApi({ live: true, scripts: [{ updates: [asked], end: "hang" }] });
    raw.sendInput.mockRejectedValueOnce(new ConnectError("answer the question first", Code.FailedPrecondition));
    const c = new SessionController(api, "p", "s1", hooks(), fast);
    c.start();
    await until(() => c.getSnapshot().awaitsAnswer);
    const pic = { id: "pic-1", data: new Uint8Array([1]), mediaType: "image/png", filename: "a.png", previewUrl: null };
    await c.send("see this", [pic]);
    const failed = c.getSnapshot().pendingMessages[0];
    expect(failed.status).toBe("failed");
    expect(c.getSnapshot().answerInFlight).toBe(false);
    expect(c.discardSend(failed.id)).toEqual({ text: "see this", pictures: [pic] });
    c.dispose();
  });
});
