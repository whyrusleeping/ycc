// New task (CreateTask): title, priority, dependencies, spec refs, and a
// markdown description the daemon scaffolds into the canonical sections. A
// failure keeps the form; success opens the created task (its page by
// default, or wherever `onCreated` puts it, e.g. the session inspector).
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import type { TaskDetail } from "../../gen/ycc/v1/ycc_pb";
import { authStore } from "../../api/auth";
import { installTask } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { parseIdList, parseLines } from "./model";

interface NewTaskForm {
  title: string;
  priority: number;
  dependsOn: string;
  specRefs: string;
  body: string;
}

const EMPTY: NewTaskForm = { title: "", priority: 3, dependsOn: "", specRefs: "", body: "" };

export function NewTaskDialog({
  project,
  open,
  onClose,
  onCreated,
}: {
  project: string;
  open: boolean;
  onClose: () => void;
  /** Show the created task (default: navigate to its page). */
  onCreated?: (task: TaskDetail) => void;
}) {
  const [form, setForm] = useState<NewTaskForm>(EMPTY);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const set = <K extends keyof NewTaskForm>(k: K, v: NewTaskForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    setError(null);
  };

  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy) return;
    if (!form.title.trim()) {
      setError("Title is required.");
      track.error("backlog.create", "required");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const resp = await client.createTask({
        project,
        title: form.title.trim(),
        body: form.body.trim(),
        priority: form.priority,
        dependsOn: parseIdList(form.dependsOn),
        specRefs: parseLines(form.specRefs),
      });
      const t = resp.task;
      track.submit("new_task");
      setForm(EMPTY);
      onClose();
      if (t) {
        installTask(qc, project, t);
        toast(`Created task ${t.id}: ${t.title}`, "info");
        if (onCreated) onCreated(t);
        else navigate(paths.task(project, t.id));
      }
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error("backlog.create", err);
      setError(`Not created: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={open} onClose={() => !busy && onClose()} title="New task" className="task-form-dialog" view="new_task" flow="new_task">
      <form
        className="task-editor"
        onSubmit={(e) => void submit(e)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
            e.preventDefault();
            void submit();
          }
        }}
      >
        {project && <p className="muted small">In {project}</p>}
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        <label className="field-label">
          Title
          <input
            className="field"
            value={form.title}
            placeholder="Short, imperative title"
            onChange={(e) => set("title", e.target.value)}
            autoFocus
          />
        </label>
        <div className="editor-row">
          <label className="field-label">
            Priority
            <select value={form.priority} onChange={(e) => set("priority", Number(e.target.value))}>
              {[1, 2, 3, 4, 5].map((p) => (
                <option key={p} value={p}>
                  P{p}
                  {p === 1 ? " · highest" : p === 5 ? " · lowest" : ""}
                </option>
              ))}
            </select>
          </label>
          <label className="field-label grow">
            Depends on
            <input
              className="field mono"
              value={form.dependsOn}
              placeholder="task ids, e.g. 0410, 0411"
              onChange={(e) => set("dependsOn", e.target.value)}
            />
          </label>
        </div>
        <label className="field-label">
          Spec refs <span className="muted small">one per line</span>
          <textarea rows={2} value={form.specRefs} onChange={(e) => set("specRefs", e.target.value)} />
        </label>
        <label className="field-label">
          Description <span className="muted small">Markdown · acceptance criteria welcome</span>
          <textarea className="mono" rows={8} value={form.body} onChange={(e) => set("body", e.target.value)} />
        </label>
        <div className="editor-actions">
          <span className="muted small">New tasks start as todo · Ctrl/⌘+Enter creates</span>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {busy ? "Creating…" : "Create task"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
