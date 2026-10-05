// Syntax highlighting (YccKit port), unified-diff parsing, and the Edit line diff.
import { describe, expect, it } from "vitest";
import { highlightLines, highlightText, languageForFence, languageForPath } from "../src/features/code/highlight";
import { diffStats, lineDiff, looksLikeDiff, opStats, parseDiff, unifiedFromOps } from "../src/features/code/diff";

const joined = (spans: { text: string }[]) => spans.map((s) => s.text).join("");

describe("highlighter", () => {
  it("reproduces every line exactly", () => {
    const src = 'package main\n\n// c <b>\nfunc main() { x := `raw\nstill` ; y := 3.14 }\n/* a\n b */ var z = "q\\"s"';
    const lines = src.split("\n");
    const out = highlightLines(lines, "go");
    expect(out.map(joined)).toEqual(lines);
  });

  it("classifies keywords, strings, comments, numbers, types", () => {
    const [line] = highlightLines(['func f(s string) error { return nil } // done 42'], "go");
    const kinds = Object.fromEntries(line.filter((s) => s.kind).map((s) => [s.text.trim(), s.kind]));
    expect(kinds.func).toBe("keyword");
    expect(kinds.string).toBe("type");
    expect(kinds.error).toBe("type");
    expect(kinds.nil).toBe("literal");
    expect(kinds["// done 42"]).toBe("comment");
  });

  it("carries block comments and multi-line strings across lines", () => {
    const out = highlightLines(["/* start", "middle", "end */ x"], "javascript");
    expect(out[1]).toEqual([{ text: "middle", kind: "comment" }]);
    expect(out[2][0]).toEqual({ text: "end */", kind: "comment" });
    const py = highlightLines(['s = """a', 'b"""'], "python");
    expect(py[1][0]).toEqual({ text: 'b"""', kind: "string" });
  });

  it("only treats # as a comment after whitespace in shell", () => {
    const [line] = highlightLines(["echo ${#x} # note"], "shell");
    expect(line.find((s) => s.kind === "comment")?.text).toBe("# note");
  });

  it("plain text without a language", () => {
    expect(highlightText("a\nb", null)).toEqual([[{ text: "a" }], [{ text: "b" }]]);
  });

  it("maps fences and paths", () => {
    expect(languageForFence("ts")).toBe("javascript");
    expect(languageForFence("golang")).toBe("go");
    expect(languageForFence("bash title=x")).toBe("shell");
    expect(languageForFence("console")).toBe("shell");
    expect(languageForFence("")).toBeNull();
    expect(languageForFence("brainfuck")).toBeNull();
    expect(languageForPath("internal/web/web.go")).toBe("go");
    expect(languageForPath("Makefile")).toBe("shell");
    expect(languageForPath("README")).toBeNull();
  });
});

describe("diff parsing", () => {
  const diff = [
    "commit abc",
    "Author: x",
    "",
    "diff --git a/a/b.go b/a/b.go",
    "index 1..2 100644",
    "--- a/a/b.go",
    "+++ b/a/b.go",
    "@@ -1,2 +1,2 @@",
    " ctx",
    "-old",
    "+new",
    "diff --git a/c.py b/c.py",
    "+++ b/c.py",
    "+x = 1",
    "",
  ].join("\r\n");

  it("classifies lines and tracks the file language", () => {
    const lines = parseDiff(diff);
    expect(lines.map((l) => l.kind)).toEqual([
      "fileHeader",
      "fileHeader",
      "context",
      "fileHeader",
      "fileHeader",
      "fileHeader",
      "fileHeader",
      "hunkHeader",
      "context",
      "deletion",
      "addition",
      "fileHeader",
      "fileHeader",
      "addition",
    ]);
    expect(lines[9]).toMatchObject({ text: "-old", language: "go", path: "a/b.go" });
    expect(lines[13]).toMatchObject({ language: "python", path: "c.py" });
    expect(diffStats(lines)).toEqual({ additions: 2, deletions: 1, files: 2 });
  });

  it("appends a truncation notice", () => {
    expect(parseDiff("+a\n", true).at(-1)?.kind).toBe("truncationNotice");
    const capped = parseDiff("+a\n+b\n+c\n", false, 2);
    expect(capped).toHaveLength(3);
    expect(capped[2].kind).toBe("truncationNotice");
  });

  it("detects diff-shaped output", () => {
    expect(looksLikeDiff("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n")).toBe(true);
    expect(looksLikeDiff("--- a/x\n+++ b/x\n@@ -1 +1 @@\n")).toBe(true);
    expect(looksLikeDiff("total 3\n-rw-r--r-- x\n")).toBe(false);
  });
});

describe("edit line diff", () => {
  it("diffs minimally and renders unified hunks", () => {
    const before = "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n";
    const after = "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\n";
    const ops = lineDiff(before, after);
    expect(opStats(ops)).toEqual({ additions: 2, deletions: 1 });
    expect(unifiedFromOps(ops, "x.txt", 1)).toBe(
      ["--- a/x.txt", "+++ b/x.txt", "@@ -1,3 +1,3 @@", " a", "-b", "+B", " c", "@@ -10,1 +10,2 @@", " j", "+k"].join("\n"),
    );
  });

  it("handles pure insertions and deletions", () => {
    expect(opStats(lineDiff("", "x\ny"))).toEqual({ additions: 2, deletions: 0 });
    expect(opStats(lineDiff("x\ny", ""))).toEqual({ additions: 0, deletions: 2 });
    expect(unifiedFromOps(lineDiff("same", "same"))).toBe("");
  });
});
