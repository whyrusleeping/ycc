// File browser logic: route locations (path, session, line fragment) and
// their round trip through paths.files, the lazily loaded tree, line
// splitting/targets, go-to-line parsing, and display helpers. Mirrors
// YccKit's FileBrowsingTests where they overlap.
import { describe, expect, it } from "vitest";
import { paths } from "../src/app/paths";
import {
  ancestorDirs,
  basename,
  childPath,
  clampLines,
  crumbs,
  dirname,
  expandTo,
  flattenTree,
  formatSize,
  isMarkdownPath,
  parseFileLocation,
  parseGoToLine,
  splitLines,
  toggle,
  type TreeEntry,
} from "../src/features/files/model";
import { fromCodeSpan, fromLink, transcriptLinkContext } from "../src/features/files/fileReference";

describe("file locations", () => {
  it("parses the splat, the session query, and a line fragment", () => {
    expect(parseFileLocation("internal/a.go", "?session=s_1", "#L12-L20")).toEqual({
      path: "internal/a.go",
      session: "s_1",
      lines: { start: 12, end: 20 },
    });
    expect(parseFileLocation("", "", "")).toEqual({ path: "", session: "", lines: null });
    expect(parseFileLocation("docs/", "", "#L3").lines).toEqual({ start: 3, end: 3 });
    expect(parseFileLocation("a/./b//c.go", "", "").path).toBe("a/b/c.go");
    // Escapes fall back to the root; junk fragments target nothing.
    expect(parseFileLocation("../../etc/passwd", "", "").path).toBe("");
    expect(parseFileLocation("a.go", "", "#top").lines).toBeNull();
  });

  it("builds file routes that parse back to the same location", () => {
    const url = paths.files("my proj", "dir/a b.go", { session: "s_x", lines: { start: 4, end: 9 } });
    expect(url).toBe("/p/my%20proj/files/dir/a%20b.go?session=s_x#L4-L9");
    const u = new URL(url, "http://h");
    const splat = decodeURIComponent(u.pathname.replace(/^\/p\/[^/]+\/files\/?/, ""));
    expect(parseFileLocation(splat, u.search, u.hash)).toEqual({ path: "dir/a b.go", session: "s_x", lines: { start: 4, end: 9 } });
    expect(paths.files("p")).toBe("/p/p/files");
    expect(paths.files("p", "a.go", { lines: { start: 3, end: 3 } })).toBe("/p/p/files/a.go#L3");
    expect(paths.files(null)).toBe("/files");
    expect(paths.files(null, "", { session: "s_1" })).toBe("/files?session=s_1");
  });

  it("opens a transcript reference at its line, against the session", () => {
    // What a transcript click hands the viewer: root-relative path + lines.
    const ctx = transcriptLinkContext("alpha", "s_ws", ["/srv/alpha", "/srv/worktrees/ws1"]);
    expect(fromCodeSpan("notes/wsref.md:3", ctx)).toEqual({ path: "notes/wsref.md", isDirectory: false, lines: { start: 3, end: 3 } });
    // An absolute path inside the session's worktree is stripped to its root-relative form.
    expect(fromLink("/srv/worktrees/ws1/notes/wsref.md#L2-L3", ctx)).toEqual({
      path: "notes/wsref.md",
      isDirectory: false,
      lines: { start: 2, end: 3 },
    });
    // Relative links inside a markdown file resolve against its directory.
    expect(fromLink("../spec.md", { ...ctx, baseDirectory: "docs/design" })?.path).toBe("docs/spec.md");
  });

  it("splits paths", () => {
    expect(dirname("a/b/c.go")).toBe("a/b");
    expect(dirname("c.go")).toBe("");
    expect(basename("a/b/c.go")).toBe("c.go");
    expect(childPath("", "x")).toBe("x");
    expect(childPath("a", "x")).toBe("a/x");
    expect(ancestorDirs("a/b/c.go")).toEqual(["a", "a/b"]);
    expect(ancestorDirs("c.go")).toEqual([]);
    expect(crumbs("a/b", "proj")).toEqual([
      { label: "proj", path: "" },
      { label: "a", path: "a" },
      { label: "b", path: "a/b" },
    ]);
  });
});

