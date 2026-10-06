// Command palette: fuzzy matching and ranking, query prefixes, file paths,
// and the default (empty-query) list.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import { BacklogTaskSummarySchema, ProjectInfoSchema, SessionSummarySchema } from "../src/gen/ycc/v1/ycc_pb";
import { fuzzyItem, fuzzyWord, rank } from "../src/features/palette/fuzzy";
import { fileItem, paletteResults, parseQuery, type PaletteSources } from "../src/features/palette/items";
import type { AppAction } from "../src/app/actions";

describe("fuzzy matching", () => {
  it("matches characters in order, case-insensitively", () => {
    expect(fuzzyWord("nss", "New session")).not.toBeNull();
    expect(fuzzyWord("NEW", "new session")?.indices).toEqual([0, 1, 2]);
    expect(fuzzyWord("sn", "New session")).not.toBeNull();
    expect(fuzzyWord("xyz", "New session")).toBeNull();
    expect(fuzzyWord("toolong", "short")).toBeNull();
  });

  it("prefers word starts and consecutive runs", () => {
    const a = fuzzyWord("ns", "New session")!;
    const b = fuzzyWord("ns", "Unassigned")!;
    expect(a.score).toBeGreaterThan(b.score);
    expect(a.indices).toEqual([0, 4]);
    const run = fuzzyWord("sess", "New session")!;
    const scattered = fuzzyWord("sess", "Stop the session")!;
    expect(run.indices).toEqual([4, 5, 6, 7]);
    // The later run beats the scattered early letters.
    expect(scattered.indices).toEqual([9, 10, 11, 12]);
  });

  it("picks the best alignment, not the first", () => {
    // Greedy matching would take the "l" of "Toggle"; the word start is better.
    expect(fuzzyWord("tl", "Toggle list")!.indices).toEqual([0, 7]);
  });

  it("matches multi-word queries in any order, falling back to secondary text", () => {
    expect(fuzzyItem(["loop", "start"], "Start the work loop in gamma")).not.toBeNull();
    expect(fuzzyItem(["gamma"], "Start the work loop", "Work loop gamma")).not.toBeNull();
    expect(fuzzyItem(["beta"], "Start the work loop", "Work loop gamma")).toBeNull();
    // Secondary matches add score but are not highlighted.
    expect(fuzzyItem(["gamma"], "Start", "gamma")!.indices).toEqual([]);
  });

  it("ranks by score with input order breaking ties", () => {
    const items = ["Open settings", "Settings › Models", "Add a model…", "Session settings"];
    const r = rank(items, "set", (t) => ({ title: t }));
    expect(r[0].item).toBe("Settings › Models");
    expect(r.map((x) => x.item)).toContain("Session settings");
    expect(r.map((x) => x.item)).not.toContain("Add a model…");
    const tie = rank(["b", "a"], "", (t) => ({ title: t }));
    expect(tie.map((x) => x.item)).toEqual(["b", "a"]);
    const boosted = rank(["Model x", "Model y"], "model", (t) => ({ title: t, boost: t.endsWith("y") ? 5 : 0 }));
    expect(boosted[0].item).toBe("Model y");
  });
});

