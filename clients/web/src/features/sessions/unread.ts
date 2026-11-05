// The browser's read store (a localStorage cache over the daemon's shared
// marks, synced across tabs) and its React binding: components that show
// unread state re-render on any mark.
import { useSyncExternalStore } from "react";
import { client } from "../../api/client";
import { READ_MARKS_KEY, SessionReadStore } from "./readStore";

function browserStorage() {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

export const readMarks = new SessionReadStore(browserStorage(), READ_MARKS_KEY, {
  sink: async (project, marks) => (await client.markSessionsRead({ project, marks })).marks,
});

if (typeof window !== "undefined") {
  // Another tab marked something read: adopt its marks.
  window.addEventListener("storage", (e) => {
    if (e.key === null || e.key === READ_MARKS_KEY) readMarks.load();
  });
  // Leaving or hiding the page: push the marks still queued for the daemon.
  window.addEventListener("pagehide", () => void readMarks.flush());
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") void readMarks.flush();
  });
}

/** Re-render on read-mark changes; returns the store. */
export function useReadMarks(): SessionReadStore {
  useSyncExternalStore(readMarks.subscribe, readMarks.getRevision);
  return readMarks;
}
