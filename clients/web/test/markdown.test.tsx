// Markdown renders inertly: raw HTML shows as text, unsafe link schemes never
// become links, images are never fetched, code is highlighted over escaped
// spans, and nothing in the client injects HTML.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown } from "../src/features/markdown/Markdown";
import { classifyLink, decodeEntities, safeExternalHref } from "../src/features/markdown/links";

const html = (md: string) => renderToStaticMarkup(<Markdown text={md} />);

describe("markdown safety", () => {
  it("renders <script> and event-handler HTML as literal text", () => {
    const out = html("Hello <script>alert(1)</script> and <img src=x onerror=alert(2)>\n\n<script>alert(3)</script>\n\n<div onclick=\"x()\">block</div>");
    expect(out).not.toMatch(/<script/i);
    expect(out).not.toMatch(/<img/i);
    expect(out).not.toMatch(/<div onclick/i);
    expect(out).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
    expect(out).toContain("&lt;img src=x onerror=alert(2)&gt;");
    expect(out).toContain("&lt;script&gt;alert(3)&lt;/script&gt;");
  });

  it("never links javascript:, data:, vbscript: or file: destinations", () => {
    const sources = [
      "[a](javascript:alert(1))",
      "[a](JaVaScRiPt:alert(1))",
      "[a](  javascript:alert(1)  )",
      "<javascript:alert(1)>",
      "[a](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)",
      "[a](vbscript:msgbox(1))",
      "[a](file:///etc/passwd)",
      "[a][r]\n\n[r]: javascript:alert(1)",
      "[a](&#106;avascript:alert(1))",
      "![img](javascript:alert(1))",
    ];
    for (const src of sources) {
      const out = html(src);
      expect(out, src).not.toMatch(/href=/i);
      expect(out, src).not.toMatch(/<img/i);
      expect(out, src).not.toMatch(/<a /i);
    }
  });

  it("links http(s) and mailto in a new tab without referrer", () => {
    const out = html("[docs](https://example.com/a?b=1) and https://auto.example and [m](mailto:a@b.c)");
    expect(out).toContain('href="https://example.com/a?b=1"');
    expect(out).toContain('href="https://auto.example/"');
    expect(out).toContain('href="mailto:a@b.c"');
    expect(out).toContain('rel="noopener noreferrer nofollow"');
    expect(out).toContain('target="_blank"');
  });

  it("never fetches images", () => {
    const out = html("![chart](https://example.com/chart.png)");
    expect(out).not.toMatch(/<img/i);
    expect(out).toContain("chart");
  });

  it("decodes entities to text, which React escapes again", () => {
    expect(html("&lt;b&gt;bold?&lt;/b&gt; &amp; &copy;")).toContain("&lt;b&gt;bold?&lt;/b&gt; &amp; ©");
    expect(decodeEntities("&#60;&#x3e;&bogus;")).toBe("<>&bogus;");
  });

  it("renders GFM tables, task lists, strikethrough and headings", () => {
    const out = html("# Title\n\n| a | b |\n|---|:-:|\n| 1 | **2** |\n\n- [x] done\n- [ ] todo\n\n~~old~~");
    expect(out).toContain('role="heading"');
    expect(out).toContain("<table");
    expect(out).toContain('<td class="align-center"><strong>2</strong></td>');
    expect(out).toContain('type="checkbox"');
    expect(out).toContain("disabled");
    expect(out).toContain("<del>old</del>");
  });

  it("highlights fenced code over escaped spans", () => {
    const out = html('```go\n// <b>note</b>\nfunc main() { s := "<script>" }\n```');
    expect(out).toContain('<span class="tok-keyword">func</span>');
    expect(out).toContain('<span class="tok-comment">// &lt;b&gt;note&lt;/b&gt;</span>');
    expect(out).toContain('<span class="tok-string">&quot;&lt;script&gt;&quot;</span>');
    expect(out).not.toMatch(/<script/i);
  });

  it("renders path-like code spans as copyable file references", () => {
    const out = html("See `internal/web/web.go:41` and `go test ./...`");
    expect(out).toContain('class="file-ref code"');
    expect(out).toContain("internal/web/web.go:41");
    expect(out).toContain("<code>go test ./...</code>");
  });
});

describe("links", () => {
  it("classifies destinations", () => {
    expect(classifyLink("https://x.y/z")).toEqual({ kind: "external", href: "https://x.y/z" });
    expect(classifyLink("javascript:alert(1)").kind).toBe("none");
    expect(classifyLink("java\tscript:alert(1)").kind).toBe("none");
    expect(classifyLink("#top").kind).toBe("none");
    expect(classifyLink("//evil.example/x").kind).toBe("none");
    expect(classifyLink("internal/a.go#L12")).toEqual({
      kind: "file",
      ref: { path: "internal/a.go", isDirectory: false, lines: { start: 12, end: 12 } },
    });
    expect(classifyLink("main.go:40").kind).toBe("file");
    expect(classifyLink("../../etc/passwd").kind).toBe("none");
    expect(safeExternalHref("data:text/html,x")).toBeNull();
  });
});

describe("no HTML injection anywhere in the client", () => {
  it("never uses dangerouslySetInnerHTML or innerHTML", () => {
    const root = join(__dirname, "../src");
    const files: string[] = [];
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) {
          if (name !== "gen") walk(p);
        } else if (/\.(ts|tsx)$/.test(name)) {
          files.push(p);
        }
      }
    };
    walk(root);
    for (const f of files) {
      const src = readFileSync(f, "utf8");
      expect(src, f).not.toMatch(/dangerouslySetInnerHTML|\.innerHTML\b|\.outerHTML\b|insertAdjacentHTML|document\.write/);
    }
  });
});
