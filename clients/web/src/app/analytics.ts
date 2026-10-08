// Private usage analytics (docs/design/usage-analytics.md): which surfaces are
// used, for how long, how actions are invoked, which flows are abandoned, and
// which failures the user sees. Events go to the daemon's RecordUiEvents in
// small batches; nothing leaves the user's own daemon.
//
// Privacy: every name, view, via, and attr is from a fixed vocabulary (route
// kinds, action ids, error codes). Never pass prompts, titles, paths, ids, or
// search text. sanitize() enforces the daemon's charset, not the rule.
//
// Recording is a push onto an array; failures are dropped silently and nothing
// here may throw into UI code.
import { useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { authStore, getToken } from "../api/auth";

/** How an action was invoked (the design doc's `via` vocabulary). */
export type Via = "shortcut" | "palette" | "click" | "menu" | "context_menu" | "swipe" | "gesture" | "keyboard" | "link" | "auto" | "";

export type Attrs = Record<string, string | number | boolean | null | undefined>;

/** One UiEvent in the Connect JSON encoding (int64 as strings). */
export interface UiEventJson {
  timeMs: string;
  kind: "visit" | "view" | "action" | "error";
  name: string;
  view?: string;
  via?: string;
  durationMs?: string;
  attrs?: Record<string, string>;
}

export interface CatalogEntry {
  kind: "view" | "action";
  name: string;
  shortcut?: string;
}

/** Every view name the web client emits (sent in the catalog). */
export const VIEW_NAMES = [
  "home",
  "new_session",
  "session",
  "settings",
  "project",
  "projects",
  "backlog",
  "task",
  "workloop",
  "workstreams",
  "files",
  "file",
  "memory",
  "plans",
  "plan",
  "usage",
  "not_found",
  "palette",
  "help",
  "quick_capture",
  "new_task",
  "add_project",
  "rename_project",
  "anthropic_login",
  "model_editor",
  "tier_editor",
  "loop_start",
  "workstream_spawn",
] as const;

const MAX_NAME = 80;
const MAX_VIA = 24;
const MAX_ATTR_KEY = 32;
const MAX_ATTR_VALUE = 64;
const MAX_ATTRS = 8;
const MAX_SHORTCUT_BYTES = 32;
export const FLUSH_INTERVAL_MS = 30_000;
export const FLUSH_AT = 100;
/** Events kept while sending is impossible (signed out); older ones drop. */
const MAX_BUFFER = 500;
/** A route left sooner than this was a redirect: it is not a view. */
export const MIN_ROUTE_DWELL_MS = 250;
/** Keepalive request bodies are capped (64 KiB across in-flight requests). */
const KEEPALIVE_MAX_BODY = 60_000;

/** Fit `s` to the daemon's token charset `[A-Za-z0-9_.:/-]` and a length bound. */
export function sanitize(s: string, max = MAX_NAME): string {
  return s.replace(/[^A-Za-z0-9_.:/-]+/g, "_").slice(0, max);
}

function cleanAttrs(attrs: Attrs | undefined): Record<string, string> | undefined {
  if (!attrs) return undefined;
  const out: Record<string, string> = {};
  let n = 0;
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === "") continue;
    const key = sanitize(k, MAX_ATTR_KEY);
    const value = sanitize(String(v), MAX_ATTR_VALUE);
    if (!key || !value) continue;
    out[key] = value;
    if (++n >= MAX_ATTRS) break;
  }
  return n ? out : undefined;
}

function shortcutFits(s: string): boolean {
  return new TextEncoder().encode(s).length <= MAX_SHORTCUT_BYTES && !/[\u0000-\u001f\u007f]/.test(s);
}

/**
 * The analytics name of a registered action id. Ids may carry an instance
 * scope after a colon ("task.edit:<project>:<id>"); that part names user data
 * and is never recorded.
 */
export function actionName(id: string): string {
  return sanitize(id.split(":")[0]);
}

/** A Connect error's code as a snake_case name ("deadline_exceeded"); short strings pass through. */
export function errorCode(cause: unknown): string {
  if (typeof cause === "string") return sanitize(cause, MAX_ATTR_VALUE) || "unknown";
  if (cause === undefined || cause === null) return "unknown";
  if (!(cause instanceof ConnectError) && cause instanceof Error && cause.name === "AbortError") return "canceled";
  if (!(cause instanceof ConnectError) && !(cause instanceof Error)) return "unknown";
  const name = Code[ConnectError.from(cause).code] ?? "unknown";
  return name.replace(/([a-z])([A-Z])/g, "$1_$2").toLowerCase();
}

