// The live sessions view (`/p/<project>/live`, spec §7.3): the project's live
// sessions with intent, held task, activity and claimed paths, and the active
// notice board, where the user posts and clears notices. Shown while the
// project's session coordination is on.
import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { ClaimedPath, ProjectLiveSession, ProjectNotice } from "../../gen/ycc/v1/ycc_pb";
import { client, errorMessage, isUnauthorized } from "../../api/client";
import { authStore } from "../../api/auth";
import { queryKeys, useLeaseHolders, useProjectNotices, useProjects } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { Modal } from "../../ui/Modal";
import { Icon } from "../../ui/icons";
import { toast } from "../../ui/toast";
import { relativeTime } from "../sessions/feed";
import { TaskLink } from "../backlog/parts";
import { activityBadge, expiresIn, EXPIRY_CHOICES, NOTICE_MAX, SUBJECT_MAX } from "./model";
import { splitClaims } from "./claims";
import { NoticeList, SessionLink } from "./parts";

/** Claimed paths shown before "N more". */
const CLAIMS_SHOWN = 6;

function useNow(periodMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), periodMs);
    return () => clearInterval(t);
  }, [periodMs]);
  return now;
}

function onRpcError(err: unknown, what: string, op: string) {
  if (isUnauthorized(err)) {
    authStore.expire();
    return;
  }
  toast(`${what}: ${errorMessage(err)}`, "error", { op, err });
}

export function CoordinationPage({ project }: { project: string }) {
  const projects = useProjects();
  const board = useProjectNotices(project);
  const holders = useLeaseHolders(project);
  const now = useNow();
  const [clearing, setClearing] = useState<ProjectNotice | null>(null);
  const head = (
    <header className="page-head">
      <h1>
        Live sessions <span className="muted">· {project}</span>
      </h1>
      {board.on && (
        <div className="page-actions">
          <button
            type="button"
            className="btn ghost small"
            disabled={board.isFetching}
            data-track="coordination.refresh"
            onClick={() => void board.refetch()}
            title="Refresh"
            aria-label="Refresh live sessions and notices"
          >
            <Icon name="refresh" size={13} />
          </button>
        </div>
      )}
    </header>
  );
  if (projects.isPending) return <div className="page muted">Loading…</div>;
  if (!board.on) {
    return (
      <div className="page coordination-page">
        {head}
        <p className="muted">
          Session coordination is off for {project}. Turn it on under{" "}
          <Link to={paths.settings("experimental")}>Settings › Experimental</Link> to see which live sessions hold which tasks and
          files, and to post notices to them.
        </p>
        {holders.length > 0 && (
          <section className="coord-section" aria-labelledby="coord-leases">
            <h2 id="coord-leases">
              Leases <span className="count">{holders.reduce((n, s) => n + s.leases.length, 0)}</span>
            </h2>
            <p className="muted small">Sessions holding exclusive leases on deploy targets or shared services.</p>
            <ul className="live-sessions">
              {holders.map((s) => (
                <LiveSessionCard key={s.sessionId} project={project} session={s} now={now} />
              ))}
            </ul>
          </section>
        )}
      </div>
    );
  }
  const data = board.data;
  return (
    <div className="page coordination-page">
      {head}
      {board.isError && (
        <p className="error" role="alert">
          {errorMessage(board.error, "Couldn’t load the notice board.")}
        </p>
      )}
      <section className="coord-section" aria-labelledby="coord-notices">
        <h2 id="coord-notices">
          Notices <span className="count">{data?.notices.length ?? 0}</span>
        </h2>
        <p className="muted small">
          Sessions see new notices at their next checkpoint and in <code>list_sessions</code>. A notice informs them; it never
          answers a question or approves anything.
        </p>
        <PostNoticeForm project={project} />
        {data && data.notices.length === 0 && <p className="muted small">No active notices.</p>}
        {data && data.notices.length > 0 && (
          <NoticeList
            project={project}
            notices={data.notices}
            now={now}
            action={(n) => (
              <button type="button" className="link" data-track="coordination.clear_notice" onClick={() => setClearing(n)}>
                Clear
              </button>
            )}
          />
        )}
      </section>
      <section className="coord-section" aria-labelledby="coord-sessions">
        <h2 id="coord-sessions">
          Sessions <span className="count">{data?.sessions.length ?? 0}</span>
        </h2>
        <p className="muted small">Live sessions of this project; those quiet for over an hour without an intent are left out.</p>
        {board.isPending && <p className="muted">Loading…</p>}
        {data && data.sessions.length === 0 && <p className="muted small">No live sessions.</p>}
        {data && data.sessions.length > 0 && (
          <ul className="live-sessions">
            {data.sessions.map((s) => (
              <LiveSessionCard key={s.sessionId} project={project} session={s} now={now} />
            ))}
          </ul>
        )}
      </section>
      <ClearNoticeDialog project={project} notice={clearing} onClose={() => setClearing(null)} />
    </div>
  );
}

