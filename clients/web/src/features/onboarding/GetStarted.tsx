// First-run checklist, shown on the home page while the daemon has no sessions
// at all: connect a model, add a project, then start the first session
// (onboarding, for a project without a spec or backlog). Each step reads the
// daemon's own state — the coordinator's health check, the project list — so
// work done elsewhere (the CLI, iOS, Settings) ticks it off too. Once any
// session exists the ordinary session list replaces it.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, refreshHealth, useHealth, useModels, useModes, useProjects } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { openAddProject } from "../projects/ProjectDialogs";
import { openAnthropicLogin, openOpenAILogin } from "../settings/AnthropicLogin";
import { modelStep, onboardingTarget } from "./model";

export function GetStarted() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const projects = useProjects();
  const models = useModels("");
  const modes = useModes();
  const health = useHealth();
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const coordinator = models.data?.coordinator ?? "";
  const coordModel = models.data?.models.find((m) => m.name === coordinator);
  const model = modelStep(coordinator, health.data, health.isError);
  const projectList = projects.data ?? [];
  const target = onboardingTarget(projectList);
  const onboard = modes.data?.presets.find((p) => p.name === "onboard");

  const recheck = async () => {
    setChecking(true);
    try {
      await qc.invalidateQueries({ queryKey: queryKeys.models("") });
      await refreshHealth(qc);
    } catch {
      // The health panel reports check failures; here the step just stays open.
    } finally {
      setChecking(false);
    }
  };

  // The daemon's first scheduled check runs a while after startup, which is
  // exactly when a new user opens this page: ask for it now instead of waiting.
  const pendingAtLoad = model.state === "pending" && health.data !== undefined;
  const askedEarly = useRef(false);
  useEffect(() => {
    if (!pendingAtLoad || askedEarly.current) return;
    askedEarly.current = true;
    void recheck();
  }, [pendingAtLoad]);

  const startOnboarding = async () => {
    if (!target || !onboard || starting) return;
    setStarting(true);
    setError(null);
    try {
      const resp = await client.startSession({ project: target.name, mode: onboard.mode, prompt: onboard.openingPrompt, preset: onboard.name });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      navigate(paths.session(target.name, resp.sessionId));
    } catch (err) {
      setStarting(false);
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error("get_started.onboard", err);
      setError(errorMessage(err, "The onboarding session could not be started."));
    }
  };

  const modelReady = model.state === "ok" || model.state === "unknown";
  const keyEnv = coordModel?.backend === "anthropic" || !coordModel ? "ANTHROPIC_API_KEY" : "OPENAI_API_KEY";

  return (
    <div className="get-started">
      <p className="get-started-intro">
        ycc runs agents on this daemon’s machine, in projects you register with it. Three steps to the first session:
      </p>
      <ol className="get-started-steps">
        <Step
          n={1}
          title="Connect a model"
          done={model.state === "ok"}
          summary={
            model.state === "ok" ? (
              <>
                Sessions use <span className="mono">{coordinator}</span>
                {coordModel?.model ? <span className="muted"> ({coordModel.model})</span> : null}. Assign other models to roles in{" "}
                <Link to={paths.settings("roles")}>Settings</Link>.
              </>
            ) : model.state === "pending" ? (
              "Checking the model…"
            ) : model.state === "unknown" ? (
              <>
                This daemon can’t report model health; check <Link to={paths.settings("models")}>Settings → Models</Link>.
              </>
            ) : (
              <>
                Sessions need a model the daemon can use. Connect one of these:
                {model.detail && <span className="get-started-detail">Model {coordinator}: {model.detail}</span>}
              </>
            )
          }
        >
          {model.state === "todo" && (
            <div className="get-started-options">
              <div className="get-started-option">
                <strong>Claude Pro or Max subscription</strong>
                <span className="muted small">Sign in from here; the daemon keeps the login.</span>
                <button type="button" className="btn primary small" data-track="get_started.connect_claude" onClick={openAnthropicLogin}>
                  Connect Claude…
                </button>
              </div>
              <div className="get-started-option">
                <strong>ChatGPT Plus or Pro subscription</strong>
                <span className="muted small">Sign in from here; the daemon keeps the login.</span>
                <button type="button" className="btn primary small" data-track="get_started.connect_chatgpt" onClick={openOpenAILogin}>
                  Connect ChatGPT…
                </button>
              </div>
              <div className="get-started-option">
                <strong>An API key</strong>
                <span className="muted small">
                  On the daemon’s machine run <code>ycc token set {keyEnv}</code>, then check again. Other providers (OpenAI-compatible
                  endpoints, Ollama) are added in <Link to={paths.settings("models")}>Settings → Models</Link>.
                </span>
              </div>
              <div>
                <button type="button" className="btn ghost small" disabled={checking} data-track="get_started.recheck" onClick={() => void recheck()}>
                  {checking ? "Checking…" : "Check again"}
                </button>
              </div>
            </div>
          )}
        </Step>
        <Step
          n={2}
          title="Add a project"
          done={projectList.length > 0}
          summary={
            projectList.length > 0 ? (
              <>Registered: {projectList.map((p) => p.name).join(", ")}.</>
            ) : (
              "A project is a directory on the daemon’s machine, usually a git repository: an existing codebase, or an empty folder for something new."
            )
          }
        >
          <div>
            <button
              type="button"
              className={`btn small${projectList.length ? " ghost" : " primary"}`}
              data-track="get_started.add_project"
              onClick={() => openAddProject(() => void qc.invalidateQueries({ queryKey: queryKeys.projects }))}
            >
              {projectList.length ? "Add another project…" : "Add project…"}
            </button>
          </div>
        </Step>
        <Step
          n={3}
          title={target?.needsOnboarding && onboard ? `Onboard ${target.name}` : "Start your first session"}
          done={false}
          summary={
            target?.needsOnboarding && onboard
              ? "The agent asks what the project is for (or, in an existing codebase, what you want to change next), then drafts a spec.md and a starter backlog with you."
              : "Describe a bug or a change, or ask a question about the code."
          }
        >
          {target && modelReady && (
            <div className="get-started-actions">
              {target.needsOnboarding && onboard && (
                <button type="button" className="btn primary small" disabled={starting} data-track="get_started.onboard" onClick={() => void startOnboarding()}>
                  {starting ? "Starting…" : "Start onboarding"}
                </button>
              )}
              <Link
                to={paths.newSession(target.name)}
                className={`btn small${target.needsOnboarding && onboard ? " ghost" : " primary"}`}
                data-track="get_started.new_session"
              >
                {target.needsOnboarding && onboard ? "Start a chat instead" : "New session…"}
              </Link>
            </div>
          )}
          {target && !modelReady && <p className="muted small">Available once a model is connected.</p>}
          {error && (
            <p className="error small" role="alert">
              {error}
            </p>
          )}
        </Step>
      </ol>
    </div>
  );
}

function Step({ n, title, done, summary, children }: { n: number; title: string; done: boolean; summary: ReactNode; children?: ReactNode }) {
  return (
    <li className={`get-started-step${done ? " done" : ""}`}>
      <span className="get-started-mark" aria-hidden="true">
        {done ? "✓" : n}
      </span>
      <div className="get-started-body">
        <h2>
          {title}
          {done && <span className="sr-only"> (done)</span>}
        </h2>
        <p className="muted">{summary}</p>
        {!done && children}
      </div>
    </li>
  );
}
