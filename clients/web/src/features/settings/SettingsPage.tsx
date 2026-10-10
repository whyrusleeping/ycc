// `/settings`: daemon-wide configuration — the Anthropic subscription login,
// default role models (SetRoleConfig without a session), default reasoning per
// role (SetThinking without a session), the work implementation strategy,
// review tiers (ListReviewTiers / UpsertReviewTier / RemoveReviewTier /
// SetReviewDefault), the model registry (ListModels / UpsertModel /
// RemoveModel / TestModel / DiscoverModels), session modes (read-only), and the
// spend-guard caps. Model and tier selects require Apply; changes persist to the daemon's
// ycc.toml; the daemon's error is shown verbatim. Mirrors iOS
// GlobalSettingsView and ReviewTiersView.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
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
import { track } from "../../app/analytics";
import { THINKING_LEVELS, parseThinking, roleModelChoices, type ThinkingLevel } from "../session/settings";
import { budgetRows } from "../usage/model";
import { WORK_IMPLEMENTATIONS } from "../workloop/model";
import { openAnthropicLogin } from "./AnthropicLogin";
import { ModelEditorDialog, type EditorTarget } from "./ModelEditor";
import { TierEditorDialog } from "./TierEditor";
import { SETTINGS_SECTIONS as SECTIONS } from "./sections";
import { NotificationControls } from "../notify/NotifyControls";
import { draftFromConfig, pricingLine, rolesOf, sortedModels, toggleReviewer, upsertModelRequest } from "./models";
import { draftFromTier, isRemovable, newTierDraft, tierBadge, tierSummary, type TierDraft } from "./tiers";

export const SETTINGS_INTENT = "settings";


