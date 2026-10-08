// Quick capture: describe a backlog item in a sentence and the daemon's
// off-stream capture agent (CaptureBacklogItem) turns it into a task, maybe
// after one clarifying question. Opened from anywhere through the app action
// registry (button, Alt+N, and the command palette) or a session header's
// Task menu; running sessions are not disturbed. The description and answer
// survive every failure. Opened from a session, "Open task" shows the result in
// the inspector beside the transcript instead of leaving the session.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useReducer, useRef, useState, useSyncExternalStore } from "react";
import { useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useProjects } from "../../api/queries";
import { paths } from "../../app/paths";
import { lastViewedProject } from "../../app/memory";
import { IS_MAC } from "../../app/platform";
import { projectChoices } from "../newSession/model";
import { useInspector } from "../inspector/inspector";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { captureOutcome, captureReducer, INITIAL_CAPTURE } from "./capture";

interface CaptureRequest {
  open: boolean;
  /** Preferred project (route or sidebar scope); null asks. */
  project: string | null;
  /** Open the created task in the inspector rather than on its page. */
  beside: boolean;
  /** Bumped per open so a re-open resets the form. */
  seq: number;
}

let request: CaptureRequest = { open: false, project: null, beside: false, seq: 0 };
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function openCapture(project: string | null, opts: { beside?: boolean } = {}) {
  if (request.open) return;
  request = { open: true, project, beside: !!opts.beside, seq: request.seq + 1 };
  emit();
}

function closeCapture() {
  if (!request.open) return;
  request = { ...request, open: false };
  emit();
}

function useCaptureRequest(): CaptureRequest {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => request,
  );
}

export { IS_MAC };

/** The shortcut label shown on capture buttons. */
export const CAPTURE_SHORTCUT_LABEL = IS_MAC ? "⌥N" : "Alt+N";

export function CaptureDialog() {
  const req = useCaptureRequest();
  return (
    <Modal
      open={req.open}
      onClose={closeCapture}
      title="Capture a backlog item"
      className="capture-dialog"
      view="quick_capture"
      flow="quick_capture"
    >
      {req.open && <CaptureBody key={req.seq} preferred={req.project} beside={req.beside} />}
    </Modal>
  );
}

