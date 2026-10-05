// Copy buttons copy the exact source text: fenced code, diffs, tool commands
// and outputs. CopyButton is replaced by a probe that exposes its `text` prop
// so server rendering can show what a click would copy.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../src/ui/CopyButton", () => ({
  CopyButton: ({ text }: { text: string }) => <button data-copy={text} />,
}));

const { Markdown } = await import("../src/features/markdown/Markdown");
const { DiffView } = await import("../src/features/code/CodeBlock");
const { ToolBody } = await import("../src/features/session/ToolBody");
const { copyText } = await import("../src/ui/copy");

function copied(markup: string): string[] {
  const unescape = (s: string) =>
    s.replace(/&quot;/g, '"').replace(/&#x27;/g, "'").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
  return [...markup.matchAll(/data-copy="([^"]*)"/g)].map((m) => unescape(m[1]));
}

const code = '  indented\tline <script>alert("x") & \'q\'\n\ntrailing spaces   \n✓ unicode';

describe("copy buttons", () => {
  it("copy fenced code verbatim", () => {
    const out = renderToStaticMarkup(<Markdown text={"Intro\n\n```ts\n" + code + "\n```\n"} />);
    expect(copied(out)).toEqual([code]);
  });

  it("copy diffs verbatim (fenced and inspector)", () => {
    const diff = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old <b>\n+new\t& more";
    expect(copied(renderToStaticMarkup(<Markdown text={"```diff\n" + diff + "\n```"} />))).toEqual([diff]);
    expect(copied(renderToStaticMarkup(<DiffView diff={diff + "\n"} truncated />))).toEqual([diff + "\n"]);
  });

  it("copy a Bash command and its output verbatim", () => {
    const command = "grep -n 'a\"b' file.go | head -3";
    const output = "1:\ta\"b <tag>\n[exit: exit status 1]";
    const out = renderToStaticMarkup(
      <ToolBody name="Bash" args={JSON.stringify({ command })} output={output} status="ok" full={false} />,
    );
    expect(copied(out)).toEqual([command, output]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("copyText writes the text unchanged", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    expect(await copyText(code)).toBe(true);
    expect(writeText).toHaveBeenCalledWith(code);
  });
});
