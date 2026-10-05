// Markdown for model turns and reports: GFM (tables, task lists,
// strikethrough, autolinks) lexed by `marked` and rendered token-by-token to
// React elements. Nothing is ever inserted as HTML: raw HTML in the source
// renders as its literal text, links are limited to safe schemes (anything
// else is inert text), images are never fetched, and code is highlighted
// over escaped text spans.
import { Fragment, memo, useMemo, type ReactNode } from "react";
import { Lexer, type Token, type Tokens } from "marked";
import { CodeBlock, DiffView } from "../code/CodeBlock";
import { languageForFence } from "../code/highlight";
import { FileRef, useFileLinks } from "../files/FileRef";
import { fromCodeSpan, type FileLinkContext } from "../files/fileReference";
import { classifyLink, decodeEntities } from "./links";

interface Ctx {
  links: FileLinkContext | undefined;
}

/** Lex markdown into marked tokens (GFM, single newlines are line breaks). */
export function lexMarkdown(text: string): Token[] {
  const lexer = new Lexer({ gfm: true, breaks: true, async: false });
  return lexer.lex(text);
}

export const Markdown = memo(function Markdown({ text, className = "" }: { text: string; className?: string }) {
  const fileLinks = useFileLinks();
  const linkContext = fileLinks?.context;
  const rendered = useMemo(() => {
    try {
      return renderMarkdown(text, { links: linkContext });
    } catch {
      // The lexer should never throw; if it does, show the source verbatim.
      return <div className="text">{text}</div>;
    }
  }, [text, linkContext]);
  return <div className={`md ${className}`.trim()}>{rendered}</div>;
});

/** Render markdown source to React nodes (exported for tests). */
export function renderMarkdown(text: string, ctx: Ctx = { links: undefined }): ReactNode {
  return blocks(lexMarkdown(text), ctx);
}

function blocks(tokens: readonly Token[], ctx: Ctx): ReactNode[] {
  return tokens.map((t, i) => block(t, ctx, i));
}

function block(token: Token, ctx: Ctx, key: number): ReactNode {
  switch (token.type) {
    case "space":
    case "def":
      return null;
    case "paragraph":
      return <p key={key}>{inlines((token as Tokens.Paragraph).tokens, ctx)}</p>;
    case "heading": {
      const t = token as Tokens.Heading;
      const level = Math.min(Math.max(t.depth, 1), 6);
      return (
        <div key={key} className={`md-h md-h${level}`} role="heading" aria-level={level}>
          {inlines(t.tokens, ctx)}
        </div>
      );
    }
    case "code": {
      const t = token as Tokens.Code;
      const lang = (t.lang ?? "").trim().split(/\s+/)[0] ?? "";
      if (/^(diff|patch|udiff)$/i.test(lang)) {
        return (
          <div key={key} className="md-diff">
            <DiffView diff={t.text} showStats={false} copyLabel="Copy diff" />
          </div>
        );
      }
      return <CodeBlock key={key} code={t.text} language={languageForFence(lang)} label={lang} />;
    }
    case "blockquote":
      return <blockquote key={key}>{blocks((token as Tokens.Blockquote).tokens, ctx)}</blockquote>;
    case "hr":
      return <hr key={key} />;
    case "list": {
      const t = token as Tokens.List;
      const items = t.items.map((item, i) => listItem(item, t.loose, ctx, i));
      if (t.ordered) {
        const start = typeof t.start === "number" ? t.start : 1;
        return (
          <ol key={key} start={start === 1 ? undefined : start}>
            {items}
          </ol>
        );
      }
      return <ul key={key}>{items}</ul>;
    }
    case "table":
      return table(token as Tokens.Table, ctx, key);
    case "html":
      // Raw HTML is shown as the literal source text, never interpreted.
      return (
        <p key={key} className="md-html">
          {(token as Tokens.HTML).text.replace(/\n+$/, "")}
        </p>
      );
    case "text": {
      const t = token as Tokens.Text;
      return <p key={key}>{t.tokens ? inlines(t.tokens, ctx) : decodeEntities(t.text)}</p>;
    }
    default:
      return token.raw ? (
        <p key={key} className="md-unknown">
          {token.raw}
        </p>
      ) : null;
  }
}

