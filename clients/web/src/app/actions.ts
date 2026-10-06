// App-wide actions with optional keyboard shortcuts. Surfaces register what
// they can do (quick capture, …); the shell dispatches matching shortcuts, and
// the command palette (backlog 0417) lists and runs the same registry.
import { useEffect, useSyncExternalStore } from "react";

export interface Shortcut {
  /** KeyboardEvent.code (layout-independent: Option+N on macOS still reports KeyN). */
  code: string;
  alt?: boolean;
  shift?: boolean;
  /** Ctrl on Linux/Windows, Cmd on macOS. */
  mod?: boolean;
  /** Human label, e.g. "Alt+N". */
  label: string;
  /** Fire even while typing in a text field (chords only; default true). */
  inEditable?: boolean;
}

export interface AppAction {
  id: string;
  title: string;
  /** Palette grouping, e.g. "Backlog". */
  group?: string;
  shortcut?: Shortcut;
  run: () => void;
}

let actions: AppAction[] = [];
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

/** Register (or replace, by id) an action; returns the unregister function. */
export function registerAction(action: AppAction): () => void {
  actions = [...actions.filter((a) => a.id !== action.id), action];
  emit();
  return () => {
    if (!actions.includes(action)) return;
    actions = actions.filter((a) => a !== action);
    emit();
  };
}

export function listActions(): readonly AppAction[] {
  return actions;
}

export function runAction(id: string): boolean {
  const a = actions.find((x) => x.id === id);
  if (!a) return false;
  a.run();
  return true;
}

export function useActions(): readonly AppAction[] {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => actions,
  );
}

/** Register an action for the lifetime of the calling component. */
export function useAction(action: AppAction | null) {
  useEffect(() => (action ? registerAction(action) : undefined), [action]);
}

export interface KeyLike {
  code: string;
  key: string;
  altKey: boolean;
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  isComposing?: boolean;
}

export function matchShortcut(e: KeyLike, s: Shortcut): boolean {
  if (e.isComposing) return false;
  const mod = e.ctrlKey || e.metaKey;
  return e.code === s.code && e.altKey === !!s.alt && e.shiftKey === !!s.shift && mod === !!s.mod;
}

/** Whether keyboard focus is in something the user types into. */
export function isEditableTarget(target: EventTarget | null): boolean {
  if (!target || typeof (target as Element).closest !== "function") return false;
  const el = target as HTMLElement;
  if (el.isContentEditable) return true;
  const tag = el.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  if (tag === "INPUT") {
    const type = (el as HTMLInputElement).type;
    return !["checkbox", "radio", "button", "submit", "reset", "range", "color", "file"].includes(type);
  }
  return false;
}

/** Dispatch registered shortcuts from window keydown (installed once by the shell). */
export function useActionShortcuts() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      const editable = isEditableTarget(e.target);
      for (const a of actions) {
        const s = a.shortcut;
        if (!s || !matchShortcut(e, s)) continue;
        if (editable && s.inEditable === false) continue;
        e.preventDefault();
        a.run();
        return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
