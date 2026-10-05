// Bearer-token storage and the global "needs token" signal. The token lives in
// this origin's localStorage (docs/design/web-client.md "Authentication"); it is
// only ever sent in the Authorization header, never in a URL.

const TOKEN_KEY = "ycc.token";

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
    return localStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

export function setToken(token: string | null) {
  memoryToken = token;
  try {
    if (token) localStorage.setItem(TOKEN_KEY, token);
    else localStorage.removeItem(TOKEN_KEY);
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
  /** A 401 anywhere: forget the token and return to token entry. */
  expire(note = "Session expired — enter the token again.") {
    if (status.kind === "needsToken") return;
    setToken(null);
    status = { kind: "needsToken", note };
    emit();
  },
};
