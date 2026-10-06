// Pure Anthropic subscription-login helpers (mirrors YccKit AnthropicLoginModel):
// validating the daemon's authorization URL before offering it, the pasted
// code's shape, and when an error is worth a "Reconnect Anthropic" offer.
import type { WorkLoopInfo } from "../../gen/ycc/v1/ycc_pb";

/**
 * Only offer a link to Anthropic's own authorize page: https://claude.com/
 * cai/oauth/authorize without embedded credentials, for an unexpired attempt.
 */
export function validLoginStart(
  resp: { attemptId: string; authorizationUrl: string; expiresAtUnix: bigint | number },
  now: number = Date.now(),
): boolean {
  if (!resp.attemptId) return false;
  if (!(Number(resp.expiresAtUnix) * 1000 > now)) return false;
  let u: URL;
  try {
    u = new URL(resp.authorizationUrl);
  } catch {
    return false;
  }
  return u.protocol === "https:" && u.hostname === "claude.com" && u.pathname === "/cai/oauth/authorize" && !u.username && !u.password && !u.port;
}

/** The provider page shows `code#state`; anything else cannot complete. */
export function codeProblem(code: string): string | null {
  const v = code.trim();
  if (!v) return null;
  const [c, state] = [v.slice(0, v.indexOf("#")), v.slice(v.indexOf("#") + 1)];
  if (!v.includes("#") || !c || !state) return "Paste the full code#state shown on Anthropic’s page.";
  if (/\s/.test(v)) return "The code has no spaces; paste it exactly as shown.";
  return null;
}

/** An error that most likely means the shared Anthropic login expired or is missing. */
export function needsAnthropicReconnect(text: string | null | undefined): boolean {
  if (!text) return false;
  return /anthropic/i.test(text) && /\b(login|oauth|credentials?|tokens?|auth|authentication|401|unauthori[sz]ed|subscription)\b/i.test(text);
}

/** A finished loop whose outcome or last session error points at the Anthropic login. */
export function loopNeedsAnthropicReconnect(loop: WorkLoopInfo | null | undefined, finished: boolean): boolean {
  if (!loop || !finished) return false;
  if (needsAnthropicReconnect(loop.outcome)) return true;
  const last = loop.sessions[loop.sessions.length - 1];
  return !!last && needsAnthropicReconnect(`${last.errorKind} ${last.errorMessage}`);
}
