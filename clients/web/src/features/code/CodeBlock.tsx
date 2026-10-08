// Highlighted, copyable code and diffs. Highlighting only splits text into
// spans rendered as React text nodes (escaped by React) — never HTML.
import { Fragment, memo, useMemo, type ReactNode } from "react";
import { CopyButton } from "../../ui/CopyButton";
import { highlightLines, highlightText, type Language, type Span } from "./highlight";
import { parseDiff, diffStats, type DiffLine } from "./diff";

export function Spans({ spans }: { spans: readonly Span[] }) {
  return (
    <>
      {spans.map((sp, i) =>
        sp.kind ? (
          <span key={i} className={`tok-${sp.kind}`}>
            {sp.text}
          </span>
        ) : (
          <Fragment key={i}>{sp.text}</Fragment>
        ),
      )}
    </>
  );
}

/** Highlighted code lines inside a <code> (no chrome). */
export const HighlightedCode = memo(function HighlightedCode({
  code,
  language,
}: {
  code: string;
  language: Language | null;
}) {
  const lines = useMemo(() => highlightText(code, language), [code, language]);
  return (
    <code>
      {lines.map((spans, i) => (
        <Fragment key={i}>
          <Spans spans={spans} />
          {i < lines.length - 1 ? "\n" : null}
        </Fragment>
      ))}
    </code>
  );
});

/** A fenced code block: language label, copy button, highlighted body. */
export const CodeBlock = memo(function CodeBlock({
  code,
  language,
  label,
  className = "",
  extra,
}: {
  /** The exact source; the copy button copies this verbatim. */
  code: string;
  language: Language | null;
  label?: string;
  className?: string;
  extra?: ReactNode;
}) {
  return (
    <div className={`code-block ${className}`.trim()}>
      <div className="code-head">
        <span className="code-lang">{label ?? ""}</span>
        {extra}
        <CopyButton text={code} title="Copy code" what="code" />
      </div>
      <pre className="code">
        <HighlightedCode code={code} language={language} />
      </pre>
    </div>
  );
});

function DiffRow({ line }: { line: DiffLine }) {
  const body = useMemo(() => {
    if (!line.language || !(line.kind === "addition" || line.kind === "deletion" || line.kind === "context")) {
      return null;
    }
    // Each line is highlighted on its own: diff sides interleave, so a
    // carried block-comment state would bleed across them.
    return highlightLines([line.text.slice(1)], line.language)[0];
  }, [line]);
  return (
    <div className={`diff-line diff-${line.kind}`}>
      {body ? (
        <>
          <span className="diff-sign">{line.text.slice(0, 1)}</span>
          <Spans spans={body} />
        </>
      ) : (
        line.text || " "
      )}
    </div>
  );
}

/**
 * A unified diff with tinted add/delete/header rows, per-file syntax
 * highlighting, a truncation notice, and a copy button for the raw diff.
 */
export const DiffView = memo(function DiffView({
  diff,
  truncated = false,
  showStats = true,
  copyLabel = "Copy diff",
}: {
  diff: string;
  truncated?: boolean;
  showStats?: boolean;
  copyLabel?: string;
}) {
  const lines = useMemo(() => parseDiff(diff, truncated), [diff, truncated]);
  const stats = useMemo(() => diffStats(lines), [lines]);
  if (!diff.trim()) return <p className="muted">No changes.</p>;
  return (
    <div className="diff-block">
      <div className="code-head">
        {showStats ? (
          <span className="diff-stats">
            <span className="add">+{stats.additions}</span> <span className="del">−{stats.deletions}</span>
            {stats.files > 0 && (
              <span className="muted">
                {" "}
                · {stats.files} {stats.files === 1 ? "file" : "files"}
              </span>
            )}
          </span>
        ) : (
          <span />
        )}
        <CopyButton text={diff} label="Copy" title={copyLabel} what="diff" />
      </div>
      {truncated && (
        <p className="truncation-notice" role="note">
          The daemon truncated this diff; the end is not shown.
        </p>
      )}
      <div className="diff">
        {lines.map((l, i) => (
          <DiffRow key={i} line={l} />
        ))}
      </div>
    </div>
  );
});
