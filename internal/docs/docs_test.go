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
