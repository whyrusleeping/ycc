// The task editor's draft: seeded from the canonical detail when editing
// starts, saved through UpdateTask as a partial update of only the fields the
// user changed. UpdateTask has no revision check, so before saving the editor
// re-reads the task (GetTask) and refuses — keeping the draft — when a field
// the user edited also changed on disk since the draft was seeded (another
// client, an agent, or a hand edit). Fields only the other side changed merge
// untouched. A successful save replaces local state with the response.
import { fromJson, toJson, type JsonValue, type MessageInitShape } from "@bufbuild/protobuf";
import { TaskDetailSchema, type TaskDetail, type UpdateTaskRequestSchema } from "../../gen/ycc/v1/ycc_pb";
import { normalizeTaskId, parseIdList, parseLines } from "./model";

export interface TaskDraft {
  title: string;
  priority: number;
  /** Dependency ids as typed ("0410, 0411"). */
  dependsOn: string;
  /** Spec references, one per line. */
  specRefs: string;
  body: string;
}

export type DraftField = keyof TaskDraft;

export const DRAFT_FIELDS: readonly DraftField[] = ["title", "priority", "dependsOn", "specRefs", "body"];

export const FIELD_LABELS: Record<DraftField, string> = {
  title: "title",
  priority: "priority",
  dependsOn: "dependencies",
  specRefs: "spec refs",
  body: "body",
};

export function draftFrom(t: TaskDetail): TaskDraft {
  return {
    title: t.title,
    priority: t.priority >= 1 && t.priority <= 5 ? t.priority : 3,
    dependsOn: t.dependsOn.join(", "),
    specRefs: t.specRefs.join("\n"),
    body: t.body,
  };
}

/** A field's comparable value (what UpdateTask would store). */
function draftValue(d: TaskDraft, f: DraftField): string {
  switch (f) {
    case "title":
      return d.title.trim();
    case "priority":
      return String(d.priority);
    case "dependsOn":
      return parseIdList(d.dependsOn).join("\n");
    case "specRefs":
      return parseLines(d.specRefs).join("\n");
    case "body":
      return d.body.trimEnd();
  }
}

function taskValue(t: TaskDetail, f: DraftField): string {
  switch (f) {
    case "title":
      return t.title.trim();
    case "priority":
      return String(t.priority);
    case "dependsOn":
      return t.dependsOn.map((s) => s.trim()).filter(Boolean).join("\n");
    case "specRefs":
      return t.specRefs.map((s) => s.trim()).filter(Boolean).join("\n");
    case "body":
      // Trailing whitespace is not a meaningful edit (the store may normalize it).
      return t.body.trimEnd();
  }
}

/** Fields the user changed relative to the detail the draft was seeded from. */
export function changedFields(base: TaskDetail, draft: TaskDraft): DraftField[] {
  return DRAFT_FIELDS.filter((f) => draftValue(draft, f) !== taskValue(base, f));
}

/** Fields that changed on the daemon between two reads of a task. */
export function serverChangedFields(base: TaskDetail, current: TaskDetail): DraftField[] {
  return DRAFT_FIELDS.filter((f) => taskValue(current, f) !== taskValue(base, f));
}

/** Fields both sides changed to different values: saving would clobber theirs. */
export function conflictingFields(base: TaskDetail, current: TaskDetail, draft: TaskDraft): DraftField[] {
  const theirs = new Set(serverChangedFields(base, current));
  return changedFields(base, draft).filter((f) => theirs.has(f) && draftValue(draft, f) !== taskValue(current, f));
}

/** Client-side validation matching UpdateTask's (title, 1..5, no self-dependency). */
export function validateDraft(draft: TaskDraft, taskId: string): string | null {
  if (!draft.title.trim()) return "Title is required.";
  if (!Number.isInteger(draft.priority) || draft.priority < 1 || draft.priority > 5) {
    return "Priority must be between 1 and 5.";
  }
  const self = normalizeTaskId(taskId);
  if (parseIdList(draft.dependsOn).some((d) => normalizeTaskId(d) === self)) return "A task cannot depend on itself.";
  return null;
}

export type UpdateTaskInit = MessageInitShape<typeof UpdateTaskRequestSchema>;

