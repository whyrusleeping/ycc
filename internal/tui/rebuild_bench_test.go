package tui

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/viewport"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
)

// benchModel builds a ready model with a long synthetic session: alternating
// model_turn narration and tool_call/tool_result pairs (some expanded), the
// shape a real agent log takes.
func benchModel(n int) *model {
	m := &model{
		ready: true, w: 120, h: 40,
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		blockCache: map[int]string{}, hiddenCache: map[int]bool{},
		selected: -1, follow: true,
		thinkLevels: map[string]string{},
	}
	m.vp = viewport.New(viewport.WithWidth(120), viewport.WithHeight(38))
	m.makeRenderer()
	seq := int64(1)
	for i := 0; i < n; i++ {
		m.appendEvent(&v1.Event{Seq: seq, Actor: "coordinator", Type: "model_turn",
			DataJson: fmt.Sprintf(`{"text":"working on step %d of the plan, considering the file layout","duration_ms":1200}`, i)})
		seq++
		m.appendEvent(&v1.Event{Seq: seq, Actor: "coordinator", Type: "tool_call",
			DataJson: fmt.Sprintf(`{"id":"c%d","name":"Bash","args":"{\"command\":\"rg -n pattern file%d.go\"}"}`, i, i)})
		seq++
		m.appendEvent(&v1.Event{Seq: seq, Actor: "coordinator", Type: "tool_result",
			DataJson: fmt.Sprintf(`{"id":"c%d","result":"file%d.go:12: some match\nfile%d.go:99: another match","duration_ms":300}`, i, i, i)})
		seq++
	}
	return m
}

// BenchmarkRebuildWarm measures rebuild() with warm caches — the steady-state
// cost of a keypress/new event in a long session.
func BenchmarkRebuildWarm(b *testing.B) {
	m := benchModel(500) // 1500 events
	m.rebuild()          // warm the caches
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.rebuild()
	}
}

// BenchmarkRebuildCold measures rebuild() with cleared caches — the old
// per-event cost before block caching (every row re-rendered from scratch).
func BenchmarkRebuildCold(b *testing.B) {
	m := benchModel(500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.invalidateRender()
		m.rebuild()
	}
}
