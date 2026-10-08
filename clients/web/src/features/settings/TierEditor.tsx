// Add / edit one review tier: strategy, the "when to pick me" description,
// the tier-wide reviewer prompt, and reviewer slots — each with its own model,
// label, focus prompt, and thinking override. Mirrors iOS ReviewTierEditorView.
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys } from "../../api/queries";
import { Modal } from "../../ui/Modal";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { SLOT_THINKING, STRATEGIES, moveSlot, newSlot, tierPayload, tierProblem, type SlotDraft, type TierDraft } from "./tiers";

export function TierEditorDialog({
  draft,
  modelNames,
  tierNames,
  onClose,
}: {
  draft: TierDraft | null;
  /** Enabled logical models (slot pickers). */
  modelNames: readonly string[];
  tierNames: readonly string[];
  onClose: () => void;
}) {
  const title = !draft ? "" : draft.existing ? `Review tier · ${draft.name}` : "New review tier";
  return (
    <Modal open={draft !== null} onClose={onClose} title={title} className="tier-editor-dialog" view="tier_editor" flow="tier_editor">
      {draft && <TierEditor key={draft.existing ? draft.name : "new"} initial={draft} modelNames={modelNames} tierNames={tierNames} onClose={onClose} />}
    </Modal>
  );
}

function TierEditor({
  initial,
  modelNames,
  tierNames,
  onClose,
}: {
  initial: TierDraft;
  modelNames: readonly string[];
  tierNames: readonly string[];
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [d, setD] = useState<TierDraft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const problem = tierProblem(d, tierNames);
  const set = <K extends keyof TierDraft>(k: K, v: TierDraft[K]) => setD((prev) => ({ ...prev, [k]: v }));
  const setSlot = (key: number, patch: Partial<SlotDraft>) =>
    setD((prev) => ({ ...prev, slots: prev.slots.map((s) => (s.key === key ? { ...s, ...patch } : s)) }));

  const save = async () => {
    if (problem || saving) return;
    setSaving(true);
    setError(null);
    try {
      await client.upsertReviewTier({ tier: tierPayload(d) });
      void qc.invalidateQueries({ queryKey: queryKeys.reviewTiers });
      toast(`Saved review tier ${d.name.trim()}.`, "info");
      track.submit("tier_editor", { existing: !!initial.existing });
      onClose();
    } catch (err) {
      if (isUnauthorized(err)) authStore.expire();
      else {
        track.error("tier_editor.save", err);
        setError(errorMessage(err, "The tier was not saved."));
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <form
      className="task-editor tier-editor"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      {!d.existing && (
        <label className="field-label">
          Name
          <input className="field mono" value={d.name} autoFocus spellCheck={false} onChange={(e) => set("name", e.target.value)} />
        </label>
      )}
      {d.builtin && (
        <p className="muted small">
          A built-in tier. Saving stores an override in ycc.toml; “Revert” on the tier list restores the built-in behaviour.
        </p>
      )}
      <fieldset className="loop-options">
        <legend>Strategy</legend>
        {STRATEGIES.map((s) => (
          <label key={s.value} className="radio-row">
            <input type="radio" name="tier-strategy" checked={d.strategy === s.value} onChange={() => set("strategy", s.value)} />
            <span>
              <strong>{s.title}</strong> <span className="muted small">{s.detail}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <label className="field-label">
        <span>
          Description <span className="muted">(shown to the coordinator when it picks a tier)</span>
        </span>
        <textarea rows={2} value={d.description} onChange={(e) => set("description", e.target.value)} />
      </label>
      {d.strategy === "agents" && (
        <>
          <label className="field-label">
            <span>
              Prompt for every reviewer <span className="muted">(optional)</span>
            </span>
            <textarea rows={2} value={d.prompt} onChange={(e) => set("prompt", e.target.value)} />
          </label>
          <fieldset className="tier-slots">
            <legend>Reviewers</legend>
            {d.slots.length === 0 && <p className="muted small">No reviewers yet.</p>}
            <ol className="slot-list">
              {d.slots.map((s, i) => (
                <li key={s.key} className="slot">
                  <div className="slot-head">
                    <span className="muted">#{i + 1}</span>
                    <label className="field-label inline">
                      Model
                      <select aria-label={`Reviewer ${i + 1} model`} value={s.model} onChange={(e) => setSlot(s.key, { model: e.target.value })}>
                        {!s.model && <option value="">Choose a model…</option>}
                        {s.model && !modelNames.includes(s.model) && <option value={s.model}>{s.model} (disabled or missing)</option>}
                        {modelNames.map((m) => (
                          <option key={m} value={m}>
                            {m}
                          </option>
                        ))}
                      </select>
                    </label>
                    <label className="field-label inline">
                      Thinking
                      <select
                        aria-label={`Reviewer ${i + 1} thinking`}
                        value={s.thinking}
                        onChange={(e) => setSlot(s.key, { thinking: e.target.value })}
                      >
                        {SLOT_THINKING.map((t) => (
                          <option key={t.value} value={t.value}>
                            {t.title}
                          </option>
                        ))}
                      </select>
                    </label>
                    <span className="spacer" />
                    <button
                      type="button"
                      className="btn ghost small"
                      aria-label={`Move reviewer ${i + 1} up`}
                      disabled={i === 0}
                      onClick={() => set("slots", moveSlot(d.slots, i, -1))}
                    >
                      ↑
                    </button>
                    <button
                      type="button"
                      className="btn ghost small"
                      aria-label={`Move reviewer ${i + 1} down`}
                      disabled={i === d.slots.length - 1}
                      onClick={() => set("slots", moveSlot(d.slots, i, 1))}
                    >
                      ↓
                    </button>
                    <button
                      type="button"
                      className="btn ghost small danger-text"
                      aria-label={`Remove reviewer ${i + 1}`}
                      onClick={() => set("slots", d.slots.filter((x) => x.key !== s.key))}
                    >
                      Remove
                    </button>
                  </div>
                  <label className="field-label">
                    <span>
                      Label <span className="muted">(defaults to the model)</span>
                    </span>
                    <input
                      className="field"
                      aria-label={`Reviewer ${i + 1} label`}
                      value={s.name}
                      spellCheck={false}
                      onChange={(e) => setSlot(s.key, { name: e.target.value })}
                    />
                  </label>
                  <label className="field-label">
                    <span>
                      Focus prompt <span className="muted">(optional)</span>
                    </span>
                    <textarea
                      rows={2}
                      aria-label={`Reviewer ${i + 1} focus prompt`}
                      value={s.prompt}
                      onChange={(e) => setSlot(s.key, { prompt: e.target.value })}
                    />
                  </label>
                </li>
              ))}
            </ol>
            <button type="button" className="btn small" onClick={() => set("slots", [...d.slots, newSlot(modelNames[0] ?? "")])}>
              + Add reviewer
            </button>
            <p className="muted small">
              One reviewer per slot; a model may appear several times with different focuses. A slot’s thinking overrides the reviewers’
              level for that slot only.
            </p>
          </fieldset>
        </>
      )}
      {error && <p className="error settings-error">{error}</p>}
      <div className="editor-actions">
        <span className="muted small">{problem ?? "Saved to the daemon’s ycc.toml [reviews]."}</span>
        <button type="button" className="btn" onClick={onClose} disabled={saving}>
          Cancel
        </button>
        <button type="submit" className="btn primary" disabled={!!problem || saving}>
          {saving ? "Saving…" : "Save tier"}
        </button>
      </div>
    </form>
  );
}
