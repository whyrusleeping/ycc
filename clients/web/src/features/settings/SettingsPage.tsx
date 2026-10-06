// `/settings`: daemon-wide configuration — the Anthropic subscription login,
// default role models (SetRoleConfig without a session), default reasoning per
// role (SetThinking without a session), the work implementation strategy,
// review tiers (ListReviewTiers / UpsertReviewTier / RemoveReviewTier /
// SetReviewDefault), the model registry (ListModels / UpsertModel /
// RemoveModel / TestModel / DiscoverModels), session modes (read-only), and the
// spend-guard caps. Every change applies at once and persists to the daemon's
// ycc.toml; the daemon's error is shown verbatim. Mirrors iOS
// GlobalSettingsView and ReviewTiersView.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useBudget, useModels, useModes, useReviewTiers } from "../../api/queries";
import { paths } from "../../app/paths";
import { useIntent } from "../../app/intents";
import type { ModelInfo } from "../../gen/ycc/v1/ycc_pb";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { MenuButton } from "../../ui/Menu";
import { toast } from "../../ui/toast";
import { THINKING_LEVELS, parseThinking, roleModelChoices, type ThinkingLevel } from "../session/settings";
import { budgetRows } from "../usage/model";
import { WORK_IMPLEMENTATIONS } from "../workloop/model";
import { openAnthropicLogin } from "./AnthropicLogin";
import { ModelEditorDialog, type EditorTarget } from "./ModelEditor";
import { TierEditorDialog } from "./TierEditor";
import { draftFromConfig, pricingLine, rolesOf, sortedModels, toggleReviewer, upsertModelRequest } from "./models";
import { draftFromTier, isRemovable, newTierDraft, tierBadge, tierSummary, type TierDraft } from "./tiers";

export const SETTINGS_INTENT = "settings";

const SECTIONS = [
  { id: "accounts", title: "Accounts" },
  { id: "roles", title: "Default roles" },
  { id: "thinking", title: "Reasoning" },
  { id: "work", title: "Work" },
  { id: "reviews", title: "Review tiers" },
  { id: "models", title: "Models" },
  { id: "modes", title: "Modes" },
  { id: "budget", title: "Spend guard" },
] as const;

function useApply() {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = useCallback(async (what: string, call: () => Promise<unknown>, after?: () => void | Promise<void>): Promise<boolean> => {
    setBusy(what);
    setError(null);
    try {
      await call();
      await after?.();
      return true;
    } catch (err) {
      if (isUnauthorized(err)) authStore.expire();
      else setError(errorMessage(err, "The change was not applied."));
      return false;
    } finally {
      setBusy(null);
    }
  }, []);
  return { busy, error, setError, run };
}

export function SettingsPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const models = useModels("");
  const [editor, setEditor] = useState<EditorTarget | null>(null);
  useIntent(
    SETTINGS_INTENT,
    useCallback((what: string) => {
      if (what === "addModel") setEditor({ kind: "new" });
    }, []),
  );
  // A deep link (/settings#models) scrolls once the sections have rendered.
  const loaded = !models.isPending;
  const initialHash = useMemo(() => decodeURIComponent(location.hash.slice(1)), []); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!loaded || !initialHash) return;
    document.getElementById(`settings-${initialHash}`)?.scrollIntoView({ block: "start" });
  }, [loaded, initialHash]);

  return (
    <div className="page settings-page">
      <header className="page-head">
        <h1>Settings</h1>
        <nav className="settings-toc" aria-label="Settings sections">
          {SECTIONS.map((s) => (
            <a
              key={s.id}
              href={`#${s.id}`}
              className="btn ghost small"
              onClick={(e) => {
                e.preventDefault();
                document.getElementById(`settings-${s.id}`)?.scrollIntoView({ block: "start", behavior: "smooth" });
                navigate({ hash: s.id }, { replace: true });
              }}
            >
              {s.title}
            </a>
          ))}
        </nav>
      </header>
      <p className="muted small settings-intro">
        Daemon-wide defaults for new sessions. Changes apply at once and are saved to the daemon’s ycc.toml. A running session keeps its own
        settings (change those from its Settings panel).
      </p>
      <AccountsSection />
      {models.isPending ? (
        <p className="muted">Loading models…</p>
      ) : models.isError || !models.data ? (
        <p className="error">{errorMessage(models.error, "Couldn’t load models.")}</p>
      ) : (
        <>
          <RolesSection data={models.data} />
          <ThinkingSection data={models.data} />
          <WorkSection current={models.data.workImplementation || "delegate"} />
          <ReviewTiersSection models={models.data.models} />
          <ModelsSection data={models.data} onEdit={setEditor} />
        </>
      )}
      <ModesSection />
      <BudgetSection />
      <ModelEditorDialog
        target={editor}
        existingNames={(models.data?.models ?? []).map((m) => m.name)}
        onClose={() => setEditor(null)}
      />
    </div>
  );
}

