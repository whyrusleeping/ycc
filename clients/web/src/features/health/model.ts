// Daemon health (GetHealth): the shell banner's summary of failing checks.
import type { HealthCheck } from "../../gen/ycc/v1/ycc_pb";

export type HealthTone = "fail" | "warn";

/** The banner for warn/fail checks (fail first), or null when nothing needs attention. */
export function healthBanner(checks: readonly HealthCheck[]): { tone: HealthTone; text: string } | null {
  const bad = checks.filter((c) => c.status === "fail" || c.status === "warn");
  if (!bad.length) return null;
  bad.sort((a, b) => (a.status === b.status ? 0 : a.status === "fail" ? -1 : 1));
  const word = (c: HealthCheck) => (c.id.startsWith("disk:") ? "low" : c.status === "fail" ? "failing" : "degraded");
  return {
    tone: bad[0].status === "fail" ? "fail" : "warn",
    text: bad.map((c) => `${c.label || c.id} ${word(c)}`).join(" · "),
  };
}
