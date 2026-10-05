// SessionController owns one open session's transcript: it loads the newest
// indexed page (GetSessionView), streams SubscribeSessionView from the
// indexed_through_seq cursor with backoff, pages earlier rows and row detail on
// demand, and runs the interactive actions (send with pictures, answer,
// interrupt/resume, stop, reopen via ResumeSession) with the daemon's durable
// state as the source of truth. React reads
// immutable snapshots through useSyncExternalStore (see useSession.ts).
import { Code, ConnectError } from "@connectrpc/connect";
import type { YccClient } from "../../api/client";
import { errorMessage } from "../../api/client";
import { toImageAttachments, type DraftPicture } from "../attachments/attachments";
import { SessionProjection, type PendingQuestion, type Phase, type TranscriptRow } from "./projection";
import { fromEvent, fromRow, fromState } from "./wire";

export type ConnState = "loading" | "streaming" | "reconnecting" | "finished" | "failed";
/** live: the daemon holds the session; persisted: a finite read-only log. */
export type SessionMode = "unknown" | "live" | "persisted";

export interface PendingMessage {
  id: string;
  text: string;
  /** Pictures sent with the message (previews stay until it retires). */
  pictures: DraftPicture[];
  status: "sending" | "sent" | "failed";
  error?: string;
  /** Cursor when submitted; only a newer user row can be its echo. */
  baselineSeq: number | null;
}

export type ControlKind = "pause" | "resume" | "retry" | "stop" | "rollover";

export interface SessionSnapshot {
  rows: TranscriptRow[];
  pendingMessages: PendingMessage[];
  phase: Phase;
  pauseRequested: boolean;
  awaitingJobs: boolean;
  pendingQuestion: PendingQuestion | null;
  awaitsAnswer: boolean;
  coordinatorModel: string;
  contextTokens: number | null;
  rolloverAvailable: boolean;
  cursor: number;
  conn: ConnState;
  failure: string | null;
  mode: SessionMode;
  installed: boolean;
  hasEarlier: boolean;
  loadingEarlier: boolean;
  /** Bumped when an earlier page is prepended (scroll anchoring). */
  earlierRevision: number;
  /** Bumped when a fresh snapshot replaces the rows. */
  installRevision: number;
  loadingDetail: ReadonlySet<string>;
  answerInFlight: boolean;
  /** Row id of a question this client answered that durable state still lists. */
  answeredRowId: string | null;
  control: { kind: ControlKind; acknowledged: boolean } | null;
  /** A ResumeSession (reopen) call is in flight. */
  reopening: boolean;
}

export type SessionApi = Pick<
  YccClient,
  | "getSessionView"
  | "getSessionViewPage"
  | "getSessionViewDetail"
  | "subscribeSessionView"
  | "listSessions"
  | "sendInput"
  | "answerQuestion"
  | "answerQuestions"
  | "interrupt"
  | "resume"
  | "stopSession"
  | "resumeSession"
>;

export interface ControllerHooks {
  onUnauthorized: () => void;
  onError: (message: string) => void;
  onInfo?: (message: string) => void;
}

export interface ControllerOptions {
  backoffInitialMs?: number;
  backoffMaxMs?: number;
  publishDelayMs?: number;
  sentEchoTimeoutMs?: number;
  controlAckTimeoutMs?: number;
}

type Disposition = "transient" | "unauthorized" | "missing" | "cancelled" | "terminal";