/** The shared view name of a route, without ids (docs/design/usage-analytics.md). */
export function routeView(pathname: string): string {
  const parts = pathname.split("/").filter(Boolean);
  // Project-scoped routes (/p/:project/...) name the same surfaces as unscoped ones.
  if (parts[0] === "p") {
    if (parts.length < 2) return "not_found";
    if (parts.length === 2) return "project";
    parts.splice(0, 2);
  }
  const [head, ...rest] = parts;
  switch (head) {
    case undefined:
      return "home";
    case "new":
      return rest.length ? "not_found" : "new_session";
    case "s":
      return rest.length === 1 ? "session" : "not_found";
    case "settings":
      return "settings";
    case "backlog":
      return rest.length ? "task" : "backlog";
    case "loop":
      return "workloop";
    case "workstreams":
      return "workstreams";
    case "files": {
      // The route does not say directory or file; an extension is a good guess.
      const last = rest[rest.length - 1] ?? "";
      return /.\.[A-Za-z0-9]{1,8}$/.test(last) ? "file" : "files";
    }
    case "memory":
      return "memory";
    case "plans":
      return rest.length ? "plan" : "plans";
    case "projects":
      return "projects";
    case "usage":
      return "usage";
    default:
      return "not_found";
  }
}

interface Segment {
  name: string;
  /** Identity: a route's pathname (a new session is a new view), else the name. */
  key: string;
  from: string;
  /** Visible milliseconds accumulated while not ticking. */
  acc: number;
  /** When the current visible stretch started; null while paused. */
  since: number | null;
}

export interface RecorderDeps {
  now: () => number;
  /** POST one RecordUiEvents JSON body; resolves whether it was accepted. */
  send: (body: string, keepalive: boolean) => Promise<boolean>;
  /** Whether a request may be sent now (authenticated). */
  canSend: () => boolean;
  setTimer: (fn: () => void, ms: number) => unknown;
  clearTimer: (t: unknown) => void;
  visitId: string;
  clientVersion?: string;
  hidden?: boolean;
  /** Catalog entries remembered from earlier visits of this build. */
  loadCatalog?: () => CatalogEntry[];
  saveCatalog?: (entries: CatalogEntry[]) => void;
}

/**
 * The analytics state machine, independent of the DOM so it can be tested:
 * a buffer with flush triggers, a view stack (route at the bottom, open modal
 * surfaces above it; only the top one's clock runs, and not while the tab is
 * hidden), open flows, and the catalog.
 */
export class Recorder {
  private buf: UiEventJson[] = [];
  private stack: Segment[] = [];
  private suspended: { name: string; key: string }[] | null = null;
  private hidden: boolean;
  private timer: unknown = null;
  private catalog = new Map<string, CatalogEntry>();
  private catalogSent = false;
  private catalogDirty = false;
  private flows = new Map<string, { submitted: boolean }>();

  constructor(private deps: RecorderDeps) {
    this.hidden = !!deps.hidden;
    for (const v of VIEW_NAMES) this.catalog.set(`view:${v}`, { kind: "view", name: v });
    for (const e of deps.loadCatalog?.() ?? []) this.catalog.set(`${e.kind}:${e.name}`, e);
  }

  /** The surface the user is looking at (top of the view stack). */
  currentView(): string {
    return this.stack[this.stack.length - 1]?.name ?? "";
  }

  pending(): readonly UiEventJson[] {
    return this.buf;
  }

  visit(attrs: Attrs) {
    this.push({ kind: "visit", name: "start", attrs: cleanAttrs(attrs) });
  }

  action(name: string, via: Via = "", attrs?: Attrs, durationMs?: number) {
    const n = sanitize(name);
    if (!n) return;
    this.push({
      kind: "action",
      name: n,
      view: this.currentView() || undefined,
      via: sanitize(via, MAX_VIA) || undefined,
      durationMs: durationMs !== undefined ? String(Math.max(0, Math.round(durationMs))) : undefined,
      attrs: cleanAttrs(attrs),
    });
  }