/** A partial UpdateTask carrying only `fields` from the draft. */
export function buildUpdate(project: string, id: string, draft: TaskDraft, fields: readonly DraftField[]): UpdateTaskInit {
  const req: UpdateTaskInit = { project, id };
  for (const f of fields) {
    switch (f) {
      case "title":
        req.title = draft.title.trim();
        break;
      case "priority":
        req.priority = draft.priority;
        break;
      case "body":
        req.body = draft.body;
        break;
      case "dependsOn":
        req.dependsOn = parseIdList(draft.dependsOn);
        req.replaceDependsOn = true;
        break;
      case "specRefs":
        req.specRefs = parseLines(draft.specRefs);
        req.replaceSpecRefs = true;
        break;
    }
  }
  return req;
}

export type SavePlan =
  | { kind: "invalid"; message: string }
  | { kind: "noop" }
  | { kind: "conflict"; fields: DraftField[]; current: TaskDetail }
  | { kind: "save"; request: UpdateTaskInit; fields: DraftField[] };

/**
 * Decide what a save does. `current` is a fresh read of the task taken just
 * before saving; `force` (the user chose to overwrite) skips the conflict
 * check and writes every field they changed.
 */
export function planSave(args: {
  project: string;
  id: string;
  base: TaskDetail;
  current: TaskDetail | null;
  draft: TaskDraft;
  force?: boolean;
}): SavePlan {
  const invalid = validateDraft(args.draft, args.id);
  if (invalid) return { kind: "invalid", message: invalid };
  const mine = changedFields(args.base, args.draft);
  if (!mine.length) return { kind: "noop" };
  if (!args.force && args.current) {
    const fields = conflictingFields(args.base, args.current, args.draft);
    if (fields.length) return { kind: "conflict", fields, current: args.current };
  }
  return { kind: "save", request: buildUpdate(args.project, args.id, args.draft, mine), fields: mine };
}

export function conflictMessage(id: string, fields: readonly DraftField[]): string {
  const list = fields.map((f) => FIELD_LABELS[f]).join(", ");
  return `Task ${id} changed since you started editing (${list}). Your draft is kept: overwrite with it, or discard it and load the current version.`;
}

/**
 * Open drafts by task, kept across navigation (switching tasks in the table,
 * closing the pane) and reloads for the life of the tab (sessionStorage), so
 * an edit is never silently lost.
 */
export interface OpenDraft {
  base: TaskDetail;
  draft: TaskDraft;
}

const STORAGE_KEY = "ycc.taskDrafts";

interface StoredDraft {
  base: JsonValue;
  draft: TaskDraft;
}

function storage(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

function load(): Map<string, OpenDraft> {
  const out = new Map<string, OpenDraft>();
  try {
    const raw = storage()?.getItem(STORAGE_KEY);
    if (!raw) return out;
    const parsed = JSON.parse(raw) as Record<string, StoredDraft>;
    for (const [key, v] of Object.entries(parsed)) {
      out.set(key, { base: fromJson(TaskDetailSchema, v.base), draft: v.draft });
    }
  } catch {
    // A corrupt entry is dropped rather than breaking the editor.
  }
  return out;
}

function save(map: Map<string, OpenDraft>) {
  const s = storage();
  if (!s) return;
  try {
    const out: Record<string, StoredDraft> = {};
    for (const [key, v] of map) out[key] = { base: toJson(TaskDetailSchema, v.base), draft: v.draft };
    if (map.size) s.setItem(STORAGE_KEY, JSON.stringify(out));
    else s.removeItem(STORAGE_KEY);
  } catch {
    // Quota or private mode: the in-memory copy still covers navigation.
  }
}

const drafts = load();

export function draftKey(project: string, id: string): string {
  return `${project}\u0000${id}`;
}

export const draftStore = {
  get: (project: string, id: string): OpenDraft | undefined => drafts.get(draftKey(project, id)),
  set: (project: string, id: string, value: OpenDraft) => {
    drafts.set(draftKey(project, id), value);
    save(drafts);
  },
  delete: (project: string, id: string) => {
    if (drafts.delete(draftKey(project, id))) save(drafts);
  },
  has: (project: string, id: string) => drafts.has(draftKey(project, id)),
};