export function classify(err: unknown): Disposition {
  if (err instanceof DOMException && err.name === "AbortError") return "cancelled";
  const ce = ConnectError.from(err);
  switch (ce.code) {
    case Code.Canceled:
      return "cancelled";
    case Code.Unauthenticated:
      return "unauthorized";
    case Code.NotFound:
      return "missing";
    case Code.FailedPrecondition:
    case Code.InvalidArgument:
    case Code.PermissionDenied:
    case Code.Unimplemented:
      return "terminal";
    default:
      return "transient";
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) return reject(new DOMException("aborted", "AbortError"));
    const t = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(t);
      reject(new DOMException("aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export class SessionController {
  readonly projection = new SessionProjection();
  private listeners = new Set<() => void>();
  private snapshot: SessionSnapshot;
  private publishTimer: ReturnType<typeof setTimeout> | null = null;
  private loop: AbortController | null = null;
  private generation = 0;
  private disposed = false;

  private conn: ConnState = "loading";
  private failure: string | null = null;
  private mode: SessionMode = "unknown";
  private installed = false;
  private earlierCursor = "";
  private loadingEarlier = false;
  private earlierInFlight: Promise<boolean> | null = null;
  private earlierRevision = 0;
  private installRevision = 0;
  private loadingDetail = new Set<string>();
  private pendingMessages: PendingMessage[] = [];
  private localCounter = 0;
  private answerInFlight = false;
  private answeredRowId: string | null = null;
  private control: { kind: ControlKind; acknowledged: boolean; token: number } | null = null;
  private controlToken = 0;
  private reopening = false;
  private opts: Required<ControllerOptions>;

  constructor(
    private api: SessionApi,
    readonly project: string,
    readonly sessionId: string,
    private hooks: ControllerHooks,
    opts: ControllerOptions = {},
  ) {
    this.opts = {
      backoffInitialMs: opts.backoffInitialMs ?? 500,
      backoffMaxMs: opts.backoffMaxMs ?? 10_000,
      publishDelayMs: opts.publishDelayMs ?? 16,
      sentEchoTimeoutMs: opts.sentEchoTimeoutMs ?? 15_000,
      controlAckTimeoutMs: opts.controlAckTimeoutMs ?? 8_000,
    };
    this.snapshot = this.buildSnapshot();
  }

  // MARK: store plumbing

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getSnapshot = () => this.snapshot;

  private buildSnapshot(): SessionSnapshot {
    const p = this.projection;
    return {
      rows: p.rows,
      pendingMessages: this.pendingMessages,
      phase: p.phase,
      pauseRequested: p.pauseRequested,
      awaitingJobs: p.awaitingJobs,
      pendingQuestion: p.pendingQuestion,
      awaitsAnswer: p.awaitsAnswer,
      coordinatorModel: p.coordinatorModel,
      contextTokens: p.contextTokens,
      rolloverAvailable: p.rolloverAvailable,
      cursor: p.lastPersistedSeq,
      conn: this.conn,
      failure: this.failure,
      mode: this.mode,
      installed: this.installed,
      hasEarlier: this.earlierCursor !== "",
      loadingEarlier: this.loadingEarlier,
      earlierRevision: this.earlierRevision,
      installRevision: this.installRevision,
      loadingDetail: new Set(this.loadingDetail),
      answerInFlight: this.answerInFlight,
      answeredRowId: this.answeredRowId,
      control: this.control ? { kind: this.control.kind, acknowledged: this.control.acknowledged } : null,
      reopening: this.reopening,
    };
  }

  /** Publish now (user actions) or coalesced (stream bursts). */
  private publish(now = true) {
    if (this.disposed) return;
    if (!now) {
      if (this.publishTimer === null) {
        this.publishTimer = setTimeout(() => {
          this.publishTimer = null;
          this.publish(true);
        }, this.opts.publishDelayMs);
      }
      return;
    }
    if (this.publishTimer !== null) {
      clearTimeout(this.publishTimer);
      this.publishTimer = null;
    }
    this.snapshot = this.buildSnapshot();
    for (const l of this.listeners) l();
  }

  // MARK: lifecycle

  /** Start (or restart) loading and streaming. */
  start() {
    if (this.disposed) return;
    if (this.loop) return;
    this.runLoop(!this.installed);
  }

  /** Stop streaming; the projection stays for a quick re-open. */
  stop() {
    this.generation++;
    this.loop?.abort();
    this.loop = null;
    if (this.projection.liveTails.length) {
      this.projection.clearLiveTails();
      this.publish();
    }
  }

  dispose() {
    this.stop();
    this.disposed = true;
    if (this.publishTimer !== null) clearTimeout(this.publishTimer);
    this.listeners.clear();
  }

  /** Resubscribe now (e.g. the tab became visible after a long sleep). */
  reconnect() {
    if (this.mode === "persisted" || this.disposed) return;
    this.stop();
    this.runLoop(!this.installed);
  }

  private current(gen: number) {
    return !this.disposed && gen === this.generation;
  }

  private setConn(conn: ConnState, failure: string | null = null) {
    this.conn = conn;
    this.failure = failure;
  }

  private async installSnapshot(signal: AbortSignal) {
    const modeAtStart = this.mode;
    const [snap, live] = await Promise.all([
      this.api.getSessionView({ project: this.project, sessionId: this.sessionId }, { signal }),
      this.mode === "unknown"
        ? this.api
            .listSessions({}, { signal })
            .then((r) => r.sessions.some((s) => s.sessionId === this.sessionId))
            .catch((err) => {
              if (classify(err) === "unauthorized") throw err;
              return true; // unknown: try to subscribe; NotFound settles it
            })
        : Promise.resolve(this.mode === "live"),
    ]);
    this.projection.installIndexed(fromState(snap.state!), snap.rows.map(fromRow));
    this.earlierCursor = snap.earlierCursor;
    this.installed = true;
    this.installRevision++;
    // A reopen that landed while this snapshot loaded wins over a stale
    // "not live" answer.
    if (!(this.mode === "live" && modeAtStart !== "live")) this.mode = live ? "live" : "persisted";
    this.reconcile();
    const state = snap.state;
    if (state?.pendingQuestionsTruncated && state.pendingRowId) void this.loadDetail(state.pendingRowId);
  }

  private runLoop(snapshotFirst: boolean) {
    const gen = ++this.generation;
    const abort = new AbortController();
    this.loop = abort;
    const signal = abort.signal;
    void (async () => {
      let delay = this.opts.backoffInitialMs;
      let needSnapshot = snapshotFirst;
      let streamed = false;
      while (this.current(gen)) {
        try {
          if (needSnapshot) {
            this.setConn(this.installed ? "reconnecting" : "loading");
            this.publish();
            await this.installSnapshot(signal);
            if (!this.current(gen)) return;
            needSnapshot = false;
            this.publish();
          }
          if (this.mode === "persisted") {
            this.setConn("finished");
            this.publish();
            return;
          }
          this.setConn("streaming");
          this.publish();
          const updates = this.api.subscribeSessionView(
            { sessionId: this.sessionId, fromSeq: BigInt(this.projection.lastPersistedSeq) },
            { signal },
          );
          for await (const update of updates) {
            if (!this.current(gen)) return;
            delay = this.opts.backoffInitialMs;
            streamed = true;
            if (this.mode !== "live") this.mode = "live";
            if (update.transientEvent) {
              this.projection.apply(fromEvent(update.transientEvent));
              this.publish(false);
            } else if (update.state) {
              this.projection.applyIndexed(
                fromState(update.state),
                update.upsertedRows.map(fromRow),
                update.deletedRowIds,
              );
              this.reconcile();
              this.publish(false);
              if (update.state.pendingQuestionsTruncated && update.state.pendingRowId) {
                void this.loadDetail(update.state.pendingRowId);
              }
            }
            // Neither: an idle-stream keepalive; nothing to fold.
          }
          if (!this.current(gen)) return;
          // The daemon closed the log: the session ended.
          this.finishReadOnly();
          return;
        } catch (err) {
          if (!this.current(gen)) return;
          switch (classify(err)) {
            case "cancelled":
              return;
            case "unauthorized":
              this.projection.clearLiveTails();
              this.setConn("failed", "unauthorized");
              this.publish();
              this.hooks.onUnauthorized();
              return;
            case "missing":
              if (!this.installed) {
                this.setConn("failed", "Session not found.");
                this.publish();
                return;
              }
              if (streamed) {
                // It ended while streaming; its log may have grown since.
                this.mode = "persisted";
                streamed = false;
                needSnapshot = true;
                continue;
              }
              this.finishReadOnly();
              return;
            case "terminal":
              this.projection.clearLiveTails();
              this.setConn("failed", errorMessage(err));
              this.publish();
              return;
            case "transient":
              // Stale tails would linger until each actor's next delta.
              this.projection.clearLiveTails();
              this.setConn("reconnecting");
              this.publish();
              try {
                await sleep(delay, signal);
              } catch {
                return;
              }
              delay = Math.min(delay * 2, this.opts.backoffMaxMs);
              // Resubscribe from the cursor; only a never-installed view
              // needs a snapshot first.
              needSnapshot = !this.installed;
          }
        }
      }
    })().finally(() => {
      if (this.loop === abort) this.loop = null;
    });
  }

  private finishReadOnly() {
    this.mode = "persisted";
    this.projection.clearLiveTails();
    this.pendingMessages = this.pendingMessages.filter((m) => m.status !== "sent");
    this.setConn("finished");
    this.publish();
  }

  // MARK: paging and detail

  /**
   * Page in the next earlier rows. Resolves true when a page was prepended;
   * a call while a page is loading shares that load (transcript search awaits
   * it rather than racing the scroll-triggered one).
   */
  loadEarlier(): Promise<boolean> {
    if (this.earlierInFlight) return this.earlierInFlight;
    if (!this.earlierCursor) return Promise.resolve(false);
    const load = this.fetchEarlier().finally(() => {
      if (this.earlierInFlight === load) this.earlierInFlight = null;
    });
    this.earlierInFlight = load;
    return load;
  }

  /** Whether earlier history remains unloaded. */
  hasEarlier(): boolean {
    return this.earlierCursor !== "";
  }

  private async fetchEarlier(): Promise<boolean> {
    const cursor = this.earlierCursor;
    const install = this.installRevision;
    this.loadingEarlier = true;
    this.publish();
    try {
      const page = await this.api.getSessionViewPage({ project: this.project, sessionId: this.sessionId, cursor });
      if (this.disposed || cursor !== this.earlierCursor || install !== this.installRevision) return false;
      this.projection.prependIndexed(page.rows.map(fromRow), Number(page.indexedThroughSeq));
      this.earlierCursor = page.earlierCursor;
      this.earlierRevision++;
      return true;
    } catch (err) {
      this.reportActionError("load earlier", err);
      return false;
    } finally {
      this.loadingEarlier = false;
      this.publish();
    }
  }

  /** Fetch a complete abbreviated row (explicit expand / inspector). */
  async loadDetail(rowId: string) {
    const row = this.projection.durableRows.find((r) => r.id === rowId);
    if (!(row?.detailAvailable || this.projection.needsPendingDetail(rowId))) return;
    if (this.loadingDetail.has(rowId)) return;
    this.loadingDetail.add(rowId);
    this.publish();
    try {
      const resp = await this.api.getSessionViewDetail({ project: this.project, sessionId: this.sessionId, rowId });
      if (this.disposed || !resp.row) return;
      this.projection.installIndexedDetail(fromRow(resp.row));
      this.reconcile();
    } catch (err) {
      this.reportActionError("load detail", err);
    } finally {
      this.loadingDetail.delete(rowId);
      this.publish();
    }
  }

  // MARK: input

  /**
   * SendInput. Text sent while a question is pending answers it (daemon
   * semantics), so it gets no provisional bubble; the durable state dismisses
   * the question.
   */
  async send(text: string, pictures: DraftPicture[] = []) {
    const trimmed = text.trim();
    if (!trimmed && !pictures.length) return;
    // Pictures never answer a question (the daemon refuses them while one is
    // pending), so only text-only input takes the answer path.
    if (this.projection.awaitsAnswer && !pictures.length) {
      this.answerInFlight = true;
      this.publish();
      try {
        await this.api.sendInput({ sessionId: this.sessionId, text: trimmed });
        this.answeredRowId = this.projection.pendingQuestion?.rowId ?? null;
      } catch (err) {
        this.pushFailed(trimmed, [], this.reportActionError("send", err));
      } finally {
        this.answerInFlight = false;
        this.reconcile();
        this.publish();
      }
      return;
    }
    const message: PendingMessage = {
      id: `local-${++this.localCounter}`,
      text: trimmed,
      pictures,
      status: "sending",
      baselineSeq: this.installed ? this.projection.lastPersistedSeq : null,
    };
    this.pendingMessages = [...this.pendingMessages, message];
    this.publish();
    await this.deliver(message.id);
  }

  async retrySend(id: string) {
    const msg = this.pendingMessages.find((m) => m.id === id && m.status === "failed");
    if (!msg) return;
    this.pendingMessages = this.pendingMessages.filter((m) => m.id !== id);
    this.publish();
    await this.send(msg.text, msg.pictures);
  }

  /** Drop a failed bubble, returning its draft for the composer. */
  discardSend(id: string): { text: string; pictures: DraftPicture[] } | undefined {
    const msg = this.pendingMessages.find((m) => m.id === id && m.status === "failed");
    if (!msg) return undefined;
    this.pendingMessages = this.pendingMessages.filter((m) => m.id !== id);
    this.publish();
    return { text: msg.text, pictures: msg.pictures };
  }

  private pushFailed(text: string, pictures: DraftPicture[], error: string) {
    this.pendingMessages = [
      ...this.pendingMessages,
      {
        id: `local-${++this.localCounter}`,
        text,
        pictures,
        status: "failed",
        error,
        baselineSeq: this.projection.lastPersistedSeq,
      },
    ];
  }

  private async deliver(id: string) {
    const msg = this.pendingMessages.find((m) => m.id === id);
    if (!msg) return;
    try {
      await this.api.sendInput({ sessionId: this.sessionId, text: msg.text, images: toImageAttachments(msg.pictures) });
      this.markMessage(id, { status: "sent" });
      // An accepted message whose echo never arrives must not linger.
      setTimeout(() => {
        const cur = this.pendingMessages.find((m) => m.id === id);
        if (cur?.status === "sent") {
          this.pendingMessages = this.pendingMessages.filter((m) => m.id !== id);
          releasePreviews(cur.pictures);
          this.publish();
        }
      }, this.opts.sentEchoTimeoutMs);
      this.reconcile();
    } catch (err) {
      this.markMessage(id, { status: "failed", error: this.reportActionError("send", err) });
    }
    this.publish();
  }

  private markMessage(id: string, patch: Partial<PendingMessage>) {
    this.pendingMessages = this.pendingMessages.map((m) => (m.id === id ? { ...m, ...patch } : m));
  }

  /** Retire provisional bubbles whose durable user_input row arrived. */
  private retireEchoes() {
    if (!this.pendingMessages.length) return;
    const withBaseline = this.pendingMessages.map((m) =>
      m.baselineSeq === null ? { ...m, baselineSeq: this.projection.lastPersistedSeq } : m,
    );
    const floor = Math.min(...withBaseline.map((m) => m.baselineSeq ?? 0));
    const candidates: TranscriptRow[] = [];
    const rows = this.projection.durableRows;
    for (let i = rows.length - 1; i >= 0; i--) {
      const row = rows[i];
      if (row.seq <= floor) break;
      if (row.kind.type === "user") candidates.unshift(row);
    }
    const claimed = new Set<string>();
    const kept = withBaseline.filter((m) => {
      if (m.status === "failed") return true;
      const echo = candidates.find(
        (row) =>
          !claimed.has(row.id) &&
          row.seq > (m.baselineSeq ?? Number.MAX_SAFE_INTEGER) &&
          row.kind.type === "user" &&
          row.kind.text.trim() === m.text &&
          row.kind.pictures.length === m.pictures.length,
      );
      if (!echo) return true;
      claimed.add(echo.id);
      releasePreviews(m.pictures);
      return false;
    });
    const changed =
      kept.length !== this.pendingMessages.length || kept.some((m, i) => m !== this.pendingMessages[i]);
    if (changed) this.pendingMessages = kept;
  }

  // MARK: reopen

  /**
   * ResumeSession: re-open a persisted session on its existing log, then
   * promote this view to live (subscribing from the painted history's cursor).
   * Idempotent server-side when the session is already live. On failure the
   * read-only history stays and the error is reported.
   */
  async reopen(): Promise<boolean> {
    if (this.reopening || this.disposed) return false;
    this.reopening = true;
    this.publish();
    try {
      await this.api.resumeSession({ project: this.project, sessionId: this.sessionId });
      if (this.disposed) return true;
      this.mode = "live";
      if (this.loop !== null || this.conn === "finished" || this.conn === "failed") {
        this.stop();
        this.runLoop(!this.installed);
      }
      return true;
    } catch (err) {
      this.reportActionError("reopen", err);
      return false;
    } finally {
      this.reopening = false;
      this.publish();
    }
  }

  // MARK: questions

  answerOption(optionIndex: number) {
    return this.submitAnswer(() =>
      this.api.answerQuestion({ sessionId: this.sessionId, text: "", optionIndex }),
    );
  }

  answerText(text: string) {
    return this.submitAnswer(() =>
      this.api.answerQuestion({ sessionId: this.sessionId, text, optionIndex: -1 }),
    );
  }

  /** Positional batch answers: optionIndex >= 0 picks an option, else text. */
  answerBatch(answers: { text: string; optionIndex: number }[]) {
    return this.submitAnswer(() => this.api.answerQuestions({ sessionId: this.sessionId, answers }));
  }

  /**
   * The answer panel stays until durable state (from any client) closes the
   * question; while the RPC is in flight further submissions are ignored.
   */
  private async submitAnswer(call: () => Promise<unknown>): Promise<boolean> {
    if (this.answerInFlight) return false;
    const rowId = this.projection.pendingQuestion?.rowId ?? null;
    this.answerInFlight = true;
    this.publish();
    try {
      await call();
      this.answeredRowId = rowId;
      return true;
    } catch (err) {
      const message = this.reportActionError("answer", err);
      if (message.toLowerCase().includes("no pending question") && this.mode === "live") {
        // Answered elsewhere: refresh durable state rather than trust ours.
        this.stop();
        this.runLoop(true);
      }
      return false;
    } finally {
      this.answerInFlight = false;
      this.reconcile();
      this.publish();
    }
  }

  // MARK: controls

  interrupt() {
    return this.runControl("pause", "interrupt", () => this.api.interrupt({ sessionId: this.sessionId }));
  }

  /** Continue a paused session, or cancel a requested pause. */
  resume() {
    return this.runControl("resume", "resume", () => this.api.resume({ sessionId: this.sessionId }));
  }

  /** Re-run a retryable failed turn (Resume on an errored session). */
  retry() {
    return this.runControl("retry", "retry", () => this.api.resume({ sessionId: this.sessionId }));
  }

  rollover() {
    return this.runControl("rollover", "context rollover", async () => {
      await this.api.resume({ sessionId: this.sessionId, rollover: true });
      this.hooks.onInfo?.("Context rollover requested");
    });
  }

  stopSession() {
    return this.runControl("stop", "stop", () => this.api.stopSession({ sessionId: this.sessionId }));
  }

  private async runControl(kind: ControlKind, label: string, call: () => Promise<unknown>) {
    if (this.control && !this.control.acknowledged) return;
    const token = ++this.controlToken;
    this.control = { kind, acknowledged: false, token };
    this.publish();
    try {
      await call();
      if (this.control?.token !== token) return;
      this.control = { kind, acknowledged: true, token };
      this.reconcile();
      // Never let a missing echo wedge the chrome in an optimistic state.
      setTimeout(() => {
        if (this.control?.token === token) {
          this.control = null;
          this.publish();
        }
      }, this.opts.controlAckTimeoutMs);
    } catch (err) {
      if (this.control?.token === token) this.control = null;
      this.reportActionError(label, err);
    }
    this.publish();
  }

  private controlSatisfied(kind: ControlKind): boolean {
    const p = this.projection;
    switch (kind) {
      case "pause":
        return p.pauseRequested || p.phase.kind !== "running";
      case "resume":
        return !p.pauseRequested && p.phase.kind !== "paused";
      case "retry":
        return p.phase.kind !== "error";
      case "stop":
        return p.phase.kind === "stopped";
      case "rollover":
        return true;
    }
  }

  private reconcile() {
    if (this.control?.acknowledged && this.controlSatisfied(this.control.kind)) this.control = null;
    if (this.answeredRowId && this.projection.pendingQuestion?.rowId !== this.answeredRowId) {
      this.answeredRowId = null;
    }
    this.retireEchoes();
  }

  private reportActionError(label: string, err: unknown): string {
    if (classify(err) === "unauthorized") {
      this.hooks.onUnauthorized();
      return "unauthorized";
    }
    const message = errorMessage(err, `${label} failed`);
    this.hooks.onError(`${label[0].toUpperCase()}${label.slice(1)} failed: ${message}`);
    return message;
  }
}

/** Free the object URLs behind sent pictures' provisional previews. */
function releasePreviews(pictures: readonly DraftPicture[]) {
  for (const p of pictures) {
    if (p.previewUrl && typeof URL.revokeObjectURL === "function") URL.revokeObjectURL(p.previewUrl);
  }
}
