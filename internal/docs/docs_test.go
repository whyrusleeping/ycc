package docs

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateGetRoundTrip(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	task, err := s.Create("Add token auth", "## Description\nDo the thing.\n", 2, []string{"0003"}, []string{"Architecture"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "0001" {
		t.Fatalf("first id = %q, want 0001", task.ID)
	}
	if filepath.Base(task.Path) != "0001-add-token-auth.md" {
		t.Fatalf("path = %s", task.Path)
	}

	got, err := s.Get("1") // numeric id should normalize to 0001
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Add token auth" || got.Status != StatusTodo || got.Priority != 2 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "0003" {
		t.Fatalf("depends_on = %v", got.DependsOn)
	}
	if !strings.Contains(got.Body, "Do the thing.") {
		t.Fatalf("body lost: %q", got.Body)
	}
}

func TestNextIDIncrements(t *testing.T) {
	s := NewStore(t.TempDir())
	a, _ := s.Create("first", "", 1, nil, nil)
	b, _ := s.Create("second", "", 1, nil, nil)
	if a.ID != "0001" || b.ID != "0002" {
		t.Fatalf("ids = %s,%s", a.ID, b.ID)
	}
}

func TestCreateWithStatus(t *testing.T) {
	s := NewStore(t.TempDir())
	p, err := s.CreateWithStatus("an idea", "", 3, nil, nil, StatusProposed)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(p.ID)
	if got.Status != StatusProposed {
		t.Fatalf("status = %q, want proposed", got.Status)
	}
	// Empty status defaults to todo.
	d, err := s.CreateWithStatus("default", "", 3, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(d.ID); got.Status != StatusTodo {
		t.Fatalf("status = %q, want todo", got.Status)
	}
}

func TestUpdateStatus(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Create("task", "", 1, nil, nil)
	if _, err := s.Update("0001", func(t *Task) { t.Status = StatusDone }); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("0001")
	if got.Status != StatusDone {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestAppendWorkLog(t *testing.T) {
	s := NewStore(t.TempDir())
	s.Create("task", "## Description\nx\n\n## Work log\n", 1, nil, nil)
	if _, err := s.AppendWorkLog("0001", "plan: do X"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendWorkLog("0001", "review: looks good"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("0001")
	if strings.Count(got.Body, "\n- ") < 2 {
		t.Fatalf("work log not appended:\n%s", got.Body)
	}
	if !strings.Contains(got.Body, "plan: do X") || !strings.Contains(got.Body, "review: looks good") {
		t.Fatalf("work log entries missing:\n%s", got.Body)
	}
}

func TestCompleteCompactsBodyAndMetadataStillResolvesDependency(t *testing.T) {
	s := NewStore(t.TempDir())
	done, err := s.Create("finished", "## Description\nKeep the intent.\n\n## Acceptance criteria\n- It works.\n\n## Plan\nVerbose plan.\n\n## Work log\n- preload: chatter\n- review: transcript\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := s.Create("dependent", "", 1, []string{done.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(done.ID, "Shipped the intended behavior.", "feat: ship behavior"); err != nil {
		t.Fatal(err)
	}

	metadata, err := s.ListMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if metadata[0].Body != "" {
		t.Fatalf("metadata listing loaded body: %q", metadata[0].Body)
	}
	if blocking := BlockingDeps(dependent, StatusByID(metadata)); len(blocking) != 0 {
		t.Fatalf("completed dependency still blocks: %v", blocking)
	}

	got, err := s.Get(done.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Keep the intent.", "- It works.", "Shipped the intended behavior.", "Commit: feat: ship behavior"} {
		if !strings.Contains(got.Body, want) {
			t.Fatalf("compact body missing %q:\n%s", want, got.Body)
		}
	}
	for _, removed := range []string{"## Plan", "## Work log", "preload: chatter", "review: transcript"} {
		if strings.Contains(got.Body, removed) {
			t.Fatalf("compact body retained %q:\n%s", removed, got.Body)
		}
	}
}

func TestCompleteOmitsMissingCriteriaAndRejectsOverlongRecords(t *testing.T) {
	s := NewStore(t.TempDir())
	task, err := s.Create("no criteria", "## Description\nIntent only.\n\n## Work log\n", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(task.ID, strings.Repeat("word ", maxOutcomeRunes), "feat: concise"); err == nil {
		t.Fatal("overlong outcome accepted")
	}
	if _, err := s.Complete(task.ID, "Readable outcome.", strings.Repeat("x", maxCommitRunes+1)); err == nil {
		t.Fatal("overlong commit subject accepted")
	}
	if _, err := s.Complete(task.ID, "Readable outcome.", "feat: concise"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Body, "## Acceptance criteria") {
		t.Fatalf("missing criteria rendered as an empty section:\n%s", got.Body)
	}
}

func TestCompactCompletedHistoryRequiresCompleteFinalRecordsAndIsIdempotent(t *testing.T) {
	s := NewStore(t.TempDir())
	prefix := "## Description\nIntent.\n\n## Acceptance criteria\nCriterion.\n\n## Work log\n"
	cases := []struct {
		title string
		log   string
		want  string
	}{
		{"complete", "- 2026-01-01 implementer report: Delivered it.\n- 2026-01-01 decision: accept — commit: feat: deliver it\n", "Delivered it."},
		{"final revision", "- 2026-01-01 implementer report: Initial result.\n- 2026-01-01 revision: Final accepted result.\n- 2026-01-01 decision: accept — commit: fix: final result\n", "Final accepted result."},
		{"truncated report", "- 2026-01-01 implementer report: Partial result\n…[truncated]\n- 2026-01-01 decision: accept — commit: fix: result\n", ""},
		{"truncated final revision", "- 2026-01-01 implementer report: Initial complete result.\n- 2026-01-01 revision: Partial final result\n…[truncated]\n- 2026-01-01 decision: accept — commit: fix: result\n", ""},
		{"truncated commit", "- 2026-01-01 implementer report: Complete result.\n- 2026-01-01 decision: accept — commit: feat: partial subject\n…[truncated]\n", ""},
		{"overlong commit", "- 2026-01-01 implementer report: Complete result.\n- 2026-01-01 decision: accept — commit: " + strings.Repeat("x", maxCommitRunes+1) + "\n", ""},
	}
	idsByTitle := map[string]string{}
	for _, tc := range cases {
		task, err := s.Create(tc.title, prefix+tc.log, 1, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		idsByTitle[tc.title] = task.ID
		if _, err := s.Update(task.ID, func(task *Task) { task.Status = StatusDone }); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := s.CompactCompletedHistory(false)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{idsByTitle["complete"], idsByTitle["final revision"]}
	if strings.Join(ids, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("dry run ids = %v, want %v", ids, wantIDs)
	}
	if _, err := s.CompactCompletedHistory(true); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.CompactCompletedHistory(false); err != nil || len(ids) != 0 {
		t.Fatalf("second run = %v, %v; want no-op", ids, err)
	}
	for _, tc := range cases {
		got, err := s.Get(idsByTitle[tc.title])
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if strings.Contains(got.Body, "## Outcome") {
				t.Fatalf("ambiguous %q task was compacted:\n%s", tc.title, got.Body)
			}
			continue
		}
		if !strings.Contains(got.Body, tc.want) {
			t.Fatalf("%q outcome missing final record %q:\n%s", tc.title, tc.want, got.Body)
		}
	}
}

func TestBlockingDeps(t *testing.T) {
	tasks := []*Task{
		{ID: "0001", Status: StatusDone},
		{ID: "0002", Status: StatusInProgress},
		{ID: "0003", Status: StatusTodo, DependsOn: []string{"0001"}},         // ready: only dep is done
		{ID: "0004", Status: StatusTodo, DependsOn: []string{"0001", "0002"}}, // blocked by 0002
		{ID: "0005", Status: StatusTodo},                                      // ready: no deps
		{ID: "0006", Status: StatusTodo, DependsOn: []string{"1"}},            // non-normalized dep id "1" -> 0001 (done) -> ready
		{ID: "0007", Status: StatusTodo, DependsOn: []string{"9999"}},         // dep names a missing task -> blocking
	}
	byID := StatusByID(tasks)
	cases := map[string][]string{
		"0003": nil,
		"0004": {"0002"},
		"0005": nil,
		"0006": nil,
		"0007": {"9999"},
	}
	for id, want := range cases {
		got := BlockingDeps(byID2task(tasks, id), byID)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("BlockingDeps(%s) = %v, want %v", id, got, want)
		}
	}
}

func byID2task(tasks []*Task, id string) *Task {
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// TestConcurrentCreateSerializes launches many goroutines that each Create a task
// on the same Store. The per-directory lock must serialize them so ids are unique
// and sequential. Run with -race to catch data races.
func TestConcurrentCreateSerializes(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Create(fmt.Sprintf("task %d", i), "", 3, nil, nil); err != nil {
				t.Errorf("create: %v", err)
				return
			}
			if _, err := s.List(); err != nil {
				t.Errorf("list: %v", err)
			}
		}(i)
	}
	wg.Wait()

	tasks, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != n {
		t.Fatalf("got %d tasks, want %d (duplicate or lost ids)", len(tasks), n)
	}
	for i, task := range tasks {
		want := fmt.Sprintf("%04d", i+1)
		if task.ID != want {
			t.Fatalf("task %d has id %q, want %q (ids not unique/sequential)", i, task.ID, want)
		}
	}
}
