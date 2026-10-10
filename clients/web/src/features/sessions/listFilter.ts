// The sidebar and its next/previous shortcuts must use the same visible list.
// Keep the selected filter for this tab, also when the routed surface changes.
import { useSyncExternalStore } from "react";
import type { SessionFilter } from "./attention";

let current: SessionFilter = "inbox";
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
};

export function setSidebarFilter(filter: SessionFilter) {
  if (current === filter) return;
  current = filter;
  for (const listener of listeners) listener();
}

export function useSidebarFilter(): SessionFilter {
  return useSyncExternalStore(subscribe, () => current);
}
