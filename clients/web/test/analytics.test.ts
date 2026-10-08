// Usage analytics: route → view names, the daemon charset, buffering and
// flush triggers, dwell accounting (hidden time and modal surfaces excluded),
// flows, and how registry actions attribute `via`.
import { fromJson } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  FLUSH_AT,
  FLUSH_INTERVAL_MS,
  Recorder,
  actionName,
  clickVia,
  errorCode,
  routeView,
  sanitize,
  setRecorderForTest,
  track,
  type RecorderDeps,
  type UiEventJson,
} from "../src/app/analytics";
import { dispatchShortcut, invokeAction, listActions, registerAction, runAction, type AppAction, type KeyLike } from "../src/app/actions";
import { RecordUiEventsRequestSchema } from "../src/gen/ycc/v1/ycc_pb";

interface Sent {
  body: { client: string; visitId: string; clientVersion?: string; events: UiEventJson[]; catalog?: { kind: string; name: string; shortcut?: string }[] };
  raw: string;
  keepalive: boolean;
}

function harness(over: Partial<RecorderDeps> = {}) {
  let now = 1_700_000_000_000;
  let accept = true;
  let can = true;
  const timers = new Set<{ fn: () => void; ms: number }>();
  const sent: Sent[] = [];
  const r = new Recorder({
    now: () => now,
    send: (raw, keepalive) => {
      sent.push({ body: JSON.parse(raw), raw, keepalive });
      return Promise.resolve(accept);
    },
    canSend: () => can,
    setTimer: (fn, ms) => {
      const h = { fn, ms };
      timers.add(h);
      return h;
    },
    clearTimer: (h) => timers.delete(h as { fn: () => void; ms: number }),
    visitId: "visit-1",
    clientVersion: "abc123",
    ...over,
  });
  return {
    r,
    sent,
    timers,
    advance: (ms: number) => (now += ms),
    fireTimers: () => {
      const due = [...timers];
      timers.clear();
      for (const t of due) t.fn();
    },
    setAccept: (v: boolean) => (accept = v),
    setCan: (v: boolean) => (can = v),
    /** Every event sent so far plus the ones still buffered. */
    events: () => [...sent.flatMap((s) => s.body.events), ...r.pending()],
  };
}

const views = (evs: UiEventJson[]) => evs.filter((e) => e.kind === "view").map((e) => ({ name: e.name, ms: Number(e.durationMs), from: e.attrs?.from }));

afterEach(() => {
  setRecorderForTest(null);
});

describe("route → view names", () => {
  it("maps every route shape to a shared name without ids", () => {
    const cases: [string, string][] = [
      ["/", "home"],
      ["/p/ycc", "project"],
      ["/p/ycc/", "project"],
      ["/new", "new_session"],
      ["/p/ycc/new", "new_session"],
      ["/s/abc", "session"],
      ["/p/ycc/s/abc", "session"],
      ["/settings", "settings"],
      ["/backlog", "backlog"],
      ["/p/ycc/backlog", "backlog"],
      ["/p/ycc/backlog/0012", "task"],
      ["/backlog/0012", "task"],
      ["/loop", "workloop"],
      ["/p/ycc/loop", "workloop"],
      ["/p/ycc/workstreams", "workstreams"],
      ["/files", "files"],
      ["/p/ycc/files/internal/web", "files"],
      ["/p/ycc/files/internal/web/serve.go", "file"],
      ["/p/ycc/memory", "memory"],
      ["/p/ycc/plans", "plans"],
      ["/p/ycc/plans/roadmap", "plan"],
      ["/projects", "projects"],
      ["/usage", "usage"],
      ["/p/ycc/usage", "usage"],
      ["/nope", "not_found"],
      ["/p/ycc/s/abc/extra", "not_found"],
    ];
    for (const [path, view] of cases) expect([path, routeView(path)]).toEqual([path, view]);
  });
});

