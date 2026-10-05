// Small browser-storage memories for the new-session flow: the last project
// the user viewed (asked about first when starting unscoped, like the iOS
// landing view) and the last mode used to start a session.
const LAST_VIEWED_KEY = "ycc.lastViewedProject";
const LAST_MODE_KEY = "ycc.newSession.mode";

function read(key: string): string | null {
  try {
    return localStorage.getItem(key) || null;
  } catch {
    return null;
  }
}

function write(key: string, value: string | null) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    // private mode: forget
  }
}

export const lastViewedProject = {
  get: () => read(LAST_VIEWED_KEY),
  set: (project: string) => {
    if (project) write(LAST_VIEWED_KEY, project);
  },
};

export const lastMode = {
  get: () => read(LAST_MODE_KEY),
  set: (mode: string) => write(LAST_MODE_KEY, mode),
};
