// SessionProjection folds a session's events and the daemon's indexed
// presentation API (GetSessionView / GetSessionViewPage / GetSessionViewDetail /
// SubscribeSessionView) into ordered transcript rows plus session chrome state.
// It mirrors YccKit's SessionProjection (clients/ios) so the clients agree on
// cursor, tail, question, and row-version semantics. It is pure and never
// throws: malformed or unknown payloads degrade to compact system rows.
import type { WireEvent, WireRow, WireState } from "./wire";

export type ToolStatus = "running" | "ok" | "error";
export type UserInputStatus = "queued" | "delivered";

export interface Picture {
  attachmentId: string;
  mediaType: string;
  filename: string;
}

export interface Question {
  prompt: string;
  options: string[];
}

export type RowKind =
  | { type: "user"; text: string; pictures: Picture[] }
  | { type: "model"; text: string }
  | { type: "report"; text: string }
  | { type: "thinking"; text: string }
  | { type: "tool"; name: string; status: ToolStatus; args: string; output: string }
  | { type: "question"; prompt: string; options: string[]; answer: string | null }
  | { type: "assumption"; questions: Question[]; response: string | null }
  | { type: "system"; text: string; activity?: boolean }
  | { type: "commit"; text: string; sha: string }
  | { type: "review"; text: string; verdict: string; summary: string; task: string; reviewedSnapshot: string }
  | { type: "liveTail"; text: string };

export interface TranscriptRow {
  id: string;
  kind: RowKind;
  /** Persisted position seq (0 for a transient live tail). */
  seq: number;
  /** Latest durable seq that changed this row. */
  updatedSeq: number;
  actor: string;
  ts: string;
  userInputStatus?: UserInputStatus;
  /** The indexed page abbreviated this row; GetSessionViewDetail has it all. */
  detailAvailable: boolean;
}

export type Phase =
  | { kind: "running" }
  | { kind: "paused" }
  | { kind: "idle" }
  | { kind: "error"; message: string; retryable: boolean }
  | { kind: "stopped" };

export interface PendingQuestion {
  /** Summary prompt (first question, "(+N more)" for a batch). */
  prompt: string;
  options: string[];
  questions: Question[];
  rowId: string;
}

export const LIVE_TAIL_ID = "live-tail";

type Data = Record<string, unknown>;

export class SessionProjection {
  /** Durable rows in log order (excludes transient live tails). */
  durableRows: TranscriptRow[] = [];
  /** One transient live-tail row per actor, in first-seen order. */
  liveTails: TranscriptRow[] = [];
  /** Highest persisted seq folded: the resume cursor. Transients never move it. */
  lastPersistedSeq = 0;
  lastEventTimestamp = "";
  pendingQuestion: PendingQuestion | null = null;
  phase: Phase = { kind: "running" };
  pauseRequested = false;
  awaitingJobs = false;
  coordinatorModel = "";
  contextTokens: number | null = null;
  rolloverAvailable = true;

  private openQuestionRowId: string | null = null;
  private openQuestions: Question[] = [];
  private indexedRowVersions = new Map<string, number>();
  private indexedTombstones = new Map<string, number>();
  private indexedPendingRows = new Map<string, TranscriptRow>();
  private indexedLoadedDetails = new Map<string, TranscriptRow>();
  private indexedPendingDetailRowId: string | null = null;

  /** Durable rows followed by every live tail. */
  get rows(): TranscriptRow[] {
    return this.liveTails.length ? [...this.durableRows, ...this.liveTails] : this.durableRows;
  }

  /** An ask_user gate is waiting (possibly a truncated batch still loading). */
  get awaitsAnswer(): boolean {
    return this.pendingQuestion !== null || this.indexedPendingDetailRowId !== null;
  }

  clearLiveTails() {
    if (this.liveTails.length) this.liveTails = [];
  }

  // MARK: raw events

