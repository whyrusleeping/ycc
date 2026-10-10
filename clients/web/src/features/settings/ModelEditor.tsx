// Add / edit / duplicate a logical model ([models.X] in ycc.toml): backend,
// authentication, base URL, the key_env NAME (never a key), model id with
// provider discovery, reasoning defaults, pricing, availability, and a live
// connection test of the unsaved draft. Mirrors iOS ModelEditorView.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import type { DiscoverModelsResponse, TestModelResponse } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys } from "../../api/queries";
import { Modal } from "../../ui/Modal";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import {
  AUTH_MODES,
  BACKENDS,
  EFFORTS,
  THINKING_DISPLAYS,
  THINKING_MODES,
  canSubmit,
  discoverRequest,
  draftFromConfig,
  emptyDraft,
  probeFingerprint,
  suggestName,
  testModelRequest,
  upsertModelRequest,
  validateDraft,
  type ModelDraft,
} from "./models";

export type EditorTarget = { kind: "new" } | { kind: "edit"; name: string } | { kind: "duplicate"; name: string };

export function ModelEditorDialog({
  target,
  existingNames,
  onClose,
}: {
  target: EditorTarget | null;
  existingNames: readonly string[];
  onClose: () => void;
}) {
  const closeGuard = useRef<(() => void) | null>(null);
  const title = !target ? "" : target.kind === "new" ? "Add model" : target.kind === "duplicate" ? `Duplicate ${target.name}` : `Edit ${target.name}`;
  const key = !target ? "" : target.kind === "new" ? "new" : `${target.kind}:${target.name}`;
  return (
    <Modal
      open={target !== null}
      onClose={() => (closeGuard.current ?? onClose)()}
      title={title}
      className="model-editor-dialog"
      view="model_editor"
      flow={target ? `model_editor.${target.kind}` : undefined}
    >
      {target && <ModelEditor key={key} target={target} existingNames={existingNames} onClose={onClose} closeGuard={closeGuard} />}
    </Modal>
  );
}

function ModelEditor({ target, existingNames, onClose, closeGuard }: {
  target: EditorTarget;
  existingNames: readonly string[];
  onClose: () => void;
  closeGuard: RefObject<(() => void) | null>;
}) {
  const source = target.kind === "new" ? "" : target.name;
  const config = useQuery({
    queryKey: queryKeys.modelConfig(source),
    enabled: source !== "",
    staleTime: 0,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const m = (await client.getModelConfig({ name: source }, { signal })).model;
      if (!m) throw new Error(`Model ${source} not found`);
      return m;
    },
  });
  const [draft, setDraft] = useState<ModelDraft | null>(target.kind === "new" ? emptyDraft() : null);
  if (draft === null && config.data) setDraft(draftFromConfig(config.data, target.kind === "duplicate"));

  if (target.kind !== "new" && !draft) {
    if (config.isError) return <p className="error" role="alert">{errorMessage(config.error, "Couldn’t load the model.")}</p>;
    return <p className="muted">Loading…</p>;
  }
  return <EditorForm target={target} initial={draft!} existingNames={existingNames} onClose={onClose} closeGuard={closeGuard} />;
}

