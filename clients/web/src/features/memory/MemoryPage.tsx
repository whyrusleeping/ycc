// Memory & plans (`/p/<project>/memory`, `/p/<project>/plans[/<name>]`):
// memory.md as typed notes grouped by section — each with its model-chosen
// classification, runtime provenance (session#event/actor), id, and whether
// it still reaches prompts — plus the prompt-budget gauge and a one-tap
// groom; and the plan library (plans/*.md) rendered as markdown.
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useMemory, usePlan, usePlans } from "../../api/queries";
import { paths } from "../../app/paths";
import type { GetMemoryResponse, MemoryGroomRun } from "../../gen/ycc/v1/ycc_pb";
import { CopyButton } from "../../ui/CopyButton";
import { toast } from "../../ui/toast";
import { CodeBlock } from "../code/CodeBlock";
import { FileLinksProvider } from "../files/FileRef";
import { SourceToggle } from "../files/FileViewer";
import { useProjectFileLinks } from "../files/links";
import { Markdown } from "../markdown/Markdown";
import { relativeTime } from "../sessions/feed";
import {
  KIND_LABELS,
  filterNotes,
  groupBySection,
  kb,
  parseMemory,
  provenance,
  type MemoryNote,
} from "./model";

function Tabs({ project, active }: { project: string; active: "memory" | "plans" }) {
  return (
    <nav className="tabs" aria-label="Memory and plans">
      <Link to={paths.memory(project)} className={`tab${active === "memory" ? " active" : ""}`} aria-current={active === "memory" ? "page" : undefined}>
        Memory
      </Link>
      <Link to={paths.plans(project)} className={`tab${active === "plans" ? " active" : ""}`} aria-current={active === "plans" ? "page" : undefined}>
        Plans
      </Link>
    </nav>
  );
}

export function MemoryPage({ project }: { project: string }) {
  const q = useMemory(project);
  const [raw, setRaw] = useState(false);
  const [showInactive, setShowInactive] = useState(false);
  const [query, setQuery] = useState("");
  const parsed = useMemo(() => parseMemory(q.data?.content ?? ""), [q.data?.content]);
  const shown = useMemo(() => groupBySection(filterNotes(parsed.notes, { showInactive, query })), [parsed, showInactive, query]);
  const links = useProjectFileLinks(project, "", "main");
  const inactive = parsed.counts.superseded + parsed.counts.retired + parsed.counts.retraction;
  const empty = q.data !== undefined && !q.data.content.trim();
  return (
    <div className="page memory-page">
      <header className="page-head">
        <h1>
          Memory & plans <span className="muted">· {project || "default"}</span>
        </h1>
        <Tabs project={project} active="memory" />
      </header>
      {q.isPending ? (
        <p className="muted">Loading…</p>
      ) : q.isError ? (
        <p className="error">{errorMessage(q.error, "Couldn’t load memory.")}</p>
      ) : (
        <>
          {q.data.softBudget > 0 && <BudgetCard project={project} status={q.data} />}
          {empty ? (
            <div className="empty-state">
              <p>
                <strong>No memory yet.</strong>
              </p>
              <p className="muted">
                Agents record operational notes about working on this project here as they learn them — environment quirks,
                gotchas, and your preferences.
              </p>
            </div>
          ) : (
            <>
              <div className="memory-toolbar">
                <input
                  type="search"
                  className="field"
                  placeholder="Filter notes"
                  aria-label="Filter notes"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  disabled={raw}
                />
                <label className="check">
                  <input type="checkbox" checked={showInactive} disabled={raw} onChange={(e) => setShowInactive(e.target.checked)} />
                  Show superseded & retired ({inactive})
                </label>
                <span className="muted small">
                  {parsed.counts.active} active {parsed.counts.active === 1 ? "note" : "notes"}
                </span>
                <span className="spacer" />
                <SourceToggle rendered={!raw} onChange={(r) => setRaw(!r)} />
              </div>
              {parsed.preamble.length > 0 && !raw && (
                <div className="memory-preamble muted small">
                  {parsed.preamble.map((l, i) => (
                    <p key={i}>{l}</p>
                  ))}
                </div>
              )}
              {raw ? (
                <CodeBlock code={q.data.content} language={null} label="memory.md" className="memory-raw" />
              ) : shown.length === 0 ? (
                <p className="muted">No notes match.</p>
              ) : (
                <FileLinksProvider value={links}>
                  {shown.map((s) => (
                    <section key={s.title} className="memory-section" aria-label={s.title}>
                      <h2>{s.title}</h2>
                      <ul className="memory-notes">
                        {s.notes.map((n) => (
                          <NoteCard key={n.id} project={project} note={n} />
                        ))}
                      </ul>
                    </section>
                  ))}
                </FileLinksProvider>
              )}
            </>
          )}
          {q.data.path && (
            <p className="muted small mono memory-path">
              {q.data.path} <CopyButton text={q.data.path} title="Copy the memory.md path" />
            </p>
          )}
        </>
      )}
    </div>
  );
}