  /** Fold one event. Idempotent on already-applied persisted seqs. */
  apply(event: WireEvent) {
    if (event.transient || event.seq === 0) {
      this.applyTransient(event);
      return;
    }
    if (event.seq <= this.lastPersistedSeq) return;
    this.lastPersistedSeq = event.seq;
    if (event.ts) this.lastEventTimestamp = event.ts;

    const data = parse(event.dataJson);
    this.foldPhase(event.type, event.actor, data);
    this.foldCoordinatorModel(event.type, event.actor, data);
    this.foldContext(event.type, event.actor, data);

    switch (event.type) {
      case "user_input": {
        const pictures = (Array.isArray(data.images) ? data.images : []).map((raw) => {
          const p = (raw ?? {}) as Data;
          return { attachmentId: str(p.attachment_id), mediaType: str(p.media_type), filename: str(p.filename) };
        });
        this.appendDurable(event, { type: "user", text: str(data.text), pictures }, {
          userInputStatus: data.queued === true ? "queued" : "delivered",
        });
        break;
      }
      case "user_input_delivered":
        this.applyUserInputDelivered(data);
        break;
      case "model_turn": {
        this.removeLiveTail(event.actor);
        const text = str(data.text);
        if (text) this.appendDurable(event, { type: "model", text });
        break;
      }
      case "thinking": {
        const text = str(data.text);
        if (text) this.appendDurable(event, { type: "thinking", text });
        break;
      }
      case "tool_call":
        this.applyToolCall(event, data);
        break;
      case "tool_result":
        this.applyToolResult(event, data);
        break;
      case "question_asked":
        this.applyQuestionAsked(event, data);
        break;
      case "question_answered":
        this.foldAnswer(answerText(data), data.auto === true);
        this.pendingQuestion = null;
        this.openQuestionRowId = null;
        this.openQuestions = [];
        break;
      case "review_submitted":
        this.appendDurable(event, reviewKind(data), { actor: relatedSubagentActor(event.type, data) ?? event.actor });
        break;
      case "subagent_spawned":
      case "subagent_finished": {
        const related = relatedSubagentActor(event.type, data);
        const text = related ? subagentSummary(event.type, data) : systemSummary(event.type, data);
        if (text !== null) this.appendDurable(event, { type: "system", text, activity: !str(data.error) }, { actor: related ?? event.actor });
        break;
      }
      case "session_error": {
        this.removeLiveTail(event.actor);
        const text = systemSummary(event.type, data);
        if (text !== null) this.appendDurable(event, { type: "system", text });
        break;
      }
      case "session_idle": {
        if (data.awaiting_jobs === true) this.removeLiveTail(event.actor);
        else this.clearLiveTails();
        const report = str(data.report).trim();
        if (report) {
          const last = this.durableRows[this.durableRows.length - 1];
          if (last && last.kind.type === "model" && reportStartsWithTurn(report, last.kind.text)) {
            this.durableRows = this.durableRows.slice(0, -1);
          }
          this.appendDurable(event, { type: "report", text: report });
        } else {
          this.appendDurable(event, { type: "system", text: "Session finished" });
        }
        break;
      }
      case "session_stopped":
      case "session_ended": {
        this.clearLiveTails();
        const text = systemSummary(event.type, data);
        if (text !== null) this.appendDurable(event, { type: "system", text });
        break;
      }
      case "commit_made": {
        const text = systemSummary("commit_made", data);
        if (text !== null) this.appendDurable(event, { type: "commit", text, sha: str(data.sha) });
        break;
      }
      default: {
        const text = systemSummary(event.type, data);
        const routineJob = event.type === "job_started" ||
          ((event.type === "job_finished" || event.type === "job_claimed") && data.status === "done");
        if (text !== null) this.appendDurable(event, routineJob ? { type: "system", text, activity: true } : { type: "system", text });
      }
    }
  }

  applyAll(events: Iterable<WireEvent>) {
    for (const e of events) this.apply(e);
  }

  private applyTransient(event: WireEvent) {
    if (event.type !== "turn_delta") return;
    const data = parse(event.dataJson);
    const text = str(data.text);
    if (data.done === true || !text) {
      this.removeLiveTail(event.actor);
      return;
    }
    const actor = normalizedActor(event.actor);
    const row: TranscriptRow = {
      id: actor === "coordinator" ? LIVE_TAIL_ID : `${LIVE_TAIL_ID}:${actor}`,
      kind: { type: "liveTail", text },
      seq: 0,
      updatedSeq: 0,
      actor,
      ts: event.ts,
      detailAvailable: false,
    };
    const index = this.liveTails.findIndex((t) => t.actor === actor);
    if (index >= 0) {
      const next = this.liveTails.slice();
      next[index] = row;
      this.liveTails = next;
    } else {
      this.liveTails = [...this.liveTails, row];
    }
  }

