// The session surface: header with status and phase-gated controls, the
// transcript, the answer panel for pending questions, and the composer.
// Sending to a persisted session reopens it transparently.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { Link, useNavigate } from "react-router";
import { paths } from "../../app/paths";
import { useAction, type AppAction } from "../../app/actions";
import { requestIntent, useIntent } from "../../app/intents";
import { readMarks } from "../sessions/unread";
import { useUserPresent } from "../../ui/useVisible";
import { Icon } from "../../ui/icons";
import { CopyButton } from "../../ui/CopyButton";
import { MenuButton } from "../../ui/Menu";
import { CAPTURE_SHORTCUT_LABEL, openCapture } from "../backlog/CaptureDialog";
import { NewTaskDialog } from "../backlog/NewTaskDialog";

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
        return snap.reopening ? { text: "Starting…", tone: "warn" } : { text: "Ready for your message", tone: "muted" };
      }
  }
  if (snap.pauseRequested) return { text: "Pausing at next checkpoint…", tone: "warn" };
  // An ask_user gate keeps the durable phase "running"; the agent is idle
  // until someone answers, so say so instead of "Running".
  if (snap.awaitsAnswer && snap.phase.kind === "running") return { text: "Needs your answer", tone: "warn" };
  switch (snap.phase.kind) {
    case "running":
      return { text: "Working", tone: "ok" };
    case "paused":
      return { text: "Paused", tone: "warn" };
    case "idle":
      return snap.awaitingJobs ? { text: "Waiting on background jobs", tone: "ok" } : { text: "Ready for your message", tone: "muted" };
    case "error":
      return { text: `Error${snap.phase.message ? `: ${snap.phase.message}` : ""}`, tone: "error" };
    case "stopped":
      return { text: "Stopped", tone: "muted" };
  }
}

/** The intent key that asks a session's controls to confirm Stop (palette). */
export function stopIntentKey(sessionId: string) {
  return `session-stop:${sessionId}`;
}

