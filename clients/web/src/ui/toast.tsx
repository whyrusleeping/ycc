// Non-fatal RPC errors and confirmations surface as toasts. A tiny external
// store so non-React code (session controllers) can raise them too.
import { useSyncExternalStore } from "react";

export interface Toast {
  id: number;
  text: string;
  tone: "error" | "info";
}

let toasts: Toast[] = [];
let nextId = 1;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function toast(text: string, tone: Toast["tone"] = "error") {
  const id = nextId++;
  toasts = [...toasts.slice(-4), { id, text, tone }];
  emit();
  setTimeout(() => dismissToast(id), tone === "error" ? 6000 : 3500);
}

export function dismissToast(id: number) {
  const next = toasts.filter((t) => t.id !== id);
  if (next.length !== toasts.length) {
    toasts = next;
    emit();
  }
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
          <button type="button" className="toast-close" aria-label="Dismiss" onClick={() => dismissToast(t.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
