// The per-session settings panel (inspector): reasoning level by role scope
// (SetThinking), role models (SetRoleConfig scoped to the session), the
// coordinator context meter with rollover, and this session's usage (GetUsage
// grouped by session and model). Each change applies to the live session
// immediately and the daemon's error is shown verbatim.
import { Icon } from "../../ui/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { queryKeys, useModels } from "../../api/queries";
import { authStore } from "../../api/auth";
import { Link } from "react-router";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { compactTokenCount } from "../sessions/feed";
import { usageSearch } from "../usage/model";
import { useSessionController, useSessionSnapshot } from "./useSession";
import {
  THINKING_LEVELS,
  THINKING_ROLES,
  formatCost,
  needsThinkingApply,
  parseThinking,
  roleModelChoices,
  sessionUsage,
  thinkingFor,
  thinkingRoleWire,
  withThinking,
  type RoleThinking,
  type ThinkingLevel,
  type ThinkingRole,
} from "./settings";

export function SessionSettingsPanel({ project, sessionId }: { project: string; sessionId: string }) {
  const controller = useSessionController(project, sessionId);
  const snap = useSessionSnapshot(controller);
  const live = snap.mode === "live" && snap.conn !== "finished";
  return (
    <div className="settings-panel">
      {!live && snap.installed && (
        <p className="muted small">
          Reasoning and models can be changed once the session is running again — just send it a message. Usage below is from its log.
        </p>
      )}
      <ReasoningAndRoles sessionId={sessionId} live={live} />
      <section className="settings-section">
        <h3>Context</h3>
        <div className="settings-row">
          <span>Coordinator context</span>
          <span className="mono">
            {snap.contextTokens !== null ? `${compactTokenCount(snap.contextTokens) ?? "0"} tokens` : "unknown"}
          </span>
        </div>
        {snap.coordinatorModel && (
          <div className="settings-row">
            <span>Coordinator model</span>
            <span className="mono">{snap.coordinatorModel}</span>
          </div>
        )}
        {live && (
          <div className="settings-actions">
            <button
              type="button"
              className="btn small"
              disabled={!snap.rolloverAvailable || snap.phase.kind === "paused" || snap.control !== null}
              title={
                snap.rolloverAvailable
                  ? "Summarize and restart the coordinator's context"
                  : "Rollover isn't available for this session right now"
              }
              data-track="session.rollover"
              onClick={() => void controller.rollover()}
            >
              {snap.control?.kind === "rollover" ? "Rolling over…" : "Roll over context"}
            </button>
          </div>
        )}
      </section>
      <SessionUsage project={project} sessionId={sessionId} />
    </div>
  );
}

