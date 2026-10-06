// Browser notifications: support and permission state, the user's on/off
// preference, and showing one deduplicated notification per event. Permission
// is only ever requested from a user gesture (enableNotifications).
import { useSyncExternalStore } from "react";
import { claimNotification } from "./policy";

export type NotifySupport =
  /** No Notification API (or an embedded browser without it). */
  | "unsupported"
  /** Plain http on a non-loopback address: browsers refuse notifications. */
  | "insecure"
  | "denied"
  /** Not asked yet. */
  | "default"
  | "granted";

const PREF_KEY = "ycc.notify.enabled";
const PROMPT_KEY = "ycc.notify.promptDismissed";

const listeners = new Set<() => void>();
function emit() {
  for (const l of listeners) l();
}

function storage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export function notifySupport(): NotifySupport {
  if (typeof window === "undefined") return "unsupported";
  if (!window.isSecureContext) return "insecure";
  if (typeof Notification === "undefined") return "unsupported";
  const p = Notification.permission;
  return p === "granted" ? "granted" : p === "denied" ? "denied" : "default";
}

function prefEnabled(): boolean {
  return storage()?.getItem(PREF_KEY) === "1";
}

/** Notifications will be shown: the user turned them on and the browser allows them. */
export function notificationsActive(): boolean {
  return prefEnabled() && notifySupport() === "granted";
}

export function setNotificationsEnabled(on: boolean) {
  const s = storage();
  try {
    if (on) s?.setItem(PREF_KEY, "1");
    else s?.removeItem(PREF_KEY);
  } catch {
    // ignore
  }
  emit();
}

/** Ask for permission (call from a click) and turn notifications on when granted. */
export async function enableNotifications(): Promise<NotifySupport> {
  const support = notifySupport();
  if (support === "unsupported" || support === "insecure" || support === "denied") return support;
  if (support === "default") {
    try {
      await Notification.requestPermission();
    } catch {
      // older callback-style implementations: permission is read below
    }
  }
  const after = notifySupport();
  setNotificationsEnabled(after === "granted");
  return after;
}

export function promptDismissed(): boolean {
  return storage()?.getItem(PROMPT_KEY) === "1";
}

export function dismissPrompt() {
  try {
    storage()?.setItem(PROMPT_KEY, "1");
  } catch {
    // ignore
  }
  emit();
}

export interface NotifyState {
  support: NotifySupport;
  /** The user's preference (may be on while the browser has since revoked permission). */
  enabled: boolean;
  active: boolean;
  promptDismissed: boolean;
}

let cached: NotifyState | null = null;
function snapshot(): NotifyState {
  const support = notifySupport();
  const enabled = prefEnabled();
  const dismissed = promptDismissed();
  const state = { support, enabled, active: enabled && support === "granted", promptDismissed: dismissed };
  if (cached && JSON.stringify(cached) === JSON.stringify(state)) return cached;
  cached = state;
  return state;
}

if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key === PREF_KEY || e.key === PROMPT_KEY) emit();
  });
  // Permission can change in the browser's site settings while the tab is open.
  window.addEventListener("focus", () => emit());
}

export function useNotifyState(): NotifyState {
  return useSyncExternalStore((l) => {
    listeners.add(l);
    return () => listeners.delete(l);
  }, snapshot);
}

export interface NotifyRequest {
  /** Dedupe key: at most one notification per key across tabs and reloads. */
  key: string;
  title: string;
  body: string;
  /** Run when the notification is clicked (after focusing the window). */
  onClick?: () => void;
}

/** Shown notifications, for tests and the smoke harness (window.__yccNotifications). */
const shown: { key: string; title: string; body: string }[] = [];

/** Show a notification unless this key was already notified (by any tab). */
export function showNotification(req: NotifyRequest, { force = false } = {}): boolean {
  if (!force && !notificationsActive()) return false;
  if (notifySupport() !== "granted") return false;
  if (!claimNotification(storage(), req.key)) return false;
  try {
    // `tag` makes the OS replace rather than stack a duplicate a racing tab shows.
    const n = new Notification(req.title, { body: req.body, tag: req.key });
    n.onclick = () => {
      window.focus();
      req.onClick?.();
      n.close();
    };
    shown.push({ key: req.key, title: req.title, body: req.body });
    (window as unknown as { __yccNotifications?: typeof shown }).__yccNotifications = shown;
    return true;
  } catch {
    // Some platforms only allow notifications from a service worker.
    return false;
  }
}

/** The user is not looking at this tab: hidden, or the window is in the background. */
export function userAway(): boolean {
  if (typeof document === "undefined") return false;
  return document.visibilityState === "hidden" || !document.hasFocus();
}