describe("file tree", () => {
  const root: TreeEntry[] = [
    { name: "cmd", isDir: true },
    { name: "internal", isDir: true },
    { name: "go.mod", isDir: false },
    { name: "bin", isDir: true, ignored: true },
  ];
  const internal: TreeEntry[] = [
    { name: "web", isDir: true },
    { name: "a.go", isDir: false },
  ];

  it("flattens expanded, loaded directories depth-first", () => {
    const listings = new Map([
      ["", root],
      ["internal", internal],
    ]);
    const rows = flattenTree(listings, new Set(["internal", "internal/web"]));
    expect(rows.map((r) => `${"  ".repeat(r.depth)}${r.name}${r.expanded ? "*" : ""}${r.loading ? "…" : ""}`)).toEqual([
      "cmd",
      "internal*",
      "  web*…",
      "  a.go",
      "go.mod",
      "bin",
    ]);
    expect(rows.find((r) => r.name === "bin")?.ignored).toBe(true);
    expect(rows.find((r) => r.name === "a.go")?.path).toBe("internal/a.go");
  });

  it("shows nothing before the root listing arrives", () => {
    expect(flattenTree(new Map(), new Set(["a"]))).toEqual([]);
  });

  it("collapsed directories hide their loaded children", () => {
    const listings = new Map([
      ["", root],
      ["internal", internal],
    ]);
    expect(flattenTree(listings, new Set()).map((r) => r.name)).toEqual(["cmd", "internal", "go.mod", "bin"]);
  });

  it("expands to a path and toggles", () => {
    expect([...expandTo(new Set(), "internal/web/web.go", false)]).toEqual(["internal", "internal/web"]);
    expect([...expandTo(new Set(["cmd"]), "internal/web", true)].sort()).toEqual(["cmd", "internal", "internal/web"]);
    expect([...expandTo(new Set(), "", true)]).toEqual([]);
    expect([...toggle(new Set(["a"]), "a")]).toEqual([]);
    expect([...toggle(new Set(), "a")]).toEqual(["a"]);
  });
});

describe("file contents", () => {
  it("splits lines on LF and CRLF without a phantom last line", () => {
    expect(splitLines("")).toEqual([]);
    expect(splitLines("a\nb\n")).toEqual(["a", "b"]);
    expect(splitLines("a\r\nb")).toEqual(["a", "b"]);
    expect(splitLines("a\n\n")).toEqual(["a", ""]);
    expect(splitLines("\n")).toEqual([""]);
  });

  it("clamps a target range to the file", () => {
    expect(clampLines({ start: 3, end: 9 }, 5)).toEqual({ start: 3, end: 5 });
    expect(clampLines({ start: 6, end: 6 }, 5)).toBeNull();
    expect(clampLines(null, 5)).toBeNull();
    expect(clampLines({ start: 1, end: 1 }, 0)).toBeNull();
  });

  it("parses go-to-line input", () => {
    expect(parseGoToLine("12")).toEqual({ start: 12, end: 12 });
    expect(parseGoToLine(" L7 ")).toEqual({ start: 7, end: 7 });
    expect(parseGoToLine("12-20")).toEqual({ start: 12, end: 20 });
    expect(parseGoToLine("12:L20")).toEqual({ start: 12, end: 20 });
    expect(parseGoToLine("20-12")).toEqual({ start: 20, end: 20 });
    for (const bad of ["", "0", "x", "-3", "1.5"]) expect(parseGoToLine(bad), bad).toBeNull();
  });

  it("knows markdown and formats sizes", () => {
    expect(isMarkdownPath("docs/README.md")).toBe(true);
    expect(isMarkdownPath("a.MARKDOWN")).toBe(true);
    expect(isMarkdownPath("a.mdx")).toBe(true);
    expect(isMarkdownPath("a.go")).toBe(false);
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(1023)).toBe("1023 B");
    expect(formatSize(1536)).toBe("1.5 KB");
    expect(formatSize(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatSize(300 * 1024)).toBe("300 KB");
  });
});