function LiveSessionCard({ project, session: s, now }: { project: string; session: ProjectLiveSession; now: number }) {
  const [allClaims, setAllClaims] = useState(false);
  const badge = activityBadge(s.activity);
  const { definite, ambiguous } = splitClaims(s.claims);
  const claims = allClaims ? definite : definite.slice(0, CLAIMS_SHOWN);
  return (
    <li className="live-session" data-session-id={s.sessionId}>
      <div className="live-session-head">
        <Link to={paths.session(project, s.sessionId)} className="live-session-title">
          {s.title || s.sessionId}
        </Link>
        <span className={`badge badge-${badge.tone}`}>{badge.label}</span>
      </div>
      <div className="live-session-meta">
        <SessionLink project={project} id={s.sessionId}>
          {s.sessionId}
        </SessionLink>
        {s.mode && <span>{s.mode}</span>}
        {s.lastActivity && <span>active {relativeTime(s.lastActivity, now)}</span>}
        {s.runningJobs > 0 && <span>{s.runningJobs === 1 ? "1 job running" : `${s.runningJobs} jobs running`}</span>}
        {s.worktree && (
          <span className="mono" title={s.worktree}>
            worktree {s.worktree.split("/").filter(Boolean).pop()}
          </span>
        )}
      </div>
      {s.intent && (
        <p className="live-session-intent">
          <span className="label">Intent</span> {s.intent}
          {s.intentSetAt && <span className="muted small"> · set {relativeTime(s.intentSetAt, now)}</span>}
        </p>
      )}
      {s.leases.length > 0 && (
        <ul className="live-session-leases">
          {s.leases.map((l) => {
            const expiry = expiresIn(l.expiresAt, now);
            return (
              <li key={l.name}>
                <span className="label">Lease</span> <span className="mono">{l.name}</span>: {l.reason}
                <span className="muted small">
                  {expiry && ` · expires ${expiry}`}
                  {l.jobId && ` · or when ${l.jobId} exits`}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {s.focusTasks.length > 0 && (
        <p className="live-session-focus">
          <span className="label">{s.held ? "Holds" : "Earlier focus"}</span>{" "}
          {s.focusTasks.map((t, i) => (
            <span key={t}>
              {i > 0 && ", "}
              <TaskLink project={project} id={t} className={s.held ? "held-task" : "task-link"} />
            </span>
          ))}
        </p>
      )}
      {definite.length > 0 && (
        <div className="live-session-claims">
          <span className="label">
            Claims {definite.length} {definite.length === 1 ? "path" : "paths"}
          </span>
          <ClaimList project={project} sessionId={s.sessionId} claims={claims} now={now} />
          {definite.length > CLAIMS_SHOWN && (
            <button type="button" className="link small" data-track="coordination.toggle_claims" onClick={() => setAllClaims((v) => !v)}>
              {allClaims ? "Show fewer" : `Show ${definite.length - CLAIMS_SHOWN} more`}
            </button>
          )}
        </div>
      )}
      {ambiguous.length > 0 && (
        <details className="live-session-claims ambiguous-claims">
          <summary
            className="label"
            title="Changed while this session's shell and another session's shell both ran, so either may have written them"
          >
            Possibly this session’s: {ambiguous.length} {ambiguous.length === 1 ? "path" : "paths"}
          </summary>
          <ClaimList project={project} sessionId={s.sessionId} claims={ambiguous} now={now} />
        </details>
      )}
    </li>
  );
}

function ClaimList({ project, sessionId, claims, now }: { project: string; sessionId: string; claims: ClaimedPath[]; now: number }) {
  return (
    <ul>
      {claims.map((c) => (
        <li key={c.path}>
          <Link to={paths.files(project, c.path, { session: sessionId })} className="mono">
            {c.path}
          </Link>
          {c.since && <span className="muted small"> {relativeTime(c.since, now)}</span>}
        </li>
      ))}
    </ul>
  );
}

function PostNoticeForm({ project }: { project: string }) {
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [subject, setSubject] = useState("");
  const [expiry, setExpiry] = useState(EXPIRY_CHOICES[1].seconds);
  const [busy, setBusy] = useState(false);
  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (busy || !text.trim()) return;
    setBusy(true);
    track.action("coordination.post_notice", "click", { subject: !!subject.trim() });
    try {
      await client.postProjectNotice({ project, text: text.trim(), subject: subject.trim(), expiresInSeconds: BigInt(expiry) });
      setText("");
      setSubject("");
      await qc.invalidateQueries({ queryKey: queryKeys.projectNotices(project) });
    } catch (err) {
      onRpcError(err, "Couldn’t post the notice", "coordination.post_notice");
    } finally {
      setBusy(false);
    }
  };
  return (
    <form className="notice-form" onSubmit={(e) => void submit(e)} aria-label="Post a notice">
      <input
        className="field notice-form-text"
        value={text}
        maxLength={NOTICE_MAX}
        placeholder="Post a notice, e.g. “deploying the API; avoid restarts until 15:00”"
        aria-label="Notice text"
        disabled={busy}
        onChange={(e) => setText(e.target.value)}
      />
      <input
        className="field notice-form-subject"
        value={subject}
        maxLength={SUBJECT_MAX}
        placeholder="Subject (optional)"
        aria-label="Subject (optional)"
        disabled={busy}
        onChange={(e) => setSubject(e.target.value)}
      />
      <select aria-label="Expires after" value={expiry} disabled={busy} onChange={(e) => setExpiry(Number(e.target.value))}>
        {EXPIRY_CHOICES.map((c) => (
          <option key={c.seconds} value={c.seconds}>
            for {c.label}
          </option>
        ))}
      </select>
      <button type="submit" className="btn primary small" disabled={busy || !text.trim()}>
        {busy ? "Posting…" : "Post"}
      </button>
    </form>
  );
}

function ClearNoticeDialog({ project, notice, onClose }: { project: string; notice: ProjectNotice | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => setReason(""), [notice?.id]);
  const submit = async (e?: FormEvent) => {
    e?.preventDefault();
    if (!notice || busy) return;
    setBusy(true);
    track.submit("notice_clear");
    try {
      await client.clearProjectNotice({ project, id: notice.id, reason: reason.trim() });
      await qc.invalidateQueries({ queryKey: queryKeys.projectNotices(project) });
      onClose();
    } catch (err) {
      onRpcError(err, "Couldn’t clear the notice", "coordination.clear_notice");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={notice !== null} onClose={() => !busy && onClose()} title={`Clear notice ${notice?.id ?? ""}`} flow="notice_clear">
      <form className="task-editor" onSubmit={(e) => void submit(e)}>
        <p className="muted small">“{notice?.text}”</p>
        <label className="field-label">
          Reason <span className="muted small">optional · other sessions see it with the clear</span>
          <input className="field" value={reason} maxLength={NOTICE_MAX} autoFocus disabled={busy} onChange={(e) => setReason(e.target.value)} />
        </label>
        <div className="editor-actions">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {busy ? "Clearing…" : "Clear notice"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
