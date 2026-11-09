// Subscription logins owned by the daemon (Begin/Complete/Cancel
// AnthropicLogin and OpenAILogin): the browser opens the provider's sign-in
// page, the user pastes back what it ends on (Anthropic shows a code#state;
// OpenAI redirects to a localhost:1455 address that usually fails to load),
// and the daemon exchanges and stores the tokens — they never reach this tab.
// The authorization URL and the pasted text live only in this dialog's state
// (never cached, logged, or put in a URL). Opened from Settings, Get started,
// and errors that point at an expired login (work loop, session). The
// Anthropic flow mirrors iOS AnthropicLoginView.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, refreshHealth } from "../../api/queries";
import { Modal } from "../../ui/Modal";
import { track } from "../../app/analytics";
import { callbackProblem, codeProblem, validLoginStart, validOpenAILoginStart } from "./anthropic";

export type LoginProvider = "anthropic" | "openai";

interface ProviderFlow {
  title: string;
  flow: string;
  account: string;
  begin: () => Promise<{ attemptId: string; authorizationUrl: string; expiresAtUnix: bigint }>;
  complete: (attemptId: string, pasted: string) => Promise<unknown>;
  cancel: (attemptId: string) => Promise<unknown>;
  validStart: (resp: { attemptId: string; authorizationUrl: string; expiresAtUnix: bigint }) => boolean;
  problem: (pasted: string) => string | null;
  pasteLabel: string;
  steps: string;
}

const FLOWS: Record<LoginProvider, ProviderFlow> = {
  anthropic: {
    title: "Connect Claude",
    flow: "anthropic_login",
    account: "Claude",
    begin: () => client.beginAnthropicLogin({}),
    complete: (attemptId, code) => client.completeAnthropicLogin({ attemptId, code }),
    cancel: (attemptId) => client.cancelAnthropicLogin({ attemptId }),
    validStart: validLoginStart,
    problem: codeProblem,
    pasteLabel: "Paste code#state",
    steps: "Sign in with your Claude Pro or Max account, then paste the full code Anthropic’s page shows (code#state).",
  },
  openai: {
    title: "Connect ChatGPT",
    flow: "openai_login",
    account: "ChatGPT",
    begin: () => client.beginOpenAILogin({}),
    complete: (attemptId, callback) => client.completeOpenAILogin({ attemptId, callback }),
    cancel: (attemptId) => client.cancelOpenAILogin({ attemptId }),
    validStart: validOpenAILoginStart,
    problem: callbackProblem,
    pasteLabel: "Paste the address",
    steps:
      "Sign in with your ChatGPT Plus or Pro account. Afterwards the browser goes to a localhost:1455 address that probably won’t load; that’s expected. Copy the whole address from the address bar and paste it here.",
  },
};

let openSeq = 0;
let openProvider: LoginProvider | null = null;
const listeners = new Set<() => void>();
function emit() {
  for (const l of listeners) l();
}

export function openSubscriptionLogin(provider: LoginProvider) {
  if (openProvider) return;
  openProvider = provider;
  openSeq++;
  emit();
}

export function openAnthropicLogin() {
  openSubscriptionLogin("anthropic");
}

export function openOpenAILogin() {
  openSubscriptionLogin("openai");
}

function closeLogin() {
  openProvider = null;
  emit();
}

function useOpen(): { provider: LoginProvider | null; seq: number } {
  const provider = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => openProvider,
  );
  return { provider, seq: openSeq };
}

/** Host rendered once by the shell. */
export function AnthropicLoginDialog() {
  const { provider, seq } = useOpen();
  const flow = FLOWS[provider ?? "anthropic"];
  return (
    <Modal open={provider !== null} onClose={closeLogin} title={flow.title} className="anthropic-login" view={flow.flow} flow={flow.flow}>
      {provider && <LoginBody key={seq} flow={flow} onClose={closeLogin} />}
    </Modal>
  );
}

interface Attempt {
  id: string;
  url: string;
  expiresAt: number;
}

