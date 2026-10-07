// Concise per-tool previews: bash command + exit, file paths, edit hunk
// summaries, YccKit field choices for other tools.
import { describe, expect, it } from "vitest";
import { argSummary, bashOutcome, receiptPath, salvageArgs, toolPreview } from "../src/features/session/toolPreview";

const args = (o: unknown) => JSON.stringify(o);

describe("tool previews", () => {
  it("Bash: command and exit status", () => {
    expect(toolPreview("Bash", args({ command: "go test ./...\necho done" }), "ok\n", "ok")).toEqual({
      glyph: "$",
      summary: "go test ./... echo done",
      path: "",
      meta: [{ text: "exit 0", tone: "ok" }],
    });
    expect(toolPreview("Bash", args({ command: "false" }), "\n[exit: exit status 2]", "ok").meta).toEqual([
      { text: "exit 2", tone: "error" },
    ]);
    expect(toolPreview("Bash", args({ command: "sleep 9" }), "\n[command timed out after 2m0s]", "ok").meta[0]).toEqual({
      text: "timed out",
      tone: "error",
    });
    expect(toolPreview("Bash", args({ command: "x" }), "", "running").meta).toEqual([]);
    expect(bashOutcome("started background job j1: x\nIt runs…", "ok").background).toBe(true);
    expect(bashOutcome("\n[exit: signal: killed]", "ok").signal).toBe("killed");
  });

  it("Edit: path plus hunk summary", () => {
    const p = toolPreview("Edit", args({ file_path: "a/b.go", old_string: "x\ny\n", new_string: "x\nY\nZ\n" }), "ok", "ok");
    expect(p.path).toBe("a/b.go");
    expect(p.summary).toBe("");
    expect(p.meta).toEqual([
      { text: "+2", tone: "add" },
      { text: "−1", tone: "del" },
    ]);
  });

  it("Read and Write: path plus extent", () => {
    const read = toolPreview("Read", args({ file_path: "/w/x.ts", offset: 10, limit: 5 }), "    10\ta\n    11\tb\n", "ok");
    expect(read.path).toBe("/w/x.ts");
    expect(read.meta.map((m) => m.text)).toEqual(["lines 10–14", "2 lines"]);
    expect(toolPreview("Write", args({ file_path: "n.md", content: "a\nb\nc\n" }), "", "ok").meta[0].text).toBe("3 lines");
  });

  it("failed calls are marked", () => {
    expect(toolPreview("Read", args({ file_path: "x" }), "no such file", "error").meta).toEqual([{ text: "failed", tone: "error" }]);
  });

  it("other tools follow YccKit's preview fields", () => {
    expect(argSummary("web_search", args({ query: "react 19", n: 3 }))).toBe("react 19");
    expect(argSummary("list_backlog", args({ status: "open" }))).toBe("");
    expect(argSummary("mystery", args({ z: 1, a: { nested: true }, b: "first string" }))).toBe("first string");
    expect(argSummary("Bash", "not json")).toBe("not json");
    // Abbreviated (truncated) args from an indexed page still preview.
    expect(argSummary("Bash", '{"command":"echo hi')).toBe("echo hi…");
    expect(argSummary("Bash", '{"command":"echo \\"hi\\" && ls","timeout_s":30,"x":"cut…')).toBe('echo "hi" && ls');
  });

  it("abbreviated args: salvage fields, never show raw JSON", () => {
    expect(salvageArgs('{"command":"go test ./...","timeout_s":120,"bg":true,"note":"long\\nte…')).toEqual({
      command: "go test ./...",
      timeout_s: "120",
      bg: "true",
      note: "long\nte…",
    });
    expect(salvageArgs("not json")).toBeNull();
    // Write args sort content before file_path, so the cut loses the path: the
    // mutation receipt still names the file and its line count.
    const cut = '{"content":"# Report\\n\\nlots of text…';
    const receipt = "created docs/report.md\noperation: create\nbefore: absent\nafter: 8960 bytes, 125 lines\nrevision (full-content sha256): ab";
    const w = toolPreview("Write", cut, receipt, "ok");
    expect(w.path).toBe("docs/report.md");
    expect(w.summary).toBe("");
    expect(w.meta).toEqual([{ text: "125 lines", tone: "muted" }]);
    expect(receiptPath("edited a b.go\noperation: edit")).toBe("a b.go");
    // A cut-off path is not offered as a link.
    expect(toolPreview("Read", '{"file_path":"/very/long/pa…', "", "running").path).toBe("");
  });
});