  private removeLiveTail(actor: string) {
    const a = normalizedActor(actor);
    if (this.liveTails.some((t) => t.actor === a)) {
      this.liveTails = this.liveTails.filter((t) => t.actor !== a);
    }
  }

  private applyUserInputDelivered(data: Data) {
    const seq = intField(data, "seq");
    if (seq === null) return;
    for (let i = this.durableRows.length - 1; i >= 0; i--) {
      const row = this.durableRows[i];
      if (row.seq === seq) {
        if (row.kind.type === "user") this.replaceRow(i, { ...row, userInputStatus: "delivered" });
        return;
      }
    }
  }

  private applyToolCall(event: WireEvent, data: Data) {
    const id = str(data.id);
    this.appendDurable(
      event,
      { type: "tool", name: str(data.name) || "tool", status: "running", args: stringField(data, "args"), output: "" },
      { id: id ? `tool-${id}` : `seq-${event.seq}` },
    );
  }

  private applyToolResult(event: WireEvent, data: Data) {
    const id = str(data.id);
    const name = str(data.name) || "tool";
    const status: ToolStatus = data.error === true ? "error" : "ok";
    const output = stringField(data, "result");
    const rowId = id ? `tool-${id}` : "";
    if (rowId) {
      for (let i = this.durableRows.length - 1; i >= 0; i--) {
        const row = this.durableRows[i];
        if (row.id === rowId && row.kind.type === "tool") {
          this.replaceRow(i, {
            ...row,
            kind: { type: "tool", name: row.kind.name || name, status, args: row.kind.args, output },
          });
          return;
        }
      }
    }
    this.appendDurable(event, { type: "tool", name, status, args: "", output }, { id: rowId || `seq-${event.seq}` });
  }

  private applyQuestionAsked(event: WireEvent, data: Data) {
    const questions = allQuestions(data);
    const summary = summaryQuestion(questions);
    const rowId = `seq-${event.seq}`;
    const automatic = data.auto === true;
    this.appendDurable(
      event,
      automatic
        ? { type: "assumption", questions, response: null }
        : { type: "question", prompt: summary.prompt, options: summary.options, answer: null },
      { id: rowId },
    );
    this.pendingQuestion = automatic ? null : { ...summary, questions, rowId };
    this.openQuestionRowId = rowId;
    this.openQuestions = questions;
  }

  private foldAnswer(answer: string, automatic = false) {
    const rowId = this.pendingQuestion?.rowId ?? this.openQuestionRowId;
    if (!rowId) return;
    let idx = -1;
    for (let i = this.durableRows.length - 1; i >= 0; i--) {
      if (this.durableRows[i].id === rowId) {
        idx = i;
        break;
      }
    }
    if (idx < 0) return;
    const row = this.durableRows[idx];
    const kind = row.kind;
    if (kind.type === "assumption") {
      this.replaceRow(idx, { ...row, kind: { ...kind, response: answer || kind.response || "" } });
    } else if (kind.type === "question") {
      const text = answer || kind.answer || "";
      if (automatic) {
        const questions = this.openQuestions.length ? this.openQuestions : [{ prompt: kind.prompt, options: kind.options }];
        this.replaceRow(idx, { ...row, kind: { type: "assumption", questions, response: text } });
      } else {
        this.replaceRow(idx, { ...row, kind: { ...kind, answer: text } });
      }
    }
  }