function listItem(item: Tokens.ListItem, loose: boolean, ctx: Ctx, key: number): ReactNode {
  const children: ReactNode[] = [];
  item.tokens.forEach((t, i) => {
    if (t.type === "checkbox") return;
    // Tight list items hold bare text tokens: keep them inline, not <p>.
    if (t.type === "text" && !loose) {
      const tt = t as Tokens.Text;
      children.push(<Fragment key={i}>{tt.tokens ? inlines(tt.tokens, ctx) : decodeEntities(tt.text)}</Fragment>);
    } else {
      children.push(block(t, ctx, i));
    }
  });
  if (item.task) {
    return (
      <li key={key} className="md-task">
        <input type="checkbox" checked={item.checked === true} disabled readOnly aria-label={item.checked ? "done" : "not done"} />{" "}
        {children}
      </li>
    );
  }
  return <li key={key}>{children}</li>;
}

function table(t: Tokens.Table, ctx: Ctx, key: number): ReactNode {
  const alignClass = (a: Tokens.TableCell["align"]) => (a ? `align-${a}` : undefined);
  return (
    <div key={key} className="md-table-wrap">
      <table className="md-table">
        <thead>
          <tr>
            {t.header.map((cell, i) => (
              <th key={i} className={alignClass(cell.align)}>
                {inlines(cell.tokens, ctx)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {t.rows.map((row, r) => (
            <tr key={r}>
              {row.map((cell, i) => (
                <td key={i} className={alignClass(cell.align)}>
                  {inlines(cell.tokens, ctx)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function inlines(tokens: readonly Token[] | undefined, ctx: Ctx): ReactNode[] {
  return (tokens ?? []).map((t, i) => inline(t, ctx, i));
}

function inline(token: Token, ctx: Ctx, key: number): ReactNode {
  switch (token.type) {
    case "text": {
      const t = token as Tokens.Text;
      if (t.tokens?.length) return <Fragment key={key}>{inlines(t.tokens, ctx)}</Fragment>;
      return <Fragment key={key}>{decodeEntities(t.text)}</Fragment>;
    }
    case "escape":
      return <Fragment key={key}>{(token as Tokens.Escape).text}</Fragment>;
    case "strong":
      return <strong key={key}>{inlines((token as Tokens.Strong).tokens, ctx)}</strong>;
    case "em":
      return <em key={key}>{inlines((token as Tokens.Em).tokens, ctx)}</em>;
    case "del":
      return <del key={key}>{inlines((token as Tokens.Del).tokens, ctx)}</del>;
    case "br":
      return <br key={key} />;
    case "codespan": {
      const text = (token as Tokens.Codespan).text;
      const ref = fromCodeSpan(text, ctx.links);
      if (ref) {
        return (
          <FileRef key={key} path={text} reference={ref}>
            {text}
          </FileRef>
        );
      }
      return <code key={key}>{text}</code>;
    }
    case "link": {
      const t = token as Tokens.Link;
      const label = t.tokens?.length ? inlines(t.tokens, ctx) : decodeEntities(t.text);
      const target = classifyLink(t.href, ctx.links);
      if (target.kind === "external") {
        return (
          <a key={key} href={target.href} target="_blank" rel="noopener noreferrer nofollow" title={target.href}>
            {label}
          </a>
        );
      }
      if (target.kind === "file") {
        return (
          <FileRef key={key} path={decodeEntities(t.href)} reference={target.ref} code={false}>
            {label}
          </FileRef>
        );
      }
      return (
        <span key={key} className="md-link-inert" title="Link not opened: unsupported or unsafe address">
          {label}
        </span>
      );
    }
    case "image": {
      // Images are never fetched (privacy, and the CSP blocks remote images):
      // show the alt text, linking to a safe URL.
      const t = token as Tokens.Image;
      const alt = decodeEntities(t.text) || "image";
      const target = classifyLink(t.href, ctx.links);
      if (target.kind === "external") {
        return (
          <a key={key} className="md-image" href={target.href} target="_blank" rel="noopener noreferrer nofollow">
            Image: {alt}
          </a>
        );
      }
      return (
        <span key={key} className="md-image">
          Image: {alt}
        </span>
      );
    }
    case "html":
      // Inline HTML shows as its literal text.
      return <Fragment key={key}>{(token as Tokens.HTML).text}</Fragment>;
    case "checkbox":
      return null;
    default:
      return <Fragment key={key}>{token.raw}</Fragment>;
  }
}
