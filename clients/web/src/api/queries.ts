// Server-state cache: query keys and hooks over the generated client. Unary
// reads are cached per query and refreshed on window focus; mutations replace
// local state with the daemon's response or invalidate the affected keys.
import { QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { BacklogTaskSummary, ProjectInfo, TaskDetail } from "../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "./client";
import { buildFeed, historyTargets, mergePage, type HistoryLoad } from "../features/sessions/feed";
import { upsertSummary } from "../features/backlog/model";
import { toast } from "../ui/toast";

export const HISTORY_PAGE = 50;

export const queryKeys = {
  projects: ["projects"] as const,
  modes: ["modes"] as const,
  /** ListModels: "" is the daemon defaults, a session id its live assignment. */
  models: (sessionId: string) => ["models", sessionId] as const,
  sessionUsage: (project: string) => ["usage", "session-model", project] as const,
  sessionFeed: (targets: string[]) => ["sessionFeed", targets] as const,
  sessionFeedAll: ["sessionFeed"] as const,
  /** ListBacklog for a project ("" resolves the sole/default project server-side). */
  backlog: (project: string) => ["backlog", project] as const,
  /** GetTask. */
  task: (project: string, id: string) => ["task", project, id] as const,
};

export function makeQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        refetchOnWindowFocus: true,
        retry: (count, err) => {
          const code = ConnectError.from(err).code;
          if (code === Code.Unauthenticated || code === Code.InvalidArgument || code === Code.NotFound) return false;
          return count < 2;
        },
      },
    },
  });
}

export function useProjects() {
  return useQuery({
    queryKey: queryKeys.projects,
    queryFn: async ({ signal }) => (await client.listProjects({}, { signal })).projects,
  });
}

/** ListModes: session modes and opening-prompt presets (daemon-wide). */
export function useModes() {
  return useQuery({
    queryKey: queryKeys.modes,
    staleTime: 5 * 60_000,
    queryFn: async ({ signal }) => client.listModes({}, { signal }),
  });
}

/** ListModels scoped to a live session ("" = the configured defaults). */
export function useModels(sessionId = "", enabled = true) {
  return useQuery({
    queryKey: queryKeys.models(sessionId),
    enabled,
    queryFn: async ({ signal }) => client.listModels({ sessionId }, { signal }),
  });
}

async function loadFirstPage(project: string, signal: AbortSignal): Promise<HistoryLoad> {
  try {
    const page = await client.listSessionHistory({ project, limit: HISTORY_PAGE }, { signal });
    return { project, sessions: page.sessions, pinned: page.pinned, nextCursor: page.nextCursor };
  } catch (err) {
    if (isUnauthorized(err) || signal.aborted) throw err;
    return { project, sessions: [], pinned: [], nextCursor: "", error: errorMessage(err, "Couldn’t load sessions.") };
  }
}

/**
 * The Recent feed: every distinct project's first history page, fetched
 * concurrently and merged client-side (ListSessionHistory is per project).
 * `scope` null shows all projects; a name shows that project's rows.
 */
export function useSessionFeed(scope: string | null) {
  const qc = useQueryClient();
  const projects = useProjects();
  const targets = projects.data ? historyTargets(projects.data) : null;
  const key = queryKeys.sessionFeed(targets ?? []);
  const loads = useQuery({
    queryKey: key,
    enabled: targets !== null,
    refetchInterval: 30_000,
    queryFn: ({ signal }) => Promise.all((targets ?? []).map((t) => loadFirstPage(t, signal))),
  });
  const [loadingOlder, setLoadingOlder] = useState(false);

  const loadOlder = useCallback(async () => {
    const current = qc.getQueryState<HistoryLoad[]>(key);
    const data = current?.data;
    if (!data || loadingOlder) return;
    const requests = data.filter((l) => l.nextCursor && (scope === null || l.project === scope));
    if (!requests.length) return;
    setLoadingOlder(true);
    try {
      const pages = await Promise.allSettled(
        requests.map((l) => client.listSessionHistory({ project: l.project, limit: HISTORY_PAGE, cursor: l.nextCursor })),
      );
      // A refresh that landed meanwhile owns newer first pages; drop this page.
      if (qc.getQueryState(key)?.dataUpdatedAt !== current.dataUpdatedAt) return;
      const failed: string[] = [];
      const next = data.map((load) => {
        const i = requests.indexOf(load);
        if (i < 0) return load;
        const result = pages[i];
        if (result.status === "fulfilled") return mergePage(load, result.value.sessions, result.value.nextCursor);
        failed.push(load.project || "(default)");
        return load;
      });
      qc.setQueryData(key, next);
      if (failed.length) toast(`Couldn’t load older sessions for ${failed.join(", ")}.`);
    } finally {
      setLoadingOlder(false);
    }
  }, [qc, key, scope, loadingOlder]);

  const feed = loads.data ? buildFeed(loads.data, scope) : null;
  return {
    projects: projects.data as ProjectInfo[] | undefined,
    feed,
    isLoading: projects.isPending || (targets !== null && loads.isPending),
    error: projects.error ?? loads.error,
    isFetching: projects.isFetching || loads.isFetching,
    refresh: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.projects });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
    },
    loadOlder,
    loadingOlder,
  };
}

/** ListBacklog: the project's task summaries with readiness. */
export function useBacklog(project: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.backlog(project),
    enabled,
    refetchInterval: 30_000,
    queryFn: async ({ signal }) => (await client.listBacklog({ project }, { signal })).tasks,
  });
}

/** GetTask: one task's full detail (frontmatter and markdown body). */
export function useTask(project: string, id: string) {
  return useQuery({
    queryKey: queryKeys.task(project, id),
    enabled: id !== "",
    queryFn: async ({ signal }) => {
      const t = (await client.getTask({ project, id }, { signal })).task;
      if (!t) throw new Error(`Task ${id} not found`);
      return t;
    },
  });
}

/**
 * Install a mutation's canonical task detail: it replaces the cached task and
 * its backlog row at once, then the list revalidates (a status change can flip
 * other rows' readiness).
 */
export function installTask(qc: QueryClient, project: string, detail: TaskDetail) {
  qc.setQueryData(queryKeys.task(project, detail.id), detail);
  qc.setQueryData<BacklogTaskSummary[]>(queryKeys.backlog(project), (list) => (list ? upsertSummary(list, detail) : list));
  void qc.invalidateQueries({ queryKey: queryKeys.backlog(project) });
  // Other tasks' readiness may follow this one; refresh them when next shown.
  void qc.invalidateQueries({ queryKey: ["task", project], refetchType: "none" });
}
