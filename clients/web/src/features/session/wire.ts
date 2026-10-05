// Plain (protobuf-free) shapes the session reducer works on, and converters
// from the generated Connect-ES messages. Keeping the reducer independent of the
// wire classes makes it trivially unit-testable with fixtures.
import type {
  Event,
  SessionPresentationRow,
  SessionViewState,
} from "../../gen/ycc/v1/ycc_pb";

export interface WireEvent {
  seq: number;
  ts: string;
  actor: string;
  type: string;
  dataJson: string;
  transient: boolean;
}

export interface WireRow {
  id: string;
  positionSeq: number;
  updatedSeq: number;
  events: WireEvent[];
  hasDetail: boolean;
}

export interface WireQuestion {
  prompt: string;
  options: string[];
}

export interface WireState {
  indexedThroughSeq: number;
  lastEventTimestamp: string;
  phase: string;
  errorMessage: string;
  errorRetryable: boolean;
  coordinatorModel: string;
  contextTokens: number;
  hasContextTokens: boolean;
  rolloverAvailable: boolean;
  pendingQuestions: WireQuestion[];
  pendingRowId: string;
  pendingQuestionsTruncated: boolean;
  pauseRequested: boolean;
  awaitingJobs: boolean;
}

export function fromEvent(e: Event): WireEvent {
  return {
    seq: Number(e.seq),
    ts: e.ts,
    actor: e.actor,
    type: e.type,
    dataJson: e.dataJson,
    transient: e.transient,
  };
}

export function fromRow(r: SessionPresentationRow): WireRow {
  return {
    id: r.id,
    positionSeq: Number(r.positionSeq),
    updatedSeq: Number(r.updatedSeq),
    events: r.events.map(fromEvent),
    hasDetail: r.hasDetail,
  };
}

export function fromState(s: SessionViewState): WireState {
  return {
    indexedThroughSeq: Number(s.indexedThroughSeq),
    lastEventTimestamp: s.lastEventTimestamp,
    phase: s.phase,
    errorMessage: s.errorMessage,
    errorRetryable: s.errorRetryable,
    coordinatorModel: s.coordinatorModel,
    contextTokens: Number(s.contextTokens),
    hasContextTokens: s.hasContextTokens,
    rolloverAvailable: s.rolloverAvailable,
    pendingQuestions: s.pendingQuestions.map((q) => ({ prompt: q.prompt, options: [...q.options] })),
    pendingRowId: s.pendingRowId,
    pendingQuestionsTruncated: s.pendingQuestionsTruncated,
    pauseRequested: s.pauseRequested,
    awaitingJobs: s.awaitingJobs,
  };
}
