// Watch logic (docs/design/watches.md, spec §9.2): status and schedule labels,
// list ordering, polling, and the editor draft ↔ WatchDefinition conversion.
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Watch, WatchDefinitionSchema, WatchReport } from "../../gen/ycc/v1/ycc_pb";

export type WatchTone = "ok" | "warn" | "alert" | "muted";

/** The daemon's name rule: 1-64 of [a-z0-9_-], starting with a letter or digit. */
export const WATCH_NAME_RE = /^[a-z0-9][a-z0-9_-]{0,63}$/;
export const MIN_INTERVAL_SECS = 300;

/** A report's label: its status (ok/warn/alert), else its kind (failed/skipped). */
export function reportLabel(r: WatchReport | undefined): string {
  if (!r) return "no runs yet";
  if (r.kind === "failed") return "failed";
  return r.status || r.kind || "unknown";
}

/** Failed runs count as warn (the design doc); skipped entries carry no verdict. */
export function reportTone(r: WatchReport | undefined): WatchTone {
  if (!r) return "muted";
  if (r.kind === "failed") return "warn";
  switch (r.status) {
    case "ok":
      return "ok";
    case "warn":
      return "warn";
    case "alert":
      return "alert";
    default:
      return "muted";
  }
}

/** What produced a report; skipped and failed entries say so in their badge and summary. */
export function kindNote(kind: string): string {
  switch (kind) {
    case "run":
      return "model run";
    case "probe":
      return "probe only";
    case "skipped":
    case "failed":
      return "";
    default:
      return kind;
  }
}

/** "in 5m", "in 3h", "due now" for a future RFC 3339 time. */
export function untilTime(iso: string, now: number = Date.now()): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.round((t - now) / 1000);
  if (s < 45) return "due now";
  const m = Math.round(s / 60);
  if (m < 60) return `in ${m}m`;
  const h = Math.round(m / 60);
  if (h < 48) return `in ${h}h`;
  return `in ${Math.round(h / 24)}d`;
}

/** "30m", "2h", "1d 6h", "1h 30m": an interval in its largest exact units. */
export function formatInterval(secs: number | bigint): string {
  let s = Number(secs);
  if (!Number.isFinite(s) || s <= 0) return "—";
  const parts: string[] = [];
  for (const [unit, size] of [["d", 86400], ["h", 3600], ["m", 60], ["s", 1]] as const) {
    if (s >= size) {
      parts.push(`${Math.floor(s / size)}${unit}`);
      s %= size;
    }
  }
  return parts.slice(0, 2).join(" ");
}

/** The schedule line: paused (by you / expired), running, or when the next run is due. */
export function scheduleState(w: Watch, now: number = Date.now()): { label: string; tone: WatchTone; running: boolean } {
  if (w.paused) return { label: w.pausedReason === "expired" ? "expired" : "paused", tone: "muted", running: false };
  if (w.running) return { label: "running", tone: "ok", running: true };
  const next = w.nextDue ? untilTime(w.nextDue, now) : "";
  return { label: next ? `next ${next}` : "scheduled", tone: "muted", running: false };
}

/** Parked runs waiting on the user, as a short indicator ("" when none). */
export function parkedLabel(w: Watch): string {
  const n = w.parkedSessionIds.length;
  if (!n) return "";
  return n === 1 ? "1 question waiting" : `${n} questions waiting`;
}

const toneRank: Record<WatchTone, number> = { alert: 0, warn: 1, ok: 2, muted: 3 };

/** Waiting questions, then alert, warn, ok, never-run; paused last; by name within. */
export function sortWatches(list: readonly Watch[]): Watch[] {
  const rank = (w: Watch) => (w.parkedSessionIds.length ? -1 : w.paused ? 4 : toneRank[reportTone(w.last)]);
  return [...list].sort((a, b) => rank(a) - rank(b) || (a.definition?.name ?? "").localeCompare(b.definition?.name ?? ""));
}

