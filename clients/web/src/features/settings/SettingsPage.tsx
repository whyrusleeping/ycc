// `/settings`: daemon-wide configuration, ordered for someone setting ycc up:
// models (subscription logins + the model registry: ListModels / UpsertModel /
// RemoveModel / TestModel / DiscoverModels), which model does what (default
// roles via SetRoleConfig, reasoning via SetThinking, and the work
// implementation strategy), code review tiers (ListReviewTiers /
// UpsertReviewTier / RemoveReviewTier / SetReviewDefault), the daemon-wide
// quick replies (GetQuickReplies / SetQuickReplies), this browser's
// notifications, the read-only spend caps, and daemon health. Model and tier
// selects require Apply; changes persist to the daemon's ycc.toml; the daemon's
// error is shown verbatim. Mirrors iOS GlobalSettingsView and ReviewTiersView.
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useBudget, useHealth, useModels, useQuickReplies, useReviewTiers } from "../../api/queries";
import { paths } from "../../app/paths";
import { useIntent } from "../../app/intents";
import type { ModelInfo } from "../../gen/ycc/v1/ycc_pb";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { MenuButton } from "../../ui/Menu";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { THINKING_LEVELS, parseThinking, roleModelChoices, type ThinkingLevel } from "../session/settings";
import { budgetRows } from "../usage/model";
import { openAnthropicLogin, openOpenAILogin } from "./AnthropicLogin";
import { ModelEditorDialog, type EditorTarget } from "./ModelEditor";
import { TierEditorDialog } from "./TierEditor";
import { SECTION_ALIASES, SETTINGS_SECTIONS as SECTIONS } from "./sections";
import { NotificationControls } from "../notify/NotifyControls";
import { QuickRepliesForm } from "../quickReplies/QuickRepliesEditor";
import { HealthPanel } from "../health/HealthPanel";
import { draftFromConfig, modelStatus, pricingLine, rolesOf, sortedModels, toggleReviewer, upsertModelRequest } from "./models";
import { draftFromTier, isRemovable, newTierDraft, tierBadge, tierPurpose, tierSummary, type TierDraft } from "./tiers";

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
  const raw = decodeURIComponent(location.hash.slice(1));
  const hash = SECTION_ALIASES[raw] ?? raw;
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
        These apply to the whole daemon: every project and every new session. A running session keeps its own choices; change those from
        its Settings panel.
      </p>
      {models.isPending ? (
        <p className="muted">Loading models…</p>
      ) : models.isError || !models.data ? (
        <p className="error" role="alert">{errorMessage(models.error, "Couldn’t load models.")}</p>
      ) : (
        <>
          <ModelsSection data={models.data} onEdit={setEditor} />
          <RolesSection data={models.data} />
          <ReviewTiersSection models={models.data.models} />
        </>
      )}
      <QuickRepliesSection />
      <Section id="notifications" title="Notifications" intro="Desktop notifications in this browser when a session needs you.">
        <NotificationControls />
      </Section>
      <BudgetSection />
      <Section id="health" title="Status">
        <HealthPanel />
      </Section>
      <ModelEditorDialog
        target={editor}
        existingNames={(models.data?.models ?? []).map((m) => m.name)}
        onClose={() => setEditor(null)}
      />
    </div>
  );
}

/** The daemon-wide quick replies (projects add extras from the Projects page). */
function QuickRepliesSection() {
  const replies = useQuickReplies("");
  return (
    <Section
      id="quickReplies"
      title="Quick replies"
      intro={
        <>
          One-click messages above the composer (a swipe-in menu on iOS) when a session is idle. These apply to every project; a project can
          add its own from the <Link to={paths.projects()}>Projects</Link> page.
        </>
      }
    >
      {replies.isPending ? (
        <p className="muted">Loading…</p>
      ) : replies.isError ? (
        <p className="error" role="alert">{errorMessage(replies.error, "Couldn’t load quick replies.")}</p>
      ) : (
        <QuickRepliesForm scope={{ global: true }} initial={replies.data.global} emptyText="No quick replies: sessions won’t offer any." />
      )}
    </Section>
  );
}