function LoginBody({ flow, onClose }: { flow: ProviderFlow; onClose: () => void }) {
  const qc = useQueryClient();
  const [attempt, setAttempt] = useState<Attempt | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Bumped by every begin/cancel: a late Begin response is cancelled, not shown.
  const generation = useRef(0);
  const pending = useRef<string | null>(null);

  const cancelPending = () => {
    const id = pending.current;
    pending.current = null;
    if (id) void flow.cancel(id).catch(() => {});
  };

  // Closing the dialog (or leaving the page) drops this screen's pending attempt.
  useEffect(
    () => () => {
      generation.current++;
      cancelPending();
    },
    [],
  );

  const fail = (err: unknown, fallback: string, op: string) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return;
    }
    track.error(op, err);
    setError(errorMessage(err, fallback));
  };

  const begin = async () => {
    if (busy) return;
    const gen = ++generation.current;
    cancelPending();
    setAttempt(null);
    setCode("");
    setError(null);
    setConnected(false);
    setBusy(true);
    try {
      const resp = await flow.begin();
      if (gen !== generation.current) {
        void flow.cancel(resp.attemptId).catch(() => {});
        return;
      }
      if (!flow.validStart(resp)) {
        void flow.cancel(resp.attemptId).catch(() => {});
        track.error(`${flow.flow}.begin`, "invalid_start");
        setError("The daemon returned an invalid or expired login. Update the daemon and try again.");
        return;
      }
      pending.current = resp.attemptId;
      setAttempt({ id: resp.attemptId, url: resp.authorizationUrl, expiresAt: Number(resp.expiresAtUnix) * 1000 });
    } catch (err) {
      if (gen === generation.current) fail(err, "Couldn’t start the login.", `${flow.flow}.begin`);
    } finally {
      if (gen === generation.current) setBusy(false);
    }
  };

  const complete = async () => {
    if (busy || !attempt) return;
    if (Date.now() >= attempt.expiresAt) {
      track.error(`${flow.flow}.complete`, "expired");
      setError("This login expired. Start a new login.");
      cancelPending();
      setAttempt(null);
      setCode("");
      return;
    }
    const pasted = code.trim();
    if (!pasted || flow.problem(pasted)) return;
    const gen = generation.current;
    // Every submit consumes the attempt (even an ambiguous failure): never retry it.
    pending.current = null;
    setAttempt(null);
    setCode("");
    setError(null);
    setBusy(true);
    try {
      await flow.complete(attempt.id, pasted);
      if (gen === generation.current) {
        track.submit(flow.flow);
        setConnected(true);
      }
      // On a daemon with no model yet the login set one up: every view of
      // models and their health is stale now (re-checking is best effort).
      void qc.invalidateQueries({ queryKey: queryKeys.modelsAll });
      void refreshHealth(qc).catch(() => {});
    } catch (err) {
      if (gen === generation.current) fail(err, "Login could not be completed. Start a new login.", `${flow.flow}.complete`);
    } finally {
      if (gen === generation.current) setBusy(false);
    }
  };

  const problem = flow.problem(code);
  if (connected) {
    return (
      <div className="login-body">
        <div className="banner ok" role="status">
          <strong>{flow.account} connected.</strong> The daemon saved the login; models that use the {flow.account} subscription use it from
          their next request. If no model was set up yet, the daemon set one up with it; otherwise no model settings changed. No work loop
          was restarted.
        </div>
        <div className="dialog-actions">
          <button type="button" className="btn primary" onClick={onClose} autoFocus>
            Done
          </button>
        </div>
      </div>
    );
  }
  return (
    <div className="login-body">
      <p>{flow.steps}</p>
      <p className="muted small">
        This replaces the shared {flow.account} login for every project on this daemon. The tokens stay on the daemon.
      </p>
      <div className="login-step">
        <button type="button" className={`btn${attempt ? "" : " primary"}`} disabled={busy} onClick={() => void begin()}>
          {attempt ? "Start a new login" : busy ? "Contacting daemon…" : `Sign in to ${flow.account}`}
        </button>
      </div>
      {attempt && (
        <form
          className="login-step task-editor"
          onSubmit={(e) => {
            e.preventDefault();
            void complete();
          }}
        >
          <a className="btn primary login-link" href={attempt.url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer">
            Open the {flow.account} sign-in page ↗
          </a>
          <label className="field-label">
            {flow.pasteLabel}
            <input
              type="password"
              className="field mono"
              value={code}
              autoComplete="off"
              spellCheck={false}
              autoFocus
              onChange={(e) => setCode(e.target.value)}
            />
          </label>
          {problem && <p className="warn small">{problem}</p>}
          <div className="editor-actions">
            <span className="muted small">
              Expires at {new Date(attempt.expiresAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}; start a new login
              if it expires.
            </span>
            <button type="submit" className="btn primary" disabled={busy || !code.trim() || !!problem}>
              Complete login
            </button>
          </div>
        </form>
      )}
      {busy && !attempt && (
        <p className="muted small">You can close this dialog while waiting; a submitted login may still finish saving on the daemon.</p>
      )}
      {error && (
        <div className="banner warn" role="alert">
          {error}
          <div className="muted small">
            If completion was interrupted, the daemon may have saved the login anyway. Never reuse a submitted code.
          </div>
        </div>
      )}
      <div className="dialog-actions">
        <button type="button" className="btn ghost" onClick={onClose}>
          Cancel
        </button>
      </div>
    </div>
  );
}