  private foldPhase(type: string, actor: string, data: Data) {
    if (isSubagentActor(actor)) return;
    switch (type) {
      case "pause_requested":
        this.pauseRequested = true;
        break;
      case "interrupted":
        this.pauseRequested = false;
        this.phase = { kind: "paused" };
        break;
      case "session_idle":
        this.phase = { kind: "idle" };
        this.awaitingJobs = data.awaiting_jobs === true;
        break;
      case "session_error": {
        if (data.action === "switch_model") this.rolloverAvailable = false;
        const message = str(data.msg) || str(data.error) || str(data.text);
        this.phase = { kind: "error", message, retryable: data.retryable !== false };
        break;
      }
      case "session_stopped":
      case "session_ended":
        this.pauseRequested = false;
        this.phase = { kind: "stopped" };
        break;
      case "pause_cancelled":
      case "session_reopened":
        this.pauseRequested = false;
        break;
      case "role_config_changed": {
        const next = str(data.coordinator);
        if (next && next !== this.coordinatorModel) this.rolloverAvailable = true;
        break;
      }
      case "resumed":
      case "session_started":
        this.pauseRequested = false;
        this.phase = { kind: "running" };
        break;
      case "user_input":
      case "user_input_delivered":
      case "model_turn":
      case "thinking":
      case "tool_call":
      case "tool_result":
      case "question_asked":
        if (this.phase.kind !== "paused") this.phase = { kind: "running" };
        break;
    }
    if (this.phase.kind !== "idle" || type === "session_reopened") this.awaitingJobs = false;
  }

  private foldCoordinatorModel(type: string, actor: string, data: Data) {
    if (type === "session_started" || type === "role_config_changed") {
      const name = str(data.coordinator);
      if (name) this.coordinatorModel = name;
    } else if (type === "model_turn" && (actor === "" || actor === "coordinator")) {
      const name = str(data.model_name);
      if (name) this.coordinatorModel = name;
    }
  }

  private foldContext(type: string, actor: string, data: Data) {
    if (type !== "model_turn" || !(actor === "" || actor === "coordinator")) return;
    const estimate = intField(data, "context_tokens_est");
    if (estimate !== null && estimate >= 0) this.contextTokens = estimate;
  }

  // MARK: indexed presentation

  /** Install a snapshot: daemon-reduced state plus the newest bounded page. */
  installIndexed(state: WireState, rows: WireRow[]) {
    this.applyIndexedState(state);
    const installed: TranscriptRow[] = [];
    for (const encoded of rows) {
      const row = this.decodePreservingDetail(encoded);
      if (row) installed.push(row);
    }
    this.durableRows = installed;
    this.indexedRowVersions = new Map(installed.map((r) => [r.id, r.updatedSeq]));
    this.indexedTombstones.clear();
    this.indexedPendingRows.clear();
    this.liveTails = [];
  }

  /**
   * Prepend an earlier page. Versions learned from live updates are kept even
   * for unloaded rows so a page captured before an upsert/delete cannot
   * resurrect stale content.
   */
  prependIndexed(rows: WireRow[], indexedThroughSeq: number) {
    const prepended: TranscriptRow[] = [];
    let next = this.durableRows;
    for (const encoded of rows) {
      const known = this.indexedRowVersions.get(encoded.id) ?? 0;
      const tombstone = this.indexedTombstones.get(encoded.id) ?? 0;
      const pending = this.indexedPendingRows.get(encoded.id);
      let candidate: TranscriptRow | null = null;
      if (pending && pending.updatedSeq >= known && pending.updatedSeq > tombstone) {
        candidate = pending;
      } else if (encoded.updatedSeq <= indexedThroughSeq && encoded.updatedSeq >= known && encoded.updatedSeq > tombstone) {
        candidate = this.decodePreservingDetail(encoded);
      }
      if (!candidate) continue;
      this.indexedPendingRows.delete(candidate.id);
      this.indexedRowVersions.set(candidate.id, candidate.updatedSeq);
      const index = next.findIndex((r) => r.id === candidate!.id);
      if (index >= 0) {
        if (next === this.durableRows) next = next.slice();
        next[index] = candidate;
      } else {
        prepended.push(candidate);
      }
    }
    this.durableRows = prepended.length ? [...prepended, ...next] : next;
  }

