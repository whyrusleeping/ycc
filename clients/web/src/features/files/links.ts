// File-link handlers for a session's surfaces (transcript, row detail,
// working changes): references resolve against the session's live worktree,
// and absolute paths under the project root or that worktree are recognized.
import { useMemo } from "react";
import { useProjects, useSessionFeed, useWorkstreams } from "../../api/queries";
import { useInspector } from "../inspector/inspector";
import type { FileLinkHandler } from "./FileRef";
import { transcriptLinkContext, type FileReference } from "./fileReference";

/** Absolute daemon-host roots a session's paths may be written under. */
export function useSessionRoots(project: string, sessionId: string): string[] {
  const projects = useProjects();
  const { feed } = useSessionFeed(null);
  const workstreams = useWorkstreams(project, project !== "" && sessionId !== "");
  const projectPath = projects.data?.find((p) => p.name === project)?.path ?? "";
  const workspace = feed?.rows.find((r) => r.session.sessionId === sessionId)?.session.workspace ?? "";
  const worktree =
    workstreams.data?.find((w) => w.sessionId === sessionId || w.integrateSessionId === sessionId)?.worktreePath ?? "";
  return useMemo(
    () => [...new Set([projectPath, workspace, worktree].filter(Boolean))],
    [projectPath, workspace, worktree],
  );
}

/**
 * Links that open references in the inspector's file viewer. `from` says
 * where the reference sits: in the inspector (pushes, keeping Back) or
 * elsewhere (replaces the inspector's content).
 */
export function useSessionFileLinks(project: string, sessionId: string, from: "main" | "inspector"): FileLinkHandler {
  const roots = useSessionRoots(project, sessionId);
  const inspector = useInspector();
  const show = from === "inspector" ? inspector.push : inspector.open;
  return useMemo<FileLinkHandler>(
    () => ({
      context: transcriptLinkContext(project, sessionId, roots),
      open: (ref: FileReference) =>
        show({ kind: "file", project, sessionId, path: ref.path, isDirectory: ref.isDirectory, lines: ref.lines }),
    }),
    [project, sessionId, roots, show],
  );
}

/** Links for project-level documents (memory, plans, task bodies): the project root, shown in the inspector. */
export function useProjectFileLinks(project: string, baseDirectory: string, from: "main" | "inspector"): FileLinkHandler {
  const projects = useProjects();
  const inspector = useInspector();
  const show = from === "inspector" ? inspector.push : inspector.open;
  const projectPath = projects.data?.find((p) => p.name === project)?.path ?? "";
  return useMemo<FileLinkHandler>(
    () => ({
      context: { project, sessionId: "", baseDirectory, absoluteRoots: projectPath ? [projectPath] : [] },
      open: (ref: FileReference) =>
        show({ kind: "file", project, sessionId: "", path: ref.path, isDirectory: ref.isDirectory, lines: ref.lines }),
    }),
    [project, baseDirectory, projectPath, show],
  );
}