function jumpTo(id: string) {
  const el = document.getElementById(`note-${id}`);
  if (!el) {
    toast(`Note ${id} is hidden by the current filter.`, "info");
    return;
  }
  el.scrollIntoView({ block: "center" });
  el.classList.remove("flash");
  void el.offsetWidth;
  el.classList.add("flash");
}

function IdRefs({ ids }: { ids: string[] }) {
  return (
    <>
      {ids.map((id, i) => (
        <span key={id}>
          {i > 0 && ", "}
          <button type="button" className="link mono" onClick={() => jumpTo(id)} title={`Show note ${id}`}>
            {id}
          </button>
        </span>
      ))}
    </>
  );
}

function NoteCard({ project, note: n }: { project: string; note: MemoryNote }) {
  const kindLabel = n.kind ? KIND_LABELS[n.kind] : "legacy (untyped)";
  const evidence = provenance(n);
  return (
    <li id={`note-${n.id}`} className={`memory-note status-${n.status}`}>
      <div className="note-head">
        <span className={`kind-badge kind-${n.kind ?? "legacy"}`} title="Classification chosen by the recording model — not verified authority">
          {kindLabel}
        </span>
        {n.date && <span className="muted small">{n.date}</span>}
        {n.status === "superseded" && (
          <span className="tag">
            superseded by <IdRefs ids={n.supersededBy} />
          </span>
        )}
        {n.status === "retired" && (
          <span className="tag">
            retired by <IdRefs ids={n.supersededBy} />
          </span>
        )}
        {n.status === "retraction" && <span className="tag">audit record</span>}
        <span className="spacer" />
        <span className="note-id mono small">{n.id}</span>
        <CopyButton text={n.id} label="Copy id" title={`Copy note id ${n.id}`} />
      </div>
      <Markdown text={n.note} className="note-text" />
      {(evidence || n.supersedes.length > 0 || (n.scope && n.scope !== "workspace")) && (
        <div className="note-prov muted small">
          {evidence && evidence !== "no evidence" ? (
            <span title="Runtime-selected candidate evidence: verify that this event supports the note">
              Evidence:{" "}
              <Link to={paths.session(project, n.session)} className="mono">
                {n.session}
              </Link>
              <span className="mono">#{n.event}</span>
              {n.actor && n.actor !== "coordinator" && <span> · by {n.actor}</span>}
            </span>
          ) : evidence ? (
            <span>No recorded evidence</span>
          ) : null}
          {n.scope && n.scope !== "workspace" && <span> · scope {n.scope}</span>}
          {n.supersedes.length > 0 && (
            <span>
              {" "}
              · {n.kind === "retraction" ? "retires" : "corrects"} <IdRefs ids={n.supersedes} />
            </span>
          )}
        </div>
      )}
    </li>
  );
}

function lastGroomSummary(run: MemoryGroomRun): string {
  const when = relativeTime(new Date(Number(run.startedUnix) * 1000).toISOString());
  if (Number(run.finishedUnix) === 0) return `Started ${when} · ${kb(run.activeBefore)} before`;
  const outcome = run.outcome || "finished";
  return `${outcome[0].toUpperCase()}${outcome.slice(1)} ${when} · ${kb(run.activeBefore)} → ${kb(run.activeAfter)}`;
}

