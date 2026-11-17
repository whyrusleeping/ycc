// Git history logic: branch picker grouping, labels, and URL parameter;
// page flattening; commit-row stats and time hover text.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { BranchRefSchema, CommitSummarySchema, ListCommitsResponseSchema, type BranchRef } from "../src/gen/ycc/v1/ycc_pb";
import {
  branchOptionLabel,
  branchParam,
  commitStats,
  commitTimeTitle,
  flattenCommits,
  groupBranches,
  isMerge,
  pickerValue,
  shortSha,
} from "../src/features/history/model";
import { paths } from "../src/app/paths";

const br = (name: string, extra: MessageInitShape<typeof BranchRefSchema> = {}): BranchRef => create(BranchRefSchema, { name, sha: "abc", ...extra });
const commit = (sha: string, extra: MessageInitShape<typeof CommitSummarySchema> = {}) => create(CommitSummarySchema, { sha, subject: sha, ...extra });

describe("branch picker", () => {
  const branches = [
    br("ycc/ws/w1-0558", { workstreamId: "w1", taskId: "0558" }),
    br("main", { current: true }),
    br("feature"),
    br("ycc/ws/w2", { workstreamId: "w2" }),
  ];

  it("groups workstream branches apart, with the current branch first", () => {
    const { local, workstreams } = groupBranches(branches);
    expect(local.map((b) => b.name)).toEqual(["main", "feature"]);
    expect(workstreams.map((b) => b.name)).toEqual(["ycc/ws/w1-0558", "ycc/ws/w2"]);
  });

  it("keeps a checked-out workstream branch among the plain ones", () => {
    const { local, workstreams } = groupBranches([br("main"), br("ycc/ws/w1", { workstreamId: "w1", current: true })]);
    expect(local.map((b) => b.name)).toEqual(["ycc/ws/w1", "main"]);
    expect(workstreams).toEqual([]);
  });

  it("labels the current branch and workstreams with their task", () => {
    expect(branchOptionLabel(branches[1])).toBe("main — current");
    expect(branchOptionLabel(branches[2])).toBe("feature");
    expect(branchOptionLabel(branches[0])).toBe("ycc/ws/w1-0558 — workstream · 0558");
    expect(branchOptionLabel(branches[3])).toBe("ycc/ws/w2 — workstream");
  });

  it("maps choices to the URL: the checked-out branch is the default", () => {
    expect(branchParam("main", branches)).toBe("");
    expect(branchParam("feature", branches)).toBe("feature");
    expect(pickerValue("", "main")).toBe("main");
    expect(pickerValue("feature", "main")).toBe("feature");
    expect(pickerValue("", "")).toBe("");
    expect(paths.history("my proj", "ycc/ws/w1")).toBe("/p/my%20proj/history?branch=ycc%2Fws%2Fw1");
    expect(paths.history(null)).toBe("/history");
  });
});

describe("commit rows", () => {
  it("flattens pages newest first without repeating a sha", () => {
    const pages = [
      create(ListCommitsResponseSchema, { commits: [commit("c3"), commit("c2")], nextCursor: "x" }),
      create(ListCommitsResponseSchema, { commits: [commit("c2"), commit("c1")] }),
    ];
    expect(flattenCommits(pages).map((c) => c.sha)).toEqual(["c3", "c2", "c1"]);
  });

  it("formats stats, and omits them when git reported none", () => {
    expect(commitStats(commit("a", { filesChanged: 1, additions: 12n, deletions: 0n }))).toEqual({ additions: "+12", deletions: "\u22120", files: "1 file" });
    expect(commitStats(commit("a", { filesChanged: 3, additions: 1200n, deletions: 4n }))?.files).toBe("3 files");
    expect(commitStats(commit("a"))).toBeNull();
  });

  it("shows both timestamps only when they differ", () => {
    expect(commitTimeTitle(commit("a", { authoredAt: "2026-11-01T10:00:00Z", committedAt: "2026-11-01T10:00:00Z" }))).toMatch(/^Authored [^\n]+$/);
    expect(commitTimeTitle(commit("a", { authoredAt: "2026-11-01T10:00:00Z", committedAt: "2026-11-02T10:00:00Z" }))).toMatch(/^Authored .+\nCommitted .+$/);
    expect(commitTimeTitle(commit("a"))).toBe("");
  });

  it("shortens shas and spots merges", () => {
    expect(shortSha("0123456789abcdef")).toBe("0123456");
    expect(isMerge(commit("a", { parents: ["p1", "p2"] }))).toBe(true);
    expect(isMerge(commit("a", { parents: ["p1"] }))).toBe(false);
  });
});
