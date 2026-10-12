---
id: "0462"
title: 'commit tool: loosen restrictions — allow committing alongside other uncommitted work instead of refusing or splitting'
status: proposed
priority: 2
created: "2026-10-12"
updated: "2026-10-12"
depends_on: []
spec_refs: []
---

## Description
**Evidence (vals, 2026-10-11):** sessions for 0369, 0380, 0425 and 0387 split tasks into fragments or left reviewed work uncommitted. In each case a file they needed also held another task's uncommitted hunks. The tool stages whole files, and agents (guided by vals memory) treated "my commit would include someone else's work" as forbidden. 0387 ended up in_review and uncommitted over a 3-line accessor in a file that held 0371's changes. The tool rarely refused outright. The cost came from agents avoiding a whole-file commit.

Across all projects' sessions, 207 `commit` calls failed. 190 were length limits: outcome over 1000 chars (114) or subject over 240 chars (76). About 15 were ownership/baseline refusals: "overlap paths that were already dirty at baseline", "baseline is stale: HEAD moved", and "finalization belongs to original baseline".

**Direction (owner, 2026-10-12):** loosen the commit restrictions generally rather than build precise hunk-level attribution. Committing whole files that also contain other uncommitted work is acceptable. Precision is not worth the complexity.

**Proposal:**
- **Ownership refusals become inclusions.** A baseline-dirty or unclaimed path the session changed is committed whole, with a note in the result listing the included preexisting paths, instead of being refused as ambiguous. Paths the session never touched still stay out by default.
- **Explicit paths.** Add an optional `paths` argument, so a session can deliberately include additional whole files, for example another in-review task's integrated files that must land together.
- **Stale baseline.** A HEAD that moved non-fast-forward, or a "finalization belongs to original baseline" mismatch, re-captures the baseline and proceeds. The work is in the worktree, so it should not ask for a new session.
- **Length limits** stop failing the call. Accept the long outcome and subject, or truncate the subject to the first line/limit and store the full outcome, and report what was done.
- **Prompt and spec guidance.** Say that a commit may carry other uncommitted work in shared files. The agent should name any co-included tasks in the message/outcome and must not split or abandon work to avoid that. Remove or retire contrary memory/guidance where it is ycc-owned.

**Acceptance:**
- A session can commit a file that held preexisting uncommitted changes. The commit succeeds and reports the included preexisting paths.
- `paths` includes extra files.
- A moved or non-fast-forward HEAD no longer blocks commit.
- An over-length outcome or subject no longer fails the call.
- Existing safety that still matters stays covered by tests: no whole-tree commits by default, and no committing paths the session never touched or named.
- Spec and prompt text are updated to match.

## Acceptance criteria

## Work log
