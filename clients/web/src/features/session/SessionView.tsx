// The session surface: header with status and phase-gated controls, the
// transcript, the answer panel for pending questions, and the composer (live
// sessions only; persisted-only sessions are a finite read-only view).
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { queryKeys } from "../../api/queries";
import { useInspector } from "../inspector/inspector";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { AnswerPanel } from "./AnswerPanel";
import { Composer, type ComposerHandle } from "./Composer";
import type { SessionController, SessionSnapshot } from "./controller";
import { Transcript } from "./Transcript";
import { useSessionController, useSessionSnapshot } from "./useSession";
import { compactTokenCount } from "../sessions/feed";

export function statusText(snap: SessionSnapshot): { text: string; tone: string } {
  switch (snap.conn) {
    case "loading":
      return { text: "Loading…", tone: "muted" };
    case "reconnecting":
      return { text: "Reconnecting…", tone: "warn" };
    case "failed":
      return { text: snap.failure ?? "Failed", tone: "error" };
    case "finished":
      if (snap.mode === "persisted") return { text: "Not live · read-only", tone: "muted" };
  }
  if (snap.pauseRequested) return { text: "Pausing at next checkpoint…", tone: "warn" };
  switch (snap.phase.kind) {
    case "running":
      return { text: "Running", tone: "ok" };
    case "paused":
      return { text: "Paused", tone: "warn" };
    case "idle":
      return snap.awaitingJobs ? { text: "Waiting on background jobs", tone: "ok" } : { text: "Idle", tone: "muted" };
    case "error":
      return { text: `Error${snap.phase.message ? `: ${snap.phase.message}` : ""}`, tone: "error" };
    case "stopped":
      return { text: "Stopped", tone: "muted" };
  }
}

function Controls({ controller, snap }: { controller: SessionController; snap: SessionSnapshot }) {
  const [confirmStop, setConfirmStop] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menuOpen) return;
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent) {
        if (e.key === "Escape") setMenuOpen(false);
        return;
      }
      if (menu.current && !menu.current.contains(e.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [menuOpen]);

  if (snap.mode !== "live" || snap.conn === "finished" || snap.conn === "failed") return null;
  const busy = snap.control !== null;
  const pending = snap.control?.kind;
  const phase = snap.phase.kind;
  const buttons = [];
  if (phase === "running" && !snap.pauseRequested) {
    buttons.push(
      <button key="pause" type="button" className="btn" disabled={busy} onClick={() => void controller.interrupt()}>
        {pending === "pause" ? "Interrupting…" : "Interrupt"}
      </button>,
    );
  }
  if (snap.pauseRequested) {
    buttons.push(
      <button key="cancel" type="button" className="btn" disabled={busy} onClick={() => void controller.resume()}>
        {pending === "resume" ? "Cancelling…" : "Cancel pause"}
      </button>,
    );
  }
  if (phase === "paused") {
    buttons.push(
      <button key="resume" type="button" className="btn primary" disabled={busy} onClick={() => void controller.resume()}>
        {pending === "resume" ? "Resuming…" : "Resume"}
      </button>,
    );
  }
  if (snap.phase.kind === "error" && snap.phase.retryable) {
    buttons.push(
      <button key="retry" type="button" className="btn primary" disabled={busy} onClick={() => void controller.retry()}>
        {pending === "retry" ? "Retrying…" : "Retry"}
      </button>,
    );
  }
  const stoppable = phase !== "stopped";
  return (
    <div className="controls">
      {buttons}
      {stoppable && (
        <div className="menu-wrap" ref={menu}>
          <button
            type="button"
            className="btn ghost"
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((o) => !o)}
          >
            More ▾
          </button>
          {menuOpen && (
            <div className="menu" role="menu">
              {snap.rolloverAvailable && phase !== "paused" && (
                <button
                  type="button"
                  role="menuitem"
                  disabled={busy}
                  onClick={() => {
                    setMenuOpen(false);
                    void controller.rollover();
                  }}
                >
                  Roll over coordinator context
                </button>
              )}
              <button
                type="button"
                role="menuitem"
                className="danger"
                disabled={busy}
                onClick={() => {
                  setMenuOpen(false);
                  setConfirmStop(true);
                }}
              >
                Stop session…
              </button>
            </div>
          )}
        </div>
      )}
      <ConfirmDialog
        open={confirmStop}
        title="Stop this session?"
        body="Stopping hard-terminates the agent loop and removes the session from the daemon. Unlike Interrupt, it cannot be resumed here."
        confirmLabel="Stop session"
        danger
        onCancel={() => setConfirmStop(false)}
        onConfirm={() => {
          setConfirmStop(false);
          void controller.stopSession();
        }}
      />
    </div>
  );
}

