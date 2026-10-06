// Whether the tab is visible (re-renders on visibilitychange).
import { useSyncExternalStore } from "react";

function subscribe(l: () => void) {
  document.addEventListener("visibilitychange", l);
  return () => document.removeEventListener("visibilitychange", l);
}

export function useDocumentVisible(): boolean {
  return useSyncExternalStore(subscribe, () => document.visibilityState !== "hidden");
}
