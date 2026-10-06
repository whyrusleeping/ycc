// The session surface: header with status and phase-gated controls, the
// transcript, the answer panel for pending questions, and the composer (live
// sessions only; persisted-only sessions are a finite read-only view).
import { useEffect, useRef, useState } from "react";
import { needsAnthropicReconnect } from "../settings/anthropic";
import { openAnthropicLogin } from "../settings/AnthropicLogin";
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
import { useDropZone } from "../attachments/pictures";
import { useTranscriptSearch } from "./SearchBar";
import { FileLinksProvider } from "../files/FileRef";
import { useSessionFileLinks } from "../files/links";
import { Link } from "react-router";
import { paths } from "../../app/paths";

export function statusText(snap: SessionSnapshot): { text: string; tone: string } {
  switch (snap.conn) {
    case "loading":
      return { text: "Loading…", tone: "muted" };
    case "reconnecting":
      return { text: "Reconnecting…", tone: "warn" };
    case "failed":
      return { text: snap.failure ?? "Failed", tone: "error" };
    case "finished":
      if (snap.mode === "persisted") {
        return snap.reopening ? { text: "Reopening…", tone: "warn" } : { text: "Not live · read-only", tone: "muted" };
      }
  }
  if (snap.pauseRequested) return { text: "Pausing at next checkpoint…", tone: "warn" };
  // An ask_user gate keeps the durable phase "running"; the agent is idle
  // until someone answers, so say so instead of "Running".
  if (snap.awaitsAnswer && snap.phase.kind === "running") return { text: "Waiting for your answer", tone: "warn" };
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
  if (snap.phase.kind === "error" && needsAnthropicReconnect(snap.phase.message)) {
    buttons.push(
      <button key="anthropic" type="button" className="btn" onClick={() => openAnthropicLogin()} title="Sign in to Anthropic again on the daemon">
        Reconnect Anthropic…
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
  focusTasks = [],
}: {
  project: string;
  sessionId: string;
  title: string;
  /** Backlog tasks the session is focused on (from its history summary). */
  focusTasks?: readonly string[];
}) {
  const controller = useSessionController(project, sessionId);
  const snap = useSessionSnapshot(controller);
  const inspector = useInspector();
  const composer = useRef<ComposerHandle>(null);
  const status = statusText(snap);
  const search = useTranscriptSearch(controller, snap.rows);
  const showSearch = search.show;
  // File references in the transcript open the viewer in the inspector,
  // resolved against this session's live worktree.
  const fileLinks = useSessionFileLinks(project, sessionId, "main");
  // Ctrl/Cmd-F searches this session's transcript (including history that
  // is not loaded yet) instead of the browser's find-in-page.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "f") {
        e.preventDefault();
        showSearch();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [showSearch]);
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
  const canCompose = snap.mode === "live" && snap.conn !== "finished";
  const drop = useDropZone((files) => composer.current?.addFiles(files), canCompose);
  const settingsOpen =
    inspector.item?.kind === "sessionSettings" &&
    inspector.item.project === project &&
    inspector.item.sessionId === sessionId;

  let placeholder = "Message the agent… (Enter to send, Shift+Enter for a newline)";
  if (snap.awaitsAnswer) placeholder = "Type an answer to the pending question…";
  else if (snap.phase.kind === "paused") placeholder = "Steer the paused session, then Resume…";
  else if (snap.phase.kind === "running") placeholder = "Send a steer — delivered at the next checkpoint…";

  return (
    <div className={`session${drop.dragging ? " dragging" : ""}`} {...drop.handlers}>
      {drop.dragging && <div className="drop-overlay">Drop pictures to attach them</div>}
      <header className="session-head">
        <div className="session-title">
          <h1 title={title}>{title}</h1>
          <div className="session-meta">
            <span className={`status status-${status.tone}`}>{status.text}</span>
            {project && <span>{project}</span>}
            {focusTasks.map((id) => {
              const open = inspector.item?.kind === "task" && inspector.item.taskId === id && inspector.item.project === project;
              return (
                <button
                  key={id}
                  type="button"
                  className={`chip task-chip${open ? " active" : ""}`}
                  title={`Task ${id} — open beside the transcript`}
                  aria-pressed={open}
                  onClick={() => (open ? inspector.close() : inspector.open({ kind: "task", project, taskId: id }))}
                >
                  task {id}
                </button>
              );
            })}
            {snap.coordinatorModel && <span>{snap.coordinatorModel}</span>}
            {ctx && <span>{ctx} ctx</span>}
            <span className="mono">{sessionId}</span>
          </div>
        </div>
        <div className="session-actions">
          <button
            type="button"
            className={`btn ghost${search.open ? " active" : ""}`}
            title="Search this session (Ctrl/Cmd-F)"
            onClick={() => (search.open ? search.close() : search.show())}
          >
            Search
          </button>
          <button
            type="button"
            className="btn ghost"
            onClick={() => inspector.open({ kind: "workingChanges", project, sessionId })}
          >
            Working changes
          </button>
          <Link
            className="btn ghost"
            to={paths.files(project || null, "", { session: sessionId })}
            title="Browse this session’s files (its live worktree)"
          >
            Files
          </Link>
          <button
            type="button"
            className={`btn ghost${settingsOpen ? " active" : ""}`}
            aria-pressed={settingsOpen}
            title="Reasoning, models, context, and usage for this session"
            onClick={() =>
              settingsOpen ? inspector.close() : inspector.open({ kind: "sessionSettings", project, sessionId })
            }
          >
            Settings
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
      <FileLinksProvider value={fileLinks}>
        <Transcript
          controller={controller}
          snap={snap}
          onEditFailed={(t, pictures) => composer.current?.setDraft(t, pictures)}
          search={search}
        />
      </FileLinksProvider>
      {snap.mode === "live" && snap.pendingQuestion && (
        <AnswerPanel
          key={snap.pendingQuestion.rowId}
          controller={controller}
          question={snap.pendingQuestion}
          inFlight={snap.answerInFlight}
          submitted={snap.answeredRowId === snap.pendingQuestion.rowId}
        />
      )}
      {canCompose ? (
        <Composer
          ref={composer}
          sessionKey={`${project}\u0000${sessionId}`}
          placeholder={placeholder}
          picturesBlocked={snap.awaitsAnswer ? "Answer the pending question before sending pictures." : undefined}
          onSend={(t, pictures) => void controller.send(t, pictures)}
        />
      ) : (
        snap.mode === "persisted" && (
          <div className="readonly-note">
            <span className="muted">This session is not live, so this is a read-only transcript.</span>
            <button
              type="button"
              className="btn primary small"
              disabled={snap.reopening}
              onClick={() => void controller.reopen()}
              title="Re-open this session on its existing log and continue it"
            >
              {snap.reopening ? "Resuming…" : "Resume session"}
            </button>
          </div>
        )
      )}
    </div>
  );
}
