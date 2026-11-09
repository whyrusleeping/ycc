// Pure state for the first-run checklist (GetStarted).
import type { HealthCheck, ProjectInfo } from "../../gen/ycc/v1/ycc_pb";

export interface ModelStep {
  /** ok: the coordinator answered; todo: it can't yet; unknown: the daemon has no health checks. */
  state: "ok" | "pending" | "todo" | "unknown";
  detail: string;
}

/** The coordinator model's readiness, from its GetHealth check. */
export function modelStep(coordinator: string, checks: readonly HealthCheck[] | undefined, healthUnavailable: boolean): ModelStep {
  if (healthUnavailable) return { state: "unknown", detail: "" };
  if (!checks || !coordinator) return { state: "pending", detail: "" };
  const check = checks.find((c) => c.id === `model:${coordinator}`);
  if (!check || check.status === "pending") return { state: "pending", detail: "" };
  if (check.status === "ok") return { state: "ok", detail: check.detail };
  return { state: "todo", detail: check.detail };
}

/** The project the first session goes to: the first one still needing onboarding, else the first. */
export function onboardingTarget(projects: readonly ProjectInfo[]): ProjectInfo | undefined {
  return projects.find((p) => p.needsOnboarding) ?? projects[0];
}
