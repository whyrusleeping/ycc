// One-shot page intents: an action (palette, shortcut, another page) asks a
// surface to open one of its dialogs, then navigates there; the surface takes
// the intent when it mounts (or at once, when already shown).
import { useEffect } from "react";

const pending = new Map<string, string>();
const listeners = new Set<() => void>();

/** Ask the surface identified by `key` (e.g. "loop:gamma") to do `what`. */
export function requestIntent(key: string, what: string) {
  pending.set(key, what);
  for (const l of listeners) l();
}

export function takeIntent(key: string): string | null {
  const v = pending.get(key) ?? null;
  pending.delete(key);
  return v;
}

/** Run `handle` for each intent addressed to `key` while mounted. */
export function useIntent(key: string, handle: (what: string) => void) {
  useEffect(() => {
    const check = () => {
      const what = takeIntent(key);
      if (what) handle(what);
    };
    check();
    listeners.add(check);
    return () => {
      listeners.delete(check);
    };
  }, [key, handle]);
}
