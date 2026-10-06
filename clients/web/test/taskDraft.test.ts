// Task editor drafts: changed-field detection, partial UpdateTask requests,
// validation, and conflict detection against a fresh read (the draft is never
// discarded by a failed save).
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import { TaskDetailSchema, type TaskDetail } from "../src/gen/ycc/v1/ycc_pb";
import {
  buildUpdate,
  changedFields,
  conflictMessage,
  conflictingFields,
  draftFrom,
  draftStore,
  planSave,
  serverChangedFields,
  validateDraft,
} from "../src/features/backlog/draft";

function detail(extra: MessageInitShape<typeof TaskDetailSchema> = {}): TaskDetail {
  return create(TaskDetailSchema, {
    id: "0042",
    title: "Retry fetch",
    status: "todo",
    priority: 3,
    dependsOn: ["0040"],
    specRefs: ["Backlog browser"],
    body: "## Description\nRetry.\n\n## Work log\n",
    ...extra,
  });
}

describe("task drafts", () => {
  it("seeds from the canonical detail and detects no changes", () => {
    const base = detail();
    const d = draftFrom(base);
    expect(d).toEqual({
      title: "Retry fetch",
      priority: 3,
      dependsOn: "0040",
      specRefs: "Backlog browser",
      body: "## Description\nRetry.\n\n## Work log\n",
    });
    expect(changedFields(base, d)).toEqual([]);
    // Whitespace-only differences are not edits.
    expect(changedFields(base, { ...d, title: " Retry fetch ", dependsOn: "0040, ", body: d.body + "\n\n" })).toEqual([]);
  });

  it("builds a partial update carrying only the changed fields", () => {
    const base = detail();
    const draft = { ...draftFrom(base), title: "Retry fetch on 5xx ", dependsOn: "0040, 0041", specRefs: "" };
    const fields = changedFields(base, draft);
    expect(fields).toEqual(["title", "dependsOn", "specRefs"]);
    expect(buildUpdate("p", "0042", draft, fields)).toEqual({
      project: "p",
      id: "0042",
      title: "Retry fetch on 5xx",
      dependsOn: ["0040", "0041"],
      replaceDependsOn: true,
      specRefs: [],
      replaceSpecRefs: true,
    });
  });

  it("validates title, priority, and self-dependency", () => {
    const d = draftFrom(detail());
    expect(validateDraft(d, "0042")).toBeNull();
    expect(validateDraft({ ...d, title: "  " }, "0042")).toBe("Title is required.");
    expect(validateDraft({ ...d, priority: 6 }, "0042")).toMatch(/between 1 and 5/);
    expect(validateDraft({ ...d, dependsOn: "0040, 42" }, "0042")).toMatch(/itself/);
  });

  it("refuses an invalid draft without a request", () => {
    const base = detail();
    const plan = planSave({ project: "p", id: "0042", base, current: base, draft: { ...draftFrom(base), title: "" } });
    expect(plan).toEqual({ kind: "invalid", message: "Title is required." });
  });

  it("treats an unchanged draft as a no-op", () => {
    const base = detail();
    expect(planSave({ project: "p", id: "0042", base, current: base, draft: draftFrom(base) }).kind).toBe("noop");
  });

  it("merges when the other side changed different fields", () => {
    const base = detail();
    // Someone else edited the body (an agent appended to the work log) and the status.
    const current = detail({ body: base.body + "- 2026-10-05: progress\n", status: "in_progress" });
    const draft = { ...draftFrom(base), title: "Retry fetch on 5xx" };
    expect(serverChangedFields(base, current)).toEqual(["body"]);
    const plan = planSave({ project: "p", id: "0042", base, current, draft });
    expect(plan.kind).toBe("save");
    if (plan.kind === "save") {
      // The body is not sent, so their work-log entry survives.
      expect(plan.request).toEqual({ project: "p", id: "0042", title: "Retry fetch on 5xx" });
    }
  });

  it("reports a conflict, keeping the draft, when both sides changed the same field", () => {
    const base = detail();
    const current = detail({ body: "## Description\nEdited elsewhere.\n" });
    const draft = { ...draftFrom(base), body: "## Description\nMy edit.\n", priority: 1 };
    expect(conflictingFields(base, current, draft)).toEqual(["body"]);
    const plan = planSave({ project: "p", id: "0042", base, current, draft });
    expect(plan.kind).toBe("conflict");
    if (plan.kind === "conflict") {
      expect(plan.fields).toEqual(["body"]);
      expect(plan.current).toBe(current);
      expect(conflictMessage("0042", plan.fields)).toMatch(/changed since you started editing \(body\)\. Your draft is kept/);
    }
    // The draft object is untouched by planning.
    expect(draft.body).toBe("## Description\nMy edit.\n");
  });

  it("does not conflict when both sides made the same change", () => {
    const base = detail();
    const current = detail({ title: "Same new title" });
    const draft = { ...draftFrom(base), title: "Same new title" };
    expect(conflictingFields(base, current, draft)).toEqual([]);
  });

  it("overwrites on request, sending every field the user changed", () => {
    const base = detail();
    const current = detail({ body: "theirs", title: "Their title" });
    const draft = { ...draftFrom(base), body: "mine" };
    const plan = planSave({ project: "p", id: "0042", base, current, draft, force: true });
    expect(plan).toMatchObject({ kind: "save", fields: ["body"], request: { body: "mine" } });
  });

  it("saves without a conflict check when the fresh read failed", () => {
    const base = detail();
    const draft = { ...draftFrom(base), priority: 2 };
    expect(planSave({ project: "p", id: "0042", base, current: null, draft })).toMatchObject({
      kind: "save",
      request: { priority: 2 },
    });
  });

  it("keeps open drafts per task across navigation", () => {
    const base = detail();
    const open = { base, draft: { ...draftFrom(base), title: "WIP" } };
    draftStore.set("p", "0042", open);
    expect(draftStore.get("p", "0042")).toBe(open);
    expect(draftStore.has("q", "0042")).toBe(false);
    draftStore.delete("p", "0042");
    expect(draftStore.get("p", "0042")).toBeUndefined();
  });

  it("persists open drafts for the tab (sessionStorage) across reloads", async () => {
    const mem = new Map<string, string>();
    vi.stubGlobal("sessionStorage", {
      getItem: (k: string) => mem.get(k) ?? null,
      setItem: (k: string, v: string) => void mem.set(k, v),
      removeItem: (k: string) => void mem.delete(k),
    });
    try {
      vi.resetModules();
      const first = await import("../src/features/backlog/draft");
      const base = detail();
      first.draftStore.set("p", "0042", { base, draft: { ...first.draftFrom(base), title: "WIP after reload" } });
      vi.resetModules();
      const second = await import("../src/features/backlog/draft");
      const restored = second.draftStore.get("p", "0042");
      expect(restored?.draft.title).toBe("WIP after reload");
      expect(restored?.base.body).toBe(base.body);
      expect(restored?.base.dependsOn).toEqual(["0040"]);
      second.draftStore.delete("p", "0042");
      expect(mem.size).toBe(0);
    } finally {
      vi.unstubAllGlobals();
      vi.resetModules();
    }
  });
});