  /** Apply one coalesced, gap-free SubscribeSessionView update. */
  applyIndexed(state: WireState, upserts: WireRow[], deletedIds: string[]) {
    if (state.indexedThroughSeq <= this.lastPersistedSeq) return;
    this.applyTailRetirement(state, upserts);
    this.applyIndexedState(state);
    if (deletedIds.length) {
      const deleted = new Set(deletedIds);
      for (const id of deletedIds) {
        this.indexedTombstones.set(id, state.indexedThroughSeq);
        this.indexedRowVersions.set(id, Math.max(this.indexedRowVersions.get(id) ?? 0, state.indexedThroughSeq));
        this.indexedPendingRows.delete(id);
        this.indexedLoadedDetails.delete(id);
      }
      this.durableRows = this.durableRows.filter((r) => !deleted.has(r.id));
    }
    const newestPosition = this.durableRows.length ? this.durableRows[this.durableRows.length - 1].seq : 0;
    let next = this.durableRows;
    const appended: TranscriptRow[] = [];
    for (const encoded of upserts) {
      const known = this.indexedRowVersions.get(encoded.id) ?? 0;
      const tombstone = this.indexedTombstones.get(encoded.id) ?? 0;
      if (encoded.updatedSeq < known || encoded.updatedSeq <= tombstone) continue;
      const row = this.decodePreservingDetail(encoded);
      if (!row) continue;
      this.indexedRowVersions.set(row.id, row.updatedSeq);
      this.indexedTombstones.delete(row.id);
      const index = next.findIndex((r) => r.id === row.id);
      const appendedIndex = appended.findIndex((r) => r.id === row.id);
      if (index >= 0) {
        this.indexedPendingRows.delete(row.id);
        if (next === this.durableRows) next = next.slice();
        next[index] = row;
      } else if (appendedIndex >= 0) {
        appended[appendedIndex] = row;
      } else if (encoded.positionSeq > newestPosition) {
        this.indexedPendingRows.delete(row.id);
        appended.push(row);
      } else {
        // An older row outside the loaded window: buffer it so a later
        // earlier-page merge installs the current version.
        this.indexedPendingRows.set(row.id, row);
      }
    }
    this.durableRows = appended.length ? [...next, ...appended] : next;
  }

  /** A truncated pending-question state needs this row's full detail. */
  needsPendingDetail(rowId: string): boolean {
    return this.indexedPendingDetailRowId === rowId;
  }

  /** Install a complete row from GetSessionViewDetail if it is still current. */
  installIndexedDetail(encoded: WireRow) {
    const restoresPending = this.indexedPendingDetailRowId === encoded.id;
    const known = this.indexedRowVersions.get(encoded.id) ?? 0;
    const tombstone = this.indexedTombstones.get(encoded.id) ?? 0;
    if (encoded.updatedSeq < known || encoded.updatedSeq <= tombstone) return;
    const decoded = decodeRow(encoded);
    if (!decoded) return;
    const row: TranscriptRow = { ...decoded, detailAvailable: false };
    const index = this.durableRows.findIndex((r) => r.id === row.id);
    if (index >= 0) {
      if (encoded.updatedSeq < this.durableRows[index].updatedSeq) return;
      this.indexedLoadedDetails.set(row.id, row);
      this.indexedRowVersions.set(row.id, row.updatedSeq);
      this.replaceRow(index, row);
    } else if (!restoresPending) {
      return;
    }
    if (!restoresPending) return;
    const detail = new SessionProjection();
    detail.applyAll(encoded.events);
    if (detail.pendingQuestion) {
      this.pendingQuestion = detail.pendingQuestion;
      this.openQuestionRowId = detail.pendingQuestion.rowId;
      this.openQuestions = detail.pendingQuestion.questions;
      this.indexedPendingDetailRowId = null;
    } else if (
      encoded.events.some((e) => e.type === "question_answered") ||
      detail.durableRows[0]?.kind.type === "assumption"
    ) {
      this.pendingQuestion = null;
      this.openQuestionRowId = null;
      this.indexedPendingDetailRowId = null;
    }
  }

  /** The loaded row with this id, if any. */
  row(id: string): TranscriptRow | undefined {
    return this.durableRows.find((r) => r.id === id) ?? this.liveTails.find((r) => r.id === id);
  }