function Controls({ controller, snap }: { controller: SessionController; snap: SessionSnapshot }) {
  const [confirmStop, setConfirmStop] = useState(false);
  useIntent(
    stopIntentKey(controller.sessionId),
    useCallback((what: string) => {
      if (what === "stop") setConfirmStop(true);
    }, []),
  );
  if (snap.mode !== "live" || snap.conn === "finished" || snap.conn === "failed") return null;
  const busy = snap.control !== null;
  const pending = snap.control?.kind;
  const phase = snap.phase.kind;
  const buttons = [];
  if (phase === "running" && !snap.pauseRequested) {
    buttons.push(
      <button key="pause" type="button" className="btn" disabled={busy} title="Pause at the next safe checkpoint; an active tool is allowed to finish" data-track="session.interrupt" onClick={() => void controller.interrupt()}>
        {pending === "pause" ? "Requesting pause…" : "Pause"}
      </button>,
    );
  }
  if (snap.pauseRequested) {
    buttons.push(
      <button key="cancel" type="button" className="btn" disabled={busy} data-track="session.cancelPause" onClick={() => void controller.resume()}>
        {pending === "resume" ? "Cancelling…" : "Cancel pause"}
      </button>,
    );
  }
  if (phase === "paused") {
    buttons.push(
      <button key="resume" type="button" className="btn primary" disabled={busy} data-track="session.resume" onClick={() => void controller.resume()}>
        {pending === "resume" ? "Resuming…" : "Resume"}
      </button>,
    );
  }
  if (snap.phase.kind === "error" && needsAnthropicReconnect(snap.phase.message)) {
    buttons.push(
      <button key="anthropic" type="button" className="btn" data-track="session.anthropicLogin" onClick={() => openAnthropicLogin()} title="Sign in to Anthropic again on the daemon">
        Reconnect Anthropic…
      </button>,
    );
  }
  if (snap.phase.kind === "error" && snap.phase.retryable) {
    buttons.push(
      <button key="retry" type="button" className="btn primary" disabled={busy} data-track="session.retry" onClick={() => void controller.retry()}>
        {pending === "retry" ? "Retrying…" : "Retry"}
      </button>,
    );
  }
  const stoppable = phase !== "stopped";
  return (
    <div className="controls">
      {buttons}
      {stoppable && (
        <MenuButton
          className="btn ghost icon-btn"
          ariaLabel="More session actions"
          label={<Icon name="more" />}
          items={[
            snap.rolloverAvailable && phase !== "paused" && {
              id: "session.rollover",
              label: "Roll over coordinator context",
              disabled: busy,
              onSelect: () => void controller.rollover(),
            },
            {
              id: "session.stop",
              label: "Stop session…",
              danger: true,
              disabled: busy,
              onSelect: () => setConfirmStop(true),
            },
          ]}
        />
      )}
      <ConfirmDialog
        open={confirmStop}
        title="Stop this session?"
        body="Stopping terminates the agent and its background jobs instead of waiting for a safe checkpoint. Unlike Pause, it cannot be resumed here."
        confirmLabel="Stop session"
        danger
        action="session.stop"
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
  const [creatingTask, setCreatingTask] = useState(false);
  const composer = useRef<ComposerHandle>(null);
  const status = statusText(snap);
  const search = useTranscriptSearch(controller, snap.rows);
  const showSearch = search.show;
  // File references in the transcript open the viewer in the inspector,
  // resolved against this session's live worktree.
  const fileLinks = useSessionFileLinks(project, sessionId, "main");
  // Ctrl/Cmd-F searches this session's transcript (including history that
  // is not loaded yet) instead of the browser's find-in-page.
  useAction(
    useMemo<AppAction>(
      () => ({
        id: "session.search",
        title: "Search this session’s transcript",
        group: "Session",
        keywords: "find",
        shortcut: { key: "f", mod: true },
        run: showSearch,
      }),
      [showSearch],
    ),
  );
  useSessionActions(controller, snap, project, sessionId);
  useAction(
    useMemo<AppAction>(
      () => ({
        id: "session.newTask",
        title: "New backlog task (form)…",
        group: "Backlog",
        keywords: "add create todo",
        run: () => setCreatingTask(true),
      }),
      [],
    ),
  );
  // What is on screen is read: keep this session's read mark at the newest
  // event shown (not while the tab is hidden or the window unfocused: nobody
  // is looking, and marking it read would suppress its notification).
  // Streaming moves the stamp many times a second: write at most once a
  // second, and at once when the view goes away.
  const visible = useUserPresent();
  const seen = useRef<{ ts: string; timer: ReturnType<typeof setTimeout> | null }>({ ts: "", timer: null });
  useEffect(() => {
    if (!visible || !snap.installed || !snap.lastEventTimestamp) return;
    const s = seen.current;
    s.ts = snap.lastEventTimestamp;
    if (s.timer) return;
    readMarks.markRead(sessionId, s.ts);
    s.timer = setTimeout(() => {
      s.timer = null;
      readMarks.markRead(sessionId, s.ts);
    }, 1000);
  }, [visible, snap.installed, snap.lastEventTimestamp, sessionId]);
  useEffect(() => {
    const s = seen.current;
    return () => {
      if (s.timer) clearTimeout(s.timer);
      s.timer = null;
      if (s.ts) readMarks.markRead(sessionId, s.ts);
    };
  }, [sessionId]);
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
  // A persisted session accepts input too: sending re-opens it transparently.
  const canCompose = snap.mode === "persisted" || (snap.mode === "live" && snap.conn !== "finished");
  const drop = useDropZone((files) => composer.current?.addFiles(files), canCompose);
  const settingsOpen =
    inspector.item?.kind === "sessionSettings" &&
    inspector.item.project === project &&
    inspector.item.sessionId === sessionId;
  const changesOpen =
    inspector.item?.kind === "workingChanges" &&
    inspector.item.project === project &&
    inspector.item.sessionId === sessionId &&
    !inspector.item.taskId;

  let placeholder = "Message the agent… (Enter to send, Shift+Enter for a newline)";
  // A persisted log's last phase says nothing about the next turn: keep the default.
  const livePhase = snap.mode === "live";
  if (livePhase && snap.awaitsAnswer) placeholder = "Type an answer to the pending question…";
  else if (livePhase && snap.phase.kind === "paused") placeholder = "Steer the paused session, then Resume…";
  else if (livePhase && snap.phase.kind === "running") placeholder = "Send a steer — delivered at the next checkpoint…";

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
                  data-track="session.task_chip"
                  onClick={() => (open ? inspector.close() : inspector.open({ kind: "task", project, taskId: id }))}
                >
                  task {id}
                </button>
              );
            })}
            {snap.coordinatorModel && <span className="meta-minor">{snap.coordinatorModel}</span>}
            {ctx && (
              <span className="meta-minor" title="Context the coordinator is carrying">
                {ctx} ctx
              </span>
            )}
            <CopyButton text={sessionId} label={sessionId} className="session-id mono" title="Copy the session id" what="session_id" />
          </div>
        </div>
        <div className="session-actions">
          <button
            type="button"
            className={`btn ghost${search.open ? " active" : ""}`}
            title="Search this session (Ctrl/Cmd-F)"
            aria-label="Search"
            aria-pressed={search.open}
            data-track="session.search"
            onClick={() => (search.open ? search.close() : search.show())}
          >
            <Icon name="search" />
            <span className="btn-label">Search</span>
          </button>
          <button
            type="button"
            className={`btn ghost${changesOpen ? " active" : ""}`}
            title="Working changes: the diff in this session’s worktree"
            aria-label="Working changes"
            aria-pressed={changesOpen}
            data-track="session.workingChanges"
            onClick={() =>
              changesOpen ? inspector.close() : inspector.open({ kind: "workingChanges", project, sessionId })
            }
          >
            <Icon name="diff" />
            <span className="btn-label">Changes</span>
          </button>
          <Link
            className="btn ghost"
            to={paths.files(project || null, "", { session: sessionId })}
            title="Browse this session’s files (its live worktree)"
            aria-label="Files"
            data-track="session.files"
          >
            <Icon name="files" />
            <span className="btn-label">Files</span>
          </Link>
          <MenuButton
            className="btn ghost"
            ariaLabel="Add a backlog task"
            title={`Add a task to ${project || "this project"}’s backlog without leaving the session`}
            label={
              <>
                <Icon name="plus" />
                <span className="btn-label">Task</span>
              </>
            }
            items={[
              {
                id: "backlog.capture",
                label: "Quick capture…",
                shortcut: CAPTURE_SHORTCUT_LABEL,
                title: "Describe a task in a sentence and let the capture agent write it up",
                onSelect: () => openCapture(project || null, { beside: true }),
              },
              {
                id: "session.newTask",
                label: "New task…",
                title: "Fill in the task form yourself",
                onSelect: () => setCreatingTask(true),
              },
            ]}
          />
          <button
            type="button"
            className={`btn ghost${settingsOpen ? " active" : ""}`}
            aria-pressed={settingsOpen}
            aria-label="Session settings"
            data-track="session.settings"
            title="Reasoning, models, context, and usage for this session"
            onClick={() =>
              settingsOpen ? inspector.close() : inspector.open({ kind: "sessionSettings", project, sessionId })
            }
          >
            <Icon name="settings" />
            <span className="btn-label">Settings</span>
          </button>
          <Controls controller={controller} snap={snap} />
        </div>
      </header>
      {snap.conn === "failed" && snap.failure !== "unauthorized" && (
        <div className="banner error">
          {snap.failure}{" "}
          <button type="button" className="link" data-track="session.reconnect" onClick={() => controller.reconnect()}>
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
      {canCompose && (
        <Composer
          ref={composer}
          sessionKey={`${project}\u0000${sessionId}`}
          placeholder={placeholder}
          picturesBlocked={livePhase && snap.awaitsAnswer ? "Answer the pending question before sending pictures." : undefined}
          sendAttrs={{ phase: snap.awaitsAnswer ? "question" : snap.pauseRequested ? "pausing" : snap.phase.kind }}
          onSend={(t, pictures) => void controller.send(t, pictures)}
        />
      )}
      <NewTaskDialog
        project={project}
        open={creatingTask}
        onClose={() => setCreatingTask(false)}
        onCreated={(t) => inspector.open({ kind: "task", project, taskId: t.id })}
      />
    </div>
  );
}

