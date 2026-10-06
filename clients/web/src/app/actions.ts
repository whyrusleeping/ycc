// App-wide actions with optional keyboard shortcuts. Surfaces register what
// they can do (quick capture, interrupt, …) for as long as they are mounted;
// the shell dispatches matching shortcuts, the command palette lists and runs
// the registry, and the help overlay lists every registered shortcut.
import { useEffect, useSyncExternalStore } from "react";
import { IS_MAC } from "./platform";

export interface Shortcut {
  /**
   * KeyboardEvent.code (layout-independent: Option+N on macOS still reports
   * KeyN). Use for Alt chords, whose `key` is a typed symbol on macOS.
   */
  code?: string;
  /**
   * KeyboardEvent.key, case-insensitively (follows the keyboard layout, like
   * the browser's own Ctrl+F). For a non-letter key such as "?" Shift is
   * whatever the layout needs and is not compared.
   */
  key?: string;
  alt?: boolean;
  shift?: boolean;
  /** Ctrl on Linux/Windows, Cmd on macOS. */
  mod?: boolean;
  /** Human label, e.g. "Alt+N" (default: derived, with ⌘/⌥/⇧ on macOS). */
  label?: string;
  /**
   * Fire even while typing in a text field. Default: true for chords
   * (Ctrl/Cmd or Alt), false for plain keys such as "?".
   */
  inEditable?: boolean;
  /** Fire while a modal dialog is open (default false). */
  inDialog?: boolean;
}

export interface AppAction {
  id: string;
  title: string;
  /** Palette and help grouping, e.g. "Backlog". */
  group?: string;
  /** Extra words the palette matches (not shown). */
  keywords?: string;
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

const KEY_GLYPHS: Record<string, string> = {
  ArrowUp: "↑",
  ArrowDown: "↓",
  ArrowLeft: "←",
  ArrowRight: "→",
  Backslash: "\\",
  Slash: "/",
  Period: ".",
  Comma: ",",
  BracketLeft: "[",
  BracketRight: "]",
  Escape: "Esc",
  Enter: "Enter",
  Space: "Space",
};

function keyName(s: Shortcut): string {
  if (s.key) return s.key.length === 1 ? s.key.toUpperCase() : (KEY_GLYPHS[s.key] ?? s.key);
  const code = s.code ?? "";
  if (/^Key[A-Z]$/.test(code)) return code.slice(3);
  if (/^Digit\d$/.test(code)) return code.slice(5);
  return KEY_GLYPHS[code] ?? code;
}

/** "Ctrl+Shift+K" / "⌘⇧K"; plain keys are just the key ("?"). */
export function formatShortcut(s: Shortcut, mac: boolean): string {
  const key = keyName(s);
  const shift = s.shift && !(s.key && !/^[a-z]$/i.test(s.key));
  if (mac) return `${s.mod ? "⌘" : ""}${s.alt ? "⌥" : ""}${shift ? "⇧" : ""}${key}`;
  return [s.mod && "Ctrl", s.alt && "Alt", shift && "Shift", key].filter(Boolean).join("+");
}

export function shortcutLabel(s: Shortcut): string {
  return s.label ?? formatShortcut(s, IS_MAC);
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
  if (e.altKey !== !!s.alt || mod !== !!s.mod) return false;
  if (s.key !== undefined) {
    if ((e.key ?? "").toLowerCase() !== s.key.toLowerCase()) return false;
    // "?" is Shift+/ on one layout and a plain key on another.
    return /^[a-z]$/i.test(s.key) ? e.shiftKey === !!s.shift : true;
  }
  return e.code === s.code && e.shiftKey === !!s.shift;
}

/** A chord (Ctrl/Cmd or Alt) rather than a plain key. */
export function isChord(s: Shortcut): boolean {
  return !!(s.mod || s.alt);
}

/**
 * Whether a matching shortcut may fire: plain keys never while typing
 * (chords unless they opt out), and nothing behind an open modal dialog
 * unless the shortcut asks for it (the palette's own toggle).
 */
export function shortcutAllowed(s: Shortcut, ctx: { editable: boolean; dialogOpen: boolean }): boolean {
  if (ctx.dialogOpen && !s.inDialog) return false;
  if (ctx.editable && !(s.inEditable ?? isChord(s))) return false;
  return true;
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

/** The registered action a key event triggers, if any. */
export function shortcutAction(e: KeyLike, list: readonly AppAction[], ctx: { editable: boolean; dialogOpen: boolean }): AppAction | null {
  for (const a of list) {
    const s = a.shortcut;
    if (s && matchShortcut(e, s) && shortcutAllowed(s, ctx)) return a;
  }
  return null;
}

/** Dispatch registered shortcuts from window keydown (installed once by the shell). */
export function useActionShortcuts() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return;
      const a = shortcutAction(e, actions, {
        editable: isEditableTarget(e.target),
        dialogOpen: !!document.querySelector("dialog[open]"),
      });
      if (!a) return;
      e.preventDefault();
      a.run();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
