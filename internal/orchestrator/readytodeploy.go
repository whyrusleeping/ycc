package orchestrator

import (
	"fmt"
	"os/exec"
	"path"
	"strings"
)

// verdictReadyToDeploy is the review outcome between accept and revise: the
// code is correct and may be deployed or run, but named acceptance criteria
// still need operational evidence, so the task is not yet ready to commit.
const verdictReadyToDeploy = "ready_to_deploy"

// deployReadiness records the latest review round whose outcome was ready to
// deploy: the snapshot the coordinator may deploy or run and the criteria that
// still need evidence. A later review of the same task compares the current
// snapshot with it to decide whether the code needs re-reviewing.
type deployReadiness struct {
	task     string
	snapshot reviewDiffBuild
	criteria []string
}

// reviewRoundOutcome summarises one round's verdicts.
type reviewRoundOutcome struct {
	accepts, ready int
	unbound        int      // reviews not submitted with submit_review for their assigned snapshot
	criteria       []string // union of the ready_to_deploy reviewers' pending criteria
	snapshot       string   // the snapshot every reviewer inspected; "" when they differ
}

// readyToDeploy reports whether the round as a whole is ready to deploy: every
// review came through submit_review for its assigned snapshot, none asked for
// revisions or failed, at least one named pending criteria, and all inspected
// one snapshot.
func (o reviewRoundOutcome) readyToDeploy(total int) bool {
	return o.ready > 0 && o.accepts+o.ready == total && o.unbound == 0 && o.snapshot != ""
}

func summarizeReviewRound(results []reviewResult) reviewRoundOutcome {
	var o reviewRoundOutcome
	seen := map[string]bool{}
	for i, r := range results {
		if !r.rv.bound {
			o.unbound++
		}
		switch r.rv.Verdict {
		case "accept":
			o.accepts++
		case verdictReadyToDeploy:
			o.ready++
			for _, c := range r.rv.PendingCriteria {
				if c = strings.TrimSpace(c); c != "" && !seen[c] {
					seen[c] = true
					o.criteria = append(o.criteria, c)
				}
			}
		}
		if i == 0 {
			o.snapshot = r.rv.SnapshotID
		} else if o.snapshot != r.rv.SnapshotID {
			o.snapshot = ""
		}
	}
	return o
}

// readyToDeployGuidance is the coordinator's next step after a ready-to-deploy
// round, or after a mixed round where reviewers saw different snapshots.
func readyToDeployGuidance(o reviewRoundOutcome, total int) string {
	if o.readyToDeploy(total) {
		var b strings.Builder
		fmt.Fprintf(&b, "\nReady to deploy: reviewers found no blocking code defect. You may deploy or run snapshot %s. "+
			"These acceptance criteria still need operational evidence:\n", o.snapshot)
		for _, c := range o.criteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("Do not commit yet: gather that evidence, record it (work log or report), then re_review with the " +
			"evidence in the handoff. A later review checks the evidence against these criteria and re-reviews any " +
			"change since this snapshot that is not evidence alone.")
		return b.String()
	}
	if o.ready > 0 && o.accepts+o.ready == total {
		if o.unbound > 0 {
			return "\nNot every review came through submit_review for its assigned snapshot, so no snapshot is ready to deploy; re_review before deploying or running it."
		}
		return "\nReviewers inspected different snapshots, so no single snapshot is ready to deploy; re_review before deploying or running it."
	}
	return ""
}

// checkSubmission marks whether submit_review produced rv for the reviewer's
// assigned snapshot (fromTool: the run's report is that tool's submission, with
// its criteria validation and trusted snapshot and receipt binding). A
// plain-text reply can claim any field, so its execution claims are shown as
// unverified, and only bound reviews count toward deploy readiness. A
// ready_to_deploy verdict that is unbound or names no criteria becomes unknown.
func checkSubmission(rv review, assigned string, fromTool bool) review {
	rv.bound = fromTool && rv.SnapshotID != "" && rv.SnapshotID == assigned
	if !fromTool {
		for i, v := range rv.Verification {
			if v.Classification == "independently_rebuilt_and_executed" {
				rv.Verification[i] = reviewVerification{Classification: "unavailable",
					Evidence: "claimed in a reply that did not use submit_review, so no execution receipt binds it: " + v.Evidence}
			}
		}
	}
	if rv.Verdict != verdictReadyToDeploy {
		return rv
	}
	var criteria []string
	for _, c := range rv.PendingCriteria {
		if c = strings.TrimSpace(c); c != "" {
			criteria = append(criteria, c)
		}
	}
	rv.PendingCriteria = criteria
	if !rv.bound || len(criteria) == 0 {
		rv.Verdict = "unknown"
		rv.Summary = "invalid ready_to_deploy verdict (not submitted with submit_review, no pending criteria, or not bound to the assigned snapshot): " + rv.Summary
	}
	return rv
}

// canonicalTaskID resolves an alias such as "1" to the task's id ("0001").
func canonicalTaskID(d *Deps, id string) string {
	if d.Docs != nil {
		if t, err := d.Docs.Get(id); err == nil {
			return t.ID
		}
	}
	return id
}

