// Markdown renders inertly: raw HTML shows as text (markdown files: through a
// GitHub-style allowlist), unsafe link schemes never
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

// Markdown files render raw HTML like GitHub: an allowlist of elements and
// attributes, built as React elements (features/markdown/html.tsx).
describe("raw HTML in markdown files", () => {
  const file = (md: string) => renderToStaticMarkup(<Markdown text={md} html />);

  it("drops script-like elements with their content, and every event handler, class, style and id", () => {
    const out = file(
      '<div onclick="x()" class="c" style="color:red" id="i">kept</div>\n\n<script>alert(1)</script>\n\n<style>body{}</style>\n\n<iframe src="https://evil"></iframe>\n\nText <img src=x onerror=alert(2)> <svg onload=alert(3)><circle/></svg>',
    );
    expect(out).toContain("<div>kept</div>");
    expect(out).not.toMatch(/onclick|onerror|onload|class="c"|color:red|id="i"/);
    expect(out).not.toMatch(/<script|alert\(1\)|<style|body\{\}|<iframe|<svg/);
    expect(out).not.toMatch(/<img/i); // no project context: never fetched
  });

  it("strips unknown tags but keeps their text, and hides comments", () => {
    const out = file("<custom-el>inner</custom-el> <!-- secret --> after\n\n<!-- block -->");
    expect(out).toContain("inner");
    expect(out).toContain("after");
    expect(out).not.toMatch(/custom-el|secret|block/);
  });

  it("gives HTML links the markdown link rules", () => {
    const out = file('<a href="javascript:alert(1)">bad</a> <a href="https://example.com" onclick="x()">good</a>');
    expect(out).not.toMatch(/javascript:|onclick/);
    expect(out).toContain('<span class="md-link-inert" title="Link not opened: unsupported or unsafe address">bad</span>');
    expect(out).toContain('href="https://example.com/"');
    expect(out).toContain('rel="noopener noreferrer nofollow"');
  });

  it("builds tables, alignment and inline formatting like GitHub", () => {
    const out = file(
      '<p align="center">centred</p>\n\n<table>\n<tr>\n<td width="33%" valign="top" colspan="2"><b>Bold.</b> text &amp; more<br>next</td>\n</tr>\n</table>\n\nPress <kbd>Ctrl</kbd>+<b>x</b> now',
    );
    expect(out).toContain('<p style="text-align:center">centred</p>');
    expect(out).toContain('<div class="md-table-wrap"><table class="md-table"><tbody><tr><td colSpan="2" style="vertical-align:top;width:33%"><b>Bold.</b> text &amp; more<br/>next</td></tr></tbody></table></div>');
    expect(out).toContain("Press <kbd>Ctrl</kbd>+<b>x</b> now");
  });

  it("wraps markdown between HTML blocks, closing what the source leaves open", () => {
    const out = file("<details open>\n<summary>More</summary>\n\nSome **markdown** inside.\n\n</details>\n\n<div><i>unclosed\n\n</span>stray close");
    expect(out).toContain('<details open="">\n<summary>More</summary><p>Some <strong>markdown</strong> inside.</p></details>');
    expect(out).toContain("<div><i>unclosed<p>stray close</p></i></div>");
  });

  it("keeps HTML literal unless asked (model and user text)", () => {
    expect(html("<b>x</b>")).toContain("&lt;b&gt;x&lt;/b&gt;");
  });

  it("implies the end tags the HTML parser would", () => {
    expect(file("<table><tr><td>one<td>two<tr><td>three</table>")).toContain(
      "<tbody><tr><td>one</td><td>two</td></tr><tr><td>three</td></tr></tbody>",
    );
    expect(file("<ul><li>one<li>two<ul><li>inner<li>inner2</ul></ul>")).toContain(
      "<ul><li>one</li><li>two<ul><li>inner</li><li>inner2</li></ul></li></ul>",
    );
    expect(file("<div><p>one<p>two<div>three</div></div>")).toContain("<div><p>one</p><p>two</p><div>three</div></div>");
  });

  it("never nests links", () => {
    const out = file('<a href="https://a.test">[b](https://b.test) <a href="https://c.test">c</a></a>');
    expect(out.match(/<a /g)).toHaveLength(1);
    expect(out).toContain('href="https://a.test/"');
  });

  it("keeps a malformed tag and its unterminated attribute as text", () => {
    const out = file('<div><span title="unfinished <b>oops</b></div>');
    expect(out).not.toContain("<b>");
    expect(out).toContain("oops");
  });

  it("decodes HTML attribute entities once", () => {
    expect(file('<a href="https://example.com/?q=&amp;copy;">q</a>')).toContain('href="https://example.com/?q=&amp;copy;"');
  });
});
