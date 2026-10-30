// Raw HTML in markdown files, rendered like GitHub: the tags marked leaves as
// `html` tokens are tokenized here and built into a tree around the markdown
// rendered between them (a `<details>` block can wrap markdown paragraphs).
// Elements and attributes follow GitHub's sanitization allowlist: anything
// else is dropped — unknown tags keep their text, script-like elements lose
// their content, comments vanish — and no `class`, `style`, `id` or event
// handler survives. Output is React elements, never inserted HTML; links and
// images take the same rules as markdown ones (safe schemes; only project
// files are fetched, through the daemon).
import { createElement, Fragment, type CSSProperties, type ReactNode } from "react";
import { FileRef } from "../files/FileRef";
import type { FileLinkContext } from "../files/fileReference";
import { classifyLink, decodeEntities } from "./links";
import { MarkdownImage } from "./MarkdownImage";

/** Elements kept as themselves (GitHub's list, less legacy and form controls). */
const ALLOWED = new Set(
  (
    "h1 h2 h3 h4 h5 h6 br b i strong em a pre code img tt div ins del sup sub p ol ul table thead tbody " +
    "tfoot blockquote dl dt dd kbd q samp var hr ruby rt rp li tr td th s strike summary details caption " +
    "figure figcaption abbr bdo cite dfn mark small span time wbr input picture source center"
  ).split(" "),
);

/** Elements removed together with their content. */
const DROPPED = new Set(
  "script style title textarea template noscript noembed noframes iframe object embed frameset frame xmp plaintext select option math svg".split(" "),
);

const VOID = new Set("area base br col embed hr img input link meta param source track wbr".split(" "));

/** Children that must be elements only (text nodes are invalid DOM there). */
const NO_TEXT = new Set("table thead tbody tfoot tr ul ol dl picture".split(" "));

/** Attribute values as written (entities not yet decoded: see `attr`). */
type Attrs = Record<string, string>;

export type HtmlEvent =
  | { kind: "open"; tag: string; attrs: Attrs; selfClosing: boolean }
  | { kind: "close"; tag: string }
  | { kind: "text"; text: string };