/** Register the open session's actions (palette) for its current state. */
function useSessionActions(controller: SessionController, snap: SessionSnapshot, project: string, sessionId: string) {
  const inspector = useInspector();
  const navigate = useNavigate();
  const live = snap.mode === "live" && snap.conn !== "finished" && snap.conn !== "failed";
  const phase = snap.phase.kind;
  const idle = snap.control === null;
  const can = {
    interrupt: live && idle && phase === "running" && !snap.pauseRequested,
    cancelPause: live && idle && snap.pauseRequested,
    resume: live && idle && phase === "paused",
    retry: live && idle && phase === "error" && snap.phase.kind === "error" && snap.phase.retryable,
    rollover: live && idle && snap.rolloverAvailable && phase !== "paused" && phase !== "stopped",
    stop: live && idle && phase !== "stopped",
  };
  const g = "Session";
  useAction(
    useMemo(
      () => (can.interrupt ? { id: "session.interrupt", title: "Pause this session at the next safe checkpoint", group: g, keywords: "interrupt pause steer", run: () => void controller.interrupt() } : null),
      [can.interrupt, controller],
    ),
  );
  useAction(
    useMemo(
      () => (can.cancelPause ? { id: "session.cancelPause", title: "Cancel the pending pause", group: g, run: () => void controller.resume() } : null),
      [can.cancelPause, controller],
    ),
  );
  useAction(
    useMemo(
      () => (can.resume ? { id: "session.resume", title: "Resume this paused session", group: g, keywords: "continue", run: () => void controller.resume() } : null),
      [can.resume, controller],
    ),
  );
  useAction(
    useMemo(
      () => (can.retry ? { id: "session.retry", title: "Retry after the error", group: g, run: () => void controller.retry() } : null),
      [can.retry, controller],
    ),
  );
  useAction(
    useMemo(
      () =>
        can.rollover
          ? { id: "session.rollover", title: "Roll over the coordinator context", group: g, keywords: "compact context", run: () => void controller.rollover() }
          : null,
      [can.rollover, controller],
    ),
  );
  useAction(
    useMemo(
      () =>
        can.stop
          ? { id: "session.stop", title: "Stop this session…", group: g, keywords: "terminate kill end", run: () => requestIntent(stopIntentKey(sessionId), "stop") }
          : null,
      [can.stop, sessionId],
    ),
  );
  useAction(
    useMemo(
      () => ({
        id: "session.workingChanges",
        title: "Show this session’s working changes",
        group: g,
        keywords: "diff git",
        run: () => inspector.open({ kind: "workingChanges", project, sessionId }),
      }),
      // eslint-disable-next-line react-hooks/exhaustive-deps
      [inspector.open, project, sessionId],
    ),
  );
  useAction(
    useMemo(
      () => ({
        id: "session.settings",
        title: "Session settings (reasoning, models, context, usage)",
        group: g,
        keywords: "thinking model rollover usage",
        run: () => inspector.open({ kind: "sessionSettings", project, sessionId }),
      }),
      // eslint-disable-next-line react-hooks/exhaustive-deps
      [inspector.open, project, sessionId],
    ),
  );
  useAction(
    useMemo(
      () => ({
        id: "session.files",
        title: "Browse this session’s files",
        group: g,
        keywords: "worktree",
        run: () => navigate(paths.files(project || null, "", { session: sessionId })),
      }),
      [navigate, project, sessionId],
    ),
  );
}