describe("sanitisation", () => {
  it("fits the daemon charset and length bounds", () => {
    expect(sanitize("session.interrupt")).toBe("session.interrupt");
    expect(sanitize("usage.openAll")).toBe("usage.openAll");
    expect(sanitize("has spaces & symbols!")).toBe("has_spaces_symbols_");
    expect(sanitize("ünïcode")).toBe("_n_code");
    expect(sanitize("x".repeat(100))).toHaveLength(80);
    expect(/^[A-Za-z0-9_.:/-]*$/.test(sanitize("a b\tc\n<d>"))).toBe(true);
  });

  it("drops the instance scope of registry ids", () => {
    expect(actionName("task.edit:my project:0012")).toBe("task.edit");
    expect(actionName("session.interrupt")).toBe("session.interrupt");
  });

  it("names Connect codes in snake case and passes short codes through", () => {
    expect(errorCode(new ConnectError("x", Code.DeadlineExceeded))).toBe("deadline_exceeded");
    expect(errorCode(new ConnectError("x", Code.Unavailable))).toBe("unavailable");
    expect(errorCode(new Error("boom"))).toBe("unknown");
    expect(errorCode("conflict")).toBe("conflict");
    expect(errorCode(undefined)).toBe("unknown");
  });

  it("cleans event attrs: enum values only, at most 8", () => {
    const h = harness();
    h.r.action("x.y", "click", { a: "has space", b: 3, c: true, d: undefined, e: null, f: "" });
    expect(h.r.pending()[0].attrs).toEqual({ a: "has_space", b: "3", c: "true" });
    const many = Object.fromEntries(Array.from({ length: 12 }, (_, i) => [`k${i}`, "v"]));
    h.r.action("x.z", "click", many);
    expect(Object.keys(h.r.pending()[1].attrs ?? {})).toHaveLength(8);
  });

  it("classifies keyboard-activated clicks", () => {
    expect(clickVia({ detail: 0 })).toBe("keyboard");
    expect(clickVia({ detail: 1 })).toBe("click");
  });
});

describe("buffering and flushing", () => {
  it("flushes on the interval timer with the Connect JSON body", () => {
    const h = harness();
    h.r.visit({ layout: "wide", theme: "dark", input: "mouse" });
    h.r.action("session.interrupt", "shortcut");
    expect(h.sent).toHaveLength(0);
    expect([...h.timers].map((t) => t.ms)).toEqual([FLUSH_INTERVAL_MS]);
    h.fireTimers();
    expect(h.sent).toHaveLength(1);
    const { body, raw, keepalive } = h.sent[0];
    expect(keepalive).toBe(false);
    expect(body).toMatchObject({ client: "web", visitId: "visit-1", clientVersion: "abc123" });
    expect(body.events[0]).toMatchObject({ kind: "visit", name: "start", attrs: { layout: "wide", theme: "dark", input: "mouse" } });
    // int64 fields travel as JSON strings, and the body decodes as the RPC's request.
    expect(typeof body.events[0].timeMs).toBe("string");
    const msg = fromJson(RecordUiEventsRequestSchema, JSON.parse(raw));
    expect(msg.events[1].name).toBe("session.interrupt");
    expect(msg.events[1].via).toBe("shortcut");
    expect(msg.events[1].timeMs).toBe(1_700_000_000_000n);
    expect(h.r.pending()).toHaveLength(0);
  });

  it("flushes at once when the buffer reaches the batch size", () => {
    const h = harness();
    for (let i = 0; i < FLUSH_AT - 1; i++) h.r.action("x.y", "click");
    expect(h.sent).toHaveLength(0);
    h.r.action("x.y", "click");
    expect(h.sent).toHaveLength(1);
    expect(h.sent[0].body.events).toHaveLength(FLUSH_AT);
    expect(h.timers.size).toBe(0);
  });

  it("flushes with keepalive when the tab hides or the page goes away", () => {
    const h = harness();
    h.r.route("home", "/");
    h.r.action("x.y", "click");
    h.r.setHidden(true);
    expect(h.sent).toHaveLength(1);
    expect(h.sent[0].keepalive).toBe(true);
    h.r.setHidden(false);
    h.advance(1000);
    h.r.suspend();
    expect(h.sent).toHaveLength(2);
    expect(h.sent[1].keepalive).toBe(true);
    expect(views(h.sent[1].body.events)).toEqual([{ name: "home", ms: 1000, from: undefined }]);
  });

  it("holds events while signed out and sends them later", () => {
    const h = harness();
    h.setCan(false);
    h.r.action("x.y", "click");
    h.fireTimers();
    expect(h.sent).toHaveLength(0);
    expect(h.r.pending()).toHaveLength(1);
    h.setCan(true);
    h.fireTimers();
    expect(h.sent).toHaveLength(1);
    expect(h.sent[0].body.events).toHaveLength(1);
  });

  it("drops a failed batch but resends the catalog", async () => {
    const h = harness();
    h.setAccept(false);
    h.r.action("x.y", "click");
    h.r.flush();
    expect(h.sent[0].body.catalog?.length).toBeGreaterThan(0);
    await Promise.resolve();
    h.setAccept(true);
    h.r.action("x.z", "click");
    h.r.flush();
    expect(h.sent[1].body.events.map((e) => e.name)).toEqual(["x.z"]);
    expect(h.sent[1].body.catalog?.length).toBeGreaterThan(0);
    await Promise.resolve();
    h.r.action("x.w", "click");
    h.r.flush();
    expect(h.sent[2].body.catalog).toBeUndefined();
  });

  it("sends the catalog once per visit and again when it grows", async () => {
    const h = harness();
    h.r.addCatalog([{ kind: "action", name: "session.interrupt" }]);
    h.r.action("x.y", "click");
    h.r.flush();
    const first = h.sent[0].body.catalog ?? [];
    expect(first).toContainEqual({ kind: "view", name: "session" });
    expect(first).toContainEqual({ kind: "action", name: "session.interrupt" });
    await Promise.resolve();
    // Re-registering the same action changes nothing.
    h.r.addCatalog([{ kind: "action", name: "session.interrupt" }]);
    h.r.action("x.y", "click");
    h.r.flush();
    expect(h.sent[1].body.catalog).toBeUndefined();
    // A new action (or a newly known shortcut) resends it, even without events.
    h.r.addCatalog([{ kind: "action", name: "session.interrupt", shortcut: "Alt+P" }]);
    h.fireTimers();
    expect(h.sent[2].body.events).toEqual([]);
    expect(h.sent[2].body.catalog).toContainEqual({ kind: "action", name: "session.interrupt", shortcut: "Alt+P" });
  });

  it("remembers catalog entries across visits of the same build", () => {
    let stored: unknown = null;
    const h = harness({ saveCatalog: (e) => (stored = e) });
    h.r.addCatalog([{ kind: "action", name: "task.edit" }]);
    const again = harness({ loadCatalog: () => stored as never });
    again.r.action("x.y", "click");
    again.r.flush();
    expect(again.sent[0].body.catalog).toContainEqual({ kind: "action", name: "task.edit" });
  });
});