  error(name: string, cause?: unknown, attrs?: Attrs) {
    const n = sanitize(name);
    if (!n) return;
    this.push({ kind: "error", name: n, view: this.currentView() || undefined, attrs: cleanAttrs({ code: errorCode(cause), ...attrs }) });
  }

  /** The routed surface changed (the bottom of the view stack). */
  route(name: string, key: string) {
    if (this.suspended) {
      this.suspended[0] = { name, key };
      return;
    }
    const cur = this.stack[0];
    if (cur?.key === key) return;
    let from = "";
    if (cur) {
      this.pause(cur);
      if (cur.acc < MIN_ROUTE_DWELL_MS) from = cur.from;
      else {
        this.emitView(cur);
        from = cur.name;
      }
    }
    const seg: Segment = { name, key, from, acc: 0, since: null };
    if (cur) this.stack[0] = seg;
    else this.stack.unshift(seg);
    if (this.stack.length === 1) this.start(seg);
  }

  /** A modal surface opened over the current view; returns its close function. */
  openView(name: string): () => void {
    const below = this.stack[this.stack.length - 1];
    if (below) this.pause(below);
    const seg: Segment = { name, key: name, from: below?.name ?? "", acc: 0, since: null };
    this.stack.push(seg);
    this.start(seg);
    return () => {
      const i = this.stack.indexOf(seg);
      if (i < 0) return;
      const wasTop = i === this.stack.length - 1;
      this.pause(seg);
      this.emitView(seg);
      this.stack.splice(i, 1);
      const top = this.stack[this.stack.length - 1];
      if (wasTop && top) this.start(top);
    };
  }

  /** A flow (form, wizard) opened: records `<flow>.open`, then `.submit` or `.cancel`. */
  openFlow(flow: string, via: Via = ""): () => void {
    const state = { submitted: false };
    this.flows.set(flow, state);
    this.action(`${flow}.open`, via);
    return () => {
      if (this.flows.get(flow) === state) this.flows.delete(flow);
      if (!state.submitted) this.action(`${flow}.cancel`);
    };
  }

  /** The open flow's form was submitted successfully. */
  submitFlow(flow: string, attrs?: Attrs) {
    const state = this.flows.get(flow);
    if (state) state.submitted = true;
    this.action(`${flow}.submit`, "", attrs);
  }

  setHidden(hidden: boolean) {
    if (hidden === this.hidden) return;
    this.hidden = hidden;
    const top = this.stack[this.stack.length - 1];
    if (!top) return;
    if (hidden) {
      this.pause(top);
      this.flush(true);
    } else this.start(top);
  }

  /** The page is going away (pagehide, freeze): end every view and flush. */
  suspend() {
    if (!this.suspended) {
      const top = this.stack[this.stack.length - 1];
      if (top) this.pause(top);
      // Report the top first: it was the one on screen.
      for (const seg of [...this.stack].reverse()) this.emitView(seg);
      this.suspended = this.stack.map((s) => ({ name: s.name, key: s.key }));
      this.stack = [];
    }
    this.flush(true);
  }

  /** Back from the back/forward cache or a freeze: reopen the views. */
  resume() {
    const prev = this.suspended;
    if (!prev) return;
    this.suspended = null;
    this.stack = prev.map((s) => ({ ...s, from: "", acc: 0, since: null }));
    const top = this.stack[this.stack.length - 1];
    if (top) this.start(top);
  }

  /** Name things the client could record; a growing catalog is resent. */
  addCatalog(entries: readonly CatalogEntry[]) {
    let grew = false;
    for (const e of entries) {
      const name = sanitize(e.name);
      if (!name) continue;
      const k = `${e.kind}:${name}`;
      const shortcut = e.shortcut && shortcutFits(e.shortcut) ? e.shortcut : undefined;
      const prev = this.catalog.get(k);
      if (prev && prev.shortcut === shortcut) continue;
      if (prev && !shortcut) continue;
      this.catalog.set(k, { kind: e.kind, name, ...(shortcut ? { shortcut } : {}) });
      grew = true;
    }
    if (!grew) return;
    this.catalogDirty = true;
    try {
      this.deps.saveCatalog?.([...this.catalog.values()]);
    } catch {
      // storage is best-effort
    }
    this.schedule();
  }