/** Apply a settings change; failures show inline and are recorded as `op` errors. */
function useApply(op: string) {
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
      else {
        track.error(op, err);
        setError(errorMessage(err, "The change was not applied."));
      }
      return false;
    } finally {
      setBusy(null);
    }
  }, [op]);
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
  // A deep link (/settings#models) scrolls once the sections have rendered,
  // and again on every later navigation to a section (palette, links, the
  // contents bar) even when Settings is already open: each navigation has
  // its own location key.
  const loaded = !models.isPending;
  const hash = decodeURIComponent(location.hash.slice(1));
  const firstScroll = useRef(true);
  useEffect(() => {
    if (!loaded || !hash) return;
    const smooth = !firstScroll.current;
    firstScroll.current = false;
    document.getElementById(`settings-${hash}`)?.scrollIntoView({ block: "start", behavior: smooth ? "smooth" : "auto" });
  }, [loaded, hash, location.key]);

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
                track.action("settings.toc", "click", { section: s.id });
                // The hash effect scrolls (also when the hash is unchanged).
                navigate({ hash: s.id }, { replace: true });
              }}
            >
              {s.title}
            </a>
          ))}
        </nav>
      </header>
      <p className="muted small settings-intro">
        Daemon-wide defaults for new sessions. Model and tier selections require Apply; changes are saved to the daemon’s ycc.toml. A running session keeps its own
        settings (change those from its Settings panel).
      </p>
      <AccountsSection />
      <Section id="notifications" title="Notifications (this browser)">
        <NotificationControls />
      </Section>
      {models.isPending ? (
        <p className="muted">Loading models…</p>
      ) : models.isError || !models.data ? (
        <p className="error" role="alert">{errorMessage(models.error, "Couldn’t load models.")}</p>
      ) : (
        <>
          <RolesSection data={models.data} />
          <ThinkingSection data={models.data} />
          <WorkSection current={models.data.workImplementation || "direct"} />
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
        <button type="button" className="btn" data-track="settings.anthropicLogin" onClick={() => openAnthropicLogin()}>
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
  const { busy, error, setError, run } = useApply("settings.roles");
  const [roleModels, setRoleModels] = useState<Partial<Record<"coordinator" | "implementer", string>>>({});
  const enabled = data.models.filter((m) => !m.disabled);
  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.models("") });
  const setRole = (field: "coordinator" | "implementer", name: string) => {
    if (name === data[field]) return;
    track.action("settings.role", "click", { role: field });
    void run(field, () => client.setRoleConfig({ sessionId: "", [field]: name }), async () => {
      await refresh();
      setRoleModels((draft) => {
        const next = { ...draft };
        delete next[field];
        return next;
      });
    });
  };
  const toggle = (name: string) => {
    const { next, error: why } = toggleReviewer(data.reviewers, name);
    if (why) {
      track.error("settings.roles", "invalid");
      setError(why);
      return;
    }
    track.action("settings.role", "click", { role: "reviewers" });
    void run("reviewers", () => client.setRoleConfig({ sessionId: "", reviewers: next }), refresh);
  };
  return (
    <Section id="roles" title="Default roles">
      {error && <p className="error settings-error" role="alert">{error}</p>}
      {enabled.length === 0 ? (
        <p className="muted">Enable a model below before assigning roles.</p>
      ) : (
        <div className="settings-grid">
          <label className="settings-row">
            <span>Coordinator</span>
            <select aria-label="Default coordinator model" value={roleModels.coordinator ?? data.coordinator} disabled={!!busy} onChange={(e) => setRoleModels((draft) => ({ ...draft, coordinator: e.target.value }))}>
              <ModelOptions models={data.models} assigned={[data.coordinator]} />
            </select>
          </label>
          <button type="button" className="btn small" disabled={!!busy || !roleModels.coordinator || roleModels.coordinator === data.coordinator} onClick={() => setRole("coordinator", roleModels.coordinator!)}>
            Apply coordinator model
          </button>
          <label className="settings-row">
            <span>Implementer</span>
            <select aria-label="Default implementer model" value={roleModels.implementer ?? data.implementer} disabled={!!busy} onChange={(e) => setRoleModels((draft) => ({ ...draft, implementer: e.target.value }))}>
              <ModelOptions models={data.models} assigned={[data.implementer]} />
            </select>
          </label>
          <button type="button" className="btn small" disabled={!!busy || !roleModels.implementer || roleModels.implementer === data.implementer} onClick={() => setRole("implementer", roleModels.implementer!)}>
            Apply implementer model
          </button>
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
  const { busy, error, run } = useApply("settings.thinking");
  // Optimistic: a picked level shows at once; a failure reverts it.
  const [pending, setPending] = useState<Partial<Record<string, ThinkingLevel>>>({});
  const choose = async (role: string, level: ThinkingLevel, current: ThinkingLevel) => {
    if (busy || level === current) return;
    track.action("settings.thinking", "click", { role, level });
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
      {error && <p className="error settings-error" role="alert">{error}</p>}
      {THINKING_ROWS.map((r) => {
        const current = parseThinking(data[r.field]);
        const shown = pending[r.role] ?? current;
        return (
          <div key={r.role} className="thinking-row">
            <span className="thinking-role">{r.title}</span>
            <div className="segmented" role="group" aria-label={`${r.title} reasoning`}>
              {THINKING_LEVELS.map((l) => (
                <button
                  key={l.value}
                  type="button"
                  aria-pressed={shown === l.value}
                  className={shown === l.value ? "selected" : ""}
                  aria-disabled={!!busy}
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
  const { busy, error, run } = useApply("settings.work_implementation");
  // Optimistic: the picked option shows at once; a failure reverts it.
  const [pending, setPending] = useState<string | null>(null);
  const shown = pending ?? current;
  const choose = async (value: string) => {
    if (value === current) return;
    track.action("settings.work_implementation", "click", { value });
    setPending(value);
    await run("work", () => client.setWorkImplementation({ implementation: value }), async () => {
      await qc.invalidateQueries({ queryKey: queryKeys.models("") });
      toast(`New work sessions will ${value === "direct" ? "edit code directly" : "delegate to an implementer"}.`, "info");
    });
    setPending(null);
  };
  return (
    <Section id="work" title="Work implementation">
      {error && <p className="error settings-error" role="alert">{error}</p>}
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
  const { busy, error, run } = useApply("settings.review_tiers");
  const [editing, setEditing] = useState<TierDraft | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const modelNames = useMemo(() => sortedModels(models.filter((m) => !m.disabled)).map((m) => m.name), [models]);
  const list = tiers.data?.tiers ?? [];
  const def = tiers.data?.defaultTier ?? "";
  const [defaultTier, setDefaultTier] = useState<string | null>(null);
  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.reviewTiers });
  const removingTier = list.find((t) => t.name === removing);
  return (
    <Section
      id="reviews"
      title="Review tiers"
      actions={
        <button type="button" className="btn small" data-track="settings.add_tier" onClick={() => setEditing(newTierDraft(modelNames[0] ?? ""))}>
          + Add tier
        </button>
      }
    >
      {error && <p className="error settings-error" role="alert">{error}</p>}
      {tiers.isPending ? (
        <p className="muted">Loading…</p>
      ) : tiers.isError ? (
        <p className="error" role="alert">{errorMessage(tiers.error, "Couldn’t load review tiers.")}</p>
      ) : (
        <>
          <label className="settings-row narrow">
            <span>Default tier</span>
            <select
              aria-label="Default review tier"
              value={defaultTier ?? def}
              disabled={!!busy}
              onChange={(e) => setDefaultTier(e.target.value)}
            >
              {list.map((t) => (
                <option key={t.name} value={t.name}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            className="btn small"
            disabled={!!busy || !defaultTier || defaultTier === def}
            onClick={() => {
              track.action("settings.default_tier", "click");
              void run("default", () => client.setReviewDefault({ name: defaultTier! }), async () => {
                await refresh();
                setDefaultTier(null);
              });
            }}
          >
            Apply default tier
          </button>
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
                  <button type="button" className="btn small" data-track="settings.edit_tier" onClick={() => setEditing(draftFromTier(t))}>
                    Edit
                  </button>
                  {isRemovable(t, def) ? (
                    <button type="button" className="btn ghost small danger-text" data-track="settings.remove_tier" onClick={() => setRemoving(t.name)}>
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
        action="settings.remove_tier"
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
  const { busy, error, run } = useApply("settings.models");
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
        <button type="button" className="btn primary small" data-track="settings.addModel" onClick={() => onEdit({ kind: "new" })}>
          + Add model
        </button>
      }
    >
      {error && <p className="error settings-error" role="alert">{error}</p>}
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
                  <button type="button" className="link model-name" data-track="settings.edit_model" onClick={() => onEdit({ kind: "edit", name: m.name })}>
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
                      { id: "settings.edit_model", label: "Edit…", onSelect: () => onEdit({ kind: "edit", name: m.name }) },
                      { id: "settings.duplicate_model", label: "Duplicate…", onSelect: () => onEdit({ kind: "duplicate", name: m.name }) },
                      {
                        id: m.disabled ? "settings.enable_model" : "settings.disable_model",
                        label: m.disabled ? "Enable" : "Disable",
                        disabled: !!busy,
                        title: !m.disabled && assigned.length > 0 ? "Roles keep it assigned; move them to another model too" : undefined,
                        onSelect: () => void toggleEnabled(m),
                      },
                      {
                        id: "settings.remove_model",
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
        action="settings.remove_model"
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
        <p className="error" role="alert">{errorMessage(modes.error, "Couldn’t load modes.")}</p>
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
        <p className="error" role="alert">{errorMessage(budget.error)}</p>
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
