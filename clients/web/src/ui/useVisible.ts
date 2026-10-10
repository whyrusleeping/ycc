// Whether the user is looking at this tab: it is visible and its window has
// focus (re-renders on visibility and focus changes). A visible but unfocused
// window — e.g. behind another app on macOS — counts as away, matching
// notifier.userAway, so read marks don't swallow notifications.
import { useSyncExternalStore } from "react";

function subscribe(l: () => void) {
  document.addEventListener("visibilitychange", l);
  window.addEventListener("focus", l);
  window.addEventListener("blur", l);
  return () => {
    document.removeEventListener("visibilitychange", l);
    window.removeEventListener("focus", l);
    window.removeEventListener("blur", l);
  };
}

export function useUserPresent(): boolean {
  return useSyncExternalStore(subscribe, () => document.visibilityState !== "hidden" && document.hasFocus());
}
