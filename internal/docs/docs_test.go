package docs

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRenderIndex(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	s.Create("alpha", "", 1, nil, nil)
	s.Create("beta", "", 3, []string{"0001"}, nil)
	if err := s.RenderIndex(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "backlog.md"))
	if err != nil {
		t.Fatal(err)
	}
	idx := string(data)
	if !strings.Contains(idx, "alpha") || !strings.Contains(idx, "beta") {
		t.Fatalf("index missing tasks:\n%s", idx)
	}
	if !strings.Contains(idx, "backlog/0002-beta.md") {
		t.Fatalf("index link wrong:\n%s", idx)
	}
}
