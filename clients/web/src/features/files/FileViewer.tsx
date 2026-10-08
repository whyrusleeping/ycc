// The read-only file viewer and directory listing, shared by the Files page
// and the inspector: ReadFile text with a line-number gutter, syntax
// highlighting over escaped text spans, a target line range (scrolled to and
// highlighted), go-to-line, rendered markdown with a source toggle, images,
// and binary/truncated/not-found/fallback notes. Nothing is inserted as HTML.
import { useQuery } from "@tanstack/react-query";
import { memo, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { Link } from "react-router";
import { client, errorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { paths } from "../../app/paths";
import type { FileEntry, ReadFileResponse } from "../../gen/ycc/v1/ycc_pb";
import { CopyButton } from "../../ui/CopyButton";
import { Icon } from "../../ui/icons";
import { Spans } from "../code/CodeBlock";
import { highlightLines, languageForPath, type Span } from "../code/highlight";
import { Markdown } from "../markdown/Markdown";
import { relativeTime } from "../sessions/feed";
import { isPictureType } from "../attachments/attachments";
import { FileLinksProvider, type FileLinkHandler } from "./FileRef";
import { formatReference, type FileReference, type LineRange } from "./fileReference";
import {
  HIGHLIGHT_LINE_LIMIT,
  VIEWER_MAX_BYTES,
  basename,
  clampLines,
  dirname,
  entryReference,
  formatSize,
  isMarkdownPath,
  parseGoToLine,
  splitLines,
} from "./model";

export interface FileTarget {
  project: string;
  /** Resolve against this session's live worktree ("" = the project root). */
  sessionId: string;
  path: string;
}

function codeOf(err: unknown): Code | null {
  return err ? ConnectError.from(err).code : null;
}

/** ListFiles for one directory (shared with the tree's cache). */
export function useFileList(t: FileTarget, enabled = true) {
  return useQuery({
    queryKey: queryKeys.fileList(t.project, t.sessionId, t.path),
    enabled,
    queryFn: ({ signal }) => client.listFiles({ project: t.project, sessionId: t.sessionId, path: t.path }, { signal }),
  });
}

function useFileRead(t: FileTarget, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.fileRead(t.project, t.sessionId, t.path),
    enabled,
    queryFn: ({ signal }) =>
      client.readFile(
        { project: t.project, sessionId: t.sessionId, path: t.path, maxBytes: BigInt(VIEWER_MAX_BYTES) },
        { signal },
      ),
  });
}

/** A note when the session's worktree is gone and the daemon fell back to the project root. */
export function FallbackNote({ show }: { show: boolean }) {
  if (!show) return null;
  return (
    <div className="banner warn file-note" role="note">
      This session’s worktree no longer exists, so this is the project root’s copy.
    </div>
  );
}

/**
 * A file or directory. `onOpen` follows links (directory entries, markdown
 * links); `onLines` reports a new target range (line-number clicks, go to
 * line) — the page writes it to the URL, the inspector keeps it locally.
 */
export function FileViewer({
  target,
  isDirectory,
  lines,
  variant,
  onOpen,
  onLines,
}: {
  target: FileTarget;
  isDirectory: boolean;
  lines: LineRange | null;
  variant: "page" | "inspector";
  onOpen: (ref: FileReference) => void;
  onLines: (lines: LineRange | null) => void;
}) {
  const read = useFileRead(target, !isDirectory);
  // ReadFile on a directory is invalid_argument: a link that named a folder
  // without a trailing "/". Show its listing instead.
  const asDirectory = isDirectory || codeOf(read.error) === Code.InvalidArgument;
  if (asDirectory) {
    return <DirListing target={target} onOpen={onOpen} fallbackError={isDirectory ? null : read.error} variant={variant} />;
  }
  if (read.isPending) return <p className="muted pad">Loading…</p>;
  if (read.isError) {
    if (codeOf(read.error) === Code.NotFound) {
      return (
        <div className="file-missing">
          <p>
            <strong>File not found:</strong> <span className="mono">{target.path}</span>
          </p>
          <p className="muted">It may have been moved or deleted, or it lived in a worktree that has since been cleaned up.</p>
        </div>
      );
    }
    return <p className="error pad">{errorMessage(read.error)}</p>;
  }
  return <FileContent target={target} resp={read.data} lines={lines} variant={variant} onOpen={onOpen} onLines={onLines} />;
}