// recordReviewOutcome keeps the deploy readiness of taskID's latest
// ready-to-deploy round. Later rounds that accept or revise leave it in place:
// what was deployed is still that snapshot, and the delta check in
// deployFollowUpNote sends any later code change through review as usual.
func (d *Deps) recordReviewOutcome(taskID string, handles []*reviewerHandle, results []reviewResult) {
	o := summarizeReviewRound(results)
	if !o.readyToDeploy(len(results)) {
		return
	}
	taskID = canonicalTaskID(d, taskID)
	for _, h := range handles {
		if h.evidence.SnapshotID == o.snapshot {
			d.mu.Lock()
			d.deployReady = &deployReadiness{task: taskID, snapshot: h.evidence, criteria: o.criteria}
			d.mu.Unlock()
			return
		}
	}
}

// evidenceExtensions are the file types that count as evidence of results
// (reports, logs, measurements). Everything else, including plain-text config
// such as requirements.txt, is reviewed as code.
var evidenceExtensions = map[string]bool{
	".md": true, ".markdown": true, ".rst": true, ".adoc": true,
	".log": true, ".csv": true, ".tsv": true,
}

// deployFollowUpNote tells a reviewer of taskID that an earlier round found a
// snapshot ready to deploy, which criteria still need evidence, and whether the
// change since that snapshot is evidence alone. It is "" when no ready-to-deploy
// round exists for the task.
func (d *Deps) deployFollowUpNote(taskID string, current reviewDiffBuild) string {
	d.mu.Lock()
	ready := d.deployReady
	d.mu.Unlock()
	if ready == nil || ready.task != taskID {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "READY-TO-DEPLOY FOLLOW-UP: an earlier review round accepted snapshot %s as ready to deploy or run. "+
		"These acceptance criteria still needed operational evidence:\n", ready.snapshot.SnapshotID)
	for _, c := range ready.criteria {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	b.WriteString("Check the evidence now supplied (handoff, reports, work log) against each of them.\n")
	changed, err := changedPaths(d, ready.snapshot.Tree, current.Tree)
	if err != nil {
		fmt.Fprintf(&b, "The change since that snapshot could not be listed (%v): review the snapshot as usual.", err)
		return b.String()
	}
	bookkeeping := BookkeepingPaths(d.Workspace, d.Docs)
	var evidence, documents, other []string
	for _, p := range changed {
		switch {
		case bookkeeping != nil && bookkeeping(p):
			evidence = append(evidence, p)
		case evidenceExtensions[strings.ToLower(path.Ext(p))]:
			evidence = append(evidence, p)
			documents = append(documents, p)
		default:
			other = append(other, p)
		}
	}
	// A report-like file the program or deployment reads (a CSV of settings,
	// say) is an input, not evidence: a snapshot file other than these that
	// names one sends the delta through review as usual.
	var readers []string
	if len(other) == 0 && len(documents) > 0 {
		readers, err = filesNaming(d, current.Tree, documents)
		if err != nil {
			fmt.Fprintf(&b, "Whether other files read the changed documents could not be checked (%v): review the snapshot as usual.", err)
			return b.String()
		}
		var code []string
		for _, r := range readers {
			if !evidenceExtensions[strings.ToLower(path.Ext(r))] && (bookkeeping == nil || !bookkeeping(r)) {
				code = append(code, r)
			}
		}
		readers = code
	}
	switch {
	case len(other) > 0:
		fmt.Fprintf(&b, "Since that snapshot, files that are not evidence alone changed (%d): %s\n"+
			"Review those changes as usual: source, configuration, deployment files, migrations, and run scripts "+
			"get a full review.", len(other), boundedPathList(other, 2048))
		if len(evidence) > 0 {
			fmt.Fprintf(&b, " Evidence files also changed (%d): %s", len(evidence), boundedPathList(evidence, 1024))
		}
	case len(readers) > 0:
		fmt.Fprintf(&b, "Since that snapshot only document-type files changed (%d): %s\nbut other snapshot files name them "+
			"and may read them as input (%d): %s\nReview those changes as usual unless you confirm the documents are "+
			"evidence that nothing reads.", len(evidence), boundedPathList(evidence, 2048), len(readers), boundedPathList(readers, 1024))
	case len(evidence) > 0:
		fmt.Fprintf(&b, "Every change since that snapshot looks like evidence (reports, work logs, documentation of results) (%d): %s\n"+
			"Confirm each is a record of results that the program, build, and deployment do not read; then you need "+
			"not re-review the unchanged code. Otherwise review the change as usual.", len(evidence), boundedPathList(evidence, 2048))
	default:
		b.WriteString("No file changed since that snapshot; you need not re-review the unchanged code.")
	}
	return b.String()
}

// filesNaming lists the files of tree whose text mentions the base name of any
// of paths.
func filesNaming(d *Deps, tree string, paths []string) ([]string, error) {
	args := []string{"-C", d.Repo.Dir, "grep", "-l", "-z", "-I", "-F", "--no-color"}
	seen := map[string]bool{}
	for _, p := range paths {
		if base := path.Base(p); !seen[base] {
			seen[base] = true
			args = append(args, "-e", base)
		}
	}
	out, err := exec.Command("git", append(args, tree, "--")...).Output()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return nil, nil // no match
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f = strings.TrimPrefix(f, tree+":"); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// changedPaths lists the paths that differ between two snapshot trees, both
// sides of a rename or deletion included, and changed submodule commits even
// where configuration ignores them.
func changedPaths(d *Deps, from, to string) ([]string, error) {
	if from == to {
		return nil, nil
	}
	if from == "" || to == "" || d.Repo == nil {
		return nil, fmt.Errorf("snapshot tree unavailable")
	}
	out, err := exec.Command("git", "-C", d.Repo.Dir, "diff", "--name-only", "--no-renames", "--ignore-submodules=none",
		"--no-relative", "-z", from, to, "--").Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
