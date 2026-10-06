// Token entry. The candidate token is validated with an authenticated
// ListProjects before it is stored; it travels only in the Authorization header.
import { useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { makeClient } from "../../api/client";
import { authStore, setToken } from "../../api/auth";

export function TokenEntry({ note }: { note?: string }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState(note ?? "");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    const candidate = value.trim();
    if (!candidate) {
      setError("Token required.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await makeClient(() => candidate).listProjects({});
      setToken(candidate);
      authStore.setStatus({ kind: "ready" });
    } catch (err) {
      const ce = ConnectError.from(err);
      setError(ce.code === Code.Unauthenticated ? "Invalid token." : `Could not reach the daemon: ${ce.rawMessage}`);
      setBusy(false);
    }
  };

  return (
    <div className="token-screen">
      <form
        className="token-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <h1 className="brand">
          <span className="brand-mark" aria-hidden="true">
            y
          </span>
          ycc
        </h1>
        <p className="muted">Enter the daemon's access token to connect.</p>
        <input
          type="password"
          className="field"
          placeholder="access token"
          autoComplete="current-password"
          aria-label="Access token"
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <button type="submit" className="btn primary" disabled={busy}>
          {busy ? "Connecting…" : "Connect"}
        </button>
        {error && <p className="error">{error}</p>}
      </form>
    </div>
  );
}