function Section({ id, title, children, actions }: { id: string; title: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="settings-card" id={`settings-${id}`} aria-labelledby={`settings-${id}-h`}>
      <div className="settings-card-head">
        <h2 id={`settings-${id}-h`}>{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

function AccountsSection() {
  return (
    <Section id="accounts" title="Provider accounts">
      <div className="settings-line">
        <div>
          <strong>Anthropic subscription</strong>
          <div className="muted small">
            Sign in (or reconnect an expired login) for models using <span className="mono">auth = "oauth"</span> on the Anthropic backend.
            The daemon runs the login and keeps the tokens.
          </div>
        </div>
        <button type="button" className="btn" onClick={() => openAnthropicLogin()}>
          Connect / reconnect Anthropic…
        </button>
      </div>
      <p className="muted small">
        Subscription allowance (usage windows and reset times) is on the <Link to={paths.usage(null)}>Usage</Link> page.
      </p>
    </Section>
  );
}

type ModelsData = NonNullable<ReturnType<typeof useModels>["data"]>;

function ModelOptions({ models, assigned }: { models: readonly ModelInfo[]; assigned: readonly string[] }) {
  return (
    <>
      {roleModelChoices(sortedModels(models), assigned).map((m) => (
        <option key={m.name} value={m.name}>
          {m.name}
          {m.disabled ? " (disabled)" : ""} · {m.model || m.backend}
        </option>
      ))}
    </>
  );
}

function RolesSection({ data }: { data: ModelsData }) {
  const qc = useQueryClient();
  const { busy, error, setError, run } = useApply();
  const enabled = data.models.filter((m) => !m.disabled);
  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.models("") });
  const setRole = (field: "coordinator" | "implementer", name: string) => {
    if (name === data[field]) return;
    void run(field, () => client.setRoleConfig({ sessionId: "", [field]: name }), refresh);
  };
  const toggle = (name: string) => {
    const { next, error: why } = toggleReviewer(data.reviewers, name);
    if (why) {
      setError(why);
      return;
    }
    void run("reviewers", () => client.setRoleConfig({ sessionId: "", reviewers: next }), refresh);
  };
  return (
    <Section id="roles" title="Default roles">
      {error && <p className="error settings-error">{error}</p>}
      {enabled.length === 0 ? (
        <p className="muted">Enable a model below before assigning roles.</p>
      ) : (
        <div className="settings-grid">
          <label className="settings-row">
            <span>Coordinator</span>
            <select aria-label="Default coordinator model" value={data.coordinator} disabled={!!busy} onChange={(e) => setRole("coordinator", e.target.value)}>
              <ModelOptions models={data.models} assigned={[data.coordinator]} />
            </select>
          </label>
          <label className="settings-row">
            <span>Implementer</span>
            <select aria-label="Default implementer model" value={data.implementer} disabled={!!busy} onChange={(e) => setRole("implementer", e.target.value)}>
              <ModelOptions models={data.models} assigned={[data.implementer]} />
            </select>
          </label>
          <fieldset className="settings-reviewers" disabled={!!busy}>
            <legend>Reviewers</legend>
            {roleModelChoices(sortedModels(data.models), data.reviewers).map((m) => (
              <label key={m.name} className="check">
                <input type="checkbox" checked={data.reviewers.includes(m.name)} onChange={() => toggle(m.name)} />
                {m.name}
                {m.disabled ? " (disabled)" : ""}
              </label>
            ))}
          </fieldset>
        </div>
      )}
      <p className="muted small">Used by new sessions. Review tiers below can name their own reviewer models per slot.</p>
    </Section>
  );
}

const THINKING_ROWS = [
  { role: "coordinator", title: "Coordinator", field: "coordinatorThinking" },
  { role: "implementer", title: "Implementer", field: "implementerThinking" },
  { role: "reviewers", title: "Reviewers", field: "reviewersThinking" },
] as const;

function ThinkingSection({ data }: { data: ModelsData }) {
  const qc = useQueryClient();
  const { busy, error, run } = useApply();
  // Optimistic: a picked level shows at once; a failure reverts it.
  const [pending, setPending] = useState<Partial<Record<string, ThinkingLevel>>>({});
  const choose = async (role: string, level: ThinkingLevel, current: ThinkingLevel) => {
    if (level === current) return;
    setPending((p) => ({ ...p, [role]: level }));
    await run(role, () => client.setThinking({ sessionId: "", role, level }), () => qc.invalidateQueries({ queryKey: queryKeys.models("") }));
    setPending((p) => {
      const next = { ...p };
      delete next[role];
      return next;
    });
  };
  return (
    <Section id="thinking" title="Default reasoning">
      {error && <p className="error settings-error">{error}</p>}
      {THINKING_ROWS.map((r) => {
        const current = parseThinking(data[r.field]);
        const shown = pending[r.role] ?? current;
        return (
          <div key={r.role} className="thinking-row">
            <span className="thinking-role">{r.title}</span>
            <div className="segmented" role="radiogroup" aria-label={`${r.title} reasoning`}>
              {THINKING_LEVELS.map((l) => (
                <button
                  key={l.value}
                  type="button"
                  role="radio"
                  aria-checked={shown === l.value}
                  className={shown === l.value ? "selected" : ""}
                  disabled={busy === r.role}
                  onClick={() => void choose(r.role, l.value, current)}
                >
                  {l.title}
                </button>
              ))}
            </div>
          </div>
        );
      })}
      <p className="muted small">
        A level is stored on the role’s current model, so roles sharing a model share it. Per-reviewer overrides live in review tiers.
      </p>
    </Section>
  );
}

function WorkSection({ current }: { current: string }) {
  const qc = useQueryClient();
  const { busy, error, run } = useApply();
  // Optimistic: the picked option shows at once; a failure reverts it.
  const [pending, setPending] = useState<string | null>(null);
  const shown = pending ?? current;
  const choose = async (value: string) => {
    if (value === current) return;
    setPending(value);
    await run("work", () => client.setWorkImplementation({ implementation: value }), async () => {
      await qc.invalidateQueries({ queryKey: queryKeys.models("") });
      toast(`New work sessions will ${value === "direct" ? "edit code directly" : "delegate to an implementer"}.`, "info");
    });
    setPending(null);
  };
  return (
    <Section id="work" title="Work implementation">
      {error && <p className="error settings-error">{error}</p>}
      <fieldset className="loop-options plain" disabled={!!busy}>
        <legend className="sr-only">Work implementation</legend>
        {WORK_IMPLEMENTATIONS.map((o) => (
          <label key={o.value} className="radio-row">
            <input
              type="radio"
              name="settings-work-implementation"
              checked={shown === o.value}
              onChange={() => void choose(o.value)}
            />
            <span>
              <strong>{o.label}</strong> <span className="muted small">{o.detail}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <p className="muted small">Applies to the next work session (including work-loop sessions); running sessions keep theirs.</p>
    </Section>
  );
}

function ReviewTiersSection({ models }: { models: readonly ModelInfo[] }) {
  const qc = useQueryClient();
  const tiers = useReviewTiers();
  const { busy, error, run } = useApply();
  const [editing, setEditing] = useState<TierDraft | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const modelNames = useMemo(() => sortedModels(models.filter((m) => !m.disabled)).map((m) => m.name), [models]);
  const list = tiers.data?.tiers ?? [];
  const def = tiers.data?.defaultTier ?? "";
  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.reviewTiers });
  const removingTier = list.find((t) => t.name === removing);
  return (
    <Section
      id="reviews"
      title="Review tiers"
      actions={
        <button type="button" className="btn small" onClick={() => setEditing(newTierDraft(modelNames[0] ?? ""))}>
          + Add tier
        </button>
      }
    >
      {error && <p className="error settings-error">{error}</p>}
      {tiers.isPending ? (
        <p className="muted">Loading…</p>
      ) : tiers.isError ? (
        <p className="error">{errorMessage(tiers.error, "Couldn’t load review tiers.")}</p>
      ) : (
        <>
          <label className="settings-row narrow">
            <span>Default tier</span>
            <select
              aria-label="Default review tier"
              value={def}
              disabled={!!busy}
              onChange={(e) => void run("default", () => client.setReviewDefault({ name: e.target.value }), refresh)}
            >
              {list.map((t) => (
                <option key={t.name} value={t.name}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <p className="muted small">Used when the coordinator doesn’t pick a tier for a change.</p>
          <ul className="tier-list">
            {list.map((t) => {
              const badge = tierBadge(t);
              return (
                <li key={t.name} className="tier-row">
                  <div className="tier-main">
                    <div className="tier-name-line">
                      <strong>{t.name}</strong>
                      <span className={`tag${badge.tone === "warn" ? " warn" : badge.tone === "accent" ? " live" : ""}`}>{badge.label}</span>
                      {t.name === def && <span className="tag">default</span>}
                    </div>
                    <div className="small tier-summary">{tierSummary(t)}</div>
                    {t.description && <div className="muted small clamp-2">{t.description}</div>}
                  </div>
                  <button type="button" className="btn small" onClick={() => setEditing(draftFromTier(t))}>
                    Edit
                  </button>
                  {isRemovable(t, def) ? (
                    <button type="button" className="btn ghost small danger-text" onClick={() => setRemoving(t.name)}>
                      {t.builtin ? "Revert" : "Remove"}
                    </button>
                  ) : (
                    <span className="tier-action-spacer" />
                  )}
                </li>
              );
            })}
          </ul>
          <p className="muted small">
            The coordinator picks a tier per change by size and risk. Built-in tiers (self-review, standard, comprehensive) always exist;
            editing one stores an override.
          </p>
        </>
      )}
      <TierEditorDialog draft={editing} modelNames={modelNames} tierNames={list.map((t) => t.name)} onClose={() => setEditing(null)} />
      <ConfirmDialog
        open={removing !== null}
        title={removingTier?.builtin ? `Revert ${removing}?` : `Remove ${removing}?`}
        body={
          removingTier?.builtin
            ? `Remove the custom configuration for “${removing}”? The tier reverts to its built-in behaviour.`
            : `Remove the “${removing}” review tier from the daemon?`
        }
        confirmLabel={removingTier?.builtin ? "Revert tier" : "Remove tier"}
        danger
        onCancel={() => setRemoving(null)}
        onConfirm={() => {
          const name = removing;
          setRemoving(null);
          if (name) void run("remove", () => client.removeReviewTier({ name }), refresh);
        }}
      />
    </Section>
  );
}

function ModelsSection({ data, onEdit }: { data: ModelsData; onEdit: (t: EditorTarget) => void }) {
  const qc = useQueryClient();
  const { busy, error, run } = useApply();
  const [removing, setRemoving] = useState<string | null>(null);
  const roles = { coordinator: data.coordinator, implementer: data.implementer, reviewers: data.reviewers };
  const toggleEnabled = async (m: ModelInfo) => {
    // Flip availability through the full record so nothing else changes.
    await run(
      m.name,
      async () => {
        const cfg = (await client.getModelConfig({ name: m.name })).model;
        if (!cfg) throw new Error(`Model ${m.name} not found`);
        await client.upsertModel(upsertModelRequest({ ...draftFromConfig(cfg), enabled: m.disabled }));
      },
      () => qc.invalidateQueries({ queryKey: queryKeys.modelsAll }),
    );
  };
  return (
    <Section
      id="models"
      title="Models"
      actions={
        <button type="button" className="btn primary small" onClick={() => onEdit({ kind: "new" })}>
          + Add model
        </button>
      }
    >
      {error && <p className="error settings-error">{error}</p>}
      <table className="models-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Backend · model id</th>
            <th>Pricing</th>
            <th>Default roles</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {sortedModels(data.models).map((m) => {
            const assigned = rolesOf(m.name, roles);
            return (
              <tr key={m.name} className={m.disabled ? "disabled" : ""}>
                <td>
                  <button type="button" className="link model-name" onClick={() => onEdit({ kind: "edit", name: m.name })}>
                    {m.name}
                  </button>
                  {m.disabled && <span className="tag">disabled</span>}
                </td>
                <td>
                  <span className="muted">{m.backend}</span> · <span className="mono">{m.model}</span>
                </td>
                <td className="small">{pricingLine(m) ?? <span className="muted">unpriced</span>}</td>
                <td>
                  {assigned.map((r) => (
                    <span key={r} className="tag live">
                      {r}
                    </span>
                  ))}
                </td>
                <td className="row-menu">
                  <MenuButton
                    label="⋯"
                    ariaLabel={`Actions for ${m.name}`}
                    items={[
                      { label: "Edit…", onSelect: () => onEdit({ kind: "edit", name: m.name }) },
                      { label: "Duplicate…", onSelect: () => onEdit({ kind: "duplicate", name: m.name }) },
                      {
                        label: m.disabled ? "Enable" : "Disable",
                        disabled: !!busy,
                        title: !m.disabled && assigned.length > 0 ? "Roles keep it assigned; move them to another model too" : undefined,
                        onSelect: () => void toggleEnabled(m),
                      },
                      {
                        label: "Remove…",
                        danger: true,
                        disabled: !!busy,
                        title: assigned.length ? "Models assigned to a role can’t be removed" : undefined,
                        onSelect: () => setRemoving(m.name),
                      },
                    ]}
                  />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <p className="muted small">
        Each logical model combines a provider connection, a credential reference (the key_env NAME — keys stay on the daemon), and a model
        id. Disable one to keep its configuration without letting it run.
      </p>
      <ConfirmDialog
        open={removing !== null}
        title={`Remove ${removing}?`}
        body={`Remove “${removing}” from the daemon and its ycc.toml? Models assigned to a role can’t be removed; usage history keeps its name.`}
        confirmLabel="Remove model"
        danger
        onCancel={() => setRemoving(null)}
        onConfirm={() => {
          const name = removing;
          setRemoving(null);
          if (name)
            void run("remove", () => client.removeModel({ name, persist: true }), async () => {
              await qc.invalidateQueries({ queryKey: queryKeys.modelsAll });
              toast(`Removed ${name}.`, "info");
            });
        }}
      />
    </Section>
  );
}

function ModesSection() {
  const modes = useModes();
  return (
    <Section id="modes" title="Session modes">
      {modes.isPending ? (
        <p className="muted">Loading…</p>
      ) : modes.isError ? (
        <p className="error">{errorMessage(modes.error, "Couldn’t load modes.")}</p>
      ) : (
        <>
          <dl className="mode-list">
            {modes.data.modes.map((m) => (
              <div key={m.name} className="mode-item">
                <dt>
                  {m.title || m.name} <span className="chip">{m.name}</span>
                </dt>
                <dd className="muted small">{m.description}</dd>
              </div>
            ))}
          </dl>
          {modes.data.presets.length > 0 && (
            <>
              <h3 className="settings-subhead">Opening-prompt presets</h3>
              <dl className="mode-list">
                {modes.data.presets.map((p) => (
                  <div key={p.name} className="mode-item">
                    <dt>
                      {p.title || p.name} <span className="chip">{p.mode}</span>
                    </dt>
                    <dd className="muted small">{p.description}</dd>
                  </div>
                ))}
              </dl>
            </>
          )}
          <p className="muted small">Built into the daemon; pick one when starting a session.</p>
        </>
      )}
    </Section>
  );
}

function BudgetSection() {
  const budget = useBudget();
  return (
    <Section id="budget" title="Spend guard">
      {budget.isPending ? (
        <p className="muted">Loading…</p>
      ) : budget.isError ? (
        <p className="error">{errorMessage(budget.error)}</p>
      ) : (
        <dl className="cap-list">
          {budgetRows(budget.data).map((r) => (
            <div key={r.label} className="cap-row">
              <dt>{r.label}</dt>
              <dd className={r.unlimited ? "muted" : ""}>{r.value}</dd>
            </div>
          ))}
        </dl>
      )}
      <p className="muted small">
        Read-only here: set in ycc.toml [budget]. See spend on the <Link to={paths.usage(null)}>Usage</Link> page.
      </p>
    </Section>
  );
}