/** Poll quickly while a run is active (a report lands soon), else slowly. */
export function pollInterval(list: readonly Watch[] | undefined): number {
  return list?.some((w) => w.running) ? 5_000 : 30_000;
}

export type IntervalUnit = "minutes" | "hours" | "days";

const UNIT_SECS: Record<IntervalUnit, number> = { minutes: 60, hours: 3600, days: 86400 };

export interface WatchDraft {
  name: string;
  brief: string;
  every: string;
  unit: IntervalUnit;
  model: string;
  probe: string;
  probeAlways: boolean;
  /** A datetime-local value ("YYYY-MM-DDTHH:mm", local time); "" = never. */
  expires: string;
  /** One pre-authorized action per line. */
  allowedActions: string;
}

export function emptyDraft(): WatchDraft {
  return { name: "", brief: "", every: "30", unit: "minutes", model: "", probe: "", probeAlways: false, expires: "", allowedActions: "" };
}

/** The interval in the largest unit that divides it evenly. */
function splitInterval(secs: number): { every: string; unit: IntervalUnit } {
  for (const unit of ["days", "hours", "minutes"] as const) {
    if (secs > 0 && secs % UNIT_SECS[unit] === 0) return { every: String(secs / UNIT_SECS[unit]), unit };
  }
  // Odd intervals (seconds) from an agent-made watch: round to whole minutes.
  return { every: String(Math.max(1, Math.round(secs / 60))), unit: "minutes" };
}

const pad = (n: number) => String(n).padStart(2, "0");

/** An RFC 3339 time as a local datetime-local value. */
export function toLocalInput(iso: string): string {
  const t = Date.parse(iso);
  if (!iso || Number.isNaN(t)) return "";
  const d = new Date(t);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local value as RFC 3339 (UTC, whole seconds); "" stays "". */
export function fromLocalInput(value: string): string {
  if (!value.trim()) return "";
  const t = new Date(value).getTime();
  if (Number.isNaN(t)) return "";
  return new Date(t).toISOString().replace(/\.\d{3}Z$/, "Z");
}

export function draftFromWatch(w: Watch): WatchDraft {
  const d = w.definition;
  return {
    name: d?.name ?? "",
    brief: d?.brief ?? "",
    ...splitInterval(Number(d?.intervalSecs ?? 0)),
    model: d?.model ?? "",
    probe: d?.probe ?? "",
    probeAlways: d?.probeAlways ?? false,
    expires: toLocalInput(d?.expires ?? ""),
    allowedActions: (d?.allowedActions ?? []).join("\n"),
  };
}

export function intervalSecs(d: WatchDraft): number {
  const n = Number(d.every);
  return Number.isFinite(n) && n > 0 ? Math.round(n * UNIT_SECS[d.unit]) : 0;
}

/** The first problem the daemon would reject, or null. */
export function draftError(d: WatchDraft, creating: boolean, now: number = Date.now()): string | null {
  if (!WATCH_NAME_RE.test(d.name.trim())) return "Name: 1–64 lowercase letters, digits, - or _, starting with a letter or digit.";
  if (!d.brief.trim()) return "Describe what each run checks.";
  if (intervalSecs(d) < MIN_INTERVAL_SECS) return "The interval must be at least 5 minutes.";
  if (d.expires.trim()) {
    const iso = fromLocalInput(d.expires);
    if (!iso) return "The expiry isn’t a valid date and time.";
    if (creating && Date.parse(iso) <= now) return "The expiry is in the past.";
  }
  return null;
}

export function buildDefinition(d: WatchDraft): MessageInitShape<typeof WatchDefinitionSchema> {
  return {
    name: d.name.trim(),
    brief: d.brief.trim(),
    intervalSecs: BigInt(intervalSecs(d)),
    model: d.model,
    probe: d.probe.trim(),
    probeAlways: d.probe.trim() !== "" && d.probeAlways,
    expires: fromLocalInput(d.expires),
    allowedActions: d.allowedActions
      .split("\n")
      .map((a) => a.trim())
      .filter(Boolean),
  };
}
