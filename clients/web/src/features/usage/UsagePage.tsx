// The usage dashboard (`/usage` for every project, `/p/<project>/usage` for
// one): GetUsage rollups grouped by any mix of task / model / role / session /
// day over a UTC date range, a spend timeline, the provider-side subscription
// allowance (GetSubscriptionUsage), and the spend-guard caps (GetBudget). The
// query lives in the URL so views are linkable. Mirrors iOS UsageView, and the
// breakdown table prints exactly what `ycc cost` prints for the same scope.
import { Icon } from "../../ui/icons";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import type { SubscriptionUsageAccount, UsageRow } from "../../gen/ycc/v1/ycc_pb";
import { errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, refreshSubscriptionUsage, useBudget, useProjects, useSubscriptionUsage, useUsageReport } from "../../api/queries";
import { paths } from "../../app/paths";
import { toast } from "../../ui/toast";
import { track } from "../../app/analytics";
import { TaskLink } from "../backlog/parts";
import {
  RANGE_PRESETS,
  USAGE_DIMS,
  accountBadge,
  anyPartial,
  budgetRows,
  buildTimeline,
  capitalize,
  cellLabel,
  commas,
  costCell,
  dimTitle,
  headlineCost,
  parseUsageQuery,
  rangeError,
  resetText,
  resolveRange,
  rowShare,
  shortTokens,
  sortRows,
  usageRequest,
  usageSearch,
  windowPercent,
  windowTone,
  type RangePreset,
  type SortKey,
  type UsageDim,
  type UsageQuery,
} from "./model";