function FileContent({
  target,
  resp,
  lines,
  variant,
  onOpen,
  onLines,
}: {
  target: FileTarget;
  resp: ReadFileResponse;
  lines: LineRange | null;
  variant: "page" | "inspector";
  onOpen: (ref: FileReference) => void;
  onLines: (lines: LineRange | null) => void;
}) {
  const isImage = isPictureType(resp.mediaType);
  const isText = !isImage && !resp.isBinary;
  const text = useMemo(() => (isText ? new TextDecoder("utf-8").decode(resp.data) : ""), [isText, resp.data]);
  const fileLines = useMemo(() => splitLines(text), [text]);
  const markdown = isText && isMarkdownPath(resp.path || target.path);
  // A line target reads in source; otherwise markdown renders.
  const [rendered, setRendered] = useState(markdown && !lines);
  const [wrap, setWrap] = useState(false);
  useEffect(() => {
    if (lines) setRendered(false);
  }, [lines]);
  const size = Number(resp.size);
  const links = useMemo<FileLinkHandler>(
    () => ({
      // Relative links in a markdown file resolve against its directory.
      context: {
        project: target.project,
        sessionId: target.sessionId,
        baseDirectory: dirname(resp.path || target.path),
        absoluteRoots: resp.root ? [resp.root] : [],
      },
      open: onOpen,
    }),
    [target.project, target.sessionId, target.path, resp.path, resp.root, onOpen],
  );
  const clamped = clampLines(lines, fileLines.length);

  return (
    <div className={`file-viewer variant-${variant}`}>
      <div className="file-toolbar">
        <span className="file-meta muted small">
          {formatSize(size)}
          {resp.mtime && <> · modified {relativeTime(resp.mtime)}</>}
          {isText && <> · {fileLines.length} {fileLines.length === 1 ? "line" : "lines"}</>}
        </span>
        <span className="file-actions">
          {markdown && (
            <SourceToggle rendered={rendered} onChange={setRendered} />
          )}
          {isText && !rendered && (
            <>
              <GoToLine
                max={fileLines.length}
                onGo={(r) => {
                  onLines(r);
                }}
              />
              <button type="button" className={`btn ghost small${wrap ? " active" : ""}`} aria-pressed={wrap} onClick={() => setWrap((w) => !w)} title="Wrap long lines">
                Wrap
              </button>
            </>
          )}
          <CopyButton text={resp.path || target.path} label="Copy path" title="Copy the root-relative path" what="file_path" />
          {isText && <CopyButton text={text} label={variant === "inspector" ? "Copy" : "Copy contents"} title="Copy the file’s contents" what="file_contents" />}
          {variant === "inspector" && (
            <Link className="btn ghost small" to={paths.files(target.project, target.path, { session: target.sessionId, lines })}>
              Open in Files
            </Link>
          )}
        </span>
      </div>
      <FallbackNote show={resp.rootFallback} />
      {resp.truncated && (
        <div className="banner warn file-note" role="note">
          This file is {formatSize(size)}; only the first {formatSize(resp.data.length)} {isText ? `(${fileLines.length} lines) are` : "is"} shown.
        </div>
      )}
      {lines && !clamped && isText && (
        <div className="banner warn file-note" role="note">
          Line {lines.start} is past the end of {resp.truncated ? "the loaded part of " : ""}this file.
        </div>
      )}
      {isImage ? (
        <ImagePreview data={resp.data} mediaType={resp.mediaType} name={basename(target.path)} size={size} />
      ) : resp.isBinary ? (
        <p className="muted pad">
          Binary file ({resp.mediaType || "unknown type"}, {formatSize(size)}) — not shown.
        </p>
      ) : rendered ? (
        <div className="file-markdown">
          <FileLinksProvider value={links}>
            <Markdown text={text} />
          </FileLinksProvider>
        </div>
      ) : fileLines.length === 0 ? (
        <p className="muted pad">Empty file.</p>
      ) : (
        <CodeLines
          lines={fileLines}
          path={resp.path || target.path}
          target={clamped}
          wrap={wrap}
          onLine={(n, extend) => {
            if (extend && clamped) onLines({ start: Math.min(clamped.start, n), end: Math.max(clamped.end, n) });
            else onLines(clamped && clamped.start === n && clamped.end === n ? null : { start: n, end: n });
          }}
        />
      )}
    </div>
  );
}

/** Rendered / Source switch for markdown documents. */
export function SourceToggle({ rendered, onChange }: { rendered: boolean; onChange: (rendered: boolean) => void }) {
  return (
    <span className="segmented inline" role="group" aria-label="Markdown view">
      <button type="button" className={rendered ? "selected" : undefined} aria-pressed={rendered} onClick={() => onChange(true)}>
        Rendered
      </button>
      <button type="button" className={!rendered ? "selected" : undefined} aria-pressed={!rendered} onClick={() => onChange(false)}>
        Source
      </button>
    </span>
  );
}

function GoToLine({ max, onGo }: { max: number; onGo: (r: LineRange) => void }) {
  const [value, setValue] = useState("");
  const [bad, setBad] = useState(false);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const r = parseGoToLine(value);
    if (!r || r.start > max) {
      setBad(true);
      return;
    }
    setBad(false);
    onGo({ start: r.start, end: Math.min(r.end, max) });
  };
  return (
    <form className="goto-line" onSubmit={submit}>
      <input
        type="text"
        inputMode="numeric"
        aria-label="Go to line"
        placeholder="Go to line"
        className={`field small${bad ? " invalid" : ""}`}
        value={value}
        title={`A line (1–${max}) or a range like 12-20`}
        onChange={(e) => {
          setValue(e.target.value);
          setBad(false);
        }}
      />
    </form>
  );
}

