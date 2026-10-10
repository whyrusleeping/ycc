// Server-state cache: query keys and hooks over the generated client. Unary
// reads are cached per query and refreshed on window focus; mutations replace
// local state with the daemon's response or invalidate the affected keys.
import { QueryClient, keepPreviousData, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { BacklogTaskSummary, ProjectInfo, TaskDetail, WorkLoopInfo, WorkstreamInfo } from "../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "./client";
import { buildFeed, historyTargets, mergePage, withFollowUp, type FeedRow, type HistoryLoad } from "../features/sessions/feed";
import { upsertSummary } from "../features/backlog/model";
import { pollInterval as loopPollInterval } from "../features/workloop/model";
import { pollInterval as workstreamPollInterval } from "../features/workstreams/model";
import { toast } from "../ui/toast";
import { useNotifyState } from "../features/notify/notifier";

export const HISTORY_PAGE = 50;
/** Session-list poll while notifications are on (also in a hidden tab). */
export const FEED_POLL_NOTIFY_MS = 15_000;

export const queryKeys = {
  projects: ["projects"] as const,
  modes: ["modes"] as const,
  /** ListModels: "" is the daemon defaults, a session id its live assignment. */
  models: (sessionId: string) => ["models", sessionId] as const,
  sessionUsage: (project: string) => ["usage", "session-model", project] as const,
  /** GetUsage for the dashboard: scope ("" = all projects) and request. */
  usageReport: (project: string, groupBy: readonly string[], since: string, until: string, task: string) =>
    ["usage", "report", project, groupBy.join(","), since, until, task] as const,
  /** Every GetUsage result (a refresh revalidates them all). */
  usageAll: ["usage"] as const,
  /** GetSubscriptionUsage: provider-side allowance of OAuth accounts. */
  subscriptionUsage: ["subscriptionUsage"] as const,
  /** Every ListModels result (defaults and per-session). */
  modelsAll: ["models"] as const,
  /** GetModelConfig: one model's full record for editing. */
  modelConfig: (name: string) => ["modelConfig", name] as const,
  /** ListReviewTiers. */
  reviewTiers: ["reviewTiers"] as const,
  sessionFeed: (targets: string[]) => ["sessionFeed", targets] as const,
  sessionFeedAll: ["sessionFeed"] as const,
  /** ListBacklog for a project ("" resolves the sole/default project server-side). */
  backlog: (project: string) => ["backlog", project] as const,
  /** GetTask. */
  task: (project: string, id: string) => ["task", project, id] as const,
  /** GetWorkLoop: the project's loop snapshot (null when none has run). */
  workLoop: (project: string) => ["workLoop", project] as const,
  workLoopAll: ["workLoop"] as const,
  /** ListWorkstreams for a project ("" lists every project's). */
  workstreams: (project: string) => ["workstreams", project] as const,
  workstreamsAll: ["workstreams"] as const,
  budget: ["budget"] as const,
  /** GetMemory: memory.md plus its prompt-budget status. */
  memory: (project: string) => ["memory", project] as const,
  /** ListPlans. */
  plans: (project: string) => ["plans", project] as const,
  /** GetPlan. */
  plan: (project: string, name: string) => ["plan", project, name] as const,
  /** ListFiles of a directory, against a session's worktree ("" = the project root). */
  fileList: (project: string, sessionId: string, path: string) => ["files", "list", project, sessionId, path] as const,
  /** ReadFile. */
  fileRead: (project: string, sessionId: string, path: string) => ["files", "read", project, sessionId, path] as const,
  /** Every file listing and read (a browser refresh revalidates them). */
  filesAll: ["files"] as const,
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
  // With notifications on, keep polling while the tab is hidden (and a bit
  // faster): the list is how this client learns about other sessions.
  const background = useNotifyState().active;
  const loads = useQuery({
    queryKey: key,
    enabled: targets !== null,
    refetchInterval: background ? FEED_POLL_NOTIFY_MS : 30_000,
    refetchIntervalInBackground: background,
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
      if (failed.length) toast(`Couldn’t load older sessions for ${failed.join(", ")}.`, "error", { op: "sessions.load_older", err: "partial" });
    } finally {
      setLoadingOlder(false);
    }
  }, [qc, key, scope, loadingOlder]);

  const feed = loads.data ? buildFeed(loads.data, scope) : null;
  return {
    projects: projects.data as ProjectInfo[] | undefined,
    feed,
    /** Every project's loaded pages (unread baselining and notifications use them all). */
    loads: loads.data,
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

/** Optimistically change only the bookmark fields, including on rollback. */
export async function setSessionFollowUp(qc: QueryClient, row: FeedRow, followUp: boolean) {
  const { project, session } = row;
  const sessionId = session.sessionId;
  await qc.cancelQueries({ queryKey: queryKeys.sessionFeedAll });
  const previous = qc.getQueriesData<HistoryLoad[]>({ queryKey: queryKeys.sessionFeedAll }).map(([key, loads]) => {
    const load = loads?.find((l) => l.project === project);
    const old = [...(load?.sessions ?? []), ...(load?.pinned ?? [])].find((s) => s.sessionId === sessionId);
    return { key, old };
  });
  const patch = (flag: boolean, at: string) => qc.setQueriesData<HistoryLoad[]>(
    { queryKey: queryKeys.sessionFeedAll },
    (loads) => loads && withFollowUp(loads, project, sessionId, flag, at),
  );
  patch(followUp, followUp ? session.followUpAt || new Date().toISOString() : "");
  try {
    const result = await client.setSessionFollowUp({ project, sessionId, followUp });
    patch(result.followUp, result.followUpAt);
  } catch (err) {
    for (const { key, old } of previous) {
      if (old) qc.setQueryData<HistoryLoad[]>(key, (loads) => loads && withFollowUp(loads, project, sessionId, old.followUp, old.followUpAt));
    }
    toast(errorMessage(err, "Couldn’t update follow-up."), "error", { op: "sessions.follow_up", err });
  }
}

export function useSetFollowUp() {
  const qc = useQueryClient();
  const pending = useRef(new Set<string>());
  const [pendingIds, setPendingIds] = useState(new Set<string>());
  const id = (row: FeedRow) => `${row.project}\u0000${row.session.sessionId}`;
  const toggle = async (row: FeedRow) => {
    const key = id(row);
    if (pending.current.has(key)) return;
    pending.current.add(key);
    setPendingIds(new Set(pending.current));
    try {
      await setSessionFollowUp(qc, row, !row.session.followUp);
    } finally {
      pending.current.delete(key);
      setPendingIds(new Set(pending.current));
    }
  };
  return { toggle, isPending: (row: FeedRow) => pendingIds.has(id(row)) };
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

async function fetchWorkLoop(project: string, signal: AbortSignal): Promise<WorkLoopInfo | null> {
  return (await client.getWorkLoop({ project }, { signal })).loop ?? null;
}

/** GetWorkLoop, polled quickly while the loop is live. */
export function useWorkLoop(project: string, enabled = true) {
  return useQuery<WorkLoopInfo | null>({
    queryKey: queryKeys.workLoop(project),
    enabled,
    refetchInterval: (q) => loopPollInterval(q.state.data),
    queryFn: ({ signal }) => fetchWorkLoop(project, signal),
  });
}

/**
 * Every project's loop (one GetWorkLoop per distinct workspace), sharing the
 * per-project cache with the loop page: drives the sidebar indicator, loop
 * markers in session lists, and finish announcements. Failures read as "no
 * loop" here; the loop page reports them.
 */
export function useWorkLoops(): { project: string; loop: WorkLoopInfo | null }[] {
  const projects = useProjects();
  const targets = useMemo(() => (projects.data ? historyTargets(projects.data) : []), [projects.data]);
  const background = useNotifyState().active;
  const results = useQueries({
    queries: targets.map((project) => ({
      queryKey: queryKeys.workLoop(project),
      refetchInterval: (q: { state: { data?: WorkLoopInfo | null } }) => loopPollInterval(q.state.data),
      // Loop finish notifications need polling while the tab is hidden.
      refetchIntervalInBackground: background,
      queryFn: ({ signal }: { signal: AbortSignal }) => fetchWorkLoop(project, signal),
    })),
  });
  return targets.map((project, i) => ({ project, loop: results[i]?.data ?? null }));
}

/** ListWorkstreams, polled quickly while any row can still change by itself. */
export function useWorkstreams(project: string, enabled = true) {
  return useQuery<WorkstreamInfo[]>({
    queryKey: queryKeys.workstreams(project),
    enabled,
    refetchInterval: (q) => workstreamPollInterval(q.state.data),
    queryFn: async ({ signal }) => (await client.listWorkstreams({ project }, { signal })).workstreams,
  });
}

/** GetBudget: the configured caps a new work loop captures. */
export function useBudget(enabled = true) {
  return useQuery({
    queryKey: queryKeys.budget,
    enabled,
    staleTime: 60_000,
    queryFn: ({ signal }) => client.getBudget({}, { signal }),
  });
}

/** GetUsage for the dashboard. Usage logs only grow, so results stay fresh briefly. */
export function useUsageReport(req: { project: string; groupBy: string[]; since: string; until: string; task: string }, enabled = true) {
  return useQuery({
    queryKey: queryKeys.usageReport(req.project, req.groupBy, req.since, req.until, req.task),
    enabled,
    staleTime: 15_000,
    placeholderData: keepPreviousData,
    queryFn: ({ signal }) => client.getUsage(req, { signal }),
  });
}

let subscriptionRefreshed = false;

/**
 * GetSubscriptionUsage. The first load of the tab asks the daemon to refresh
 * (within its own throttling); later loads reuse its cache until the user
 * refreshes explicitly (refreshSubscriptionUsage).
 */
export function useSubscriptionUsage(enabled = true) {
  return useQuery({
    queryKey: queryKeys.subscriptionUsage,
    enabled,
    staleTime: 5 * 60_000,
    queryFn: async ({ signal }) => {
      const refresh = !subscriptionRefreshed;
      const resp = await client.getSubscriptionUsage({ refresh }, { signal });
      subscriptionRefreshed = true;
      return resp.accounts;
    },
  });
}

export async function refreshSubscriptionUsage(qc: QueryClient) {
  const resp = await client.getSubscriptionUsage({ refresh: true });
  subscriptionRefreshed = true;
  qc.setQueryData(queryKeys.subscriptionUsage, resp.accounts);
}

/** ListReviewTiers: effective tiers (built-ins overlaid) and the default. */
export function useReviewTiers(enabled = true) {
  return useQuery({
    queryKey: queryKeys.reviewTiers,
    enabled,
    queryFn: ({ signal }) => client.listReviewTiers({}, { signal }),
  });
}

/** GetMemory, polled slowly (agents write to it while sessions run). */
export function useMemory(project: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.memory(project),
    enabled,
    refetchInterval: 30_000,
    queryFn: ({ signal }) => client.getMemory({ project }, { signal }),
  });
}

/** ListPlans: the project's plan library (plans/*.md). */
export function usePlans(project: string, enabled = true) {
  return useQuery({
    queryKey: queryKeys.plans(project),
    enabled,
    queryFn: async ({ signal }) => (await client.listPlans({ project }, { signal })).plans,
  });
}

/** GetPlan: one plan's markdown. */
export function usePlan(project: string, name: string) {
  return useQuery({
    queryKey: queryKeys.plan(project, name),
    enabled: name !== "",
    queryFn: ({ signal }) => client.getPlan({ project, name }, { signal }),
  });
}