  private applyTailRetirement(state: WireState, rows: WireRow[]) {
    if ((state.phase === "idle" && !state.awaitingJobs) || state.phase === "stopped") {
      this.clearLiveTails();
      return;
    }
    for (const row of rows) {
      for (const event of row.events) {
        if (event.type === "model_turn" || event.type === "session_error") this.removeLiveTail(event.actor);
        else if (event.type === "session_idle" || event.type === "session_stopped" || event.type === "session_ended") {
          this.clearLiveTails();
        }
      }
    }
  }

  private applyIndexedState(state: WireState) {
    this.lastPersistedSeq = state.indexedThroughSeq;
    this.lastEventTimestamp = state.lastEventTimestamp;
    this.coordinatorModel = state.coordinatorModel;
    this.contextTokens = state.hasContextTokens ? state.contextTokens : null;
    this.rolloverAvailable = state.rolloverAvailable;
    this.pauseRequested = state.pauseRequested;
    switch (state.phase) {
      case "paused":
        this.phase = { kind: "paused" };
        break;
      case "idle":
        this.phase = { kind: "idle" };
        break;
      case "error":
        this.phase = { kind: "error", message: state.errorMessage, retryable: state.errorRetryable };
        break;
      case "stopped":
        this.phase = { kind: "stopped" };
        break;
      default:
        this.phase = { kind: "running" };
    }
    this.awaitingJobs = this.phase.kind === "idle" && state.awaitingJobs;
    this.openQuestions = [];
    if (state.pendingQuestionsTruncated && state.pendingRowId) {
      // Never expose an incomplete batch to AnswerQuestions: the controller
      // fetches the row's detail, which restores the full gate.
      this.pendingQuestion = null;
      this.openQuestionRowId = state.pendingRowId;
      this.indexedPendingDetailRowId = state.pendingRowId;
    } else if (state.pendingQuestions.length && state.pendingRowId) {
      const questions = state.pendingQuestions.map((q) => ({ prompt: q.prompt, options: q.options }));
      this.openQuestions = questions;
      this.pendingQuestion = { ...summaryQuestion(questions), questions, rowId: state.pendingRowId };
      this.openQuestionRowId = state.pendingRowId;
      this.indexedPendingDetailRowId = null;
    } else {
      this.pendingQuestion = null;
      this.openQuestionRowId = null;
      this.indexedPendingDetailRowId = null;
    }
  }

  private decodePreservingDetail(encoded: WireRow): TranscriptRow | null {
    const detail = this.indexedLoadedDetails.get(encoded.id);
    if (detail) {
      if (detail.updatedSeq === encoded.updatedSeq) return detail;
      // A new version never inherits full text from an older one.
      this.indexedLoadedDetails.delete(encoded.id);
    }
    return decodeRow(encoded);
  }

  private replaceRow(index: number, row: TranscriptRow) {
    const next = this.durableRows.slice();
    next[index] = row;
    this.durableRows = next;
  }

  private appendDurable(
    event: WireEvent,
    kind: RowKind,
    opts: { id?: string; actor?: string; userInputStatus?: UserInputStatus } = {},
  ) {
    this.durableRows = [
      ...this.durableRows,
      {
        id: opts.id ?? `seq-${event.seq}`,
        kind,
        seq: event.seq,
        updatedSeq: event.seq,
        actor: opts.actor ?? event.actor,
        ts: event.ts,
        userInputStatus: opts.userInputStatus,
        detailAvailable: false,
      },
    ];
  }
}

/** Reduce one independently reducible presentation row. */
export function decodeRow(encoded: WireRow): TranscriptRow | null {
  const p = new SessionProjection();
  p.applyAll(encoded.events);
  const row = p.durableRows[0];
  if (!row) return null;
  // The server is authoritative for stable identity and position.
  return {
    ...row,
    id: encoded.id,
    seq: encoded.positionSeq,
    updatedSeq: encoded.updatedSeq,
    detailAvailable: encoded.hasDetail,
  };
}

// MARK: payload helpers

