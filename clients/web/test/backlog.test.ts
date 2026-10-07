// Backlog browser logic: statuses and the actionable flag, filter/sort,
// keyboard cursor, list parsing, the work-log split, and task-file links.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { BacklogTaskSummarySchema, TaskDetailSchema, type BacklogTaskSummary } from "../src/gen/ycc/v1/ycc_pb";
import {
  DEFAULT_FILTER,
  DEFAULT_SORT,
  adjacentStatus,
  blockedLabel,
  boardColumns,
  moveBoardCursor,
  compareIds,
  filterTasks,
  isActionable,
  moveCursor,
  normalizeTaskId,
  parseIdList,
  parseLines,
  readiness,
  sortTasks,
  splitWorkLog,
  statusCounts,
  taskIdFromPath,
  toggleSort,
  upsertSummary,
  withStatus,
} from "../src/features/backlog/model";
import { fromLink } from "../src/features/files/fileReference";

function t(id: string, extra: MessageInitShape<typeof BacklogTaskSummarySchema> = {}): BacklogTaskSummary {
  return create(BacklogTaskSummarySchema, { id, title: `Task ${id}`, status: "todo", priority: 3, ready: true, ...extra });
}

const ids = (rows: { id: string }[]) => rows.map((r) => r.id);

const tasks = [
  t("0001", { status: "done", priority: 2, title: "Ship the scaffold" }),
  t("0002", { status: "todo", priority: 1, title: "Wire the API" }),
  t("0003", { status: "proposed", priority: 3, title: "Idea: dark mode" }),
  t("0004", { status: "todo", priority: 2, dependsOn: ["0003"], ready: false, blockedBy: ["0003"], title: "Theme picker" }),
  t("0005", { status: "in_progress", priority: 4, dependsOn: ["0001"], title: "Logging" }),
  t("0006", { status: "blocked", priority: 2, title: "Waiting on vendor" }),
  t("0010", { status: "in_review", priority: 3, title: "Search" }),
];