function EditorForm({
  target,
  initial,
  existingNames,
  onClose,
  closeGuard,
}: {
  target: EditorTarget;
  initial: ModelDraft;
  existingNames: readonly string[];
  onClose: () => void;
  closeGuard: RefObject<(() => void) | null>;
}) {
  const qc = useQueryClient();
  const [d, setD] = useState<ModelDraft>(initial);
  const [saving, setSaving] = useState(false);
  const savingNow = useRef(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const dirty = JSON.stringify(d) !== JSON.stringify(initial);
  const requestClose = () => {
    // React also bubbles Escape from the nested discard dialog to the modal.
    if (savingNow.current || confirmDiscard) return;
    if (dirty) setConfirmDiscard(true);
    else onClose();
  };
  // The modal's Escape and header close take the same path as Cancel.
  useLayoutEffect(() => {
    closeGuard.current = requestClose;
    return () => { closeGuard.current = null; };
  });
  const [error, setError] = useState<string | null>(null);
  const [discovery, setDiscovery] = useState<DiscoverModelsResponse | null>(null);
  const [discovering, setDiscovering] = useState(false);
  const [discoverFilter, setDiscoverFilter] = useState("");
  const [test, setTest] = useState<{ fp: string; result: TestModelResponse | { success: false; message: string; durationMs: 0n } } | null>(null);
  const [testing, setTesting] = useState(false);
  const isNew = target.kind !== "edit";
  const check = useMemo(() => validateDraft(d, existingNames, isNew), [d, existingNames, isNew]);
  const ok = canSubmit(check);
  const fp = probeFingerprint(d);
  const shownTest = test && test.fp === fp ? test.result : null;
  // Field errors show once a field has been edited (an untouched new form isn't scolded).
  const [touched, setTouched] = useState<ReadonlySet<string>>(() => new Set(isNew ? [] : ["name", "backend", "auth", "baseUrl", "modelId", "keyEnv", "price"]));
  const set = <K extends keyof ModelDraft>(k: K, v: ModelDraft[K]) => {
    setD((prev) => ({ ...prev, [k]: v }));
    const field = String(k).startsWith("price") ? "price" : String(k);
    setTouched((t) => (t.has(field) ? t : new Set([...t, field])));
  };

  const expire = (err: unknown) => {
    if (isUnauthorized(err)) {
      authStore.expire();
      return true;
    }
    return false;
  };

  const discover = async () => {
    track.action("model_editor.discover", "click");
    setDiscovering(true);
    setError(null);
    try {
      setDiscovery(await client.discoverModels(discoverRequest(d)));
      setDiscoverFilter("");
    } catch (err) {
      if (!expire(err)) {
        track.error("model_editor.discover", err);
        setError(errorMessage(err, "Discovery failed."));
      }
    } finally {
      setDiscovering(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    const at = fp;
    try {
      const result = await client.testModel(testModelRequest(d));
      track.action("model_editor.test", "click", { ok: result.success });
      setTest({ fp: at, result });
    } catch (err) {
      if (!expire(err)) setTest({ fp: at, result: { success: false, message: errorMessage(err, "The test could not run."), durationMs: 0n } });
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    if (!ok || savingNow.current) return;
    savingNow.current = true;
    setSaving(true);
    setError(null);
    try {
      await client.upsertModel(upsertModelRequest(d));
      void qc.invalidateQueries({ queryKey: queryKeys.modelsAll });
      qc.removeQueries({ queryKey: queryKeys.modelConfig(d.name.trim()) });
      toast(target.kind === "edit" ? `Saved ${d.name.trim()}.` : `Added ${d.name.trim()}.`, "info");
      track.submit(`model_editor.${target.kind}`);
      onClose();
    } catch (err) {
      if (!expire(err)) {
        track.error("model_editor.save", err);
        setError(errorMessage(err, "The model was not saved."));
      }
    } finally {
      savingNow.current = false;
      setSaving(false);
    }
  };

  const pick = (id: string) => {
    setD((prev) => ({ ...prev, modelId: id, name: isNew && !prev.name.trim() ? suggestName(id, existingNames) : prev.name }));
    setTouched((t) => new Set([...t, "modelId", "name"]));
  };
  const firstProblem = Object.values(check.errors)[0];

  const ids = discovery?.modelIds ?? [];
  const filtered = discoverFilter ? ids.filter((id) => id.toLowerCase().includes(discoverFilter.toLowerCase())) : ids;
  const fieldErr = (k: keyof typeof check.errors) =>
    check.errors[k] && touched.has(k) ? (
      <span className="field-error" role="alert">
        {check.errors[k]}
      </span>
    ) : null;

  return (
    <form
      className="task-editor model-editor"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
      autoComplete="off"
    >
      <div className="editor-row">
        <label className="field-label grow">
          Logical name
          <input
            className="field mono"
            value={d.name}
            disabled={!isNew}
            title={isNew ? undefined : "Renaming would create a second model; duplicate it instead."}
            autoFocus={isNew}
            spellCheck={false}
            onChange={(e) => set("name", e.target.value)}
          />
          {isNew && fieldErr("name")}
        </label>
        <label className="field-label">
          Backend
          <select value={d.backend} onChange={(e) => set("backend", e.target.value)}>
            {BACKENDS.map((b) => (
              <option key={b.value} value={b.value}>
                {b.title}
              </option>
            ))}
          </select>
        </label>
        <label className="field-label">
          Authentication
          <select value={d.auth} onChange={(e) => set("auth", e.target.value)}>
            {AUTH_MODES.map((a) => (
              <option key={a.value} value={a.value}>
                {a.title}
              </option>
            ))}
          </select>
        </label>
      </div>
      {fieldErr("auth")}
      <label className="field-label check-label">
        <span className="check">
          <input type="checkbox" checked={d.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          Enabled
        </span>
        <span className="muted small">A disabled model keeps its configuration but can’t run or be assigned to a role.</span>
      </label>
      <label className="field-label">
        <span>
          Base URL <span className="muted">(optional for hosted providers)</span>
        </span>
        <input
          className="field mono"
          value={d.baseUrl}
          placeholder={d.backend === "ollama" ? "http://127.0.0.1:11434" : d.backend === "openai-compatible" ? "https://host/v1" : "provider default"}
          spellCheck={false}
          onChange={(e) => set("baseUrl", e.target.value)}
        />
        {fieldErr("baseUrl")}
      </label>
      <label className="field-label">
        <span>
          API key variable <span className="muted">(key_env: a NAME, never the key)</span>
        </span>
        <input
          className="field mono"
          name="key_env"
          value={d.keyEnv}
          placeholder={d.auth === "oauth" ? "not used with OAuth" : "e.g. ANTHROPIC_API_KEY"}
          spellCheck={false}
          autoCapitalize="characters"
          data-1p-ignore
          data-lpignore="true"
          onChange={(e) => set("keyEnv", e.target.value)}
        />
        {fieldErr("keyEnv") ?? (
          <span className="muted small">
            The daemon resolves it from its environment or its <span className="mono">ycc token set</span> secrets store; the key never
            reaches the browser.
          </span>
        )}
      </label>
      <div className="field-label">
        <label htmlFor="model-id">Model id</label>
        <div className="input-row">
          <input id="model-id" className="field mono grow" value={d.modelId} spellCheck={false} onChange={(e) => set("modelId", e.target.value)} />
          <button type="button" className="btn" disabled={discovering || !d.backend} onClick={() => void discover()}>
            {discovering ? "Discovering…" : "Discover models"}
          </button>
        </div>
        {fieldErr("modelId")}
      </div>
      {discovery && (
        <div className="discovery" aria-label="Discovered models">
          <div className="discovery-head">
            <span className={`small ${discovery.fromNetwork ? "muted" : "warn"}`}>{discovery.note}</span>
            {ids.length > 8 && (
              <input
                className="field small"
                placeholder="Filter…"
                aria-label="Filter discovered models"
                value={discoverFilter}
                onChange={(e) => setDiscoverFilter(e.target.value)}
              />
            )}
          </div>
          <div className="discovery-list">
            {filtered.map((id) => (
              <button
                key={id}
                type="button"
                className={`chip discovered${id === d.modelId.trim() ? " selected" : ""}`}
                aria-pressed={id === d.modelId.trim()}
                onClick={() => pick(id)}
              >
                {id}
              </button>
            ))}
            {filtered.length === 0 && <span className="muted small">No match.</span>}
          </div>
        </div>
      )}
      <div className="editor-row">
        <label className="field-label grow">
          Thinking
          <select value={d.thinking} onChange={(e) => set("thinking", e.target.value)}>
            {THINKING_MODES.map((o) => (
              <option key={o.value} value={o.value}>
                {o.title}
              </option>
            ))}
          </select>
        </label>
        <label className="field-label grow">
          Effort
          <select value={d.effort} onChange={(e) => set("effort", e.target.value)}>
            {EFFORTS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.title}
              </option>
            ))}
          </select>
        </label>
        <label className="field-label grow">
          Reasoning display
          <select value={d.thinkingDisplay} onChange={(e) => set("thinkingDisplay", e.target.value)}>
            {THINKING_DISPLAYS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.title}
              </option>
            ))}
          </select>
        </label>
      </div>
      <fieldset className="price-fields">
        <legend>
          Pricing <span className="muted">($ per million tokens; blank uses built-in prices or leaves it unpriced)</span>
        </legend>
        {(
          [
            ["priceInput", "Input"],
            ["priceOutput", "Output"],
            ["priceCacheRead", "Cache read"],
            ["priceCacheWrite", "Cache write"],
          ] as const
        ).map(([k, label]) => (
          <label key={k} className="field-label">
            {label}
            <input className="field mono" inputMode="decimal" value={d[k]} onChange={(e) => set(k, e.target.value)} />
          </label>
        ))}
      </fieldset>
      {fieldErr("price")}
      {check.warnings.map((w) => (
        <p key={w} className="warn small">
          {w}
        </p>
      ))}
      <div className="model-test">
        <button type="button" className="btn" disabled={!ok || testing || saving} title={firstProblem} onClick={() => void runTest()}>
          {testing ? "Testing…" : "Test connection"}
        </button>
        <span className="muted small">Sends one small request with these unsaved settings (your provider may bill it).</span>
        {shownTest && (
          <div className={`test-result ${shownTest.success ? "ok" : "fail"}`} role={shownTest.success ? "status" : "alert"}>
            <strong>{shownTest.success ? "✓ " : "✕ "}</strong>
            {shownTest.message}
            {Number(shownTest.durationMs) > 0 && <span className="muted"> · {String(shownTest.durationMs)} ms</span>}
            {"errorKind" in shownTest && shownTest.errorKind && (
              <span className="muted">
                {" "}
                · {shownTest.errorKind}
                {shownTest.status ? ` (HTTP ${shownTest.status})` : ""}
              </span>
            )}
          </div>
        )}
      </div>
      {error && <p className="error settings-error" role="alert">{error}</p>}
      <div className="editor-actions">
        <span className="muted small" role={firstProblem ? "alert" : undefined}>
          {firstProblem ?? "Saved to the daemon’s ycc.toml. Context window and capabilities stay TOML-only."}
        </span>
        <button type="button" className="btn" onClick={requestClose} disabled={saving}>
          Cancel
        </button>
        <button type="submit" className="btn primary" disabled={!ok || saving} title={firstProblem}>
          {saving ? "Saving…" : target.kind === "edit" ? "Save" : "Add model"}
        </button>
      </div>
      <ConfirmDialog
        open={confirmDiscard}
        title="Discard your changes?"
        body="Your unsaved model edits will be lost."
        confirmLabel="Discard"
        danger
        action="model_editor.discard"
        onCancel={() => setConfirmDiscard(false)}
        onConfirm={() => {
          if (savingNow.current) return;
          setConfirmDiscard(false);
          onClose();
        }}
      />
    </form>
  );
}