  flush(keepalive = false) {
    this.clearTimer();
    const withCatalog = !this.catalogSent || this.catalogDirty;
    if (!this.buf.length && !this.catalogDirty) return;
    if (!this.deps.canSend()) {
      if (this.buf.length) this.schedule();
      return;
    }
    const events = this.buf;
    this.buf = [];
    const body: Record<string, unknown> = { client: "web", visitId: this.deps.visitId, events };
    if (this.deps.clientVersion) body.clientVersion = this.deps.clientVersion;
    if (withCatalog) {
      body.catalog = [...this.catalog.values()];
      this.catalogSent = true;
      this.catalogDirty = false;
    }
    const json = JSON.stringify(body);
    let sent: Promise<boolean>;
    try {
      sent = this.deps.send(json, keepalive && json.length <= KEEPALIVE_MAX_BODY);
    } catch {
      sent = Promise.resolve(false);
    }
    // A lost batch stays lost; only the catalog is worth resending.
    void sent.then(
      (ok) => {
        if (!ok && withCatalog) this.catalogDirty = true;
      },
      () => {
        if (withCatalog) this.catalogDirty = true;
      },
    );
  }

  private push(e: Omit<UiEventJson, "timeMs">) {
    const ev: UiEventJson = { timeMs: String(this.deps.now()), ...e };
    for (const k of Object.keys(ev) as (keyof UiEventJson)[]) if (ev[k] === undefined) delete ev[k];
    this.buf.push(ev);
    if (this.buf.length > MAX_BUFFER) this.buf.splice(0, this.buf.length - MAX_BUFFER);
    if (this.buf.length >= FLUSH_AT) this.flush();
    else this.schedule();
  }

  private emitView(seg: Segment) {
    this.push({ kind: "view", name: seg.name, durationMs: String(Math.round(seg.acc)), attrs: seg.from ? { from: seg.from } : undefined });
  }

  private pause(seg: Segment) {
    if (seg.since === null) return;
    seg.acc += Math.max(0, this.deps.now() - seg.since);
    seg.since = null;
  }

  private start(seg: Segment) {
    if (!this.hidden && seg.since === null) seg.since = this.deps.now();
  }

  private schedule() {
    if (this.timer !== null) return;
    this.timer = this.deps.setTimer(() => {
      this.timer = null;
      this.flush();
    }, FLUSH_INTERVAL_MS);
  }

  private clearTimer() {
    if (this.timer === null) return;
    this.deps.clearTimer(this.timer);
    this.timer = null;
  }
}

// --- Browser wiring -------------------------------------------------------

// Defined by scripts/build.mjs (the build-inputs hash prefix); absent in dev and tests.
declare const __YCC_WEB_BUILD__: string | undefined;

const CATALOG_KEY = "ycc.analytics.catalog";
const BUILD = typeof __YCC_WEB_BUILD__ === "string" ? __YCC_WEB_BUILD__ : "";

let recorder: Recorder | null = null;

function randomId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  }
}

function send(body: string, keepalive: boolean): Promise<boolean> {
  const headers: Record<string, string> = { "Content-Type": "application/json", "Connect-Protocol-Version": "1" };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  return fetch("/ycc.v1.SessionService/RecordUiEvents", { method: "POST", headers, body, keepalive, credentials: "same-origin" }).then(
    (r) => r.ok,
    () => false,
  );
}

function media(q: string): boolean {
  try {
    return window.matchMedia(q).matches;
  } catch {
    return false;
  }
}

function visitAttrs(): Attrs {
  return {
    // The stylesheet's compact-layout breakpoint.
    layout: window.innerWidth <= 1240 ? "narrow" : "wide",
    theme: media("(prefers-color-scheme: dark)") ? "dark" : "light",
    input: media("(pointer: coarse)") ? "touch" : "mouse",
    display: media("(display-mode: standalone)") ? "standalone" : "browser",
  };
}

function loadCatalog(): CatalogEntry[] {
  try {
    const raw = JSON.parse(localStorage.getItem(CATALOG_KEY) ?? "null");
    // Entries of another build may name actions that no longer exist.
    if (!raw || raw.build !== BUILD || !Array.isArray(raw.entries)) return [];
    return raw.entries.filter((e: CatalogEntry) => e && (e.kind === "view" || e.kind === "action") && typeof e.name === "string");
  } catch {
    return [];
  }
}

