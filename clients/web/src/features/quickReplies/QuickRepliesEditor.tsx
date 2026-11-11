// Editor for a list of quick replies: the daemon-wide list (Settings) or a
// project's extras (Projects page dialog). Rows keep their id so an edited
// default keeps its analytics id; order is the order clients offer them in.
// Icons are picked on iOS; rows keep theirs so a web save doesn't drop them.
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys } from "../../api/queries";
import { track } from "../../app/analytics";
import type { GetQuickRepliesResponse, QuickReply } from "../../gen/ycc/v1/ycc_pb";
import { toast } from "../../ui/toast";

export const MAX_QUICK_REPLIES = 12;
// follows: the message is a copy of the label made by typing the label.
type Row = { key: number; id: string; label: string; text: string; icon: string; follows?: boolean };
let rowKey = 0;
const toRows = (replies: readonly QuickReply[]): Row[] =>
  replies.map((r) => ({ key: ++rowKey, id: r.id, label: r.label, text: r.text, icon: r.icon }));
const same = (rows: Row[], replies: readonly QuickReply[]) =>
  rows.length === replies.length && rows.every((r, i) => r.label === replies[i].label && r.text === replies[i].text);

export type QuickReplyScope = { global: true } | { global: false; project: string };

export function QuickRepliesForm({
  scope,
  initial,
  intro,
  emptyText,
  onSaved,
  onCancel,
}: {
  scope: QuickReplyScope;
  initial: readonly QuickReply[];
  intro?: ReactNode;
  emptyText: string;
  onSaved?: () => void;
  /** Shown as Cancel (a dialog); without it, Revert appears once edited. */
  onCancel?: () => void;
}) {
  const qc = useQueryClient();
  const [saved, setSaved] = useState(initial);
  const [rows, setRows] = useState<Row[]>(() => toRows(initial));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const incomplete = rows.some((r) => !r.label.trim() || !r.text.trim());
  const dirty = !same(rows, saved);
  const update = (key: number, patch: Partial<Row>) => setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  // Typing a label into an empty message fills it in, and the message keeps
  // following the label until it is edited itself.
  const setLabel = (key: number, label: string) =>
    setRows((rs) =>
      rs.map((r) => (r.key !== key ? r : r.text === "" || r.follows ? { ...r, label, text: label, follows: true } : { ...r, label })),
    );
  const move = (index: number, by: number) =>
    setRows((rs) => {
      const next = [...rs];
      const [row] = next.splice(index, 1);
      next.splice(index + by, 0, row);
      return next;
    });
  const flow = scope.global ? "settings.quick_replies" : "projects.quick_replies";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || incomplete) return;
    setBusy(true);
    setError(null);
    try {
      const resp = await client.setQuickReplies({
        global: scope.global,
        project: scope.global ? "" : scope.project,
        replies: rows.map((r) => ({ id: r.id, label: r.label, text: r.text, icon: r.icon })),
      });
      const stored = resp.replies;
      if (scope.global) {
        qc.setQueriesData<GetQuickRepliesResponse>({ queryKey: queryKeys.quickRepliesAll }, (old) =>
          old ? { ...old, global: stored } : old,
        );
      } else {
        qc.setQueryData<GetQuickRepliesResponse>(queryKeys.quickReplies(scope.project), (old) =>
          old ? { ...old, project: stored } : old,
        );
      }
      void qc.invalidateQueries({ queryKey: queryKeys.quickRepliesAll });
      track.action(scope.global ? "settings.quick_replies.save" : "projects.quick_replies.save", "click", { count: stored.length });
      setSaved(stored);
      setRows(toRows(stored));
      setBusy(false);
      if (!onCancel) toast("Quick replies saved.", "info");
      onSaved?.();
    } catch (err) {
      setBusy(false);
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      track.error(flow, err);
      setError(errorMessage(err, "The quick replies could not be saved."));
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)} className="quick-replies-form">
      {intro && <p className="muted small">{intro}</p>}
      {rows.length === 0 && <p className="muted small">{emptyText}</p>}
      {rows.map((r, i) => (
        <fieldset key={r.key} className="quick-reply-edit" disabled={busy}>
          <label className="field-label">
            Label
            <input
              type="text"
              className="field"
              value={r.label}
              maxLength={32}
              placeholder="Deploy"
              autoFocus={i === rows.length - 1 && r.label === "" && r.text === ""}
              onChange={(e) => setLabel(r.key, e.target.value)}
            />
          </label>
          <label className="field-label">
            Message
            <textarea
              className="field"
              rows={2}
              value={r.text}
              placeholder="Deploy to prod following the deploy runbook"
              onChange={(e) => update(r.key, { text: e.target.value, follows: false })}
            />
          </label>
          <div className="quick-reply-row-actions">
            <button type="button" className="btn ghost small" aria-label={`Move ${r.label || "this reply"} up`} disabled={i === 0} onClick={() => move(i, -1)}>
              ↑
            </button>
            <button
              type="button"
              className="btn ghost small"
              aria-label={`Move ${r.label || "this reply"} down`}
              disabled={i === rows.length - 1}
              onClick={() => move(i, 1)}
            >
              ↓
            </button>
            <button
              type="button"
              className="btn ghost small"
              aria-label={`Remove ${r.label || "this reply"}`}
              onClick={() => setRows((rs) => rs.filter((x) => x.key !== r.key))}
            >
              Remove
            </button>
          </div>
        </fieldset>
      ))}
      <button
        type="button"
        className="btn ghost small"
        disabled={busy || rows.length >= MAX_QUICK_REPLIES}
        onClick={() => setRows((rs) => [...rs, { key: ++rowKey, id: "", label: "", text: "", icon: "" }])}
      >
        + Add reply
      </button>
      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}
      <div className="dialog-actions">
        {onCancel ? (
          <button type="button" className="btn ghost" onClick={onCancel}>
            Cancel
          </button>
        ) : (
          dirty && (
            <button type="button" className="btn ghost" disabled={busy} onClick={() => setRows(toRows(saved))}>
              Revert
            </button>
          )
        )}
        <button type="submit" className="btn primary" disabled={busy || incomplete || (!onCancel && !dirty)}>
          {busy ? "Saving…" : "Save"}
        </button>
      </div>
    </form>
  );
}