function BudgetCard({ project, status }: { project: string; status: GetMemoryResponse }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [starting, setStarting] = useState(false);
  const over = status.activeBytes >= status.softBudget;
  const scale = Math.max(status.hardBudget, status.softBudget, 1);
  const pct = Math.min(100, (status.activeBytes / scale) * 100);
  const softPct = Math.min(100, (status.softBudget / scale) * 100);
  const groom = async () => {
    if (starting) return;
    setStarting(true);
    try {
      const resp = await client.startSession({ project, mode: "pm", preset: "memory-groom" });
      void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      void qc.invalidateQueries({ queryKey: queryKeys.memory(project) });
      navigate(paths.session(project, resp.sessionId));
    } catch (err) {
      if (isUnauthorized(err)) {
        authStore.expire();
        return;
      }
      toast(`Couldn’t start grooming: ${errorMessage(err)}`);
      setStarting(false);
    }
  };
  const last = status.lastGroom;
  return (
    <section className={`budget-card${over ? " over" : ""}`} aria-label="Prompt budget">
      <div className="budget-head">
        <strong>Prompt budget</strong>
        <span>
          {kb(status.activeBytes)} of {kb(status.softBudget)} · {status.activeNotes} active {status.activeNotes === 1 ? "note" : "notes"}
        </span>
        <span className="spacer" />
        {status.groomSessionId ? (
          <Link to={paths.session(project, status.groomSessionId)} className="btn small">
            Automatic groom running — watch
          </Link>
        ) : (
          <button type="button" className="btn small" disabled={starting} onClick={() => void groom()} title="Start a memory-groom session (pm mode)">
            {starting ? "Starting…" : "Groom now"}
          </button>
        )}
      </div>
      <div
        className="gauge"
        role="meter"
        aria-label="Active memory"
        aria-valuemin={0}
        aria-valuemax={scale}
        aria-valuenow={status.activeBytes}
        title={`Soft budget ${kb(status.softBudget)} · hard limit ${kb(status.hardBudget)}`}
      >
        <div className="gauge-fill" style={{ width: `${pct}%` }} />
        <div className="gauge-mark" style={{ left: `${softPct}%` }} />
      </div>
      <p className="muted small">
        Active notes are sent with every agent prompt; superseded and retired notes stay in the file as an audit trail but cost
        nothing.
        {over &&
          (status.autoGroom
            ? " Over budget: the daemon grooms automatically (at most every few hours)."
            : " Over budget, and automatic grooming is off (memory.auto_groom).")}{" "}
        Hard limit {kb(status.hardBudget)}.
      </p>
      {last && (
        <p className="small">
          Last automatic groom:{" "}
          <Link to={paths.session(project, last.sessionId)}>{lastGroomSummary(last)}</Link>
        </p>
      )}
    </section>
  );
}

export function PlansPage({ project, name }: { project: string; name: string }) {
  const plans = usePlans(project);
  const navigate = useNavigate();
  const list = plans.data ?? [];
  return (
    <div className="page plans-page">
      <header className="page-head">
        <h1>
          Memory & plans <span className="muted">· {project || "default"}</span>
        </h1>
        <Tabs project={project} active="plans" />
      </header>
      {plans.isPending ? (
        <p className="muted">Loading…</p>
      ) : plans.isError ? (
        <p className="error">{errorMessage(plans.error, "Couldn’t load plans.")}</p>
      ) : list.length === 0 ? (
        <div className="empty-state">
          <p>
            <strong>No plans yet.</strong>
          </p>
          <p className="muted">
            Plans are reusable runbooks saved as <code>plans/*.md</code> in the project.
          </p>
        </div>
      ) : (
        <div className="plans-body">
          <nav className="plan-list" aria-label="Plans">
            <ul>
              {list.map((p) => (
                <li key={p.name}>
                  <button
                    type="button"
                    className={`plan-item${p.name === name ? " current" : ""}`}
                    aria-current={p.name === name ? "page" : undefined}
                    onClick={() => navigate(paths.plans(project, p.name))}
                  >
                    <span className="plan-title">{p.title || p.name}</span>
                    <span className="plan-name mono small muted">{p.name}</span>
                  </button>
                </li>
              ))}
            </ul>
          </nav>
          <section className="plan-view">
            {name ? <PlanView key={name} project={project} name={name} /> : <p className="muted pad">Choose a plan.</p>}
          </section>
        </div>
      )}
    </div>
  );
}

function PlanView({ project, name }: { project: string; name: string }) {
  const q = usePlan(project, name);
  const [rendered, setRendered] = useState(true);
  // Plans live in plans/: relative links resolve there.
  const links = useProjectFileLinks(project, "plans", "main");
  if (q.isPending) return <p className="muted pad">Loading…</p>;
  if (q.isError) return <p className="error pad">{errorMessage(q.error, "Couldn’t load the plan.")}</p>;
  const file = `plans/${name}.md`;
  return (
    <article className="plan-doc">
      <div className="file-toolbar">
        <span className="plan-doc-title">
          <strong>{q.data.title || name}</strong> <span className="muted mono small">{file}</span>
        </span>
        <span className="file-actions">
          <SourceToggle rendered={rendered} onChange={setRendered} />
          <CopyButton text={q.data.content} label="Copy" title="Copy the plan’s markdown" />
          <Link to={paths.files(project, file)} className="btn ghost small">
            Open in Files
          </Link>
        </span>
      </div>
      {rendered ? (
        <FileLinksProvider value={links}>
          <Markdown text={q.data.content} className="plan-md" />
        </FileLinksProvider>
      ) : (
        <CodeBlock code={q.data.content} language={null} label="markdown" />
      )}
    </article>
  );
}
