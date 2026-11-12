// @vitest-environment jsdom
// HTML files render in a sandboxed frame that loads nothing and never navigates itself away:
// in-page anchors stay in the frame, every other link opens a new tab without an opener.
import { describe, expect, it } from "vitest";
import { htmlFrameDoc } from "../src/features/files/model";

describe("htmlFrameDoc", () => {
  it("keeps anchors in-page, sends other links to a new tab, keeps the doctype", () => {
    const out = htmlFrameDoc('<!doctype html><p><a href="#end">a</a> <a href="https://x.test/">b</a> <a href="other.html">c</a></p>');
    const doc = new DOMParser().parseFromString(out, "text/html");
    const [anchor, ext, rel] = Array.from(doc.querySelectorAll("a"));
    expect(out.startsWith("<!doctype html>")).toBe(true);
    // The policy is the head's first element, ahead of anything that could load.
    expect(doc.head.firstElementChild?.getAttribute("http-equiv")).toBe("Content-Security-Policy");
    expect(anchor.getAttribute("href")).toBe("about:srcdoc#end");
    expect(anchor.hasAttribute("target")).toBe(false);
    for (const a of [ext, rel]) {
      expect(a.getAttribute("target")).toBe("_blank");
      expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    }
  });
});