function Section({ id, title, intro, children, actions }: { id: string; title: string; intro?: ReactNode; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="settings-card" id={`settings-${id}`} aria-labelledby={`settings-${id}-h`}>
      <div className="settings-card-head">
        <div>
          <h2 id={`settings-${id}-h`}>{title}</h2>
          {intro && <p className="settings-card-intro">{intro}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  );
}

/** Subscription logins: the daemon runs them and keeps the tokens. */
function Subscriptions() {
  return (
    <div className="settings-sub" id="settings-accounts">
      <h3 className="settings-subhead">Subscriptions</h3>
      <div className="settings-line">
        <div>
          <strong>Claude Pro or Max</strong>
          <div className="muted small">Sign in, or reconnect an expired login. Used by Anthropic models set to sign in with a subscription.</div>
        </div>
        <button type="button" className="btn" data-track="settings.anthropicLogin" onClick={() => openAnthropicLogin()}>
          Connect Claude…
        </button>
      </div>
      <div className="settings-line">
        <div>
          <strong>ChatGPT Plus or Pro</strong>
          <div className="muted small">Sign in, or reconnect an expired login. Used by OpenAI models set to sign in with a subscription.</div>
        </div>
        <button type="button" className="btn" data-track="settings.openaiLogin" onClick={() => openOpenAILogin()}>
          Connect ChatGPT…
        </button>
      </div>
      <p className="muted small">
        Usage windows and reset times are on the <Link to={paths.usage(null)}>Usage</Link> page.
      </p>
    </div>
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

type Role = "coordinator" | "implementer";

const ROLE_TEXT = {
  coordinator: { title: "Main agent", hint: "Talks with you, plans, and writes the code in every session." },
  implementer: { title: "Implementer", hint: "Writes the code instead when work sessions hand it off (below)." },
  reviewers: { title: "Reviewers", hint: "Check changes before they’re committed. Code review (next section) decides how many of them look at each change." },
} as const;

const THINKING_FIELD = { coordinator: "coordinatorThinking", implementer: "implementerThinking", reviewers: "reviewersThinking" } as const;

function RolesSection({ data }: { data: ModelsData }) {
  const qc = useQueryClient();
  const { busy, error, setError, run } = useApply("settings.roles");
  const [roleModels, setRoleModels] = useState<Partial<Record<Role, string>>>({});
  const enabled = data.models.filter((m) => !m.disabled);
  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.models("") });
  const setRole = (field: Role, name: string) => {
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
  const modelPick = (field: Role) => {
    const draft = roleModels[field];
    const changed = !!draft && draft !== data[field];
    return (
      <div className="role-pick">
        <select
          aria-label={`Default ${field} model`}
          value={draft ?? data[field]}
          disabled={!!busy}
          onChange={(e) => setRoleModels((d) => ({ ...d, [field]: e.target.value }))}
        >
          <ModelOptions models={data.models} assigned={[data[field]]} />
        </select>
        {changed && (
          <button type="button" className="btn primary small" aria-label={`Apply ${field} model`} disabled={!!busy} onClick={() => setRole(field, draft)}>
            Apply
          </button>
        )}
      </div>
    );
  };
  return (
    <Section id="roles" title="Who does what" intro="Each job in a session can use a different model. New sessions use these choices.">
      {error && <p className="error settings-error" role="alert">{error}</p>}
      {enabled.length === 0 ? (
        <p className="muted">Add or enable a model above first.</p>
      ) : (
        <div className="role-rows">
          <RoleRow role="coordinator">
            {modelPick("coordinator")}
            <ReasoningPick data={data} role="coordinator" />
          </RoleRow>
          <RoleRow role="reviewers">
            <fieldset className="settings-reviewers" disabled={!!busy}>
              <legend className="sr-only">Reviewer models</legend>
              {roleModelChoices(sortedModels(data.models), data.reviewers).map((m) => (
                <label key={m.name} className="check">
                  <input type="checkbox" checked={data.reviewers.includes(m.name)} onChange={() => toggle(m.name)} />
                  {m.name}
                  {m.disabled ? " (disabled)" : ""}
                </label>
              ))}
            </fieldset>
            <ReasoningPick data={data} role="reviewers" />
          </RoleRow>
          <RoleRow role="implementer">
            {modelPick("implementer")}
            <ReasoningPick data={data} role="implementer" />
          </RoleRow>
        </div>
      )}
      <WorkImplementation current={data.workImplementation || "direct"} />
      <p className="muted small">
        Reasoning is how long a model thinks before it answers: higher is slower and uses more of your plan or budget. It is stored per model,
        so roles that share a model share its level; review tiers can override it per reviewer.
      </p>
    </Section>
  );
}

function RoleRow({ role, children }: { role: keyof typeof ROLE_TEXT; children: ReactNode }) {
  const text = ROLE_TEXT[role];
  return (
    <div className="role-row">
      <div className="role-label">
        <strong>{text.title}</strong> <span className="chip">{role}</span>
        <div className="muted small">{text.hint}</div>
      </div>
      <div className="role-controls">{children}</div>
    </div>
  );
}

function ReasoningPick({ data, role }: { data: ModelsData; role: keyof typeof THINKING_FIELD }) {
  const qc = useQueryClient();
  const { busy, error, run } = useApply("settings.thinking");
  // Optimistic: a picked level shows at once; a failure reverts it.
  const [pending, setPending] = useState<ThinkingLevel | null>(null);
  const current = parseThinking(data[THINKING_FIELD[role]]);
  const choose = async (level: ThinkingLevel) => {
    if (busy || level === current) return;
    track.action("settings.thinking", "click", { role, level });
    setPending(level);
    await run(role, () => client.setThinking({ sessionId: "", role, level }), () => qc.invalidateQueries({ queryKey: queryKeys.models("") }));
    setPending(null);
  };
  return (
    <label className="reasoning-pick">
      <span className="muted small">Reasoning</span>
      <select
        aria-label={`${ROLE_TEXT[role].title} reasoning`}
        aria-disabled={!!busy}
        value={pending ?? current}
        onChange={(e) => void choose(e.target.value as ThinkingLevel)}
      >
        {THINKING_LEVELS.map((l) => (
          <option key={l.value} value={l.value}>
            {l.title}
          </option>
        ))}
      </select>
      {error && (
        <span className="error small" role="alert">
          {error}
        </span>
      )}
    </label>
  );
}

const WORK_CHOICES = [
  { value: "direct", label: "The main agent writes it", detail: "Default. Reviews are still done by separate reviewer agents." },
  { value: "delegate", label: "Hand it to the implementer", detail: "The main agent plans and checks; the implementer model writes the changes." },
] as const;

function WorkImplementation({ current }: { current: string }) {
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
    <div className="settings-sub" id="settings-work">
      <h3 className="settings-subhead">In backlog work sessions, who writes the code?</h3>
      {error && <p className="error settings-error" role="alert">{error}</p>}
      <fieldset className="loop-options plain" disabled={!!busy}>
        <legend className="sr-only">Work implementation</legend>
        {WORK_CHOICES.map((o) => (
          <label key={o.value} className="radio-row">
            <input type="radio" name="settings-work-implementation" checked={shown === o.value} onChange={() => void choose(o.value)} />
            <span>
              <strong>{o.label}</strong> <span className="muted small">{o.detail}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <p className="muted small">Applies from the next work session, including work loops.</p>
    </div>
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
      title="Code review"
      intro="Before committing, the main agent picks how much review a change needs: a quick look of its own for tiny changes, one reviewer for ordinary ones, several in parallel for risky ones. These levels are called tiers."
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
          <div className="role-pick tier-default">
            <span>When the agent doesn’t choose, use</span>
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
            {defaultTier && defaultTier !== def && (
              <button
                type="button"
                className="btn primary small"
                aria-label="Apply default tier"
                disabled={!!busy}
                onClick={() => {
                  track.action("settings.default_tier", "click");
                  void run("default", () => client.setReviewDefault({ name: defaultTier }), async () => {
                    await refresh();
                    setDefaultTier(null);
                  });
                }}
              >
                Apply
              </button>
            )}
          </div>
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
                    {tierPurpose(t) && <div className="muted small clamp-2">{tierPurpose(t)}</div>}
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
            The built-in tiers (self-review, standard, comprehensive) always exist; editing one saves your version, and Revert restores it.
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
  const health = useHealth();
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
      intro="The AI models ycc can use. Connect a subscription, or add a model that uses an API key, any OpenAI-compatible endpoint, or a local Ollama server."
    >
      <Subscriptions />
      <div className="settings-sub">
        <div className="settings-card-head">
          <h3 className="settings-subhead">Configured models</h3>
          <button type="button" className="btn primary small" data-track="settings.addModel" onClick={() => onEdit({ kind: "new" })}>
            + Add model
          </button>
        </div>
        {error && <p className="error settings-error" role="alert">{error}</p>}
        <table className="models-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Status</th>
              <th>Used for</th>
              <th>
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {sortedModels(data.models).map((m) => {
              const assigned = rolesOf(m.name, roles);
              const check = health.data?.find((c) => c.id === `model:${m.name}`);
              const status = m.disabled ? { label: "disabled", tone: "muted" as const } : modelStatus(check);
              const price = pricingLine(m);
              return (
                <tr key={m.name} className={m.disabled ? "disabled" : ""}>
                  <td>
                    <button type="button" className="link model-name" data-track="settings.edit_model" onClick={() => onEdit({ kind: "edit", name: m.name })}>
                      {m.name}
                    </button>
                    <div className="muted small">
                      {m.backend} · <span className="mono">{m.model}</span>
                      {price && <> · {price}</>}
                    </div>
                  </td>
                  <td>
                    <span className={`model-status tone-${status.tone}`} title={check?.detail || undefined}>
                      {status.label}
                    </span>
                  </td>
                  <td>
                    {assigned.map((r) => (
                      <span key={r} className="tag live">
                        {r === "coordinator" ? "main agent" : r}
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
          Click a model to change its provider, model id, sign-in, pricing, or notes. API keys are never entered here: they stay on the
          daemon’s machine (<code>ycc token set NAME</code>), and a model refers to one by name. Disable a model to keep it without letting it run.
        </p>
      </div>
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

function BudgetSection() {
  const budget = useBudget();
  return (
    <Section id="budget" title="Spending limits" intro="Caps that stop a session or a work loop when it reaches a cost or token limit.">
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
        Set these in the <code>[budget]</code> section of the daemon’s ycc.toml. Spending so far is on the <Link to={paths.usage(null)}>Usage</Link>{" "}
        page.
      </p>
    </Section>
  );
}