// An open or close tag at the current position, per CommonMark's raw HTML grammar.
const TAG_RE = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)((?:\s+[^\s"'>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*(\/?)>/y;
const ATTR_RE = /([^\s"'>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g;

/**
 * Tokenize raw HTML into tag and text events, scanning left to right.
 * Comments vanish; a malformed tag is text through its closing `>` (outside
 * quotes), so nothing inside an unterminated attribute becomes a tag.
 */
export function htmlEvents(raw: string): HtmlEvent[] {
  const out: HtmlEvent[] = [];
  let text = "";
  const flush = () => {
    if (text) out.push({ kind: "text", text: decodeEntities(text) });
    text = "";
  };
  let i = 0;
  while (i < raw.length) {
    const lt = raw.indexOf("<", i);
    if (lt < 0) {
      text += raw.slice(i);
      break;
    }
    text += raw.slice(i, lt);
    if (raw.startsWith("<!--", lt)) {
      const end = raw.indexOf("-->", lt + 4);
      i = end < 0 ? raw.length : end + 3;
      continue;
    }
    TAG_RE.lastIndex = lt;
    const m = TAG_RE.exec(raw);
    if (!m) {
      const end = /[a-zA-Z/]/.test(raw[lt + 1] ?? "") ? tagEnd(raw, lt) : lt + 1;
      text += raw.slice(lt, end);
      i = end;
      continue;
    }
    flush();
    i = lt + m[0].length;
    const tag = m[2].toLowerCase();
    if (m[1]) {
      out.push({ kind: "close", tag });
      continue;
    }
    const attrs: Attrs = Object.create(null);
    for (const a of (m[3] ?? "").matchAll(ATTR_RE)) {
      const name = a[1].toLowerCase();
      if (!(name in attrs)) attrs[name] = a[2] ?? a[3] ?? a[4] ?? "";
    }
    out.push({ kind: "open", tag, attrs, selfClosing: m[4] === "/" });
  }
  flush();
  return out;
}

/** The end of a tag-like run starting at `from`: past its `>` outside quotes. */
function tagEnd(raw: string, from: number): number {
  let quote = "";
  for (let j = from + 1; j < raw.length; j++) {
    const c = raw[j];
    if (quote) {
      if (c === quote) quote = "";
    } else if (c === '"' || c === "'") quote = c;
    else if (c === ">") return j + 1;
  }
  return raw.length;
}

/** An attribute's value with entities decoded (once). */
function attr(attrs: Attrs, name: string): string | undefined {
  const v = attrs[name];
  return v === undefined ? undefined : decodeEntities(v);
}

/**
 * Opening these closes an open element the HTML parser would end implicitly
 * (`<li>a<li>b`, `<td>1<td>2`), searching no further than a boundary.
 */
const IMPLIED: Record<string, { closes: ReadonlySet<string>; boundary: ReadonlySet<string> }> = {};
const implied = (tags: string, closes: string, boundary: string) => {
  for (const t of tags.split(" ")) IMPLIED[t] = { closes: new Set(closes.split(" ")), boundary: new Set(boundary.split(" ")) };
};
implied("li", "li", "ul ol");
implied("dt dd", "dt dd", "dl");
implied("tr", "tr td th", "table thead tbody tfoot");
implied("td th", "td th", "tr table");
implied("thead tbody tfoot", "thead tbody tfoot tr td th", "table");

/** Block elements that end an open `<p>`, which can't contain them. */
const CLOSES_P = new Set(
  (
    "address article aside blockquote center details dialog dir div dl fieldset figcaption figure footer form " +
    "h1 h2 h3 h4 h5 h6 header hgroup hr main menu nav ol p pre section summary table ul"
  ).split(" "),
);
const P_SCOPE = new Set("table td th caption button".split(" "));

interface Frame {
  tag: string;
  attrs: Attrs;
  children: ReactNode[];
}

export interface HtmlContext {
  links: FileLinkContext | undefined;
  inLink?: boolean;
}

/**
 * Builds sanitized React output from a sequence of raw HTML (`raw`) and
 * already-rendered markdown nodes (`node`). End tags the HTML parser would
 * imply are implied, unclosed elements close at `finish`, and stray close
 * tags are ignored.
 */
export class HtmlBuilder {
  private stack: Frame[] = [{ tag: "", attrs: Object.create(null), children: [] }];
  constructor(private ctx: HtmlContext) {}

  node(n: ReactNode) {
    this.top().children.push(n);
  }

  raw(html: string) {
    for (const e of htmlEvents(html)) {
      if (e.kind === "text") this.node(e.text);
      else if (e.kind === "close") this.close(e.tag);
      else {
        this.implyEnds(e.tag);
        if (VOID.has(e.tag) || e.selfClosing) this.node(this.element(e.tag, e.attrs, []));
        else this.stack.push({ tag: e.tag, attrs: e.attrs, children: [] });
      }
    }
  }

  finish(): ReactNode[] {
    while (this.stack.length > 1) this.pop();
    return this.stack[0].children.map((c, i) => <Fragment key={i}>{c}</Fragment>);
  }

  private top(): Frame {
    return this.stack[this.stack.length - 1];
  }

  private implyEnds(tag: string) {
    if (CLOSES_P.has(tag)) this.closeFirst(new Set(["p"]), P_SCOPE, false);
    const rule = IMPLIED[tag];
    if (rule) this.closeFirst(rule.closes, rule.boundary, true);
  }

  /**
   * Close down to an open element in `closes` above the nearest `boundary`:
   * the outermost such one when `outermost` (a new `<tr>` ends the open cell
   * and its row), else the innermost.
   */
  private closeFirst(closes: ReadonlySet<string>, boundary: ReadonlySet<string>, outermost: boolean) {
    let at = -1;
    for (let i = this.stack.length - 1; i > 0; i--) {
      const tag = this.stack[i].tag;
      if (boundary.has(tag)) break;
      if (closes.has(tag)) {
        at = i;
        if (!outermost) break;
      }
    }
    if (at > 0) while (this.stack.length > at) this.pop();
  }

  private close(tag: string) {
    for (let i = this.stack.length - 1; i > 0; i--) {
      if (this.stack[i].tag !== tag) continue;
      while (this.stack.length > i) this.pop();
      return;
    }
  }

  private pop() {
    const f = this.stack.pop()!;
    this.node(this.element(f.tag, f.attrs, f.children));
  }

  /** Inside an open `<a>` (or a markdown link): nested refs and images stay inert. */
  insideLink(): boolean {
    return !!this.ctx.inLink || this.stack.some((f) => f.tag === "a");
  }

  private element(tag: string, attrs: Attrs, rawChildren: ReactNode[]): ReactNode {
    if (DROPPED.has(tag)) return null;
    if (!ALLOWED.has(tag)) return rawChildren.length ? createElement(Fragment, null, ...rawChildren) : null;
    const children = NO_TEXT.has(tag) ? rawChildren.filter((c) => !(typeof c === "string" && !c.trim())) : rawChildren;
    const props = commonProps(tag, attrs);
    switch (tag) {
      case "a":
        return this.link(attrs, children);
      case "img":
        return this.image(attrs);
      case "source":
        return null; // <picture> falls back to its <img>
      case "picture":
        return createElement(Fragment, null, ...children);
      case "input":
        return attr(attrs, "type")?.toLowerCase() === "checkbox" ? (
          <input type="checkbox" checked={"checked" in attrs} disabled readOnly />
        ) : null;
      case "h1":
      case "h2":
      case "h3":
      case "h4":
      case "h5":
      case "h6": {
        const level = Number(tag[1]);
        return createElement("div", { ...props, className: `md-h md-h${level}`, role: "heading", "aria-level": level }, ...children);
      }
      case "center":
        return createElement("div", { ...props, style: { textAlign: "center" } }, ...children);
      case "table":
        return <div className="md-table-wrap">{createElement("table", { ...props, className: "md-table" }, ...wrapRows(children))}</div>;
      case "details":
        return createElement("details", { ...props, open: "open" in attrs || undefined }, ...children);
      case "ol":
        return createElement("ol", { ...props, start: intAttr(attr(attrs, "start")) }, ...children);
      default:
        return createElement(tag, props, ...children);
    }
  }

  private link(attrs: Attrs, children: ReactNode[]): ReactNode {
    const label = createElement(Fragment, null, ...children);
    // A named anchor, or a link inside a link (which can't nest).
    if (!attrs.href || this.insideLink()) return label;
    // classifyLink decodes entities itself: pass the value as written.
    const target = classifyLink(attrs.href, this.ctx.links);
    if (target.kind === "external") {
      return (
        <a href={target.href} target="_blank" rel="noopener noreferrer nofollow" title={attr(attrs, "title") || target.href}>
          {label}
        </a>
      );
    }
    if (target.kind === "file") {
      return (
        <FileRef path={decodeEntities(attrs.href)} reference={target.ref} code={false}>
          {label}
        </FileRef>
      );
    }
    return (
      <span className="md-link-inert" title="Link not opened: unsupported or unsafe address">
        {label}
      </span>
    );
  }

  private image(attrs: Attrs): ReactNode {
    const alt = attr(attrs, "alt") || "image";
    const src = attrs.src ?? "";
    const target = src ? classifyLink(src, this.ctx.links) : { kind: "none" as const };
    const links = this.ctx.links;
    if (target.kind === "file" && links?.project && !target.ref.isDirectory && !this.insideLink()) {
      return (
        <MarkdownImage
          alt={alt}
          href={decodeEntities(src)}
          reference={target.ref}
          project={links.project}
          sessionId={links.sessionId}
          style={imageStyle(attrs)}
        />
      );
    }
    if (target.kind === "external" && !this.insideLink()) {
      return (
        <a className="md-image" href={target.href} target="_blank" rel="noopener noreferrer nofollow">
          Image: {alt}
        </a>
      );
    }
    return <span className="md-image">Image: {alt}</span>;
  }
}

const ALIGN = new Set(["left", "right", "center", "justify"]);
const VALIGN = new Set(["top", "middle", "bottom", "baseline"]);

/** A length attribute (`200`, `33%`) as a CSS length, or undefined. */
function lengthAttr(v: string | undefined): string | undefined {
  const m = v?.trim().match(/^(\d+(?:\.\d+)?)(%|px)?$/);
  if (!m) return undefined;
  return m[2] === "%" ? `${m[1]}%` : `${m[1]}px`;
}

function intAttr(v: string | undefined): number | undefined {
  if (!v || !/^-?\d{1,9}$/.test(v.trim())) return undefined;
  return Number(v);
}

/** Attributes every allowed element may carry, presentational ones as styles. */
function commonProps(tag: string, raw: Attrs): Record<string, unknown> {
  const attrs = decoded(raw);
  const props: Record<string, unknown> = {};
  const style: CSSProperties = {};
  if (attrs.title) props.title = attrs.title;
  if (attrs.dir && /^(ltr|rtl|auto)$/i.test(attrs.dir)) props.dir = attrs.dir.toLowerCase();
  if (attrs.lang && /^[a-zA-Z-]{1,35}$/.test(attrs.lang)) props.lang = attrs.lang;
  const align = attrs.align?.toLowerCase();
  if (align && ALIGN.has(align)) style.textAlign = align as CSSProperties["textAlign"];
  const valign = attrs.valign?.toLowerCase();
  if (valign && VALIGN.has(valign)) style.verticalAlign = valign;
  if (tag === "td" || tag === "th" || tag === "table" || tag === "col") {
    const width = lengthAttr(attrs.width);
    if (width) style.width = width;
    props.colSpan = intAttr(attrs.colspan);
    props.rowSpan = intAttr(attrs.rowspan);
  }
  if (tag === "time" && attrs.datetime) props.dateTime = attrs.datetime;
  if (Object.keys(style).length) props.style = style;
  return props;
}

/** An `<img>`'s width/height/align as styles on the rendered picture. */
function imageStyle(raw: Attrs): CSSProperties | undefined {
  const attrs = decoded(raw);
  const style: CSSProperties = {};
  const width = lengthAttr(attrs.width);
  const height = lengthAttr(attrs.height);
  if (width) style.width = width;
  if (height) style.height = height;
  const align = attrs.align?.toLowerCase();
  if (align === "left" || align === "right") style.float = align;
  else if (align === "middle" || align === "top" || align === "bottom") style.verticalAlign = align;
  return Object.keys(style).length ? style : undefined;
}

function decoded(raw: Attrs): Partial<Record<string, string>> {
  const out: Partial<Record<string, string>> = Object.create(null);
  for (const k of Object.keys(raw)) out[k] = decodeEntities(raw[k]);
  return out;
}

/** Rows written directly in a <table> go in a <tbody>, as the HTML parser would. */
function wrapRows(children: ReactNode[]): ReactNode[] {
  const out: ReactNode[] = [];
  let rows: ReactNode[] = [];
  const flush = () => {
    if (rows.length) out.push(createElement("tbody", { key: out.length }, ...rows));
    rows = [];
  };
  for (const c of children) {
    if (isElementOf(c, "tr")) rows.push(c);
    else {
      flush();
      out.push(c);
    }
  }
  flush();
  return out;
}

function isElementOf(n: ReactNode, type: string): boolean {
  return typeof n === "object" && n !== null && "type" in n && n.type === type;
}
