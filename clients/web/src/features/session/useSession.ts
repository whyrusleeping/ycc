// React bindings for SessionController plus a small refcounted registry, so
// the main pane and the inspector share one controller per open session and a
// quickly re-opened session reuses its cached transcript.
import { useEffect, useMemo, useSyncExternalStore } from "react";
import { client } from "../../api/client";
import { authStore } from "../../api/auth";
import { toast } from "../../ui/toast";
import { SessionController, type SessionSnapshot } from "./controller";

interface Entry {
  controller: SessionController;
  refs: number;
  parkTimer: ReturnType<typeof setTimeout> | null;
  disposeTimer: ReturnType<typeof setTimeout> | null;
}

const entries = new Map<string, Entry>();
/** Keep a released session streaming briefly (route flicker), then park it. */
const PARK_AFTER_MS = 2_000;
/** Drop a parked session's cached transcript after this long. */
const DISPOSE_AFTER_MS = 5 * 60_000;

function key(project: string, sessionId: string) {
  return `${project}\u0000${sessionId}`;
}

function scheduleDispose(k: string, entry: Entry) {
  entry.disposeTimer = setTimeout(() => {
    if (entry.refs > 0) return;
    entries.delete(k);
    entry.controller.dispose();
  }, DISPOSE_AFTER_MS);
}

/** The cached controller for a session, created (unstarted) if needed. */
export function peekSession(project: string, sessionId: string): SessionController {
  const k = key(project, sessionId);
  let entry = entries.get(k);
  if (!entry) {
    entry = {
      controller: new SessionController(client, project, sessionId, {
        onUnauthorized: () => authStore.expire(),
        onError: (m) => toast(m, "error"),
        onInfo: (m) => toast(m, "info"),
      }),
      refs: 0,
      parkTimer: null,
      disposeTimer: null,
    };
    entries.set(k, entry);
    scheduleDispose(k, entry);
  }
  return entry.controller;
}

function retain(controller: SessionController) {
  const entry = entries.get(key(controller.project, controller.sessionId));
  if (!entry || entry.controller !== controller) return;
  entry.refs++;
  if (entry.parkTimer) clearTimeout(entry.parkTimer);
  if (entry.disposeTimer) clearTimeout(entry.disposeTimer);
  entry.parkTimer = entry.disposeTimer = null;
  controller.start();
}

function release(controller: SessionController) {
  const k = key(controller.project, controller.sessionId);
  const entry = entries.get(k);
  if (!entry || entry.controller !== controller) return;
  entry.refs = Math.max(0, entry.refs - 1);
  if (entry.refs > 0) return;
  entry.parkTimer = setTimeout(() => {
    entry.parkTimer = null;
    controller.stop();
    scheduleDispose(k, entry);
  }, PARK_AFTER_MS);
}

/** Reconnect every open live session (e.g. the tab became visible again). */
export function reconnectActiveSessions() {
  for (const entry of entries.values()) {
    if (entry.refs > 0 && entry.controller.getSnapshot().conn === "reconnecting") entry.controller.reconnect();
  }
}

/** Hold (and stream) a session's controller for the component's lifetime. */
export function useSessionController(project: string, sessionId: string): SessionController {
  const controller = useMemo(() => peekSession(project, sessionId), [project, sessionId]);
  useEffect(() => {
    retain(controller);
    return () => release(controller);
  }, [controller]);
  return controller;
}

export function useSessionSnapshot(controller: SessionController): SessionSnapshot {
  return useSyncExternalStore(controller.subscribe, controller.getSnapshot);
}