describe("dwell accounting", () => {
  it("emits a view when it is left, with visible time and the previous view", () => {
    const h = harness();
    h.r.route("home", "/");
    h.advance(3000);
    h.r.route("session", "/p/x/s/a");
    h.advance(5000);
    h.r.route("backlog", "/p/x/backlog");
    expect(views(h.events())).toEqual([
      { name: "home", ms: 3000, from: undefined },
      { name: "session", ms: 5000, from: "home" },
    ]);
  });

  it("pauses the clock while the tab is hidden", () => {
    const h = harness();
    h.r.route("session", "/p/x/s/a");
    h.advance(2000);
    h.r.setHidden(true);
    h.advance(60_000);
    h.r.setHidden(false);
    h.advance(1000);
    h.r.route("home", "/");
    expect(views(h.events())).toEqual([{ name: "session", ms: 3000, from: undefined }]);
  });

  it("does not count a page that loads hidden until it is shown", () => {
    const h = harness({ hidden: true });
    h.r.route("home", "/");
    h.advance(10_000);
    h.r.setHidden(false);
    h.advance(500);
    h.r.route("usage", "/usage");
    expect(views(h.events())).toEqual([{ name: "home", ms: 500, from: undefined }]);
  });

  it("counts a new entity on the same surface as a new view", () => {
    const h = harness();
    h.r.route("session", "/p/x/s/a");
    h.advance(1000);
    h.r.route("session", "/p/x/s/a"); // same path: no-op
    h.advance(1000);
    h.r.route("session", "/p/x/s/b");
    h.advance(1000);
    h.r.route("home", "/");
    expect(views(h.events())).toEqual([
      { name: "session", ms: 2000, from: undefined },
      { name: "session", ms: 1000, from: "session" },
    ]);
  });

  it("skips redirects (routes left at once) and keeps the real origin", () => {
    const h = harness();
    h.r.route("home", "/");
    h.advance(4000);
    h.r.route("backlog", "/backlog");
    h.advance(10);
    h.r.route("backlog", "/p/x/backlog");
    h.advance(2000);
    h.r.route("task", "/p/x/backlog/0001");
    expect(views(h.events())).toEqual([
      { name: "home", ms: 4000, from: undefined },
      { name: "backlog", ms: 2000, from: "home" },
    ]);
  });

  it("stacks modal surfaces: the view below pauses and resumes", () => {
    const h = harness();
    h.r.route("session", "/p/x/s/a");
    h.advance(1000);
    const closePalette = h.r.openView("palette");
    expect(h.r.currentView()).toBe("palette");
    h.advance(400);
    closePalette();
    closePalette(); // idempotent
    expect(h.r.currentView()).toBe("session");
    h.advance(1000);
    h.r.route("backlog", "/p/x/backlog");
    expect(views(h.events())).toEqual([
      { name: "palette", ms: 400, from: "session" },
      { name: "session", ms: 2000, from: undefined },
    ]);
  });

  it("attributes actions and errors to the view on top", () => {
    const h = harness();
    h.r.route("backlog", "/p/x/backlog");
    const close = h.r.openView("quick_capture");
    h.r.action("quick_capture.abort", "click");
    close();
    h.r.error("backlog.update", new ConnectError("x", Code.Unavailable));
    const [a, e] = h.events().filter((x) => x.kind !== "view");
    expect(a).toMatchObject({ kind: "action", view: "quick_capture", via: "click" });
    expect(e).toMatchObject({ kind: "error", name: "backlog.update", view: "backlog", attrs: { code: "unavailable" } });
  });

  it("ends every view on suspend and reopens them on resume", () => {
    const h = harness();
    h.r.route("session", "/p/x/s/a");
    h.advance(1000);
    h.r.openView("help");
    h.advance(200);
    h.r.suspend();
    expect(views(h.events())).toEqual([
      { name: "help", ms: 200, from: "session" },
      { name: "session", ms: 1000, from: undefined },
    ]);
    h.r.resume();
    expect(h.r.currentView()).toBe("help");
    h.advance(300);
    h.r.suspend();
    expect(views(h.events()).slice(2)).toEqual([
      { name: "help", ms: 300, from: undefined },
      { name: "session", ms: 0, from: undefined },
    ]);
  });
});