export function UsagePage({ project }: { project: string }) {
  const [params, setParams] = useSearchParams();
  const query = useMemo(() => parseUsageQuery(params), [params]);
  const navigate = useNavigate();
  const qc = useQueryClient();
  const projects = useProjects();
  const list = projects.data ?? [];
  // Rows link to tasks and sessions only when they belong to one known project.
  const linkProject = project || (list.length === 1 ? list[0].name : list.length === 0 ? "" : null);
  const req = usageRequest(project, query);
  const invalidRange = rangeError(query);
  const report = useUsageReport(req, !invalidRange);
  const dayOnly = query.by.length === 1 && query.by[0] === "day";
  const timelineReq = usageRequest(project, query, new Date(), ["day"]);
  const timeline = useUsageReport(timelineReq, !invalidRange && !dayOnly);
  const timelineData = dayOnly ? report.data : timeline.data;
  const budget = useBudget();
  const subscription = useSubscriptionUsage();
  const [refreshing, setRefreshing] = useState(false);

  const setQuery = (patch: Partial<UsageQuery>) => {
    const next = { ...query, ...patch };
    setParams(new URLSearchParams(usageSearch(next).slice(1)), { replace: true });
  };

  const refresh = async () => {
    setRefreshing(true);
    try {
      await Promise.all([
        qc.invalidateQueries({ queryKey: queryKeys.usageAll }),
        qc.invalidateQueries({ queryKey: queryKeys.budget }),
        refreshSubscriptionUsage(qc).catch((err) => {
          if (isUnauthorized(err)) authStore.expire();
          // The allowance is best-effort; local usage is unaffected.
        }),
      ]);
    } catch (err) {
      toast(`Couldn’t refresh usage: ${errorMessage(err)}`, "error", { op: "usage.refresh", err });
    } finally {
      setRefreshing(false);
    }
  };

  const data = report.data;
  const total = data?.total ?? null;
  const scopeLabel = project
    ? project
    : projects.isPending
      ? ""
      : list.length > 1
        ? "All projects"
        : list.length === 1
          ? list[0].name
          : "Default workspace";

  return (
    <div className="page usage-page">
      <header className="page-head">
        <h1>
          Usage {scopeLabel && <span className="muted">· {scopeLabel}</span>}
        </h1>
        <div className="page-actions">
          {list.length > 1 && (
            <label className="usage-scope">
              <span className="sr-only">Usage scope</span>
              <select
                aria-label="Usage scope"
                value={project}
                onChange={(e) => navigate(paths.usage(e.target.value || null, usageSearch(query)))}
              >
                <option value="">All projects</option>
                {list.map((p) => (
                  <option key={p.name} value={p.name}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button
            type="button"
            className="btn ghost"
            onClick={() => void refresh()}
            disabled={refreshing || report.isFetching}
            data-track="usage.refresh"
            title="Refresh usage, budget, and subscription allowance"
            aria-label="Refresh usage"
          >
            <Icon name="refresh" size={15} />
          </button>
        </div>
      </header>

      <div className="usage-top">
        <SubscriptionAllowance accounts={subscription.data} />
        <section className="usage-card budget-caps" aria-labelledby="usage-budget">
          <h2 id="usage-budget">Spend guard</h2>
          {budget.isPending ? (
            <p className="muted small">Loading…</p>
          ) : budget.isError ? (
            <p className="error small" role="alert">{errorMessage(budget.error)}</p>
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
          <p className="muted small">Caps from ycc.toml [budget]. The daemon stops a session or loop run that reaches one.</p>
        </section>
      </div>

      <UsageControls
        query={query}
        onChange={(patch) => {
          // Which controls are used (field names only, never their values).
          track.action("usage.query", "click", { fields: Object.keys(patch).sort().join("_") });
          setQuery(patch);
        }}
      />

      {invalidRange ? (
        <p className="error" role="alert">{invalidRange}</p>
      ) : report.isPending ? (
        <p className="muted">Loading usage…</p>
      ) : report.isError && !data ? (
        <p className="error" role="alert">{errorMessage(report.error, "Couldn’t load usage.")}</p>
      ) : !data || data.rows.length === 0 ? (
        <div className="empty-state usage-empty">
          <p>No model usage recorded{query.task ? ` for task ${query.task}` : ""} in this range.</p>
        </div>
      ) : (
        <>
          {total && <Totals total={total} />}
          {timelineData && timelineData.rows.length > 0 && (
            <Timeline rows={timelineData.rows} since={req.since} until={req.until} loading={!dayOnly && timeline.isFetching} />
          )}
          <Breakdown
            rows={data.rows}
            total={total}
            by={query.by}
            linkProject={linkProject}
            stale={report.isPlaceholderData}
          />
          {report.isError && <p className="warn small" role="alert">Couldn’t refresh: {errorMessage(report.error)}</p>}
          <p className="muted small usage-foot">
            {anyPartial(data.rows, total) && <>* partial pricing (some models unpriced). </>}
            {data.workspace ? (
              <>
                Workspace <span className="mono">{data.workspace}</span>.{" "}
              </>
            ) : (
              <>Across every registered project. </>
            )}
            Days are UTC. Costs use each model’s configured prices; unpriced models count tokens only.
          </p>
        </>
      )}
    </div>
  );
}

function UsageControls({ query, onChange }: { query: UsageQuery; onChange: (patch: Partial<UsageQuery>) => void }) {
  const remaining = USAGE_DIMS.filter((d) => !query.by.includes(d.value));
  const [task, setTask] = useState(query.task);
  const [taskFor, setTaskFor] = useState(query.task);
  if (taskFor !== query.task) {
    // The URL changed underneath (navigation): follow it.
    setTaskFor(query.task);
    setTask(query.task);
  }
  const applyTask = () => {
    const v = task.trim();
    if (v !== query.task) onChange({ task: v });
  };
  return (
    <div className="usage-controls" role="group" aria-label="Usage filters">
      <div className="usage-control">
        <span className="label">Group by</span>
        <div className="group-chips">
          {query.by.length === 1 ? (
            <select aria-label="Group by" value={query.by[0]} onChange={(e) => onChange({ by: [e.target.value as UsageDim] })}>
              {USAGE_DIMS.map((d) => (
                <option key={d.value} value={d.value}>
                  {d.title}
                </option>
              ))}
            </select>
          ) : (
            query.by.map((d, i) => (
              <span key={d} className="group-chip">
                {i > 0 && <span className="muted">then </span>}
                {dimTitle(d)}
                <button
                  type="button"
                  className="chip-x"
                  aria-label={`Stop grouping by ${dimTitle(d).toLowerCase()}`}
                  onClick={() => onChange({ by: query.by.filter((x) => x !== d) })}
                >
                  ×
                </button>
              </span>
            ))
          )}
          {remaining.length > 0 && (
            <select
              aria-label="Then group by"
              className="add-group"
              value=""
              onChange={(e) => e.target.value && onChange({ by: [...query.by, e.target.value as UsageDim] })}
            >
              <option value="">+ then by…</option>
              {remaining.map((d) => (
                <option key={d.value} value={d.value}>
                  {d.title}
                </option>
              ))}
            </select>
          )}
        </div>
      </div>
      <div className="usage-control">
        <label className="label" htmlFor="usage-range">
          Range
        </label>
        <select
          id="usage-range"
          value={query.range}
          onChange={(e) => {
            const range = e.target.value as RangePreset;
            if (range === "custom") {
              // Start the custom range from what the preset showed.
              const b = resolveRange(query);
              onChange({ range, since: b.since, until: b.until });
            } else onChange({ range, since: "", until: "" });
          }}
        >
          {RANGE_PRESETS.map((r) => (
            <option key={r.value} value={r.value}>
              {r.title}
            </option>
          ))}
        </select>
        {query.range === "custom" && (
          <>
            <input
              type="date"
              className="field"
              aria-label="From (UTC day)"
              value={query.since}
              max={query.until || undefined}
              onChange={(e) => onChange({ since: e.target.value })}
            />
            <span className="muted">–</span>
            <input
              type="date"
              className="field"
              aria-label="Until (UTC day)"
              value={query.until}
              min={query.since || undefined}
              onChange={(e) => onChange({ until: e.target.value })}
            />
          </>
        )}
      </div>
      <form
        className="usage-control"
        onSubmit={(e) => {
          e.preventDefault();
          applyTask();
        }}
      >
        <label className="label" htmlFor="usage-task">
          Task
        </label>
        <input
          id="usage-task"
          className="field mono task-filter"
          placeholder="any"
          value={task}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => setTask(e.target.value)}
          onBlur={applyTask}
        />
        {query.task && (
          <button
            type="button"
            className="btn ghost small"
            onClick={() => {
              setTask("");
              onChange({ task: "" });
            }}
          >
            Clear
          </button>
        )}
      </form>
    </div>
  );
}

function Totals({ total }: { total: UsageRow }) {
  const tiles: { label: string; value: string; title?: string }[] = [
    { label: "Cost", value: headlineCost(total.cost, total.priceStatus), title: costCell(total.cost, total.priceStatus) },
    { label: "Total tokens", value: shortTokens(total.total), title: commas(total.total) },
    { label: "Input", value: shortTokens(total.input), title: commas(total.input) },
    { label: "Output", value: shortTokens(total.output), title: commas(total.output) },
    {
      label: "Cache",
      value: shortTokens(total.cacheRead + total.cacheWrite),
      title: `read ${commas(total.cacheRead)} · write ${commas(total.cacheWrite)}`,
    },
  ];
  return (
    <div className="usage-tiles" aria-label="Totals">
      {tiles.map((t) => (
        <div key={t.label} className="usage-tile" title={t.title}>
          <div className="tile-value">{t.value}</div>
          <div className="tile-label">{t.label}</div>
        </div>
      ))}
    </div>
  );
}

function Timeline({ rows, since, until, loading }: { rows: UsageRow[]; since: string; until: string; loading: boolean }) {
  const { size, buckets } = useMemo(() => buildTimeline(rows, { since, until }), [rows, since, until]);
  const priced = rows.some((r) => r.priceStatus !== "unpriced");
  const [metric, setMetric] = useState<"cost" | "tokens">(priced ? "cost" : "tokens");
  const m = priced ? metric : "tokens";
  const value = (b: (typeof buckets)[number]) => (m === "cost" ? b.cost : Number(b.tokens));
  const max = Math.max(0, ...buckets.map(value));
  if (!buckets.length) return null;
  const describe = (b: (typeof buckets)[number]) => {
    const when = b.start === b.end ? b.label : `${b.label} (${b.start} – ${b.end})`;
    return `${when}: ${commas(b.tokens)} tokens · ${costCell(b.cost, b.hasUsage ? b.status : "priced")}`;
  };
  return (
    <section className="usage-card usage-timeline" aria-labelledby="usage-timeline">
      <div className="timeline-head">
        <h2 id="usage-timeline">
          Over time <span className="muted small">by {size}</span>
          {loading && <span className="muted small"> · updating…</span>}
        </h2>
        {priced && (
          <div className="segmented compact" role="group" aria-label="Timeline metric">
            {(["cost", "tokens"] as const).map((v) => (
              <button key={v} type="button" aria-pressed={m === v} className={m === v ? "selected" : ""}
                onClick={() => {
                  track.action("usage.metric", "click", { metric: v });
                  setMetric(v);
                }}
              >
                {v === "cost" ? "Cost" : "Tokens"}
              </button>
            ))}
          </div>
        )}
      </div>
      <ol className="timeline-bars" aria-label={`Usage by ${size}`}>
        {buckets.map((b) => {
          const v = value(b);
          const pct = max > 0 ? (v / max) * 100 : 0;
          return (
            <li key={b.start} className={`timeline-bar${b.hasUsage ? "" : " empty"}`} title={describe(b)} aria-label={describe(b)}>
              <span className="bar-fill" style={{ height: `${Math.max(pct, b.hasUsage ? 2 : 0)}%` }} />
            </li>
          );
        })}
      </ol>
      <div className="timeline-axis muted small">
        <span>{buckets[0].label}</span>
        <span>
          peak {m === "cost" ? `$${max.toFixed(max > 0 && max < 0.01 ? 4 : 2)}` : `${shortTokens(max)} tokens`}
        </span>
        <span>{buckets[buckets.length - 1].label}</span>
      </div>
    </section>
  );
}

const COLUMNS: { key: SortKey; title: string }[] = [
  { key: "input", title: "Input" },
  { key: "output", title: "Output" },
  { key: "cache", title: "Cache" },
  { key: "total", title: "Total" },
  { key: "cost", title: "Cost" },
];

function Breakdown({
  rows,
  total,
  by,
  linkProject,
  stale,
}: {
  rows: UsageRow[];
  total: UsageRow | null;
  by: UsageDim[];
  linkProject: string | null;
  stale: boolean;
}) {
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "default", desc: true });
  const sorted = useMemo(() => sortRows(rows, by, sort.key, sort.desc), [rows, by, sort]);
  const shareMetric = total && total.priceStatus !== "unpriced" && total.cost > 0 ? "cost" : "tokens";
  const header = (key: SortKey, title: ReactNode, className = "") => {
    const active = sort.key === key;
    return (
      <th className={className} aria-sort={active ? (sort.desc ? "descending" : "ascending") : "none"}>
        <button
          type="button"
          className="sort-btn"
          onClick={() =>
            setSort((s) =>
              s.key === key ? (s.desc ? { key, desc: false } : { key: "default", desc: true }) : { key, desc: key !== "label" },
            )
          }
          title="Sort"
        >
          {title}
          {active && <span className="sort-mark">{sort.desc ? " ▾" : " ▴"}</span>}
        </button>
      </th>
    );
  };
  return (
    <section className={`usage-breakdown${stale ? " stale" : ""}`} aria-labelledby="usage-breakdown">
      <h2 id="usage-breakdown">
        By {by.map((d) => dimTitle(d).toLowerCase()).join(", then ")} <span className="count muted">{rows.length}</span>
      </h2>
      <div className="usage-table-wrap">
        <table className="usage-table usage-report">
          <thead>
            <tr>
              {by.map((d, i) => (i === 0 ? header("label", dimTitle(d), "dim") : <th key={d} className="dim">{dimTitle(d)}</th>))}
              {COLUMNS.map((c) => header(c.key, c.title))}
              <th className="share-col">Share of {shareMetric}</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map((r, i) => {
              const share = rowShare(r, total, shareMetric);
              return (
                <tr key={i}>
                  {by.map((d) => (
                    <td key={d} className="dim">
                      <DimCell row={r} dim={d} linkProject={linkProject} />
                    </td>
                  ))}
                  <td>{commas(r.input)}</td>
                  <td>{commas(r.output)}</td>
                  <td title={`read ${commas(r.cacheRead)} · write ${commas(r.cacheWrite)}`}>{commas(r.cacheRead + r.cacheWrite)}</td>
                  <td>{commas(r.total)}</td>
                  <td className={r.priceStatus === "unpriced" ? "muted" : ""}>{costCell(r.cost, r.priceStatus)}</td>
                  <td className="share-col">
                    <div className="share-cell">
                      <span className="share-bar" aria-hidden="true">
                        <span style={{ width: `${share * 100}%` }} />
                      </span>
                      <span className="share-pct">{(share * 100).toFixed(share > 0 && share < 0.01 ? 1 : 0)}%</span>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
          {total && (
            <tfoot>
              <tr>
                {by.map((d, i) => (
                  <td key={d} className="dim">
                    {i === 0 ? "TOTAL" : ""}
                  </td>
                ))}
                <td>{commas(total.input)}</td>
                <td>{commas(total.output)}</td>
                <td>{commas(total.cacheRead + total.cacheWrite)}</td>
                <td>{commas(total.total)}</td>
                <td>{costCell(total.cost, total.priceStatus)}</td>
                <td className="share-col" />
              </tr>
            </tfoot>
          )}
        </table>
      </div>
    </section>
  );
}

function DimCell({ row, dim, linkProject }: { row: UsageRow; dim: UsageDim; linkProject: string | null }) {
  const label = cellLabel(row, dim);
  if (dim === "task" && row.task && linkProject !== null) {
    return <TaskLink project={linkProject} id={row.task} className="task-chip" />;
  }
  if (dim === "session" && row.session && linkProject !== null) {
    return (
      <Link to={paths.session(linkProject, row.session)} className="mono" title="Open this session">
        {label}
      </Link>
    );
  }
  if (dim === "session" || dim === "day") return <span className="mono">{label}</span>;
  if (label.startsWith("(")) return <span className="muted">{label}</span>;
  return <>{label}</>;
}

function SubscriptionAllowance({ accounts }: { accounts: SubscriptionUsageAccount[] | undefined }) {
  if (!accounts?.length) return null;
  return (
    <section className="usage-card subscription" aria-labelledby="usage-subscription">
      <h2 id="usage-subscription">Subscription allowance</h2>
      {accounts.map((a) => {
        const badge = accountBadge(a);
        return (
          <div key={a.provider} className="sub-account">
            <div className="sub-head">
              <strong>{capitalize(a.provider)}</strong>
              {a.plan && <span className="muted">{capitalize(a.plan)}</span>}
              {badge && <span className={`tag${badge.tone === "warn" ? " warn" : ""}`}>{badge.label}</span>}
            </div>
            {a.models.length > 0 && <div className="muted small">Models: {a.models.join(", ")}</div>}
            {a.windows.length === 0 ? (
              <div className="muted small">{a.message || "Allowance unavailable"}</div>
            ) : (
              a.windows.map((w) => {
                const pct = windowPercent(w);
                const reset = resetText(w.resetsAtUnix);
                return (
                  <div key={w.id || w.label} className={`sub-window tone-${windowTone(w)}`}>
                    <div className="sub-window-head small">
                      <span>{w.label}</span>
                      <span className="mono">{pct.toFixed(0)}% used</span>
                    </div>
                    <div
                      className="gauge"
                      role="meter"
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={Math.round(pct)}
                      aria-label={`${capitalize(a.provider)} ${w.label}`}
                    >
                      <div className="gauge-fill" style={{ width: `${pct}%` }} />
                    </div>
                    {reset && (
                      <div className="muted small" title={new Date(Number(w.resetsAtUnix) * 1000).toLocaleString()}>
                        {reset}
                      </div>
                    )}
                  </div>
                );
              })
            )}
            {a.windows.length > 0 && a.message && <div className="muted small">{a.message}</div>}
          </div>
        );
      })}
      <p className="muted small">Shared provider allowance, separate from the ycc token usage below.</p>
    </section>
  );
}