describe("backlog model", () => {
  it("marks only ready todo/in-progress work actionable (docs.EligibilityFor)", () => {
    expect(tasks.filter(isActionable).map((x) => x.id)).toEqual(["0002", "0005"]);
    expect(isActionable(t("x", { status: "proposed", ready: true }))).toBe(false);
    expect(isActionable(t("x", { status: "todo", ready: false }))).toBe(false);
  });

  it("labels readiness by dependency state, then lifecycle gate", () => {
    const label = (id: string) => readiness(tasks.find((x) => x.id === id)!);
    expect(label("0002")).toMatchObject({ kind: "actionable" });
    expect(label("0004")).toMatchObject({ kind: "blocked", title: "Blocked by 0003" });
    expect(label("0003")).toMatchObject({ kind: "gated", label: "needs promotion" });
    expect(label("0006")).toMatchObject({ kind: "gated", label: "on hold" });
    expect(label("0010")).toMatchObject({ kind: "gated", label: "awaiting review" });
    expect(label("0001")).toMatchObject({ kind: "none", label: "" });
  });

  it("annotates blocked rows but never done ones", () => {
    expect(blockedLabel(tasks[3])).toBe("Blocked by 0003");
    expect(blockedLabel(t("x", { status: "done", ready: false, blockedBy: ["1"] }))).toBeNull();
    expect(blockedLabel(tasks[1])).toBeNull();
  });

  it("normalizes and orders ids like the store", () => {
    expect(normalizeTaskId(" 42 ")).toBe("0042");
    expect(normalizeTaskId("abc")).toBe("abc");
    expect(["10", "9", "b", "a", "0011"].sort(compareIds)).toEqual(["9", "10", "0011", "a", "b"]);
  });

  it("hides done by default and shows it on request or when filtered for", () => {
    expect(ids(filterTasks(tasks, DEFAULT_FILTER))).not.toContain("0001");
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, showDone: true }))).toContain("0001");
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, statuses: ["done"] }))).toEqual(["0001"]);
  });

  it("filters by status set, actionable flag, and text terms", () => {
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, statuses: ["todo", "proposed"] }))).toEqual(["0002", "0003", "0004"]);
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, actionableOnly: true }))).toEqual(["0002", "0005"]);
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, text: "THEME" }))).toEqual(["0004"]);
    // A dependency id matches its dependents; a bare number matches the padded id.
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, text: "0003" }))).toEqual(["0003", "0004"]);
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, text: "#10" }))).toEqual(["0010"]);
    // Every term must match.
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, text: "the api" }))).toEqual(["0002"]);
    expect(ids(filterTasks(tasks, { ...DEFAULT_FILTER, text: "in review" }))).toEqual(["0010"]);
  });

  it("sorts by status rank then priority by default, ties newest first", () => {
    const rows = sortTasks([...tasks, t("0007", { status: "todo", priority: 1 })], DEFAULT_SORT);
    expect(ids(rows)).toEqual(["0005", "0010", "0007", "0002", "0004", "0006", "0003", "0001"]);
  });

  it("sorts by each column in both directions", () => {
    expect(ids(sortTasks(tasks, { key: "id", dir: "desc" }))[0]).toBe("0010");
    expect(ids(sortTasks(tasks, { key: "id", dir: "asc" }))[0]).toBe("0001");
    expect(ids(sortTasks(tasks, { key: "priority", dir: "asc" })).slice(0, 4)).toEqual(["0002", "0006", "0004", "0001"]);
    expect(ids(sortTasks(tasks, { key: "title", dir: "asc" }))[0]).toBe("0003");
    expect(ids(sortTasks(tasks, { key: "deps", dir: "desc" }))[0]).toBe("0004");
    expect(ids(sortTasks(tasks, { key: "actionable", dir: "asc" })).slice(0, 2)).toEqual(["0005", "0002"]);
  });

  it("toggles a column's direction and starts new columns in their default", () => {
    expect(toggleSort({ key: "status", dir: "asc" }, "status")).toEqual({ key: "status", dir: "desc" });
    expect(toggleSort({ key: "status", dir: "asc" }, "id")).toEqual({ key: "id", dir: "desc" });
    expect(toggleSort({ key: "id", dir: "desc" }, "priority")).toEqual({ key: "priority", dir: "asc" });
  });

  it("counts statuses over the whole list", () => {
    expect(statusCounts(tasks)).toMatchObject({ todo: 2, done: 1, proposed: 1 });
  });

  it("moves the keyboard cursor with clamping", () => {
    const list = ["a", "b", "c"];
    expect(moveCursor(list, null, 1)).toBe("a");
    expect(moveCursor(list, null, -1)).toBe("c");
    expect(moveCursor(list, "a", 1)).toBe("b");
    expect(moveCursor(list, "c", 1)).toBe("c");
    expect(moveCursor(list, "a", -1)).toBe("a");
    expect(moveCursor(list, "gone", 1)).toBe("a");
    expect(moveCursor([], "a", 1)).toBeNull();
  });

  it("parses dependency and spec-ref lists", () => {
    expect(parseIdList(" 0410, 0411\n0412  0410,,")).toEqual(["0410", "0411", "0412"]);
    expect(parseLines("Backlog browser\n\n docs/design/web-client.md#Data access, more \nBacklog browser")).toEqual([
      "Backlog browser",
      "docs/design/web-client.md#Data access, more",
    ]);
  });

  it("splits the work log off the body, ignoring headings in code fences", () => {
    const body = "## Description\nDo it.\n\n```md\n## Work log\n```\n\n## Work log\n- 2026-10-05: started\n";
    expect(splitWorkLog(body)).toEqual({
      main: "## Description\nDo it.\n\n```md\n## Work log\n```",
      workLog: "- 2026-10-05: started",
    });
    expect(splitWorkLog("## Description\nNo log here.\n")).toEqual({ main: "## Description\nNo log here.", workLog: null });
    expect(splitWorkLog("## Work Log\n")).toEqual({ main: "", workLog: "" });
  });

  it("recognizes backlog task files as task links", () => {
    expect(taskIdFromPath("backlog/0412-desktop-web-rich.md")).toBe("0412");
    expect(taskIdFromPath("backlog/7.md")).toBe("0007");
    expect(taskIdFromPath("backlog/README.md")).toBeNull();
    expect(taskIdFromPath("docs/backlog/0412-x.md")).toBeNull();
    expect(taskIdFromPath("backlog/0412-x.go")).toBeNull();
    // Relative links in a task body resolve against backlog/.
    const ctx = { project: "p", sessionId: "", baseDirectory: "backlog", absoluteRoots: ["/w/p"] };
    expect(taskIdFromPath(fromLink("0411-new-session.md", ctx)!.path)).toBe("0411");
    expect(taskIdFromPath(fromLink("/w/p/backlog/0410-shell.md", ctx)!.path)).toBe("0410");
    expect(fromLink("../../etc/passwd", ctx)).toBeNull();
  });

  it("replaces or appends a row from a canonical detail", () => {
    const detail = create(TaskDetailSchema, { id: "0003", title: "Dark mode", status: "todo", priority: 2, ready: true });
    const next = upsertSummary(tasks, detail);
    expect(next.find((x) => x.id === "0003")).toMatchObject({ status: "todo", title: "Dark mode", priority: 2 });
    expect(next).toHaveLength(tasks.length);
    expect(tasks.find((x) => x.id === "0003")!.status).toBe("proposed");
    expect(upsertSummary(tasks, create(TaskDetailSchema, { id: "0099", title: "New" }))).toHaveLength(tasks.length + 1);
  });

  it("groups the board by status in workflow order, done collapsed until shown", () => {
    const cols = boardColumns(
      [...tasks, t("0011", { status: "todo", priority: 1 }), t("0012", { status: "done" }), t("0013", { status: "parked" })],
      DEFAULT_FILTER,
    );
    expect(cols.map((c) => c.status)).toEqual(["proposed", "todo", "in_progress", "in_review", "blocked", "done", "parked"]);
    // Priority first, then newest.
    expect(ids(cols[1].tasks)).toEqual(["0011", "0002", "0004"]);
    // Done is a collapsed drop target that still counts its cards (newest first).
    expect(cols[5]).toMatchObject({ collapsed: true });
    expect(ids(cols[5].tasks)).toEqual(["0012", "0001"]);
    expect(boardColumns(tasks, { ...DEFAULT_FILTER, showDone: true }).find((c) => c.status === "done")!.collapsed).toBe(false);
  });

  it("narrows board columns and cards by the filter", () => {
    const byStatus = boardColumns(tasks, { ...DEFAULT_FILTER, statuses: ["done", "todo"] });
    expect(byStatus.map((c) => [c.status, c.collapsed])).toEqual([
      ["todo", false],
      ["done", false],
    ]);
    const actionable = boardColumns(tasks, { ...DEFAULT_FILTER, actionableOnly: true });
    expect(actionable.flatMap((c) => ids(c.tasks))).toEqual(["0002", "0005"]);
    expect(actionable).toHaveLength(6);
    const text = boardColumns(tasks, { ...DEFAULT_FILTER, text: "vendor" });
    expect(text.flatMap((c) => ids(c.tasks))).toEqual(["0006"]);
  });

  it("moves the board cursor within and across non-empty columns", () => {
    const cols = [["a1", "a2", "a3"], [], ["c1"], ["d1", "d2"]];
    expect(moveBoardCursor(cols, null, "down")).toBe("a1");
    expect(moveBoardCursor(cols, "gone", "left")).toBe("a1");
    expect(moveBoardCursor(cols, "a1", "up")).toBe("a1");
    expect(moveBoardCursor(cols, "a2", "down")).toBe("a3");
    expect(moveBoardCursor(cols, "a3", "down")).toBe("a3");
    // Skips the empty column; clamps the row.
    expect(moveBoardCursor(cols, "a3", "right")).toBe("c1");
    expect(moveBoardCursor(cols, "c1", "right")).toBe("d1");
    expect(moveBoardCursor(cols, "d2", "right")).toBe("d2");
    expect(moveBoardCursor(cols, "d2", "left")).toBe("c1");
    expect(moveBoardCursor([[], []], null, "down")).toBeNull();
  });

  it("steps a card through the workflow statuses", () => {
    expect(adjacentStatus("todo", 1)).toBe("in_progress");
    expect(adjacentStatus("TODO", -1)).toBe("proposed");
    expect(adjacentStatus("proposed", -1)).toBeNull();
    expect(adjacentStatus("done", 1)).toBeNull();
    expect(adjacentStatus("parked", 1)).toBeNull();
    const moved = withStatus(tasks[1], "in_progress");
    expect(moved).toMatchObject({ id: "0002", status: "in_progress", title: "Wire the API" });
    expect(tasks[1].status).toBe("todo");
  });
});