describe("flows", () => {
  it("records open then submit, or open then cancel", () => {
    const h = harness();
    const close = h.r.openFlow("new_task");
    h.r.submitFlow("new_task");
    close();
    const cancel = h.r.openFlow("quick_capture");
    cancel();
    expect(h.events().map((e) => e.name)).toEqual(["new_task.open", "new_task.submit", "quick_capture.open", "quick_capture.cancel"]);
  });
});

describe("via attribution for registry actions", () => {
  const key = (over: Partial<KeyLike>) => ({
    code: "",
    key: "",
    altKey: false,
    shiftKey: false,
    ctrlKey: false,
    metaKey: false,
    defaultPrevented: false,
    target: null,
    preventDefault: vi.fn(),
    ...over,
  });

  it("records shortcut, palette, and click invocations, and catalogs registrations", () => {
    const h = harness();
    setRecorderForTest(h.r);
    const run = vi.fn();
    const action: AppAction = { id: "test.thing:my-project:0042", title: "Do it", shortcut: { key: "j", mod: true, label: "Ctrl+J" }, run };
    const off = registerAction(action);
    try {
      expect(dispatchShortcut(key({ key: "j", ctrlKey: true }), false)).toBe(true);
      invokeAction(listActions().find((a) => a === action)!, "palette");
      expect(runAction(action.id)).toBe(true);
      expect(run).toHaveBeenCalledTimes(3);
      // Not while a dialog is open (the shortcut does not opt in).
      expect(dispatchShortcut(key({ key: "j", ctrlKey: true }), true)).toBe(false);
    } finally {
      off();
    }
    const acts = h.events().filter((e) => e.kind === "action");
    expect(acts.map((e) => [e.name, e.via])).toEqual([
      ["test.thing", "shortcut"],
      ["test.thing", "palette"],
      ["test.thing", "click"],
    ]);
    h.r.flush();
    expect(h.sent[0].body.catalog).toContainEqual({ kind: "action", name: "test.thing", shortcut: "Ctrl+J" });
  });

  it("is a no-op when analytics is not installed", () => {
    expect(() => track.action("x.y", "click")).not.toThrow();
    expect(track.view()).toBe("");
  });
});
