// File paths in the transcript. Until the file viewer lands (backlog 0415)
// they render as copyable text: clicking copies the path. A FileLinksProvider
// higher in the tree can supply `open`, which turns every resolvable reference
// into a link to the viewer without touching the call sites, and `route`,
// which links references that name another surface (task bodies link their
// sibling backlog task files to the task).
import { createContext, useContext, useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { Link } from "react-router";
import { copyText } from "../../ui/copy";
import { fromLink, type FileLinkContext, type FileReference } from "./fileReference";

export interface FileLinkHandler {
  context: FileLinkContext;
  /** Open a reference in the file viewer; absent until that surface exists. */
  open?: (ref: FileReference) => void;
  /**
   * An in-app route for references that name another surface (a backlog
   * task file opens the task); checked before `open`, null falls through.
   */
  route?: (ref: FileReference) => string | null;
}

const FileLinks = createContext<FileLinkHandler | null>(null);

export function FileLinksProvider({ value, children }: { value: FileLinkHandler; children: ReactNode }) {
  return <FileLinks.Provider value={value}>{children}</FileLinks.Provider>;
}

export function useFileLinks(): FileLinkHandler | null {
  return useContext(FileLinks);
}

/**
 * A file path: `path` is what gets copied; `children` (default: the path) is
 * what shows. `reference` skips re-resolving an already parsed reference.
 */
export function FileRef({
  path,
  reference,
  code = true,
  children,
}: {
  path: string;
  reference?: FileReference | null;
  /** Render in code style (inline code spans, tool previews). */
  code?: boolean;
  children?: ReactNode;
}) {
  const links = useFileLinks();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  const label = children ?? path;
  const cls = `file-ref${code ? " code" : ""}${copied ? " copied" : ""}`;
  const resolved = links?.open || links?.route ? (reference ?? fromLink(path, links.context)) : null;
  const to = resolved && links?.route ? links.route(resolved) : null;
  if (to) {
    return (
      <Link to={to} className={`${cls} linked`} title={`Open ${path}`} onClick={(e) => e.stopPropagation()}>
        {label}
      </Link>
    );
  }
  if (links?.open && resolved) {
    const open = links.open;
    return (
      <button
        type="button"
        className={`${cls} linked`}
        title={`Open ${path}`}
        onClick={(e) => {
          e.preventDefault();
          e.stopPropagation();
          open(resolved);
        }}
      >
        {label}
      </button>
    );
  }
  const onClick = async (e: MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    if (await copyText(path)) {
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1200);
    }
  };
  return (
    <button
      type="button"
      className={cls}
      title={copied ? "Copied" : `Copy path: ${path}`}
      aria-label={`Copy path ${path}`}
      onClick={(e) => void onClick(e)}
    >
      {label}
    </button>
  );
}
