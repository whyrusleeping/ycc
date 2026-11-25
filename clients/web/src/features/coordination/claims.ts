// A live session's claimed paths split into definite and ambiguous ones
// (spec §7.3): an ambiguous claim marks a path that changed while this
// session's shell and another session's shell both ran, so either may have
// written it.
import type { ClaimedPath } from "../../gen/ycc/v1/ycc_pb";

/** Splits claims, keeping their order, into definite and ambiguous ones. */
export function splitClaims(claims: readonly ClaimedPath[]): { definite: ClaimedPath[]; ambiguous: ClaimedPath[] } {
  const definite: ClaimedPath[] = [];
  const ambiguous: ClaimedPath[] = [];
  for (const c of claims) (c.ambiguous ? ambiguous : definite).push(c);
  return { definite, ambiguous };
}
