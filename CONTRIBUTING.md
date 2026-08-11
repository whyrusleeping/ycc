# Contributing

Keep changes small and proportional to the risk they address. Tests, documentation,
plans, abstractions, and reviewer agents have maintenance costs; they are tools, not
default deliverables.

## Changes

- Make the smallest change that solves the requested problem. Do not add speculative
  hardening, compatibility paths, or adjacent refactors.
- Acceptance criteria describe observable behavior or an important invariant. Do not
  prescribe tests, documentation, or implementation details unless those are the actual
  deliverable.
- A written plan is useful for complex, ambiguous, or multi-step work. Routine changes
  need only a brief stated approach.

## Tests

- Add a test when it protects against a plausible regression. Be able to name the failure
  it would catch.
- Prefer observable contracts and important invariants over implementation details.
- Do not test prompt prose, comments, trivial accessors, direct literal assignments, or
  behavior already enforced by the type checker.
- Test a behavior at the lowest useful boundary. Do not repeat the same assertion at unit,
  RPC, model, and UI layers unless each boundary has an independent failure mode.
- Preserve strong coverage around credentials, authorization, persistence, concurrency,
  path confinement, accounting, parsing, destructive operations, and observed regressions.
- Refactors do not require new tests when existing behavioral coverage is sufficient.
  There is no coverage target; coverage is diagnostic, not a deliverable.

## Documentation and comments

- `README.md` covers installation and user operation. The spec records durable behavior,
  architecture, interfaces, and invariants. Design notes retain rationale only when it is
  still useful. Code comments explain non-obvious local constraints.
- Keep one source of truth. Update or replace existing text instead of repeating it in
  another document.
- Do not put task IDs, review history, implementation chronology, or routine spec-section
  citations in code comments. Git and the backlog already retain that history.
- Delete or shorten obsolete documentation. Git preserves removed material.
- Save runbooks only for procedures that are genuinely repeatable and likely to be reused.

## Review

- Use self-review for tiny, low-risk changes; one focused reviewer for ordinary changes;
  and multi-agent review only for large, security-sensitive, destructive, highly concurrent,
  architectural, or hard-to-reverse changes.
- Review against the task and credible failure modes, not personal taste. A request for more
  tests or documentation must identify the concrete defect, regression, or reader need it
  addresses.
- Nits and optional improvements do not block an otherwise correct change.
