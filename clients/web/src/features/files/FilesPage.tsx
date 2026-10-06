// The Files surface (`/p/<project>/files/<path>[?session=<id>][#L12-L20]`):
// a lazily loaded tree (ListFiles per expanded directory) beside the viewer.
// `?session=` resolves everything against that session's live worktree.
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { client, errorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { paths } from "../../app/paths";
import type { ListFilesResponse } from "../../gen/ycc/v1/ycc_pb";
import { DirListing, FileViewer, useFileList } from "./FileViewer";
import type { FileReference, LineRange } from "./fileReference";
import { crumbs, dirname, expandTo, flattenTree, parseFileLocation, toggle, type TreeEntry } from "./model";

export function FilesPage({ project, splat }: { project: string; splat: string }) {
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const loc = useMemo(() => parseFileLocation(splat, location.search, location.hash), [splat, location.search, location.hash]);
  const session = loc.session;
  const trailingSlash = splat.endsWith("/");
  // Whether the path names a directory: the root, a trailing slash, or the
  // parent listing (which the tree loads anyway) says so.
  const parent = useFileList({ project, sessionId: session, path: dirname(loc.path) }, loc.path !== "" && !trailingSlash);
  let kind: "dir" | "file" | "pending" = "pending";
  if (loc.path === "" || trailingSlash) kind = "dir";
  else if (parent.data) kind = parent.data.entries.find((e) => e.name === loc.path.split("/").pop())?.isDir ? "dir" : "file";
  else if (parent.isError) kind = "file";
  const root = useFileList({ project, sessionId: session, path: "" });

  const open = useCallback(
    (ref: FileReference) => navigate(paths.files(project, ref.path, { session, lines: ref.lines })),
    [navigate, project, session],
  );
  const setLines = useCallback(
    (lines: LineRange | null) =>
      navigate(paths.files(project, loc.path, { session, lines }), { replace: true, preventScrollReset: true }),
    [navigate, project, loc.path, session],
  );

  return (
    <div className="page files-page">
      <header className="page-head files-head">
        <nav className="crumbs" aria-label="Path">
          {crumbs(loc.path, project || "Files").map((c, i, all) =>
            i === all.length - 1 ? (
              <span key={c.path} className="crumb current" aria-current="page">
                {c.label}
              </span>
            ) : (
              <span key={c.path} className="crumb">
                <Link to={paths.files(project, c.path, { session })}>{c.label}</Link>
                <span className="crumb-sep" aria-hidden>
                  /
                </span>
              </span>
            ),
          )}
        </nav>
        <div className="files-head-actions">
          {session && (
            <span className="chip worktree-chip" title={root.data?.root ? `Worktree: ${root.data.root}` : undefined}>
              Worktree of <Link to={paths.session(project, session)} className="mono">{session}</Link>
              <Link
                to={paths.files(project, loc.path, { lines: loc.lines })}
                className="chip-x"
                title="Show the project root instead"
                aria-label="Show the project root instead"
              >
                ×
              </Link>
            </span>
          )}
          <button
            type="button"
            className="btn ghost small"
            onClick={() => void qc.invalidateQueries({ queryKey: queryKeys.filesAll })}
            title="Reload the tree and file"
          >
            ↻ Refresh
          </button>
        </div>
      </header>
      {root.data?.root && <div className="files-root muted mono small">{root.data.root}</div>}
      <div className="files-body">
        <FileTree project={project} session={session} current={loc.path} currentIsDir={kind === "dir"} onOpen={open} />
        <section className="files-view" aria-label={loc.path || "Project root"}>
          {kind === "pending" ? (
            <p className="muted pad">Loading…</p>
          ) : kind === "dir" ? (
            <DirListing key={loc.path} target={{ project, sessionId: session, path: loc.path }} onOpen={open} variant="page" />
          ) : (
            <FileViewer
              key={loc.path}
              target={{ project, sessionId: session, path: loc.path }}
              isDirectory={false}
              lines={loc.lines}
              variant="page"
              onOpen={open}
              onLines={setLines}
            />
          )}
        </section>
      </div>
    </div>
  );
}

/** The project tree: directories expand in place; the current path's ancestors start expanded. */
function FileTree({
  project,
  session,
  current,
  currentIsDir,
  onOpen,
}: {
  project: string;
  session: string;
  current: string;
  currentIsDir: boolean;
  onOpen: (ref: FileReference) => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(() => expandTo(new Set(), current, currentIsDir));
  useEffect(() => setExpanded((e) => expandTo(e, current, currentIsDir)), [current, currentIsDir]);
  const dirs = useMemo(() => ["", ...expanded], [expanded]);
  const results = useQueries({
    queries: dirs.map((path) => ({
      queryKey: queryKeys.fileList(project, session, path),
      queryFn: ({ signal }: { signal: AbortSignal }) => client.listFiles({ project, sessionId: session, path }, { signal }),
    })),
  });
  const listings = new Map<string, TreeEntry[]>();
  const errors = new Map<string, unknown>();
  results.forEach((r, i) => {
    const data = r.data as ListFilesResponse | undefined;
    if (data) listings.set(dirs[i], data.entries);
    else if (r.error) errors.set(dirs[i], r.error);
  });
  const rows = flattenTree(listings, expanded);
  const currentRow = useRef<HTMLLIElement>(null);
  const scrolled = useRef(false);
  useEffect(() => {
    if (!scrolled.current && currentRow.current) {
      scrolled.current = true;
      currentRow.current.scrollIntoView({ block: "nearest" });
    }
  });
  return (
    <nav className="file-tree" aria-label="Project files">
      {errors.has("") ? (
        <p className="error small pad">{errorMessage(errors.get(""))}</p>
      ) : !listings.has("") ? (
        <p className="muted small pad">Loading…</p>
      ) : (
        <ul role="tree">
          {rows.map((r) => {
            const isCurrent = r.path === current;
            return (
              <li
                key={r.path}
                ref={isCurrent ? currentRow : undefined}
                role="treeitem"
                aria-expanded={r.isDir ? r.expanded : undefined}
                aria-selected={isCurrent}
                className={`tree-row${isCurrent ? " current" : ""}${r.ignored ? " ignored" : ""}`}
                style={{ paddingLeft: 6 + r.depth * 14 }}
              >
                {r.isDir ? (
                  <button
                    type="button"
                    className="tree-toggle"
                    aria-label={`${r.expanded ? "Collapse" : "Expand"} ${r.name}`}
                    onClick={() => setExpanded((e) => toggle(e, r.path))}
                  >
                    {r.expanded ? "▾" : "▸"}
                  </button>
                ) : (
                  <span className="tree-toggle spacer" aria-hidden />
                )}
                <button
                  type="button"
                  className="tree-name"
                  title={r.path}
                  onClick={() => {
                    if (r.isDir) setExpanded((e) => new Set(e).add(r.path));
                    onOpen({ path: r.path, isDirectory: r.isDir, lines: null });
                  }}
                >
                  {r.name}
                  {r.isDir ? "/" : ""}
                </button>
                {r.loading && <span className="muted small"> …</span>}
                {r.isDir && r.expanded && errors.has(r.path) && (
                  <span className="error small" title={errorMessage(errors.get(r.path))}>
                    {" "}
                    !
                  </span>
                )}
              </li>
            );
          })}
          {rows.length === 0 && <li className="muted small pad">Empty.</li>}
        </ul>
      )}
    </nav>
  );
}
