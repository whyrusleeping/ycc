// Bearer-token storage and the global "needs token" signal. The token lives in
// this origin's localStorage (docs/design/web-client.md "Authentication"); it is
// only ever sent in the Authorization header, never in a URL.

const TOKEN_KEY = "ycc.token";
// The vanilla phone client this app replaced stored the token here. Read it
// once and move it so an upgrade does not ask for the token again.
const LEGACY_TOKEN_KEY = "ycc_token";

export type AuthStatus =
  | { kind: "checking" }
  | { kind: "needsToken"; note?: string }
  | { kind: "ready" };

type Listener = () => void;

let memoryToken: string | null = null;
let status: AuthStatus = { kind: "checking" };
const listeners = new Set<Listener>();

function emit() {
  for (const l of listeners) l();
}

export function getToken(): string | null {
  if (memoryToken !== null) return memoryToken;
  try {
    const stored = localStorage.getItem(TOKEN_KEY);
    if (stored) return stored;
    const legacy = localStorage.getItem(LEGACY_TOKEN_KEY);
    if (legacy) {
      localStorage.setItem(TOKEN_KEY, legacy);
      localStorage.removeItem(LEGACY_TOKEN_KEY);
    }
    return legacy || null;
  } catch {
    return null;
  }
}

export function setToken(token: string | null) {
  memoryToken = token;
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
  } catch {
    // Private mode: keep the in-memory copy only.
  }
}

export const authStore = {
  subscribe(listener: Listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  getStatus(): AuthStatus {
    return status;
  },
  setStatus(next: AuthStatus) {
    status = next;
    emit();
  },
  /** Explicit sign-out: remove current and legacy tokens and show token entry. */
  forgetToken() {
    setToken(null);
    status = { kind: "needsToken", note: "Token forgotten." };
    emit();
  },
  /** A 401 anywhere: forget the token and return to token entry. */
  expire(note = "Session expired — enter the token again.") {
    if (status.kind === "needsToken") return;
    setToken(null);
    status = { kind: "needsToken", note };
    emit();
  },
};
