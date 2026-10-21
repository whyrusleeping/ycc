import type { Status } from "./model";

/** A node's status at a glance; running pulses. */
export function StatusDot({ status }: { status: Status }) {
  return <span className={`act-dot st-${status}`} role="img" aria-label={status} title={status} />;
}
