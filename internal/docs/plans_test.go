package docs

import (
	"strings"
	"testing"
)

func TestSetPlanInsertsAndReplaces(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	task, err := s.Create("Do a thing", "## Description\nstuff.\n\n## Work log\n- 2026-01-01 created.\n", 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.SetPlan(task.ID, "Step 1.\nStep 2.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Body, "## Plan") {
		t.Fatalf("body missing ## Plan section:\n%s", got.Body)
	}
	if !strings.Contains(got.Body, "Step 1.") || !strings.Contains(got.Body, "Step 2.") {
		t.Fatalf("plan content lost:\n%s", got.Body)
	}
	// Plan must be placed ABOVE the work log.
	if strings.Index(got.Body, "## Plan") > strings.Index(got.Body, "## Work log") {
		t.Fatalf("## Plan should precede ## Work log:\n%s", got.Body)
	}
	// Work log content preserved.
	if !strings.Contains(got.Body, "2026-01-01 created.") {
		t.Fatalf("work log lost:\n%s", got.Body)
	}

	// Second call REPLACES, no duplicate section.
	got2, err := s.SetPlan(task.ID, "Replaced plan.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got2.Body, "## Plan") != 1 {
		t.Fatalf("expected exactly one ## Plan section, got:\n%s", got2.Body)
	}
	if strings.Contains(got2.Body, "Step 1.") {
		t.Fatalf("old plan content not replaced:\n%s", got2.Body)
	}
	if !strings.Contains(got2.Body, "Replaced plan.") {
		t.Fatalf("new plan content missing:\n%s", got2.Body)
	}
}

func TestSetPlanAppendsWhenNoWorkLog(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)
	// Body without a work log section.
	task := &Task{ID: "0001", Title: "x", Status: StatusTodo, Body: "## Description\nhi.\n", Slug: "x"}
	task.Path = s.dir + "/0001-x.md"
	if err := s.write(task); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetPlan("0001", "the plan")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Body, "## Plan") || !strings.Contains(got.Body, "the plan") {
		t.Fatalf("plan not appended:\n%s", got.Body)
	}
}

func TestPlanLibraryRoundTrip(t *testing.T) {
	ws := t.TempDir()
	s := NewStore(ws)

	// Empty library.
	plans, err := s.ListPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("expected no plans, got %d", len(plans))
	}

	name, err := s.SavePlan("Build And Test!", "# Build and test\n\n1. go build ./...\n")
	if err != nil {
		t.Fatal(err)
	}
	if name != "build-and-test" {
		t.Fatalf("saved name = %q, want build-and-test", name)
	}

	plans, err = s.ListPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].Name != "build-and-test" || plans[0].Title != "Build and test" {
		t.Fatalf("plan info = %+v", plans[0])
	}

	// ReadPlan with and without .md.
	c1, err := s.ReadPlan("build-and-test")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.ReadPlan("build-and-test.md")
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 || !strings.Contains(c1, "go build ./...") {
		t.Fatalf("read mismatch: %q vs %q", c1, c2)
	}

	if _, err := s.ReadPlan("nonexistent"); err == nil {
		t.Fatal("expected error reading missing plan")
	}
}