/** Numbered, highlighted lines; the target range is tinted and scrolled into view. */
const CodeLines = memo(function CodeLines({
  lines,
  path,
  target,
  wrap,
  onLine,
}: {
  lines: string[];
  path: string;
  target: LineRange | null;
  wrap: boolean;
  onLine: (n: number, extend: boolean) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const language = languageForPath(path);
  const spans = useMemo<Span[][] | null>(
    () => (language && lines.length <= HIGHLIGHT_LINE_LIMIT ? highlightLines(lines, language) : null),
    [lines, language],
  );
  const start = target?.start ?? 0;
  useEffect(() => {
    if (!start || !box.current) return;
    const el = box.current.querySelector(`[data-line="${start}"]`);
    el?.scrollIntoView({ block: "center" });
  }, [start, lines]);
  const width = String(lines.length).length;
  return (
    <div
      ref={box}
      className={`file-code${wrap ? " wrap" : ""}`}
      style={{ ["--ln-width" as string]: `${width + 1}ch` }}
      role="region"
      aria-label={`Contents of ${path}`}
    >
      {lines.map((line, i) => {
        const n = i + 1;
        const hit = target !== null && n >= target.start && n <= target.end;
        return (
          <div key={i} className={`fl${hit ? " target" : ""}`} data-line={n}>
            <span
              className="ln"
              aria-hidden
              title={`Line ${n} (Shift-click extends the selection)`}
              onClick={(e) => onLine(n, e.shiftKey)}
            >
              {n}
            </span>
            <span className="lc">{spans ? <Spans spans={spans[i]} /> : line || " "}</span>
          </div>
        );
      })}
    </div>
  );
});

function ImagePreview({ data, mediaType, name, size }: { data: Uint8Array; mediaType: string; name: string; size: number }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!data.length || !isPictureType(mediaType)) {
      setUrl(null);
      return;
    }
    const u = URL.createObjectURL(new Blob([data as BlobPart], { type: mediaType }));
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [data, mediaType]);
  if (!data.length) return <p className="muted pad">Image too large to preview ({formatSize(size)}).</p>;
  return <div className="file-image">{url && <img src={url} alt={name} />}</div>;
}

/** A directory's entries: folders first, gitignored entries dimmed. */
export function DirListing({
  target,
  onOpen,
  fallbackError,
  variant,
}: {
  target: FileTarget;
  onOpen: (ref: FileReference) => void;
  /** Shown when the path is neither a readable file nor a listable directory. */
  fallbackError?: unknown;
  variant: "page" | "inspector";
}) {
  const q = useFileList(target);
  if (q.isPending) return <p className="muted pad">Loading…</p>;
  if (q.isError) {
    const err = fallbackError ?? q.error;
    if (codeOf(q.error) === Code.NotFound || codeOf(err) === Code.NotFound) {
      return (
        <div className="file-missing">
          <p>
            <strong>Not found:</strong> <span className="mono">{target.path || "/"}</span>
          </p>
          <p className="muted">It may have been moved or deleted, or it lived in a worktree that has since been cleaned up.</p>
        </div>
      );
    }
    return <p className="error pad">{errorMessage(err)}</p>;
  }
  const r = q.data;
  return (
    <div className="dir-listing">
      <FallbackNote show={r.rootFallback} />
      {variant === "inspector" && (
        <div className="file-toolbar">
          <span className="muted small">
            {r.entries.length} {r.entries.length === 1 ? "entry" : "entries"}
          </span>
          <Link className="btn ghost small" to={paths.files(target.project, target.path, { session: target.sessionId })}>
            Open in Files
          </Link>
        </div>
      )}
      {r.entries.length === 0 ? (
        <p className="muted pad">Empty directory.</p>
      ) : (
        <table className="dir-table">
          <thead>
            <tr>
              <th>Name</th>
              <th className="num">Size</th>
              <th>Modified</th>
            </tr>
          </thead>
          <tbody>
            {r.entries.map((e) => (
              <DirRow key={e.name} entry={e} dir={r.path} onOpen={onOpen} />
            ))}
          </tbody>
        </table>
      )}
      {r.truncated && <p className="muted small pad">The listing was truncated; not every entry is shown.</p>}
    </div>
  );
}

function DirRow({ entry, dir, onOpen }: { entry: FileEntry; dir: string; onOpen: (ref: FileReference) => void }) {
  const ref = entryReference(dir, entry);
  return (
    <tr className={entry.ignored ? "ignored" : undefined}>
      <td>
        <button type="button" className="link dir-entry" onClick={() => onOpen(ref)} title={formatReference(ref)}>
          <Icon name={entry.isDir ? "files" : "file"} size={14} className="fs-icon" />
          {entry.name}
          {entry.isDir ? "/" : ""}
          {entry.isSymlink && <span className="tag">link</span>}
          {entry.ignored && <span className="tag">ignored</span>}
        </button>
      </td>
      <td className="num muted">{entry.isDir ? "" : formatSize(Number(entry.size))}</td>
      <td className="muted">{entry.mtime ? relativeTime(entry.mtime) : ""}</td>
    </tr>
  );
}