export function parse(json: string): Data {
  if (!json) return {};
  try {
    const value: unknown = JSON.parse(json);
    return value && typeof value === "object" && !Array.isArray(value) ? (value as Data) : {};
  } catch {
    return {};
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function intField(data: Data, key: string): number | null {
  const v = data[key];
  return typeof v === "number" && Number.isFinite(v) ? Math.trunc(v) : null;
}

/** A string field, JSON-encoding non-string values so object args still render. */
function stringField(data: Data, key: string): string {
  const v = data[key];
  if (v === undefined || v === null) return "";
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(sortKeys(v));
  } catch {
    return String(v);
  }
}

function sortKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === "object") {
    const out: Data = {};
    for (const k of Object.keys(v as Data).sort()) out[k] = sortKeys((v as Data)[k]);
    return out;
  }
  return v;
}

function normalizedActor(actor: string): string {
  return actor || "coordinator";
}

export function isSubagentActor(actor: string): boolean {
  switch (normalizedActor(actor).toLowerCase()) {
    case "coordinator":
    case "user":
    case "system":
    case "daemon":
      return false;
    default:
      return true;
  }
}

function relatedSubagentActor(type: string, data: Data): string | null {
  const role = str(data.role) || (type === "review_submitted" ? "reviewer" : "");
  switch (role) {
    case "generic": {
      const id = str(data.agent_id);
      return id ? `agent:${id}` : null;
    }
    case "implementer":
      return "implementer";
    case "reviewer": {
      const model = str(data.model);
      return model ? `reviewer:${model}` : null;
    }
    default:
      return null;
  }
}

function subagentSummary(type: string, data: Data): string | null {
  switch (type) {
    case "subagent_spawned":
      return "Spawned";
    case "subagent_finished": {
      const error = str(data.error);
      return error ? `Failed: ${firstLine(error)}` : "Finished";
    }
    default:
      return null;
  }
}

/** Normalized review verdict: accept, revise, or unknown. */
export function reviewVerdict(data: Data): string {
  const v = str(data.verdict).toLowerCase();
  return v === "accept" || v === "revise" ? v : "unknown";
}

function reviewKind(data: Data): RowKind {
  return {
    type: "review",
    text: reviewHeading(data),
    verdict: reviewVerdict(data),
    summary: str(data.summary),
    task: str(data.task),
    reviewedSnapshot: str(data.reviewed_snapshot_id),
  };
}

function reviewHeading(data: Data): string {
  const reviewer = str(data.reviewer) || str(data.model) || "reviewer";
  const model = str(data.logical_model);
  const verdict = (str(data.verdict) || "unknown").toUpperCase();
  const round = intField(data, "round");
  const findings = intField(data, "findings");
  const severities = (data.findings_by_severity ?? {}) as Data;
  const counts = Object.keys(severities)
    .sort()
    .map((k) => `${k}:${String(severities[k])}`)
    .join(", ");
  const snapshot = str(data.reviewed_snapshot_id);
  return (
    reviewer +
    (model && model !== reviewer ? ` (${model})` : "") +
    (round !== null ? ` round ${round}` : "") +
    ` — ${verdict}` +
    (findings !== null ? ` · ${findings} findings` : "") +
    (counts ? ` (${counts})` : "") +
    (snapshot ? ` · reviewed snapshot ${snapshot.slice(0, 12)} only` : "")
  );
}

function reportStartsWithTurn(report: string, turn: string): boolean {
  const r = report.trim();
  const t = turn.trim();
  if (!t) return false;
  return r === t || r.startsWith(t + "\n");
}

export function allQuestions(data: Data): Question[] {
  const q = str(data.question);
  if (q) return [{ prompt: q, options: stringArray(data.options) }];
  if (Array.isArray(data.questions) && data.questions.length) {
    return data.questions.map((raw) => {
      const item = (raw ?? {}) as Data;
      return { prompt: str(item.question) || "a question was asked", options: stringArray(item.options) };
    });
  }
  return [{ prompt: "a question was asked", options: [] }];
}

