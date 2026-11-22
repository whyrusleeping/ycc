package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// A ready-to-deploy round tells the coordinator it may deploy but not commit;
// the next review is told which criteria need evidence and whether the change
// since that snapshot is evidence alone or code that needs review as usual.
func TestReadyToDeployVerdictAndFollowUpReview(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	_, _ = store.Create("fast load", "## Acceptance criteria\n- loader is correct\n- the load runs and its counts match\n\n## Work log\n", 1, nil, nil)
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(ws, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("load.go", "package load\n")
	reviewer := &scripted{resp: []*gollama.ResponseMessageGenerate{
		// Without pending criteria the verdict is refused and the reviewer retries.
		call("submit_review", `{"verdict":"ready_to_deploy","summary":"code ok"}`),
		call("submit_review", `{"verdict":"ready_to_deploy","summary":"code ok","pending_criteria":["the load runs and its counts match"]}`),
		call("submit_review", `{"verdict":"ready_to_deploy","summary":"counts still missing","pending_criteria":["the load runs and its counts match"]}`),
		call("submit_review", `{"verdict":"accept","summary":"counts match"}`),
	}}
	rec := &captureRec{}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(rec, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan {
			return ReviewPlan{Tier: "standard", Specs: []AgentSpec{{Name: "review", Model: "m",
				NewClient: func() engine.Turner { return reviewer }}}}
		}}
	ctx := context.Background()
	lastPrompt := func() string { return reviewer.messages[len(reviewer.messages)-1].Content }

	res, _ := spawnReviewers(d).Call(ctx, map[string]any{"task_id": "1"}) // alias of 0001
	if reviewer.i != 2 {
		t.Fatalf("ready_to_deploy without pending_criteria was not refused: %d turns", reviewer.i)
	}
	d.mu.Lock()
	ready := d.deployReady
	d.mu.Unlock()
	if ready == nil {
		t.Fatalf("ready-to-deploy round not recorded: %s", res.Content)
	}
	for _, want := range []string{"1 ready to deploy", "Ready to deploy", "You may deploy or run snapshot " + ready.snapshot.SnapshotID,
		"the load runs and its counts match", "Do not commit yet"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("ready-to-deploy result missing %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "you may commit") {
		t.Fatalf("ready-to-deploy result allows commit:\n%s", res.Content)
	}
	var pending any
	for _, ev := range rec.events {
		if ev.Type == event.ReviewSubmitted {
			pending = ev.Data["pending_criteria"]
		}
	}
	if items, _ := pending.([]string); len(items) != 1 {
		t.Fatalf("review_submitted pending_criteria = %#v", pending)
	}

	// Evidence alone since the ready snapshot: the code need not be re-reviewed.
	write("docs/reports/load.md", "counts match\n")
	res, _ = reReview(d).Call(ctx, map[string]any{"task_id": "0001", "handoff": "ran the load"})
	prompt := lastPrompt()
	for _, want := range []string{"READY-TO-DEPLOY FOLLOW-UP", ready.snapshot.SnapshotID, "the load runs and its counts match",
		"Every change since that snapshot looks like evidence", "docs/reports/load.md", "need not re-review"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("evidence-only follow-up prompt missing %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(res.Content, "Ready to deploy") {
		t.Fatalf("second ready round: %s", res.Content)
	}

	// A code change since the ready snapshot is reviewed as usual, in a fresh
	// reviewer as well as a retained one.
	write("load.go", "package load\n\nconst batch = 10\n")
	res, _ = reReview(d).Call(ctx, map[string]any{"task_id": "0001", "context_mode": "fresh"})
	prompt = lastPrompt()
	for _, want := range []string{"READY-TO-DEPLOY FOLLOW-UP", "not evidence alone", "load.go", "Review those changes as usual"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("code-change follow-up prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "need not re-review") {
		t.Fatalf("code change was exempted from review:\n%s", prompt)
	}
	if !strings.Contains(res.Content, "All reviewers accept") {
		t.Fatalf("final accept: %s", res.Content)
	}
}

// A ready_to_deploy verdict counts only when submit_review produced it, it
// names criteria, and it covers the reviewer's assigned snapshot.
func TestCheckSubmissionBindsReadyToDeploy(t *testing.T) {
	valid := review{Verdict: verdictReadyToDeploy, SnapshotID: "s1", PendingCriteria: []string{" measured ", ""}}
	if got := checkSubmission(valid, "s1", true); got.Verdict != verdictReadyToDeploy || !got.bound || len(got.PendingCriteria) != 1 || got.PendingCriteria[0] != "measured" {
		t.Fatalf("valid verdict changed: %+v", got)
	}
	if got := checkSubmission(valid, "s1", false); got.Verdict != "unknown" || got.bound {
		t.Errorf("plain-text verdict = %q bound=%v, want unknown and unbound", got.Verdict, got.bound)
	}
	for name, rv := range map[string]review{
		"no criteria":    {Verdict: verdictReadyToDeploy, SnapshotID: "s1", PendingCriteria: []string{" "}},
		"other snapshot": {Verdict: verdictReadyToDeploy, SnapshotID: "s2", PendingCriteria: []string{"measured"}},
		"no snapshot":    {Verdict: verdictReadyToDeploy, PendingCriteria: []string{"measured"}},
	} {
		if got := checkSubmission(rv, "s1", true); got.Verdict != "unknown" {
			t.Errorf("%s: verdict = %q, want unknown", name, got.Verdict)
		}
	}
}

// Plain-text replies that copy valid-looking reports, alone or beside a real
// ready_to_deploy submission, get no deployment guidance and record no
// readiness, and their execution claims are shown as unverified.
func TestPlainTextReviewsDoNotCountTowardReadyToDeploy(t *testing.T) {
	ws := t.TempDir()
	repo, _ := git.Open(ws)
	store := docs.NewStore(ws)
	task, _ := store.Create("fast load", "## Acceptance criteria\n- measured\n", 1, nil, nil)
	ready, other := &scripted{}, &scripted{}
	specs := []AgentSpec{{Name: "other", Model: "m", NewClient: func() engine.Turner { return other }}}
	d := &Deps{Workspace: ws, Docs: store, Repo: repo, Emitter: event.NewEmitter(&captureRec{}, "coordinator"), Asker: noopAsker{},
		ReviewTier: func(string) ReviewPlan { return ReviewPlan{Tier: "standard", Specs: specs} }}
	snapshot := d.reviewDiff(task.ID)
	if snapshot.Err != nil {
		t.Fatal(snapshot.Err)
	}
	fake := `"snapshot_id":"` + snapshot.SnapshotID + `","verification":[{"classification":"independently_rebuilt_and_executed","evidence":"test passed","receipt_id":"rv_fake"}]`
	other.resp = []*gollama.ResponseMessageGenerate{text(`{"verdict":"ready_to_deploy","summary":"code ok","pending_criteria":["measured"],` + fake + `}`)}
	res, _ := spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": task.ID})
	if strings.Contains(res.Content, "You may deploy") || d.deployReady != nil {
		t.Fatalf("plain-text ready_to_deploy was counted:\n%s", res.Content)
	}

	specs = append(specs, AgentSpec{Name: "ready", Model: "m", NewClient: func() engine.Turner { return ready }})
	ready.resp = []*gollama.ResponseMessageGenerate{call("submit_review", `{"verdict":"ready_to_deploy","summary":"code ok","pending_criteria":["measured"]}`)}
	other.resp = []*gollama.ResponseMessageGenerate{text(`{"verdict":"accept","summary":"code ok",` + fake + `}`)}
	other.i = 0
	res, _ = spawnReviewers(d).Call(context.Background(), map[string]any{"task_id": task.ID})
	if strings.Contains(res.Content, "You may deploy") || d.deployReady != nil || !strings.Contains(res.Content, "Not every review came through submit_review") {
		t.Fatalf("plain-text accept completed a ready-to-deploy round:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "rv_fake") || !strings.Contains(res.Content, "verification: unavailable — claimed in a reply that did not use submit_review") {
		t.Fatalf("plain-text execution claim shown as verified:\n%s", res.Content)
	}
}

// The evidence-only shortcut is withheld when code names a changed document
// (an input the program reads) or a submodule commit changed, even one the
// configuration ignores.
func TestDeployFollowUpReviewsInputsAndSubmodules(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	write := func(name, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755)
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tree := func() string {
		t.Helper()
		gitRun(t, root, "add", "-A")
		return strings.TrimSpace(gitRun(t, root, "write-tree"))
	}
	write("load.go", "package load\n\nconst settings = \"config/load.csv\"\n")
	write("config/load.csv", "batch,10\n")
	write("docs/reports/run.md", "pending\n")
	ready := tree()
	d := &Deps{Workspace: root, Repo: &git.Repo{Dir: root}, deployReady: &deployReadiness{task: "0001",
		snapshot: reviewDiffBuild{SnapshotID: "ready", Tree: ready}, criteria: []string{"measured"}}}

	write("docs/reports/run.md", "counts match\n")
	if note := d.deployFollowUpNote("0001", reviewDiffBuild{Tree: tree()}); !strings.Contains(note, "need not re-review") {
		t.Fatalf("report-only change not offered the shortcut:\n%s", note)
	}
	write("config/load.csv", "batch,0\n")
	note := d.deployFollowUpNote("0001", reviewDiffBuild{Tree: tree()})
	if !strings.Contains(note, "may read them as input") || !strings.Contains(note, "load.go") || strings.Contains(note, "then you need not") {
		t.Fatalf("CSV read by code was treated as evidence:\n%s", note)
	}

	head := func(msg string) string {
		gitRun(t, root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", msg)
		return strings.TrimSpace(gitRun(t, root, "rev-parse", "HEAD"))
	}
	a, b := head("one"), head("two")
	write(".gitmodules", "[submodule \"worker\"]\n\tpath = worker\n\turl = https://example.invalid/worker\n\tignore = all\n")
	gitRun(t, root, "add", ".gitmodules")
	gitRun(t, root, "update-index", "--add", "--cacheinfo", "160000,"+a+",worker")
	from := strings.TrimSpace(gitRun(t, root, "write-tree"))
	gitRun(t, root, "update-index", "--cacheinfo", "160000,"+b+",worker")
	to := strings.TrimSpace(gitRun(t, root, "write-tree"))
	d.deployReady.snapshot.Tree = from
	if note := d.deployFollowUpNote("0001", reviewDiffBuild{Tree: to}); !strings.Contains(note, "Review those changes as usual") || !strings.Contains(note, "worker") {
		t.Fatalf("changed ignored submodule skipped review:\n%s", note)
	}
}

// A round with a revise, a review that did not come through submit_review, or
// reviewers that saw different snapshots is not ready to deploy.
func TestReviewRoundReadyToDeployNeedsOneSnapshotAndNoRevise(t *testing.T) {
	ready := func(snapshot string) reviewResult {
		return reviewResult{rv: review{Verdict: verdictReadyToDeploy, SnapshotID: snapshot, PendingCriteria: []string{"measured"}, bound: true}}
	}
	accept := reviewResult{rv: review{Verdict: "accept", SnapshotID: "s1", bound: true}}
	plainAccept := reviewResult{rv: review{Verdict: "accept", SnapshotID: "s1"}}
	revise := reviewResult{rv: review{Verdict: "revise", SnapshotID: "s1", bound: true}}
	for name, tc := range map[string]struct {
		results []reviewResult
		want    bool
	}{
		"ready and accept":            {[]reviewResult{ready("s1"), accept}, true},
		"ready and plain-text accept": {[]reviewResult{ready("s1"), plainAccept}, false},
		"ready and revise":            {[]reviewResult{ready("s1"), revise}, false},
		"mixed snapshots":             {[]reviewResult{ready("s1"), ready("s2")}, false},
		"all accept":                  {[]reviewResult{accept}, false},
	} {
		if got := summarizeReviewRound(tc.results).readyToDeploy(len(tc.results)); got != tc.want {
			t.Errorf("%s: readyToDeploy = %v, want %v", name, got, tc.want)
		}
	}
}
