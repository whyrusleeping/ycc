package orchestrator

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	// "é" is two bytes and "—" three; every cut point inside them must back up.
	s := strings.Repeat("aé—", 100)
	for n := 0; n < len(s); n++ {
		got := truncate(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(_, %d) produced invalid UTF-8: %q", n, got)
		}
		if body := strings.TrimSuffix(got, "\n…[truncated]"); !strings.HasPrefix(s, body) || len(body) > n {
			t.Fatalf("truncate(_, %d) = %q, want a prefix of at most %d bytes", n, got, n)
		}
	}
	line := oneLine("first\n" + s)
	if !utf8.ValidString(line) || strings.Contains(line, "\n") || !strings.HasSuffix(line, "…") {
		t.Fatalf("oneLine = %q, want a valid single line ending in …", line)
	}
}

// Implementer reports and block reasons must not land in the work log as
// clipped fragments (0529).
func TestImplementerWorkLogHasNoClippedFragments(t *testing.T) {
	long := strings.Repeat("überprüft — ", 40)
	for _, tc := range []struct {
		name string
		resp *gollama.ResponseMessageGenerate
		want string
	}{
		{"report", call("finish", `{"report":"`+long+`"}`), ""},
		{"blocked", call("report_blocked", `{"reason":"`+long+`"}`), "BLOCKED — " + strings.TrimSpace(long)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			repo, err := git.Open(ws)
			if err != nil {
				t.Fatal(err)
			}
			store := docs.NewStore(ws)
			if _, err := store.Create("a task", "## Work log\n", 1, nil, nil); err != nil {
				t.Fatal(err)
			}
			implTurner := &scripted{resp: []*gollama.ResponseMessageGenerate{tc.resp}}
			d := &Deps{
				Workspace:   ws,
				Docs:        store,
				Repo:        repo,
				Emitter:     event.NewEmitter(&captureRec{}, "coordinator"),
				Implementer: AgentSpec{Name: "impl", Model: "m", NewClient: func() engine.Turner { return implTurner }},
				Asker:       noopAsker{},
			}
			res, err := spawnImplementer(d).Call(context.Background(), map[string]any{"task_id": "0001", "plan": "go"})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatalf("unexpected error result: %s", res.Content)
			}
			task, err := store.Get("0001")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(task.Body, "[truncated]") || !utf8.ValidString(task.Body) {
				t.Fatalf("work log has a clipped fragment:\n%s", task.Body)
			}
			if tc.want == "" {
				if strings.Contains(task.Body, "überprüft") {
					t.Fatalf("implementer report copied into the work log:\n%s", task.Body)
				}
			} else if !workLogContains(t, store, "0001", tc.want) {
				t.Fatalf("work log missing the whole block reason:\n%s", task.Body)
			}
		})
	}
}