function CaptureBody({ preferred, beside }: { preferred: string | null; beside: boolean }) {
  const projects = useProjects();
  const inspector = useInspector();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [state, dispatch] = useReducer(captureReducer, INITIAL_CAPTURE);
  const [description, setDescription] = useState("");
  const [answer, setAnswer] = useState("");
  const list = projects.data ?? [];
  const choices = useMemo(() => projectChoices(list, lastViewedProject.get()), [list]);
  const [project, setProject] = useState<string | null>(null);
  const abort = useRef<AbortController | null>(null);
  const answerRef = useRef<HTMLInputElement>(null);

  // Default project: the preferred one, else the sole one, else last viewed.
  useEffect(() => {
    if (project !== null || !projects.data) return;
    const names = projects.data.map((p) => p.name);
    if (preferred !== null && (names.includes(preferred) || names.length === 0)) setProject(preferred);
    else if (names.length === 0) setProject("");
    else setProject(choices[0] ?? "");
  }, [projects.data, preferred, project, choices]);

  // Leaving the dialog cancels a capture still in flight.
  useEffect(() => () => abort.current?.abort(), []);

  useEffect(() => {
    if (state.stage === "question") answerRef.current?.focus();
  }, [state.stage, state.question]);

  const run = async (target: string, desc: string, question: string, reply: string) => {
    abort.current?.abort();
    const ctl = new AbortController();
    abort.current = ctl;
    try {
      const stream = client.captureBacklogItem(
        { project: target, description: desc, priorQuestion: question, priorAnswer: reply },
        { signal: ctl.signal },
      );
      for await (const ev of stream) {
        dispatch({ type: "event", event: ev });
        const outcome = captureOutcome(ev);
        if (outcome?.kind === "created") {
          track.submit("quick_capture", { asked: question !== "" });
          void qc.invalidateQueries({ queryKey: queryKeys.backlog(target) });
          toast(`Captured task ${outcome.taskId}: ${outcome.title}`, "info");
        }
      }
      if (!ctl.signal.aborted) dispatch({ type: "ended" });
    } catch (err) {
      if (ctl.signal.aborted) return;
      if (isUnauthorized(err)) {
        closeCapture();
        authStore.expire();
        return;
      }
      track.error("backlog.capture", err);
      dispatch({ type: "failed", message: errorMessage(err, "The capture failed.") });
    }
  };

  const submit = () => {
    const desc = description.trim();
    if (!desc || state.busy || project === null) return;
    dispatch({ type: "submit", description: desc });
    void run(project, desc, "", "");
  };
  const submitAnswer = () => {
    const reply = answer.trim();
    if (!reply || state.busy || project === null) return;
    dispatch({ type: "answer", answer: reply });
    void run(project, state.description, state.question, reply);
  };
  const cancelRun = () => {
    track.action("quick_capture.abort", "click");
    abort.current?.abort();
    dispatch({ type: "failed", message: "Capture cancelled." });
  };

  const asking = choices.length > 1;
  const describing = state.stage === "describe";

  return (
    <div className="capture">
      {asking && (
        <label className="field-label inline">
          Project
          <select
            value={project ?? ""}
            disabled={!describing || state.busy}
            onChange={(e) => setProject(e.target.value)}
            aria-label="Project"
          >
            {choices.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </label>
      )}
      {!asking && project && <p className="muted small capture-project">In {project}</p>}
      {describing ? (
        <label className="field-label">
          Describe the task
          <textarea
            rows={3}
            value={description}
            autoFocus
            disabled={state.busy}
            placeholder="e.g. Retry the fetch on 5xx responses (Enter to capture, Shift+Enter for a newline)"
            onChange={(e) => setDescription(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                submit();
              }
            }}
          />
        </label>
      ) : (
        <div className="capture-desc">
          <span className="label">Description</span>
          <div>{state.description}</div>
        </div>
      )}
      {state.stage === "question" && (
        <div className="capture-question">
          <div className="label">The capture agent asks</div>
          <div className="question-text">{state.question}</div>
          <input
            ref={answerRef}
            className="field"
            value={answer}
            disabled={state.busy}
            aria-label="Your answer"
            placeholder="Your answer (Enter to send)"
            onChange={(e) => setAnswer(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                e.preventDefault();
                submitAnswer();
              }
            }}
          />
        </div>
      )}
      {state.log.length > 0 && (
        <ol className="capture-log" aria-label="Capture progress">
          {state.log.slice(-12).map((l) => (
            <li key={l.key} className={`who-${l.who}`}>
              {l.who === "you" ? "› " : ""}
              {l.text}
            </li>
          ))}
        </ol>
      )}
      {state.busy && <p className="muted small capture-busy">Capturing…</p>}
      {state.error && (
        <div className="banner error" role="alert">
          {state.error}
        </div>
      )}
      {state.created && (
        <div className="banner ok capture-created" role="status">
          Created task <strong>{state.created.taskId}</strong>: {state.created.title}
        </div>
      )}
      <div className="editor-actions">
        <span className="muted small">Runs off-stream — running sessions keep going.</span>
        {state.busy ? (
          <button type="button" className="btn" onClick={cancelRun}>
            Cancel capture
          </button>
        ) : state.created ? (
          <>
            <button
              type="button"
              className="btn"
              data-track="quick_capture.another"
              onClick={() => {
                dispatch({ type: "reset" });
                setDescription("");
                setAnswer("");
              }}
            >
              Capture another
            </button>
            <button
              type="button"
              className="btn primary"
              autoFocus
              data-track="quick_capture.open_task"
              onClick={() => {
                const id = state.created?.taskId ?? "";
                closeCapture();
                if (beside) inspector.open({ kind: "task", project: project ?? "", taskId: id });
                else navigate(paths.task(project ?? "", id));
              }}
            >
              Open task
            </button>
          </>
        ) : (
          <>
            <button type="button" className="btn" onClick={closeCapture}>
              Close
            </button>
            {state.stage === "question" ? (
              <button type="button" className="btn primary" disabled={!answer.trim() || project === null} onClick={submitAnswer}>
                Send answer
              </button>
            ) : (
              <button type="button" className="btn primary" disabled={!description.trim() || project === null} onClick={submit}>
                Capture
              </button>
            )}
          </>
        )}
      </div>
    </div>
  );
}