function ReasoningAndRoles({ sessionId, live }: { sessionId: string; live: boolean }) {
  const qc = useQueryClient();
  // Without a live session, ListModels/SetThinking would address the global
  // defaults, so the controls only load (and apply) for a live session.
  const models = useModels(sessionId, live);
  const [role, setRole] = useState<ThinkingRole>("all");
  const [levels, setLevels] = useState<RoleThinking | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reviewers, setReviewers] = useState<string[] | null>(null);

  const data = models.data;
  useEffect(() => {
    if (!data || busy) return;
    setLevels({
      coordinator: parseThinking(data.coordinatorThinking),
      implementer: parseThinking(data.implementerThinking),
      reviewers: parseThinking(data.reviewersThinking),
    });
    setReviewers(null);
  }, [data, busy]);

  const apply = async (call: () => Promise<unknown>, onSuccess?: () => void) => {
    setBusy(true);
    setError(null);
    try {
      await call();
      onSuccess?.();
      await qc.invalidateQueries({ queryKey: queryKeys.models(sessionId) });
    } catch (err) {
      if (isUnauthorized(err)) authStore.expire();
      else track.error("session_settings.apply", err);
      setError(errorMessage(err, "The change was not applied."));
    } finally {
      setBusy(false);
    }
  };

  const chooseLevel = (level: ThinkingLevel) => {
    if (!levels || !needsThinkingApply(role, level, levels)) return;
    track.action("session_settings.thinking", "click", { role, level });
    void apply(
      () => client.setThinking({ sessionId, level, role: thinkingRoleWire(role) }),
      () => setLevels(withThinking(role, level, levels)),
    );
  };

  const setRoleModel = (field: "coordinator" | "implementer", name: string) => {
    if (!data || name === data[field]) return;
    track.action("session_settings.role", "click", { role: field });
    void apply(() => client.setRoleConfig({ sessionId, [field]: name }));
  };

  if (!live) return null;
  if (models.isPending) return <p className="muted">Loading settings…</p>;
  if (models.isError || !data) return <p className="error">{errorMessage(models.error, "Couldn’t load models.")}</p>;
  const current = levels ? thinkingFor(role, levels) : "medium";
  const shownReviewers = reviewers ?? data.reviewers;
  const reviewersChanged =
    reviewers !== null &&
    (reviewers.length !== data.reviewers.length || reviewers.some((r) => !data.reviewers.includes(r)));

  return (
    <>
      {error && <p className="error settings-error">{error}</p>}
      <section className="settings-section">
        <h3>Reasoning</h3>
        <label className="settings-row">
          <span>Scope</span>
          <select aria-label="Reasoning scope" value={role} disabled={busy} onChange={(e) => setRole(e.target.value as ThinkingRole)}>
            {THINKING_ROLES.map((r) => (
              <option key={r.value} value={r.value}>
                {r.title}
              </option>
            ))}
          </select>
        </label>
        <div className="segmented" role="radiogroup" aria-label="Reasoning level">
          {THINKING_LEVELS.map((l) => (
            <button
              key={l.value}
              type="button"
              role="radio"
              aria-checked={current === l.value}
              className={current === l.value ? "selected" : ""}
              disabled={busy}
              onClick={() => chooseLevel(l.value)}
            >
              {l.title}
            </button>
          ))}
        </div>
        <p className="muted small">Applies from the next turn and is saved as the model’s default.</p>
      </section>
      <section className="settings-section">
        <h3>Models</h3>
        <label className="settings-row">
          <span>Coordinator</span>
          <select aria-label="Coordinator model" value={data.coordinator} disabled={busy} onChange={(e) => setRoleModel("coordinator", e.target.value)}>
            {roleModelChoices(data.models, [data.coordinator]).map((m) => (
              <option key={m.name} value={m.name}>
                {m.name}
                {m.disabled ? " (disabled)" : ""} · {m.model || m.backend}
              </option>
            ))}
          </select>
        </label>
        <label className="settings-row">
          <span>Implementer</span>
          <select aria-label="Implementer model" value={data.implementer} disabled={busy} onChange={(e) => setRoleModel("implementer", e.target.value)}>
            {roleModelChoices(data.models, [data.implementer]).map((m) => (
              <option key={m.name} value={m.name}>
                {m.name}
                {m.disabled ? " (disabled)" : ""} · {m.model || m.backend}
              </option>
            ))}
          </select>
        </label>
        <fieldset className="settings-reviewers" disabled={busy}>
          <legend>Reviewers</legend>
          {roleModelChoices(data.models, data.reviewers).map((m) => (
            <label key={m.name} className="check">
              <input
                type="checkbox"
                checked={shownReviewers.includes(m.name)}
                onChange={(e) =>
                  setReviewers(
                    e.target.checked ? [...shownReviewers, m.name] : shownReviewers.filter((r) => r !== m.name),
                  )
                }
              />
              {m.name}
              {m.disabled ? " (disabled)" : ""}
            </label>
          ))}
          {reviewersChanged && (
            <div className="settings-actions">
              <button type="button" className="btn ghost small" onClick={() => setReviewers(null)}>
                Reset
              </button>
              <button
                type="button"
                className="btn primary small"
                disabled={!reviewers?.length}
                data-track="session_settings.reviewers"
                onClick={() => void apply(() => client.setRoleConfig({ sessionId, reviewers: reviewers ?? [] }))}
              >
                Apply reviewers
              </button>
            </div>
          )}
        </fieldset>
        <p className="muted small">Model changes apply to this session only.</p>
      </section>
    </>
  );
}

function SessionUsage({ project, sessionId }: { project: string; sessionId: string }) {
  const q = useQuery({
    queryKey: queryKeys.sessionUsage(project),
    queryFn: ({ signal }) => client.getUsage({ project, groupBy: ["session", "model"] }, { signal }),
  });
  const usage = q.data ? sessionUsage(q.data.rows, sessionId) : null;
  return (
    <section className="settings-section">
      <h3>
        Usage
        <button
          type="button"
          className="btn ghost small"
          onClick={() => void q.refetch()}
          disabled={q.isFetching}
          aria-label="Refresh usage"
          title="Refresh usage"
        >
          <Icon name="refresh" size={15} />
        </button>
      </h3>
      {q.isPending && <p className="muted">Loading…</p>}
      {q.isError && <p className="error">{errorMessage(q.error, "Couldn’t load usage.")}</p>}
      {usage && !usage.total && <p className="muted">No model usage recorded yet.</p>}
      {(usage?.total || q.isError) && (
        <p className="small">
          <Link to={paths.usage(project || null, usageSearch({ by: ["session", "model"] }))}>
            Full usage{project ? ` for ${project}` : ""} →
          </Link>
        </p>
      )}
      {usage?.total && (
        <table className="usage-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Input</th>
              <th>Output</th>
              <th>Cache</th>
              <th>Cost</th>
            </tr>
          </thead>
          <tbody>
            {usage.rows.map((r) => (
              <tr key={r.model}>
                <td>{r.model || "(unknown)"}</td>
                <td>{compactTokenCount(Number(r.input)) ?? "0"}</td>
                <td>{compactTokenCount(Number(r.output)) ?? "0"}</td>
                <td title={`read ${r.cacheRead} · write ${r.cacheWrite}`}>
                  {compactTokenCount(Number(r.cacheRead + r.cacheWrite)) ?? "0"}
                </td>
                <td>{formatCost(r.cost, r.priceStatus)}</td>
              </tr>
            ))}
          </tbody>
          {usage.rows.length > 1 && (
            <tfoot>
              <tr>
                <td>Total</td>
                <td>{compactTokenCount(Number(usage.total.input)) ?? "0"}</td>
                <td>{compactTokenCount(Number(usage.total.output)) ?? "0"}</td>
                <td>{compactTokenCount(Number(usage.total.cacheRead + usage.total.cacheWrite)) ?? "0"}</td>
                <td>{formatCost(usage.total.cost, usage.total.priceStatus)}</td>
              </tr>
            </tfoot>
          )}
        </table>
      )}
    </section>
  );
}