describe("palette items", () => {
  const ts = (m: number) => `2026-01-01T00:${String(m).padStart(2, "0")}:00.000Z`;
  const s = (id: string, minute: number, extra: Record<string, unknown> = {}) =>
    create(SessionSummarySchema, { sessionId: id, lastActivity: ts(minute), startedAt: ts(minute), status: "idle", title: `title ${id}`, ...extra });
  const action = (id: string, title: string, extra: Partial<AppAction> = {}): AppAction => ({ id, title, run: vi.fn(), ...extra });
  const src = (over: Partial<PaletteSources> = {}): PaletteSources => ({
    rows: [
      { session: s("s1", 1), project: "alpha" },
      { session: s("s2", 2, { title: "fix the login bug" }), project: "beta" },
      { session: s("s3", 3, { live: true, waitingInput: true, status: "running", title: "asks a question" }), project: "alpha" },
      { session: s("s4", 4, { title: "unread one" }), project: "alpha" },
    ],
    activeSessionId: null,
    isUnread: (row) => row.session.sessionId === "s4",
    relativeTime: () => "now",
    tasks: [{ project: "alpha", tasks: [create(BacklogTaskSummarySchema, { id: "0007", title: "Login rate limit", status: "todo" }), create(BacklogTaskSummarySchema, { id: "0001", title: "Login page", status: "done" })] }],
    projects: [create(ProjectInfoSchema, { name: "alpha", path: "/w/alpha" }), create(ProjectInfoSchema, { name: "beta", path: "/w/beta" })],
    actions: [action("session.new", "New session", { shortcut: { code: "KeyN", alt: true, shift: true } }), action("backlog.capture", "Quick capture a backlog item…"), action("palette.open", "Command palette")],
    target: "alpha",
    shortcutLabel: (a) => (a.shortcut ? "Alt+Shift+N" : undefined),
    ...over,
  });

  it("parses mode prefixes", () => {
    expect(parseQuery("> new")).toEqual({ mode: "actions", text: "new" });
    expect(parseQuery("#login")).toEqual({ mode: "tasks", text: "login" });
    expect(parseQuery("@ fix")).toEqual({ mode: "sessions", text: "fix" });
    expect(parseQuery(" plain ")).toEqual({ mode: "all", text: "plain" });
  });

  it("lists waiting and unread sessions, recent ones, then actions and pages by default", () => {
    const r = paletteResults(src(), "");
    const ids = r.map((x) => x.item.id);
    expect(ids[0]).toBe("session:alpha:s3"); // needs answer first
    expect(ids[1]).toBe("session:alpha:s4"); // then unread
    expect(r[0].item.marker).toBe("needs");
    expect(r[1].item.marker).toBe("unread");
    expect(ids).toContain("action:session.new");
    expect(ids).not.toContain("action:palette.open"); // the palette doesn't list itself
    expect(ids).toContain("page:backlog");
    expect(ids.some((i) => i.startsWith("task:"))).toBe(false); // tasks only when searched
  });

  it("finds sessions, tasks, projects, and actions by query", () => {
    const login = paletteResults(src(), "login").map((x) => x.item.id);
    expect(login).toContain("session:beta:s2");
    expect(login).toContain("task:alpha:0007");
    // Done tasks rank below open ones.
    expect(login.indexOf("task:alpha:0007")).toBeLessThan(login.indexOf("task:alpha:0001"));
    expect(paletteResults(src(), "beta").map((x) => x.item.id)).toContain("project:beta");
    const cap = paletteResults(src(), "capture");
    expect(cap[0].item.id).toBe("action:backlog.capture");
    expect(paletteResults(src(), "new ses")[0].item.shortcut).toBe("Alt+Shift+N");
    expect(paletteResults(src(), "#0007")[0].item.to).toBe("/p/alpha/backlog/0007");
    expect(paletteResults(src(), "> login")).toEqual([]);
    expect(paletteResults(src(), "settings models").map((x) => x.item.to)).toContain("/settings#models");
  });

  it("offers a path-like query as a file", () => {
    expect(fileItem("internal/web/web.go:41", "alpha")?.to).toBe("/p/alpha/files/internal/web/web.go#L41");
    expect(fileItem("README.md", null)?.to).toBe("/files/README.md");
    expect(fileItem("a.go:3-9", "alpha")?.to).toBe("/p/alpha/files/a.go#L3-L9");
    expect(fileItem("new session", "alpha")).toBeNull();
    expect(fileItem("capture", "alpha")).toBeNull();
    expect(paletteResults(src(), "spec.md")[0].item.kind).toBe("file");
  });
});
