// Link safety and text decoding for rendered markdown. Model text is
// untrusted: only http(s) and mailto links become real links; every other
// scheme (javascript:, data:, vbscript:, file:, …) renders as inert text.
import { fromLink, type FileLinkContext, type FileReference } from "../files/fileReference";

export const SAFE_LINK_SCHEMES: ReadonlySet<string> = new Set(["http:", "https:", "mailto:"]);

export type LinkTarget =
  | { kind: "external"; href: string }
  | { kind: "file"; ref: FileReference }
  | { kind: "none" };

/** A link destination's URL scheme ("" when it has none). */
function schemeOf(href: string): string {
  // Browsers ignore ASCII whitespace/control characters inside a scheme
  // ("java\tscript:"), so strip them before deciding.
  const cleaned = href.replace(/[\u0000-\u0020\u007f-\u009f]/g, "");
  const m = /^([a-zA-Z][a-zA-Z0-9+.-]*):/.exec(cleaned);
  return m ? m[1].toLowerCase() + ":" : "";
}

/** The href to use for an external link, or null when it is not safe. */
export function safeExternalHref(href: string): string | null {
  const trimmed = href.trim();
  const scheme = schemeOf(trimmed);
  if (!scheme || !SAFE_LINK_SCHEMES.has(scheme)) return null;
  try {
    const url = new URL(trimmed);
    if (!SAFE_LINK_SCHEMES.has(url.protocol)) return null;
    return url.href;
  } catch {
    return null;
  }
}

/**
 * What a markdown link should do: open an external URL in a new tab, refer to
 * a project file, or nothing (unsafe scheme, bare anchor, unresolvable path).
 */
export function classifyLink(href: string, context?: FileLinkContext): LinkTarget {
  const raw = decodeEntities(href).trim();
  if (!raw || raw.startsWith("#")) return { kind: "none" };
  const scheme = schemeOf(raw);
  if (scheme) {
    const safe = safeExternalHref(raw);
    if (safe) return { kind: "external", href: safe };
    // A bare `path.go:12` parses with "scheme" `path.go`; only real schemes
    // (no dot, or file:) are refused outright.
    if (scheme !== "file:" && !scheme.slice(0, -1).includes(".")) return { kind: "none" };
  }
  if (raw.startsWith("//")) return { kind: "none" };
  const ref = fromLink(raw, context);
  return ref ? { kind: "file", ref } : { kind: "none" };
}

const NAMED_ENTITIES: Record<string, string> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
  apos: "'",
  nbsp: "\u00a0",
  copy: "©",
  reg: "®",
  trade: "™",
  hellip: "…",
  mdash: "—",
  ndash: "–",
  larr: "←",
  rarr: "→",
  uarr: "↑",
  darr: "↓",
  harr: "↔",
  times: "×",
  divide: "÷",
  middot: "·",
  bull: "•",
  deg: "°",
  plusmn: "±",
  ne: "≠",
  le: "≤",
  ge: "≥",
  laquo: "«",
  raquo: "»",
  lsquo: "‘",
  rsquo: "’",
  ldquo: "“",
  rdquo: "”",
  check: "✓",
};

/**
 * Decode HTML character references in markdown text to the characters they
 * name (CommonMark semantics). The result is still rendered as a text node,
 * so a decoded `&lt;script&gt;` is just the visible text "<script>".
 */
export function decodeEntities(text: string): string {
  if (!text.includes("&")) return text;
  return text.replace(/&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});/g, (whole, body: string) => {
    if (body[0] === "#") {
      const code = body[1] === "x" || body[1] === "X" ? parseInt(body.slice(2), 16) : parseInt(body.slice(1), 10);
      if (!Number.isFinite(code) || code <= 0 || code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) return "\ufffd";
      return String.fromCodePoint(code);
    }
    return NAMED_ENTITIES[body] ?? whole;
  });
}