export function SessionView({
  project,
  sessionId,
  title,
}: {
  project: string;
  sessionId: string;
  title: string;
}) {
  const controller = useSessionController(project, sessionId);
  const snap = useSessionSnapshot(controller);
  const inspector = useInspector();
  const composer = useRef<ComposerHandle>(null);
  const status = statusText(snap);
  // The session list's status/needs-answer markers follow this session's
  // durable lifecycle: refresh them when it changes.
  const qc = useQueryClient();
  const lifecycleKey = `${snap.mode}|${snap.phase.kind}|${snap.pendingQuestion?.rowId ?? ""}|${snap.awaitingJobs}`;
  const firstLifecycle = useRef(true);
  useEffect(() => {
    if (!snap.installed) return;
    if (firstLifecycle.current) {
      firstLifecycle.current = false;
      return;
    }
    void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
  }, [qc, lifecycleKey, snap.installed]);
  const ctx = snap.contextTokens !== null ? compactTokenCount(snap.contextTokens) : null;

  let placeholder = "Message the agent… (Enter to send, Shift+Enter for a newline)";
  if (snap.awaitsAnswer) placeholder = "Type an answer to the pending question…";
  else if (snap.phase.kind === "paused") placeholder = "Steer the paused session, then Resume…";
  else if (snap.phase.kind === "running") placeholder = "Send a steer — delivered at the next checkpoint…";

  return (
    <div className="session">
      <header className="session-head">
        <div className="session-title">
          <h1 title={title}>{title}</h1>
          <div className="session-meta">
            <span className={`status status-${status.tone}`}>{status.text}</span>
            {project && <span>{project}</span>}
            {snap.coordinatorModel && <span>{snap.coordinatorModel}</span>}
            {ctx && <span>{ctx} ctx</span>}
            <span className="mono">{sessionId}</span>
          </div>
        </div>
        <div className="session-actions">
          <button
            type="button"
            className="btn ghost"
            onClick={() => inspector.open({ kind: "workingChanges", project, sessionId })}
          >
            Working changes
          </button>
          <Controls controller={controller} snap={snap} />
        </div>
      </header>
      {snap.conn === "failed" && snap.failure !== "unauthorized" && (
        <div className="banner error">
          {snap.failure}{" "}
          <button type="button" className="link" onClick={() => controller.reconnect()}>
            Retry
          </button>
        </div>
      )}
      <Transcript controller={controller} snap={snap} onEditFailed={(t) => composer.current?.setText(t)} />
      {snap.mode === "live" && snap.pendingQuestion && (
        <AnswerPanel
          key={snap.pendingQuestion.rowId}
          controller={controller}
          question={snap.pendingQuestion}
          inFlight={snap.answerInFlight}
          submitted={snap.answeredRowId === snap.pendingQuestion.rowId}
        />
      )}
      {snap.mode === "live" && snap.conn !== "finished" ? (
        <Composer
          ref={composer}
          sessionKey={`${project}\u0000${sessionId}`}
          placeholder={placeholder}
          onSend={(t) => void controller.send(t)}
        />
      ) : (
        snap.mode === "persisted" && (
          <div className="readonly-note muted">
            This session is not live, so this is a read-only transcript. Resuming sessions from the web arrives in
            a later phase.
          </div>
        )
      )}
    </div>
  );
}
