# Design: automatic workstream integration

> Status: accepted and implemented.

## Context

Worktree isolation lets tasks finish concurrently, but merging each finished branch manually
turns the user into a queue worker. Blind automatic merging is unsafe: workstreams share a moving
base, verification can fail after rebasing, and a model-produced commit is not proof that the
result belongs on the base branch.

## Decision

The daemon owns one serialized integration queue per project. A ready workstream is reconsidered
against the current configured base, not only the commit from which it started. Integration work
happens in the workstream tree; the primary checkout is never used as a conflict-resolution
scratch area.

The default automatic strategy is rebase, verify, then fast-forward:

1. Rebase the workstream onto the current base inside its own worktree.
2. Run the configured verification command there.
3. Confirm the base has not moved and advance it by fast-forward.
4. Record success and clean up the worktree/branch.

The clean fast path makes no model call. A conflicted rebase or failed verification may start a
bounded unattended integration session in the same worktree. That agent can resolve/fix and
commit, but cannot move the base itself. The daemon repeats rebase and verification before any
advance. Exhausted or blocked recovery leaves base unchanged, retains the worktree, records
needs-attention, and notifies best-effort.

A moving base is normal, not exceptional. The queue rechecks it before advancing and retries from
the new base rather than overwriting another integration. Git operations that alter daemon-owned
worktrees are joined during shutdown/reclamation so cleanup cannot race them.

## Policy modes

- **auto** executes the supported rebase/verify/fast-forward path. Missing verification or an
  unsupported history strategy degrades to the gate.
- **gate** prepares a preview and requires explicit acceptance before integration.
- **manual** records readiness but leaves integration to an external operator.

The gate is also the fallback because doing less automation is safer than silently substituting a
different history shape. Squash and merge-commit strategies may be selected for gated/manual
workflows but are not impersonated by the automatic queue.

## Bootstrap and resources

A worktree may need ignored config, dependencies, or generated files before verification. The
project can declare copied paths and bootstrap commands. Project-tree configuration takes
precedence over daemon-global defaults because the repository owns its build prerequisites.
Bootstrap failure prevents the work session from starting and leaves an inspectable worktree.

The queue limits active workstreams when configured. It does not attempt to allocate arbitrary
exclusive external resources such as ports, devices, or databases; those remain project-specific
bootstrap/test concerns.

## Rejected alternatives

- Merging directly in the primary checkout risks leaving the user's tree conflicted.
- Letting the integration agent move base bypasses the daemon's independent verification and
  serialization boundary.
- Testing only the original branch ignores interactions with work integrated since it forked.
- Making every clean result ask a model adds cost without evidence; deterministic git and verify
  checks are stronger on the fast path.
