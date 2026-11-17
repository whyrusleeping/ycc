// Git history (ListCommits) presentation: the branch picker's grouping and
// labels, the URL's branch parameter, and commit-row formatting.
import type { BranchRef, CommitSummary, ListCommitsResponse } from "../../gen/ycc/v1/ycc_pb";

/** Commits per ListCommits page. */
export const COMMITS_PAGE = 50;

export function shortSha(sha: string): string {
  return sha.slice(0, 7);
}

/**
 * The picker's two groups: plain branches (the checked-out one first, then
 * the daemon's most-recent-tip order) and in-flight workstream branches. A
 * checked-out workstream branch stays first among the plain ones.
 */
export function groupBranches(branches: readonly BranchRef[]): { local: BranchRef[]; workstreams: BranchRef[] } {
  const local: BranchRef[] = [];
  const workstreams: BranchRef[] = [];
  for (const b of branches) (b.workstreamId && !b.current ? workstreams : local).push(b);
  local.sort((a, b) => Number(b.current) - Number(a.current));
  return { local, workstreams };
}

/** One picker option's text (a native option cannot hold tags). */
export function branchOptionLabel(b: BranchRef): string {
  const notes: string[] = [];
  if (b.current) notes.push("current");
  if (b.workstreamId) notes.push(b.taskId ? `workstream · ${b.taskId}` : "workstream");
  return notes.length ? `${b.name} — ${notes.join(" · ")}` : b.name;
}

/** The picker's value: the requested branch, else the resolved HEAD branch ("" = detached HEAD). */
export function pickerValue(requested: string, resolved: string): string {
  return requested || resolved;
}

/** The URL's branch for a picker choice: the checked-out branch is the default (no parameter). */
export function branchParam(choice: string, branches: readonly BranchRef[]): string {
  return branches.find((b) => b.name === choice)?.current ? "" : choice;
}

/** Every loaded page's commits, newest first; a sha repeated across pages shows once. */
export function flattenCommits(pages: readonly ListCommitsResponse[]): CommitSummary[] {
  const seen = new Set<string>();
  const out: CommitSummary[] = [];
  for (const p of pages) {
    for (const c of p.commits) {
      if (seen.has(c.sha)) continue;
      seen.add(c.sha);
      out.push(c);
    }
  }
  return out;
}

export interface CommitStats {
  additions: string;
  deletions: string;
  files: string;
}

/** The row's shortstat ("+12", "−3", "4 files"); null when git reported none (e.g. an empty or merge commit). */
export function commitStats(c: Pick<CommitSummary, "filesChanged" | "additions" | "deletions">): CommitStats | null {
  if (c.filesChanged === 0 && c.additions === 0n && c.deletions === 0n) return null;
  return {
    additions: `+${c.additions.toLocaleString()}`,
    deletions: `\u2212${c.deletions.toLocaleString()}`,
    files: `${c.filesChanged} ${c.filesChanged === 1 ? "file" : "files"}`,
  };
}

/** Hover text for the row's time: both timestamps when a rebase or amend split them. */
export function commitTimeTitle(c: Pick<CommitSummary, "authoredAt" | "committedAt">): string {
  const fmt = (iso: string) => {
    const t = Date.parse(iso);
    return Number.isNaN(t) ? "" : new Date(t).toLocaleString();
  };
  const authored = fmt(c.authoredAt);
  const committed = fmt(c.committedAt);
  if (!authored) return committed ? `Committed ${committed}` : "";
  if (!committed || committed === authored) return `Authored ${authored}`;
  return `Authored ${authored}\nCommitted ${committed}`;
}

export function isMerge(c: Pick<CommitSummary, "parents">): boolean {
  return c.parents.length > 1;
}
