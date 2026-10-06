// The browser's shared read store (localStorage, synced across tabs) and its
// React binding: components that show unread state re-render on any mark.
import { useSyncExternalStore } from "react";
import { READ_MARKS_KEY, SessionReadStore } from "./readStore";

function browserStorage() {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export const readMarks = new SessionReadStore(browserStorage());

if (typeof window !== "undefined") {
  // Another tab marked something read (or baselined): adopt its marks.
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key === READ_MARKS_KEY || e.key === READ_MARKS_KEY + ".watermark") readMarks.load();
  });
}

/** Re-render on read-mark changes; returns the store. */
export function useReadMarks(): SessionReadStore {
  useSyncExternalStore(readMarks.subscribe, readMarks.getRevision);
  return readMarks;
}
