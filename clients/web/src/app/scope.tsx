// Sidebar scope: null is the daemon-wide Recent feed, a name scopes the
// session list (and project navigation) to one registered project.
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

const SCOPE_KEY = "ycc.scope";

interface ScopeState {
  scope: string | null;
  setScope: (scope: string | null) => void;
}

const Ctx = createContext<ScopeState | null>(null);

function stored(): string | null {
  try {
    return localStorage.getItem(SCOPE_KEY) || null;
  } catch {
    return null;
  }
}

export function ScopeProvider({ children }: { children: ReactNode }) {
  const [scope, setState] = useState<string | null>(stored);
  const setScope = useCallback((next: string | null) => {
    setState(next);
    try {
      if (next) localStorage.setItem(SCOPE_KEY, next);
      else localStorage.removeItem(SCOPE_KEY);
    } catch {
      // ignore
    }
  }, []);
  const value = useMemo(() => ({ scope, setScope }), [scope, setScope]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useScope(): ScopeState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useScope outside ScopeProvider");
  return v;
}