function saveCatalog(entries: CatalogEntry[]) {
  try {
    localStorage.setItem(CATALOG_KEY, JSON.stringify({ build: BUILD, entries: entries.filter((e) => e.kind === "action") }));
  } catch {
    // private mode
  }
}

/** Start recording for this page load (main.tsx). Safe to call more than once. */
export function installAnalytics() {
  if (recorder || typeof window === "undefined" || typeof document === "undefined") return;
  try {
    const r = new Recorder({
      now: () => Date.now(),
      send,
      canSend: () => authStore.getStatus().kind === "ready",
      setTimer: (fn, ms) => setTimeout(fn, ms),
      clearTimer: (t) => clearTimeout(t as ReturnType<typeof setTimeout>),
      visitId: randomId(),
      clientVersion: BUILD || undefined,
      hidden: document.visibilityState === "hidden",
      loadCatalog,
      saveCatalog,
    });
    recorder = r;
    r.visit(visitAttrs());
    document.addEventListener("visibilitychange", () => r.setHidden(document.visibilityState === "hidden"));
    window.addEventListener("pagehide", () => r.suspend());
    window.addEventListener("pageshow", (e) => {
      if (e.persisted) r.resume();
    });
    // Page Lifecycle: a frozen (possibly discarded) tab may never see pagehide.
    document.addEventListener("freeze", () => r.suspend());
    document.addEventListener("resume", () => r.resume());
    // Plain controls opt in with data-track="<action id>"; capture phase, so
    // the action is attributed to the view it happened on, before navigating.
    document.addEventListener(
      "click",
      (e) => {
        const el = (e.target as Element | null)?.closest?.("[data-track]");
        const name = el?.getAttribute("data-track");
        // data-track-via overrides the input method (e.g. "menu" for menu items).
        if (name) guard((rec) => rec.action(name, (el?.getAttribute("data-track-via") as Via | null) || clickVia(e)));
      },
      { capture: true },
    );
  } catch {
    recorder = null;
  }
}

/** A click from Enter/Space on a focused control has detail 0. */
export function clickVia(e: { detail: number }): Via {
  return e.detail === 0 ? "keyboard" : "click";
}

function guard(fn: (r: Recorder) => void) {
  if (!recorder) return;
  try {
    fn(recorder);
  } catch {
    // analytics never breaks the UI
  }
}

/** The app-facing recording API; a no-op until installAnalytics() (tests, SSR). */
export const track = {
  action: (name: string, via: Via = "", attrs?: Attrs, durationMs?: number) => guard((r) => r.action(name, via, attrs, durationMs)),
  /** A user-visible failure of `operation`; `cause` is the error (its Connect code is kept) or a short code. */
  error: (operation: string, cause?: unknown, attrs?: Attrs) => guard((r) => r.error(operation, cause, attrs)),
  /** A flow's form succeeded (see useFlow). */
  submit: (flow: string, attrs?: Attrs) => guard((r) => r.submitFlow(flow, attrs)),
  route: (pathname: string) => guard((r) => r.route(routeView(pathname), pathname)),
  catalog: (entries: readonly CatalogEntry[]) => guard((r) => r.addCatalog(entries)),
  view: () => recorder?.currentView() ?? "",
};

/** Count `name` as a view while `open` (modal surfaces). */
export function useTrackedView(name: string | undefined, open: boolean) {
  useEffect(() => {
    if (!open || !name || !recorder) return;
    const r = recorder;
    let close: (() => void) | null = null;
    guard(() => (close = r.openView(name)));
    return () => guard(() => close?.());
  }, [name, open]);
}

/** Record `<flow>.open` while `open`, then `.cancel` unless track.submit(flow) ran. */
export function useFlow(flow: string | undefined, open = true) {
  useEffect(() => {
    if (!open || !flow || !recorder) return;
    const r = recorder;
    let close: (() => void) | null = null;
    guard(() => (close = r.openFlow(flow)));
    return () => guard(() => close?.());
  }, [flow, open]);
}

/** Test seam: install a recorder built with fake dependencies. */
export function setRecorderForTest(r: Recorder | null) {
  recorder = r;
}