function stringArray(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

export function summaryQuestion(questions: Question[]): { prompt: string; options: string[] } {
  const first = questions[0];
  if (!first) return { prompt: "a question was asked", options: [] };
  const suffix = questions.length > 1 ? ` (+${questions.length - 1} more)` : "";
  return { prompt: first.prompt + suffix, options: first.options };
}

function answerText(data: Data): string {
  if (typeof data.answer === "string") return data.answer;
  if (Array.isArray(data.answers)) {
    const strs = data.answers.filter((x): x is string => typeof x === "string");
    if (strs.length) return strs.join("; ");
  }
  return "";
}

/** One-line summary for lifecycle/system events; null drops plumbing. */
export function systemSummary(type: string, data: Data): string | null {
  const s = (k: string) => str(data[k]);
  switch (type) {
    case "session_started": {
      const parts = [s("mode"), s("coordinator")].filter(Boolean);
      return parts.length ? `Session started · ${parts.join(" · ")}` : "Session started";
    }
    case "session_idle":
      return "Session idle";
    case "session_error": {
      const msg = s("msg") || s("error") || s("text");
      return msg ? `Session error: ${msg}` : "Session error";
    }
    case "session_notice":
      return s("msg") ? `Session notice: ${s("msg")}` : "Session notice";
    case "session_stopped":
      return "Session stopped";
    case "session_reopened":
      return "Session reopened";
    case "pause_requested":
      return "Pause requested";
    case "pause_cancelled":
      return "Pause cancelled";
    case "interrupted":
      return "Interrupted";
    case "resumed":
      return "Resumed";
    case "role_config_changed": {
      const parts: string[] = [];
      if (s("coordinator")) parts.push(`coordinator ${s("coordinator")}`);
      if (s("implementer")) parts.push(`implementer ${s("implementer")}`);
      const reviewers = stringArray(data.reviewers);
      if (reviewers.length) parts.push(`reviewers ${reviewers.join(", ")}`);
      return parts.length ? `Roles: ${parts.join(" · ")}` : "Roles changed";
    }
    case "thinking_level_changed": {
      const role = s("role");
      const scope = !role || role === "all" ? "all roles" : role;
      return s("to") ? `Thinking (${scope}) → ${s("to")}` : "Thinking changed";
    }
    case "user_input_delivered":
    case "job_notified":
      return null;
    case "commit_made": {
      const sha = s("sha");
      const msg = firstLine(s("message"));
      if (!sha) return msg ? `Committed: ${msg}` : "Commit made";
      return msg ? `Committed ${sha}: ${msg}` : `Committed ${sha}`;
    }
    case "decision_made": {
      const base = s("decision") ? `Decision: ${s("decision")}` : "Decision made";
      return s("task") ? `${base} (task ${s("task")})` : base;
    }
    case "plan_proposed":
      return "Plan proposed";
    case "review_submitted":
      return reviewHeading(data);
    case "review_tier_selected":
      return s("tier") ? `Review tier: ${s("tier")}` : "Review tier selected";
    case "doc_updated": {
      if (!s("task")) return "Doc updated";
      return s("status") ? `Task ${s("task")} → ${s("status")}` : `Task ${s("task")} updated`;
    }
    case "task_focus":
      return s("task") ? `Focus: task ${s("task")}` : "Task focus";
    case "subagent_spawned": {
      const base = s("role") ? `Spawned ${s("role")}` : "Subagent spawned";
      return s("model") ? `${base} (${s("model")})` : base;
    }
    case "subagent_finished":
      return s("role") ? `${s("role")} finished` : "Subagent finished";
    case "job_started": {
      const label = firstLine(s("label"));
      return label ? `Job started: ${label}` : "Job started";
    }
    case "job_finished": {
      const label = firstLine(s("label"));
      const base = label ? `Job finished: ${label}` : "Job finished";
      return s("status") ? `${base} [${s("status")}]` : base;
    }
    case "log": {
      const msg = firstLine(s("msg"));
      return msg || null;
    }
    default: {
      const humanized = type.replace(/_/g, " ");
      const t = firstLine(s("text"));
      return t ? `${humanized}: ${t}` : humanized;
    }
  }
}

export function firstLine(s: string): string {
  for (const line of s.split("\n")) {
    const trimmed = line.trim();
    if (trimmed) return trimmed;
  }
  return s.trim();
}
