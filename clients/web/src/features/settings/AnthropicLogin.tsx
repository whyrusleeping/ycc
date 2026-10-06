// Anthropic subscription login, owned by the daemon (BeginAnthropicLogin /
// CompleteAnthropicLogin / CancelAnthropicLogin): the browser opens
// Anthropic's sign-in page, the user pastes the code#state it shows, and the
// daemon exchanges and stores the tokens — they never reach this tab. The
// authorization URL and the pasted code live only in this dialog's state
// (never cached, logged, or put in a URL). Opened from Settings and from
// errors that point at an expired login (work loop, session). Mirrors iOS
// AnthropicLoginView.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { Modal } from "../../ui/Modal";
import { codeProblem, validLoginStart } from "./anthropic";

let openSeq = 0;
let isOpen = false;
const listeners = new Set<() => void>();
function emit() {
  for (const l of listeners) l();
}

export function openAnthropicLogin() {
  if (isOpen) return;
  isOpen = true;
  openSeq++;
  emit();
}

function closeAnthropicLogin() {
  isOpen = false;
  emit();
}

function useOpen(): { open: boolean; seq: number } {
  const open = useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => isOpen,
  );
  return { open, seq: openSeq };
}

/** Host rendered once by the shell. */
export function AnthropicLoginDialog() {
  const { open, seq } = useOpen();
  return (
    <Modal open={open} onClose={closeAnthropicLogin} title="Anthropic login" className="anthropic-login">
      {open && <LoginBody key={seq} onClose={closeAnthropicLogin} />}
    </Modal>
  );
}

interface Attempt {
  id: string;
  url: string;
  expiresAt: number;
}

function LoginBody({ onClose }: { onClose: () => void }) {
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
    if (id) void client.cancelAnthropicLogin({ attemptId: id }).catch(() => {});
  };

  // Closing the dialog (or leaving the page) drops this screen's pending attempt.
  useEffect(
    () => () => {
      generation.current++;
      cancelPending();
    },
    [],
  );

  const fail = (err: unknown, fallback: string) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return;
    }
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
      const resp = await client.beginAnthropicLogin({});
      if (gen !== generation.current) {
        void client.cancelAnthropicLogin({ attemptId: resp.attemptId }).catch(() => {});
        return;
      }
      if (!validLoginStart(resp)) {
        void client.cancelAnthropicLogin({ attemptId: resp.attemptId }).catch(() => {});
        setError("The daemon returned an invalid or expired login. Update the daemon and try again.");
        return;
      }
      pending.current = resp.attemptId;
      setAttempt({ id: resp.attemptId, url: resp.authorizationUrl, expiresAt: Number(resp.expiresAtUnix) * 1000 });
    } catch (err) {
      if (gen === generation.current) fail(err, "Couldn’t start the login.");
    } finally {
      if (gen === generation.current) setBusy(false);
    }
  };

  const complete = async () => {
    if (busy || !attempt) return;
    if (Date.now() >= attempt.expiresAt) {
      setError("This login expired. Start a new login and use its new code.");
      cancelPending();
      setAttempt(null);
      setCode("");
      return;
    }
    const pasted = code.trim();
    if (!pasted || codeProblem(pasted)) return;
    const gen = generation.current;
    // Every submit consumes the attempt (even an ambiguous failure): never retry it.
    pending.current = null;
    setAttempt(null);
    setCode("");
    setError(null);
    setBusy(true);
    try {
      await client.completeAnthropicLogin({ attemptId: attempt.id, code: pasted });
      if (gen === generation.current) setConnected(true);
    } catch (err) {
      if (gen === generation.current) fail(err, "Login could not be completed. Start a new login.");
    } finally {
      if (gen === generation.current) setBusy(false);
    }
  };

  const problem = codeProblem(code);
  if (connected) {
    return (
      <div className="login-body">
        <div className="banner ok" role="status">
          <strong>Anthropic connected.</strong> Credentials were saved on the daemon; Anthropic OAuth models use them from their next
          request. No model settings changed and no work loop was restarted.
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
      <p>
        Sign in with your Claude subscription in the browser, then paste the full code Anthropic’s page shows (<span className="mono">code#state</span>
        ).
      </p>
      <p className="muted small">
        This replaces the shared Anthropic subscription login for every project on this daemon. Access and refresh tokens stay on the
        daemon.
      </p>
      <div className="login-step">
        <button type="button" className={`btn${attempt ? "" : " primary"}`} disabled={busy} onClick={() => void begin()}>
          {attempt ? "Start a new login" : busy ? "Contacting daemon…" : "Sign in to Anthropic"}
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
            Open Anthropic sign-in page ↗
          </a>
          <label className="field-label">
            Paste code#state
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
        <p className="muted small">You can close this dialog while waiting; a submitted code may still finish saving on the daemon.</p>
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
