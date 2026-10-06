// Non-fatal RPC errors and confirmations surface as toasts. A tiny external
// store so non-React code (session controllers) can raise them too. A toast
// repeating one already shown collapses into it ("×3") and restarts its timer.
import { useSyncExternalStore } from "react";

export interface Toast {
  id: number;
  text: string;
  tone: "error" | "info";
  /** How many times this text was raised while shown. */
  count: number;
}

let toasts: Toast[] = [];
let nextId = 1;
const timers = new Map<number, ReturnType<typeof setTimeout>>();
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function schedule(t: Toast) {
  const prev = timers.get(t.id);
  if (prev) clearTimeout(prev);
  timers.set(
    t.id,
    setTimeout(() => dismissToast(t.id), t.tone === "error" ? 6000 : 3500),
  );
}

export function toast(text: string, tone: Toast["tone"] = "error") {
  const same = toasts.find((t) => t.text === text && t.tone === tone);
  if (same) {
    const bumped = { ...same, count: same.count + 1 };
    toasts = toasts.map((t) => (t === same ? bumped : t));
    schedule(bumped);
    emit();
    return;
  }
  const t: Toast = { id: nextId++, text, tone, count: 1 };
  const dropped = toasts.slice(0, -4);
  for (const d of dropped) {
    clearTimeout(timers.get(d.id));
    timers.delete(d.id);
  }
  toasts = [...toasts.slice(-4), t];
  schedule(t);
  emit();
}

export function dismissToast(id: number) {
  const next = toasts.filter((t) => t.id !== id);
  const timer = timers.get(id);
  if (timer) clearTimeout(timer);
  timers.delete(id);
  if (next.length !== toasts.length) {
    toasts = next;
    emit();
  }
}

export function currentToasts(): readonly Toast[] {
  return toasts;
}

export function useToasts(): Toast[] {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => toasts,
  );
}

export function Toasts() {
  const items = useToasts();
  return (
    <div className="toasts" role="status" aria-live="polite">
      {items.map((t) => (
        <div key={t.id} className={`toast toast-${t.tone}`}>
          <span>{t.text}</span>
          {t.count > 1 && (
            <span className="toast-count" aria-label={`shown ${t.count} times`}>
              ×{t.count}
            </span>
          )}
          <button type="button" className="toast-close" aria-label="Dismiss" onClick={() => dismissToast(t.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
