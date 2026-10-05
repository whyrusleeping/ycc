import { describe, expect, it } from "vitest";
import { formatReference, fromCodeSpan, fromLink, transcriptLinkContext } from "../src/features/files/fileReference";

describe("file references (YccKit parity)", () => {
  it("recognizes path-like code spans conservatively", () => {
    expect(fromCodeSpan("internal/a.go")).toEqual({ path: "internal/a.go", isDirectory: false, lines: null });
    expect(fromCodeSpan("internal/a.go:12-20")?.lines).toEqual({ start: 12, end: 20 });
    expect(fromCodeSpan("a.go#L3")?.lines).toEqual({ start: 3, end: 3 });
    expect(fromCodeSpan("Makefile")?.path).toBe("Makefile");
    expect(fromCodeSpan("internal/")).toEqual({ path: "internal", isDirectory: true, lines: null });
    for (const no of ["go test ./...", "session.Manager", "--flag", "$HOME/x.go", "https://x.y/a.go", "../../x.go", "x.unknownext"]) {
      expect(fromCodeSpan(no), no).toBeNull();
    }
  });

  it("strips known absolute roots and refuses escapes", () => {
    const ctx = transcriptLinkContext("p", "s", ["/home/u/proj"]);
    expect(fromLink("/home/u/proj/cmd/main.go:7", ctx)).toEqual({ path: "cmd/main.go", isDirectory: false, lines: { start: 7, end: 7 } });
    expect(fromLink("/etc/passwd", ctx)).toBeNull();
    expect(fromLink("a/../../b", ctx)).toBeNull();
    expect(formatReference({ path: "a.go", isDirectory: false, lines: { start: 2, end: 5 } })).toBe("a.go:2-5");
  });
});
