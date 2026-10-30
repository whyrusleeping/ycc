// Daemon health: the sidebar banner for warn/fail checks and the Settings
// panel listing every check with a "Check now" re-run.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { errorMessage } from "../../api/client";
import { refreshHealth, useHealth } from "../../api/queries";
import { paths } from "../../app/paths";
import { track } from "../../app/analytics";
import { relativeTime } from "../sessions/feed";
import { healthBanner } from "./model";

/** Shown only while a check is warn/fail; a failing GetHealth shows nothing. */
export function HealthBanner() {
  const health = useHealth();
  const banner = health.data ? healthBanner(health.data) : null;
  if (!banner) return null;
  return (
    <Link
      to={paths.settings("health")}
      className={`health-banner tone-${banner.tone}`}
      role="status"
      title="Open daemon health"
      data-track="sidebar.health"
    >
      <span className="health-banner-mark" aria-hidden="true" />
      <span className="health-banner-text">{banner.text}</span>
    </Link>
  );
}

function useMinuteTick(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);
  return now;
}

function checkedAgo(iso: string, now: number): string {
  const r = relativeTime(iso, now);
  return r === "now" ? "checked just now" : `checked ${r}`;
}

/** Remedies quote commands in backticks. */
function Inline({ text }: { text: string }) {
  return (
    <>
      {text.split(/`([^`]+)`/).map((part, i) => (i % 2 ? <code key={i}>{part}</code> : part))}
    </>
  );
}

export function HealthPanel() {
  const qc = useQueryClient();
  const health = useHealth();
  const now = useMinuteTick();
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const checkNow = async () => {
    track.action("settings.health_check_now", "click");
    setChecking(true);
    setError(null);
    try {
      await refreshHealth(qc);
    } catch (err) {
      track.error("settings.health", err);
      setError(errorMessage(err, "Couldn’t run the health checks."));
    } finally {
      setChecking(false);
    }
  };
  if (health.isPending) return <p className="muted">Loading…</p>;
  if (health.isError && !health.data) {
    return ConnectError.from(health.error).code === Code.Unimplemented ? (
      <p className="muted">This daemon doesn’t report health; it predates health checks.</p>
    ) : (
      <p className="error" role="alert">{errorMessage(health.error, "Couldn’t load health.")}</p>
    );
  }
  const checks = health.data ?? [];
  return (
    <>
      <div className="settings-line">
        <span className="muted small">
          {checking
            ? "Re-running every check; this can take up to a minute."
            : "The daemon re-checks these in the background; Check now re-runs them all."}
        </span>
        <button type="button" className="btn small" disabled={checking} onClick={() => void checkNow()}>
          {checking ? "Checking…" : "Check now"}
        </button>
      </div>
      {error && <p className="error settings-error" role="alert">{error}</p>}
      {checks.length === 0 ? (
        <p className="muted">This daemon runs no health checks.</p>
      ) : (
        <ul className="health-list">
          {checks.map((c) => (
            <li key={c.id} className="health-row">
              <span className={`health-status st-${c.status}`}>{c.status}</span>
              <div className="health-main">
                <strong>{c.label || c.id}</strong>
                {c.detail && (
                  <div className="small">
                    <Inline text={c.detail} />
                  </div>
                )}
                {c.remedy && c.status !== "ok" && (
                  <div className="muted small">
                    <Inline text={c.remedy} />
                  </div>
                )}
              </div>
              <span className="muted small health-when" title={c.checkedAt ? new Date(c.checkedAt).toLocaleString() : undefined}>
                {c.checkedAt ? checkedAgo(c.checkedAt, now) : "not yet checked"}
              </span>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
