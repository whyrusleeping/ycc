package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

// eventAt maps a clicked content row back to the event whose block contains it —
// the core of click-to-expand.
func TestEventAt(t *testing.T) {
	m := &model{eventStart: []int{0, 3, 5, 9}}
	cases := []struct{ row, want int }{
		{-1, -1}, {0, 0}, {2, 0}, {3, 1}, {4, 1}, {5, 2}, {8, 2}, {9, 3}, {100, 3},
	}
	for _, c := range cases {
		if got := m.eventAt(c.row); got != c.want {
			t.Errorf("eventAt(%d) = %d, want %d", c.row, got, c.want)
		}
	}
}

// A tool_call immediately followed by its matching tool_result must fold into a
// single combined chat-log row (task 0043). Spawn-style tools (whose subagent
// events appear between call and result) and id-mismatched pairs must not fold.
func TestMergedResultPairing(t *testing.T) {
	m := &model{evs: []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator"},
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"ok"}`},
		{Seq: 4, Type: "model_turn", Actor: "coordinator"},
	}}
	if got := m.mergedResultIdx(1); got != 2 {
		t.Fatalf("mergedResultIdx(1) = %d, want 2", got)
	}
	if !m.isMergedResult(2) {
		t.Fatal("isMergedResult(2) = false, want true")
	}
	if m.isMergedResult(1) {
		t.Fatal("isMergedResult(1) = true, want false (call is not a result)")
	}

	// Spawn-style: a non-adjacent result (subagent event in between) must not fold.
	spawn := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"s1","name":"spawn_implementer"}`},
		{Seq: 2, Type: "subagent_spawned", Actor: "coordinator"},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"s1","result":"done"}`},
	}}
	if got := spawn.mergedResultIdx(0); got != -1 {
		t.Fatalf("spawn mergedResultIdx(0) = %d, want -1", got)
	}

	// Id mismatch (adjacent but different ids) must not fold.
	mismatch := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"a","name":"Read"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"b","result":"ok"}`},
	}}
	if got := mismatch.mergedResultIdx(0); got != -1 {
		t.Fatalf("mismatch mergedResultIdx(0) = %d, want -1", got)
	}
}

// After rebuild, a folded tool_result shares its call's start line and emits no
// block of its own, and clicks anywhere in the combined region resolve to the
// call (task 0043).
func TestRebuildCombinesRow(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hi"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"x.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"contents"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"bye"}`})
	m.rebuild() // appendEvent no longer rebuilds; caller batches + rebuilds once

	if m.eventStart[2] != m.eventStart[1] {
		t.Fatalf("folded result start %d != call start %d", m.eventStart[2], m.eventStart[1])
	}
	// The result block must not advance the line counter past the call.
	if m.eventStart[3] <= m.eventStart[1] {
		t.Fatalf("trailing event start %d should be after combined row %d", m.eventStart[3], m.eventStart[1])
	}
	// A click in the combined region resolves to the call (index 1).
	if got := m.eventAt(m.eventStart[1]); got != 1 {
		t.Fatalf("eventAt(call start) = %d, want 1", got)
	}
}

// An empty model_turn (an agent turn carrying only tool calls, no text) is
// hidden: it renders no row of its own and shares the previous rendered row's
// start line, so the chat no longer shows a bare line with just a duration. The
// following tool call still resolves and selection skips the hidden turn.
func TestRebuildHidesEmptyModelTurn(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`})
	// Empty agent turn: no text, just timing — the noise we want gone.
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"","duration_ms":340}`})
	m.appendEvent(&v1.Event{Seq: 3, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"x.go\"}"}`})
	m.appendEvent(&v1.Event{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"ok"}`})
	m.rebuild() // appendEvent no longer rebuilds; caller batches + rebuilds once

	if !m.isEmptyModelTurn(1) {
		t.Fatal("isEmptyModelTurn(1) = false, want true for a text-less model_turn")
	}
	if m.isEmptyModelTurn(0) {
		t.Fatal("isEmptyModelTurn(0) = true, want false for a user_input")
	}
	// The hidden turn shares the previous row's start line and emits no block.
	if m.eventStart[1] != m.eventStart[0] {
		t.Fatalf("empty model_turn start %d != previous row start %d", m.eventStart[1], m.eventStart[0])
	}
	// The following tool call advances past the shared row.
	if m.eventStart[2] <= m.eventStart[0] {
		t.Fatalf("tool_call start %d should be after the user_input row %d", m.eventStart[2], m.eventStart[0])
	}
	// A click on the hidden turn's line resolves to the previous visible row.
	if got := m.eventAt(m.eventStart[1]); got != 0 {
		t.Fatalf("eventAt(empty turn line) = %d, want 0", got)
	}
	// Selecting downward from the user_input skips the hidden turn onto the call.
	m.selected = 0
	m.moveSelection(1)
	if m.selected != 2 {
		t.Fatalf("moveSelection(1) landed on %d, want 2 (skip the empty turn)", m.selected)
	}
}

// Expanding a combined tool_call+tool_result row reveals both the full params and the full response with no information lost (task 0043).
func TestRenderCombinedExpanded(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{\"file_path\":\"hello.go\"}"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"RESULTBODY"}`},
	}
	m.expanded[2] = true
	out := m.renderBlock(0, m.evs[0])
	if !strings.Contains(out, "hello.go") {
		t.Fatalf("expanded combined row missing args content:\n%s", out)
	}
	if !strings.Contains(out, "RESULTBODY") {
		t.Fatalf("expanded combined row missing result content:\n%s", out)
	}
	if !strings.Contains(out, "Response") {
		t.Fatalf("expanded combined row missing Response box:\n%s", out)
	}
	// Collapsed: still shows the tool name + a status marker.
	m.expanded[2] = false
	m.bodyCache = map[int]string{}
	col := m.renderBlock(0, m.evs[0])
	if !strings.Contains(col, "Read") || !strings.Contains(col, "✓") {
		t.Fatalf("collapsed combined row missing name/status:\n%s", col)
	}
}

// The actor name is spelled out only when an actor first starts a run of rows;
// continuation rows by the same actor show its compact glyph instead. A
// model_turn is rendered as framing prose, dropping the redundant type label.
func TestActorRunDedupAndFraming(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"first words"}`},
		{Seq: 2, Type: "thinking", Actor: "coordinator", DataJson: `{"text":"pondering"}`},
		{Seq: 3, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"now me"}`},
	}

	// First coordinator row spells out the name; model_turn omits the type label.
	first := m.renderBlock(0, m.evs[0])
	if !strings.Contains(first, "coordinator") {
		t.Fatalf("first-of-run row should spell out actor name:\n%s", first)
	}
	if strings.Contains(first, "model_turn") {
		t.Fatalf("model_turn row should drop the redundant type label:\n%s", first)
	}
	if !strings.Contains(first, "first words") {
		t.Fatalf("model_turn row should show its prose:\n%s", first)
	}

	// Second coordinator row (continuation) shows the glyph, not the name.
	cont := m.renderBlock(1, m.evs[1])
	if strings.Contains(cont, "coordinator") {
		t.Fatalf("continuation row should not repeat the actor name:\n%s", cont)
	}
	if !strings.Contains(cont, actorGlyph("coordinator")) {
		t.Fatalf("continuation row should show the actor glyph:\n%s", cont)
	}

	// Actor switch spells out the new actor again.
	switched := m.renderBlock(2, m.evs[2])
	if !strings.Contains(switched, "implementer") {
		t.Fatalf("actor switch should spell out the new actor:\n%s", switched)
	}
}

// Each event type renders its consistent leading glyph, and continuation rows
// still carry the actor glyph (column alignment preserved).
func TestTypeGlyphsInHeader(t *testing.T) {
	m := &model{w: 120, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "thinking", Actor: "coordinator", DataJson: `{"text":"pondering"}`},
		{Seq: 2, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`},
		{Seq: 3, Type: "review_submitted", Actor: "reviewer-1", DataJson: `{"model":"claude","verdict":"accept","summary":"lgtm"}`},
	}
	cases := []struct {
		i    int
		typ  string
		want string
	}{
		{0, "thinking", typeGlyph("thinking")},
		{1, "user_input", typeGlyph("user_input")},
		{2, "review_submitted", typeGlyph("review_submitted")},
	}
	for _, c := range cases {
		out := m.renderBlock(c.i, m.evs[c.i])
		if !strings.Contains(out, c.want) {
			t.Fatalf("%s row should contain glyph %q:\n%s", c.typ, c.want, out)
		}
	}
}

// detailLine color-codes review verdicts: accept=success, revise=warn, reject=danger.
func TestVerdictColorsInDetailLine(t *testing.T) {
	// lipgloss v2 styles always emit ANSI (the program output layer handles
	// profile downsampling), so no color-profile setup is needed here.
	for _, v := range []string{"accept", "revise", "reject"} {
		ev := &v1.Event{Seq: 1, Type: "review_submitted", Actor: "reviewer-1",
			DataJson: fmt.Sprintf(`{"model":"claude","verdict":%q,"summary":"sum"}`, v)}
		d := detailLine(ev)
		styled := verdictStyle(v).Render(v)
		if !strings.Contains(d, styled) {
			t.Fatalf("verdict %q should be styled via verdictStyle in detailLine:\ngot  %q\nwant substring %q", v, d, styled)
		}
		// The styled token must differ from the bare token (i.e. ANSI was applied).
		if styled == v {
			t.Fatalf("verdictStyle(%q) produced no styling: %q", v, styled)
		}
	}
}

// A contiguous sub-agent run renders ├─ on its non-last rows and └─ on the last,
// nesting the sub-agents under the coordinator.
func TestSubAgentTreeConnectors(t *testing.T) {
	m := &model{w: 120, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.evs = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"dispatching"}`},
		{Seq: 2, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"working a"}`},
		{Seq: 3, Type: "model_turn", Actor: "implementer", DataJson: `{"text":"working b"}`},
		{Seq: 4, Type: "model_turn", Actor: "reviewer-1", DataJson: `{"text":"reviewing"}`},
	}
	// Coordinator row: no connector.
	if c := m.renderBlock(0, m.evs[0]); strings.Contains(c, "├─") || strings.Contains(c, "└─") {
		t.Fatalf("coordinator row should not have a sub-agent connector:\n%s", c)
	}
	// Non-last sub-agent rows use ├─.
	for _, i := range []int{1, 2} {
		out := m.renderBlock(i, m.evs[i])
		if !strings.Contains(out, "├─") {
			t.Fatalf("non-last sub-agent row %d should use ├─:\n%s", i, out)
		}
	}
	// Last sub-agent row of the run uses └─.
	last := m.renderBlock(3, m.evs[3])
	if !strings.Contains(last, "└─") {
		t.Fatalf("last sub-agent row should use └─:\n%s", last)
	}
}

// A tool_result carrying a structured view renders as a connector tree (summary
// headline + nested nodes) instead of the raw text, inside the expanded card.
func TestToolViewTreeRendering(t *testing.T) {
	m := &model{w: 90, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	view := `{"summary":"1/2 reviewers accept","status":"warn","nodes":[` +
		`{"label":"claude","detail":"accept","kind":"ok"},` +
		`{"label":"gpt","detail":"reject","kind":"error","children":[{"label":"off-by-one","detail":"[blocker]","kind":"error"}]}]}`
	m.evs = []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"re_review","args":"{\"task_id\":\"0042\"}"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"RAWTEXT","view":` + view + `}`},
	}
	m.expanded[1] = true
	out := m.renderBlock(0, m.evs[0])
	for _, want := range []string{"1/2 reviewers accept", "claude", "├─", "└─", "off-by-one", "[blocker]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view tree missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "RAWTEXT") {
		t.Fatalf("view present but raw result text still rendered:\n%s", out)
	}
	// No view => raw text path still works.
	if toolViewOf(&v1.Event{DataJson: `{"result":"x"}`}) != nil {
		t.Fatal("toolViewOf should be nil without a view field")
	}
}

func TestDiffDetectionAndColorize(t *testing.T) {
	diff := "diff --git a/x b/x\n@@ -1 +1 @@\n-old line\n+new line\n unchanged"
	if !looksDiff(diff) {
		t.Fatal("looksDiff should detect a git diff")
	}
	out := colorizeDiff(diff)
	for _, want := range []string{"old line", "new line", "unchanged", "@@ -1 +1 @@"} {
		if !strings.Contains(out, want) {
			t.Fatalf("colorizeDiff dropped %q:\n%s", want, out)
		}
	}
	if looksDiff("just some text\nno diff here") {
		t.Fatal("looksDiff false positive")
	}
}

func TestCatNDimming(t *testing.T) {
	src := "     1\tpackage main\n     2\tfunc main() {}"
	if !looksCatN(src) {
		t.Fatal("looksCatN should detect cat -n output")
	}
	out := dimLineNumbers(src)
	if !strings.Contains(out, "package main") || !strings.Contains(out, "func main() {}") {
		t.Fatalf("dimLineNumbers dropped code:\n%s", out)
	}
}

// --- language inference (task 0017) ---

// dataField must surface JSON booleans as "true"/"false" so checks like the
// tool_result error routing (dataField(ev,"error") == "true") work — the engine
// emits "error" as a JSON bool.
func TestDataFieldBool(t *testing.T) {
	if got := dataField(&v1.Event{DataJson: `{"error":true}`}, "error"); got != "true" {
		t.Fatalf("dataField bool true = %q, want \"true\"", got)
	}
	if got := dataField(&v1.Event{DataJson: `{"error":false}`}, "error"); got != "false" {
		t.Fatalf("dataField bool false = %q, want \"false\"", got)
	}
}

func TestLexerNameForPath(t *testing.T) {
	cases := []struct {
		path    string
		want    string // exact name, or "" for empty
		contain string // substring expectation when want is ""
	}{
		{"main.go", "", "Go"},
		{"sub/dir/x.py", "Python", ""},
		{"a.ts", "TypeScript", ""},
		{"noext", "", ""},
		{"weird.zzzzz", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		got := lexerNameForPath(c.path)
		switch {
		case c.want != "":
			if got != c.want {
				t.Errorf("lexerNameForPath(%q) = %q, want %q", c.path, got, c.want)
			}
		case c.contain != "":
			if !strings.Contains(got, c.contain) {
				t.Errorf("lexerNameForPath(%q) = %q, want containing %q", c.path, got, c.contain)
			}
		default:
			if got != "" {
				t.Errorf("lexerNameForPath(%q) = %q, want \"\"", c.path, got)
			}
		}
	}
}

func TestLexerNameForCommand(t *testing.T) {
	if got := lexerNameForCommand("rg -g '*.go' foo"); !strings.Contains(got, "Go") {
		t.Errorf("rg -g '*.go' => %q, want Go", got)
	}
	if got := lexerNameForCommand("rg --type py foo"); got != "Python" {
		t.Errorf("rg --type py => %q, want Python", got)
	}
	if got := lexerNameForCommand("rg -t go foo src/"); !strings.Contains(got, "Go") {
		t.Errorf("rg -t go => %q, want Go", got)
	}
	if got := lexerNameForCommand("rg --glob=*.py foo"); got != "Python" {
		t.Errorf("rg --glob=*.py => %q, want Python", got)
	}
	// Ambiguous: a Go type AND a Python glob.
	if got := lexerNameForCommand("rg -t go -g '*.py' foo"); got != "" {
		t.Errorf("ambiguous mixed => %q, want \"\"", got)
	}
	// No restriction at all.
	if got := lexerNameForCommand("rg foo"); got != "" {
		t.Errorf("rg foo => %q, want \"\"", got)
	}
	if got := lexerNameForCommand("grep -rn foo ."); got != "" {
		t.Errorf("grep -rn => %q, want \"\"", got)
	}
	// Negated glob alone is ignored (not a positive restriction).
	if got := lexerNameForCommand("rg -g '!*.go' foo"); got != "" {
		t.Errorf("negated glob => %q, want \"\"", got)
	}
}

func TestLexerNameForGrepPaths(t *testing.T) {
	uniform := "internal/a.go:10:func A() {}\ninternal/b.go:3:func B() {}"
	if got := lexerNameForGrepPaths(uniform); !strings.Contains(got, "Go") {
		t.Errorf("uniform .go => %q, want Go", got)
	}
	mixed := "a.go:1:x\nb.py:2:y"
	if got := lexerNameForGrepPaths(mixed); got != "" {
		t.Errorf("mixed => %q, want \"\"", got)
	}
	none := "just some text\nno prefixes here"
	if got := lexerNameForGrepPaths(none); got != "" {
		t.Errorf("no prefixes => %q, want \"\"", got)
	}
	// Column-numbered prefixes are also recognized.
	withCol := "a.go:10:5:func A() {}\nb.go:1:1:package main"
	if got := lexerNameForGrepPaths(withCol); !strings.Contains(got, "Go") {
		t.Errorf("with col => %q, want Go", got)
	}
}

func TestHighlightCodeFallbacks(t *testing.T) {
	const code = "func main() {}"
	if got := highlightCode(code, ""); got != code {
		t.Errorf("empty lexer should return input unchanged, got %q", got)
	}
	if got := highlightCode(code, "no-such-lexer-xyz"); got != code {
		t.Errorf("unknown lexer should return input unchanged, got %q", got)
	}
}

func TestHighlightCatNNeverDrops(t *testing.T) {
	src := "     1\tpackage main\n     2\tfunc main() {}"
	out := highlightCatN(src, "Go")
	if !strings.Contains(stripANSI(out), "package main") || !strings.Contains(stripANSI(out), "func main() {}") {
		t.Fatalf("highlightCatN dropped code:\n%q", out)
	}
	// Line count must be preserved.
	if got, want := len(strings.Split(out, "\n")), 2; got != want {
		t.Fatalf("highlightCatN line count = %d, want %d", got, want)
	}
	// With no lexer it behaves like dimLineNumbers.
	if got := highlightCatN(src, ""); got != dimLineNumbers(src) {
		t.Fatalf("highlightCatN with no lexer should equal dimLineNumbers")
	}
}

func TestHighlightGrepNeverDrops(t *testing.T) {
	src := "internal/x.go:10:func Foo() {}"
	out := highlightGrep(src, "Go")
	plain := stripANSI(out)
	if !strings.Contains(plain, "func Foo() {}") {
		t.Fatalf("highlightGrep dropped match text:\n%q", out)
	}
	if !strings.Contains(plain, "internal/x.go:10:") {
		t.Fatalf("highlightGrep dropped path prefix:\n%q", out)
	}
	// Non-prefixed lines pass through; with no lexer the input is unchanged.
	if got := highlightGrep(src, ""); got != src {
		t.Fatalf("highlightGrep with no lexer should return input unchanged")
	}
}

// argField/callFor correlate a tool_result with its originating tool_call so the
// renderer can infer language from the call's args.
func TestCallForAndArgField(t *testing.T) {
	call := &v1.Event{Seq: 1, Type: "tool_call", DataJson: `{"name":"Read","args":"{\"file_path\":\"x.go\"}","id":"c1"}`}
	res := &v1.Event{Seq: 2, Type: "tool_result", DataJson: `{"name":"Read","result":"...","id":"c1"}`}
	m := &model{evs: []*v1.Event{call, res}}
	if got := m.callFor(res); got != call {
		t.Fatalf("callFor did not match by id")
	}
	if got := argField(call, "file_path"); got != "x.go" {
		t.Fatalf("argField(file_path) = %q, want x.go", got)
	}
	// Fallback to nearest preceding tool_call when id is absent.
	res2 := &v1.Event{Seq: 2, Type: "tool_result", DataJson: `{"name":"Read","result":"..."}`}
	m2 := &model{evs: []*v1.Event{call, res2}}
	if got := m2.callFor(res2); got != call {
		t.Fatalf("callFor fallback to preceding tool_call failed")
	}
}

func TestPrettyArgs(t *testing.T) {
	out := prettyArgs(`{"file_path":"a.go","content":"x"}`)
	if !strings.Contains(out, "\n") || !strings.Contains(out, "file_path") {
		t.Fatalf("prettyArgs should indent JSON:\n%s", out)
	}
	if prettyArgs("not json") != "not json" {
		t.Fatal("prettyArgs should pass through non-JSON")
	}
}

func TestDetailLineToolCall(t *testing.T) {
	ev := &v1.Event{Type: "tool_call", DataJson: `{"name":"Read","args":"{\"file_path\":\"x\"}"}`}
	if d := detailLine(ev); !strings.HasPrefix(d, "Read(") {
		t.Fatalf("detailLine = %q", d)
	}
}

// fmtDurMS renders sub-second durations in milliseconds and longer ones as
// one-decimal seconds.
func TestFmtDurMS(t *testing.T) {
	cases := map[int64]string{
		0:    "0ms",
		340:  "340ms",
		999:  "999ms",
		1000: "1.0s",
		1200: "1.2s",
		1250: "1.2s",
		9999: "10.0s",
	}
	for ms, want := range cases {
		if got := fmtDurMS(ms); got != want {
			t.Errorf("fmtDurMS(%d) = %q, want %q", ms, got, want)
		}
	}
}

// durationMSField extracts duration_ms from an event's data JSON, tolerating
// missing fields and malformed JSON.
func TestDurationMSField(t *testing.T) {
	if got := durationMSField(&v1.Event{DataJson: `{"duration_ms":340}`}); got != 340 {
		t.Errorf("duration_ms present = %d, want 340", got)
	}
	if got := durationMSField(&v1.Event{DataJson: `{"text":"hi"}`}); got != 0 {
		t.Errorf("duration_ms absent = %d, want 0", got)
	}
	if got := durationMSField(&v1.Event{DataJson: ``}); got != 0 {
		t.Errorf("empty data = %d, want 0", got)
	}
	if got := durationMSField(&v1.Event{DataJson: `not json`}); got != 0 {
		t.Errorf("bad json = %d, want 0", got)
	}
}

// Collapsed model_turn and tool_result rows append a compact duration suffix
// when duration_ms is positive, and omit it otherwise.
func TestDetailLineDuration(t *testing.T) {
	mt := &v1.Event{Type: "model_turn", DataJson: `{"text":"done","duration_ms":1200}`}
	if d := detailLine(mt); !strings.Contains(d, "1.2s") || !strings.Contains(d, "done") {
		t.Fatalf("model_turn detailLine = %q, want text + 1.2s", d)
	}
	tr := &v1.Event{Type: "tool_result", DataJson: `{"result":"ok","duration_ms":340}`}
	if d := detailLine(tr); !strings.Contains(d, "340ms") || !strings.Contains(d, "ok") {
		t.Fatalf("tool_result detailLine = %q, want result + 340ms", d)
	}
	// No duration field -> no suffix.
	noDur := &v1.Event{Type: "model_turn", DataJson: `{"text":"done"}`}
	if d := detailLine(noDur); strings.Contains(d, "ms") || strings.Contains(d, "s ") {
		t.Fatalf("model_turn without duration should have no suffix: %q", d)
	}
	// Zero duration -> no suffix.
	zeroDur := &v1.Event{Type: "tool_result", DataJson: `{"result":"ok","duration_ms":0}`}
	if d := detailLine(zeroDur); d != "ok" {
		t.Fatalf("zero duration should add no suffix: %q", d)
	}
}

// The markdown renderer must build with a fixed style (no terminal query, which
// would block under Bubble Tea) and render content.
func TestRendererBuildsAndRenders(t *testing.T) {
	m := &model{w: 80}
	m.makeRenderer()
	if m.glam == nil {
		t.Fatal("renderer was not created")
	}
	out := m.markdown("# Title\n\nSome **bold** and `code`.")
	if !strings.Contains(out, "Title") {
		t.Fatalf("markdown render missing content: %q", out)
	}
}

func TestAutoExpand(t *testing.T) {
	if !autoExpand("session_idle") || !autoExpand("question_asked") {
		t.Fatal("session_idle and question_asked should auto-expand")
	}
	if autoExpand("tool_call") {
		t.Fatal("tool_call should not auto-expand")
	}
	// Thinking events should stay collapsed by default so they don't clutter.
	if autoExpand("thinking") {
		t.Fatal("thinking should not auto-expand")
	}
}

// cycle walks the thinking-level list in both directions and wraps around at the
// ends — the behavior the overlay's ←/→ keys rely on.
func TestCycleThinkLevels(t *testing.T) {
	if got := cycle(thinkLevels, "high", 1); got != "xhigh" {
		t.Fatalf("high +1 = %q, want xhigh", got)
	}
	if got := cycle(thinkLevels, "high", -1); got != "medium" {
		t.Fatalf("high -1 = %q, want medium", got)
	}
	if got := cycle(thinkLevels, "max", 1); got != "off" {
		t.Fatalf("max +1 = %q, want off (wrap)", got)
	}
	if got := cycle(thinkLevels, "off", -1); got != "max" {
		t.Fatalf("off -1 = %q, want max (wrap)", got)
	}
	// thinkLevels covers exactly the levels the session layer accepts.
	want := []string{"off", "low", "medium", "high", "xhigh", "max"}
	if strings.Join(thinkLevels, ",") != strings.Join(want, ",") {
		t.Fatalf("thinkLevels = %v, want %v", thinkLevels, want)
	}
}

// A thinking event renders a one-line "(reasoning)" detail and an expandable
// body carrying the reasoning summary.
func TestThinkingRendering(t *testing.T) {
	ev := &v1.Event{Type: "thinking", DataJson: `{"text":"first I will read the file","blocks":1}`}
	if d := detailLine(ev); !strings.Contains(d, "reasoning") || !strings.Contains(d, "read the file") {
		t.Fatalf("detailLine = %q", d)
	}
	m := &model{w: 80}
	body := m.renderBody(ev)
	if !strings.Contains(body, "read the file") {
		t.Fatalf("renderBody = %q", body)
	}
	// An empty reasoning summary produces no body (nothing to expand).
	empty := &v1.Event{Type: "thinking", DataJson: `{"text":""}`}
	if b := m.renderBody(empty); strings.TrimSpace(b) != "" {
		t.Fatalf("empty thinking body = %q", b)
	}
}

// When a prose row is expanded, the header drops its one-line snippet (the full
// text is in the body box) but keeps non-body metadata like a model_turn's
// elapsed time; collapsed rows still show the snippet preview.
func TestExpandedHeaderDropsSnippet(t *testing.T) {
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"here is my long final answer about things","duration_ms":1200}`}
	m := &model{w: 120, bodyCache: map[int]string{}}

	// Collapsed: snippet present.
	collapsed := m.renderHeader(0, turn, false, false, true, true)
	if !strings.Contains(collapsed, "long final answer") {
		t.Fatalf("collapsed header should show the snippet, got %q", collapsed)
	}

	// Expanded: snippet gone, duration kept.
	expanded := m.renderHeader(0, turn, false, true, true, true)
	if strings.Contains(expanded, "long final answer") {
		t.Fatalf("expanded header should drop the redundant snippet, got %q", expanded)
	}
	if !strings.Contains(expanded, "1.2s") {
		t.Fatalf("expanded model_turn header should keep its elapsed time, got %q", expanded)
	}

	// A user_input row drops its snippet entirely when expanded.
	in := &v1.Event{Seq: 2, Type: "user_input", Actor: "user", DataJson: `{"text":"please refactor the parser module"}`}
	if h := m.renderHeader(1, in, false, true, true, true); strings.Contains(h, "refactor the parser") {
		t.Fatalf("expanded user_input header should drop the snippet, got %q", h)
	}
}

// A session_idle report that merely echoes the model's final assistant turn
// renders no body (the final output must not be printed twice), while any extra
// the report adds (autonomous-mode assumptions) — or a genuinely different
// control-tool report — still renders.
func TestIdleReportDeduped(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All done — shipped the feature and tests pass."}`}

	// Exact echo: no body.
	idle := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass."}`}
	m := mk(turn, idle)
	if b := m.renderBody(idle); strings.TrimSpace(b) != "" {
		t.Fatalf("echoing idle report should render empty body, got %q", b)
	}

	// Echo + appended assumptions: only the assumptions remain.
	idle2 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass.\n\nAssumptions made without consulting the user (autonomous mode):\n- used port 8080\n"}`}
	m = mk(turn, idle2)
	b := m.renderBody(idle2)
	if strings.Contains(b, "shipped the feature") {
		t.Fatalf("idle body should drop the duplicated final turn, got %q", b)
	}
	if !strings.Contains(b, "Assumptions") || !strings.Contains(b, "port 8080") {
		t.Fatalf("idle body should keep the appended assumptions, got %q", b)
	}

	// Different control-tool report: rendered in full.
	idle3 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042 and committed the change."}`}
	m = mk(turn, idle3)
	if b := m.renderBody(idle3); !strings.Contains(b, "task 0042") {
		t.Fatalf("a differing report should render in full, got %q", b)
	}
}

// A session_idle whose report merely echoes the final model_turn is hidden
// entirely (hiddenRow): otherwise its collapsed header re-prints the full report
// a second time, directly below the identical model_turn row. Any idle that adds
// content (assumptions) or differs stays visible.
func TestEchoedIdleHidden(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All green. Shipped it."}`}

	echo := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it."}`}
	m := mk(turn, echo)
	if !m.hiddenRow(1) {
		t.Fatal("a session_idle echoing the final turn should be a hidden row")
	}

	added := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it.\n\nAssumptions:\n- used port 8080"}`}
	m = mk(turn, added)
	if m.hiddenRow(1) {
		t.Fatal("an idle that adds content should stay visible")
	}

	diff := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042."}`}
	m = mk(turn, diff)
	if m.hiddenRow(1) {
		t.Fatal("a differing idle report should stay visible")
	}
}

// needsOnboarding flags an un-onboarded workspace (no real spec.md + no backlog
// tasks) so the home menu can surface onboarding prominently (spec §19.2).
func TestNeedsOnboarding(t *testing.T) {
	t.Run("fresh empty dir", func(t *testing.T) {
		if !needsOnboarding(t.TempDir()) {
			t.Fatal("empty workspace should need onboarding")
		}
	})
	t.Run("non-trivial spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n\n## Goals\nship it\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a real spec should not need onboarding")
		}
	})
	t.Run("backlog task but no spec", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/0001-foo.md", "# task\n")
		if needsOnboarding(ws) {
			t.Fatal("workspace with a backlog task should not need onboarding")
		}
	})
	t.Run("trivially empty spec, no backlog", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "spec.md", "# Spec\n")
		if !needsOnboarding(ws) {
			t.Fatal("trivially-empty spec + no backlog should need onboarding")
		}
	})
	t.Run("only generated index, no tasks", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/backlog.md", "# Backlog\n")
		if !needsOnboarding(ws) {
			t.Fatal("generated backlog index without tasks should still need onboarding")
		}
	})
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The status header must not latch on "error": after a session_error sets the
// status, a subsequent model_turn (forward progress) clears it back to running
// (task 0051).
func TestAppendEventClearsLatchedError(t *testing.T) {
	m := &model{w: 80, follow: true}
	m.appendEvent(&v1.Event{Type: "session_error", DataJson: `{"msg":"boom"}`})
	if m.status != "error" {
		t.Fatalf("after session_error status = %q, want error", m.status)
	}
	m.appendEvent(&v1.Event{Type: "model_turn", DataJson: `{"text":"recovered"}`})
	if m.status != "running" {
		t.Fatalf("after model_turn status = %q, want running", m.status)
	}
}

// The session view must fit exactly within the terminal: every rendered line must
// be no wider than the terminal (so nothing wraps to a second physical row) and
// the total number of lines must equal the terminal height. A wrapping footer or
// header pushes the frame down a row, which is what hides the agent's last output
// line behind the input box (task 0052).
// TestOverlayRendersAsCard checks that modal overlays (settings, backlog) render
// as bordered, centered cards: the rendered View contains rounded-border glyphs
// and no physical line exceeds the terminal width (task 0061).
func TestOverlayRendersAsCard(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*model)
	}{
		{"settings", func(m *model) { m.openOverlay() }},
		{"backlog", func(m *model) {
			m.backlog = true
			m.backlogTasks = []*v1.BacklogTaskSummary{
				{Id: "0001", Status: "todo", Priority: 1, Title: "do a thing", Ready: true},
				{Id: "0002", Status: "doing", Priority: 2, Title: "another task", Ready: false, BlockedBy: []string{"0001"}},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model{
				state: stateMenu, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
				thinkLevels: map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
			}
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = updated.(model)
			tc.setup(&m)

			view := m.render()
			if !strings.ContainsAny(view, "╭╰│╮╯") {
				t.Fatalf("%s overlay does not render a rounded border:\n%s", tc.name, view)
			}
			for i, ln := range strings.Split(view, "\n") {
				if w := lipgloss.Width(ln); w > 80 {
					t.Fatalf("%s overlay line %d width %d exceeds terminal width 80: %q", tc.name, i, w, ln)
				}
			}
		})
	}
}

func TestSessionViewFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {60, 20}}
	for _, sz := range sizes {
		m := model{
			state: stateSession, status: "running", mode: "implement",
			sessionID: "sess12345678abcdef", follow: true,
			expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = updated.(model)

		// Fill well past the viewport so the frame is full and GotoBottom is active.
		for i := 0; i < 40; i++ {
			m.appendEvent(&v1.Event{
				Seq: int64(i), Type: "model_turn", Actor: "coordinator",
				DataJson: fmt.Sprintf(`{"text":"this is a fairly long output line number %d that is meant to wrap inside the body region but must never overflow the terminal width"}`, i),
			})
		}
		// The agent's final output (multi-line), the line that was being clipped.
		m.appendEvent(&v1.Event{
			Seq: 100, Type: "session_idle", Actor: "coordinator",
			DataJson: `{"report":"final report line one\nfinal report line two\nthis is the last visible line of the final output"}`,
		})

		view := m.sessionView()
		lines := strings.Split(view, "\n")
		if len(lines) != sz.h {
			t.Fatalf("%dx%d: sessionView produced %d lines, want %d", sz.w, sz.h, len(lines), sz.h)
		}
		for i, ln := range lines {
			if w := lipgloss.Width(ln); w > sz.w {
				t.Fatalf("%dx%d: line %d width %d exceeds terminal width %d: %q", sz.w, sz.h, i, w, sz.w, ln)
			}
		}
	}
}

// TestSpinnerInInputRow verifies the activity spinner moved from the top status
// bar to the bottom input row (task 0076): while running, the input row shows a
// spinner frame and the status bar shows only the static dot.
func TestSpinnerInInputRow(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "implement",
		sessionID: "sess12345678abcdef", follow: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		spin: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	glyph := m.spin.View()
	if glyph == "" {
		t.Fatal("spinner produced an empty frame")
	}

	if row := m.inputRow(); !strings.Contains(row, glyph) {
		t.Fatalf("inputRow should contain spinner frame %q while running:\n%s", glyph, row)
	}
	if bar := m.statusBar(); strings.Contains(bar, glyph) {
		t.Fatalf("status bar should not contain spinner frame %q (spinner moved to input): %q", glyph, bar)
	}
	if bar := m.statusBar(); !strings.Contains(bar, "●") {
		t.Fatalf("status bar should contain the static dot ●: %q", bar)
	}

	// On idle the gutter is blank: the input row no longer carries a spinner.
	m.status = "idle"
	if row := m.inputRow(); strings.Contains(row, glyph) {
		t.Fatalf("inputRow should not animate while idle: %q", row)
	}
}

// --- model-backends modal tests (task 0044) ---

// fakeClient is an in-memory SessionServiceClient for driving the model-backends
// modal. Embedding the generated interface means unimplemented methods compile
// (and panic if accidentally called), while the four model RPCs are backed by a
// map. RemoveModel rejects names in `referenced` to exercise inline validation.
type fakeClient struct {
	yccv1connect.SessionServiceClient
	models     map[string]*v1.ModelConfig
	order      []string
	referenced map[string]bool

	lastUpsert  *v1.ModelConfig
	upserts     []*v1.ModelConfig // every UpsertModel call, in order
	lastPersist bool
	lastRemove  string
	lastStopped string

	discoverIDs  []string // returned by DiscoverModels
	discoverNote string
	lastDiscover *v1.DiscoverModelsRequest

	lastStartLevel string // InteractionLevel of the most recent StartSession

	lastRoleReq *v1.SetRoleConfigRequest // most recent SetRoleConfig call

	// previous-sessions screen (spec §18.6)
	history      []*v1.SessionSummary
	lastReopened string
	transcript   []*v1.Event // returned by GetSessionTranscript
	lastTransID  string

	// cost view (spec §20.5, task 0039)
	usageRows   []*v1.UsageRow
	usageTotal  *v1.UsageRow
	usageWksp   string
	lastGroupBy []string

	// plan library browser (task 0077)
	plans []*v1.PlanSummary

	// batch digest (task 0098): canned per-task detail returned by GetTask so the
	// digest can surface a blocked task's reason, plus the last id requested.
	taskDetails map[string]*v1.TaskDetail
	lastGetTask string
	// backlog grooming (task 0099): records the last UpdateTask request so keypress
	// tests can assert the grooming RPC fired with the expected mutation, plus the
	// canned list a post-update ListBacklog refresh returns.
	lastUpdateTask *v1.UpdateTaskRequest
	backlogList    []*v1.BacklogTaskSummary
}

func newFakeClient(cfgs ...*v1.ModelConfig) *fakeClient {
	f := &fakeClient{models: map[string]*v1.ModelConfig{}, referenced: map[string]bool{}}
	for _, c := range cfgs {
		f.models[c.Name] = c
		f.order = append(f.order, c.Name)
	}
	return f
}

func (f *fakeClient) ListModels(_ context.Context, _ *connect.Request[v1.ListModelsRequest]) (*connect.Response[v1.ListModelsResponse], error) {
	var out []*v1.ModelInfo
	for _, name := range f.order {
		c := f.models[name]
		out = append(out, &v1.ModelInfo{Name: c.Name, Backend: c.Backend, Model: c.Model})
	}
	return connect.NewResponse(&v1.ListModelsResponse{Models: out}), nil
}

// SetRoleConfig records the most recent role-config request so tests can assert
// that cycling a role picker issues it immediately.
func (f *fakeClient) SetRoleConfig(_ context.Context, req *connect.Request[v1.SetRoleConfigRequest]) (*connect.Response[v1.SetRoleConfigResponse], error) {
	f.lastRoleReq = req.Msg
	return connect.NewResponse(&v1.SetRoleConfigResponse{}), nil
}

// StopSession records the stopped session id; the loop-idle test exercises it
// without dialing a real daemon.
func (f *fakeClient) StopSession(_ context.Context, req *connect.Request[v1.StopSessionRequest]) (*connect.Response[v1.StopSessionResponse], error) {
	f.lastStopped = req.Msg.SessionId
	return connect.NewResponse(&v1.StopSessionResponse{}), nil
}

// StartSession records the requested interaction level so the loop-autonomy test
// can assert that a loop iteration starts an unattended (autonomous) session.
func (f *fakeClient) StartSession(_ context.Context, req *connect.Request[v1.StartSessionRequest]) (*connect.Response[v1.StartSessionResponse], error) {
	f.lastStartLevel = req.Msg.InteractionLevel
	return connect.NewResponse(&v1.StartSessionResponse{SessionId: "s-new"}), nil
}

func (f *fakeClient) GetModelConfig(_ context.Context, req *connect.Request[v1.GetModelConfigRequest]) (*connect.Response[v1.GetModelConfigResponse], error) {
	c, ok := f.models[req.Msg.Name]
	if !ok {
		return nil, fmt.Errorf("no such model %q", req.Msg.Name)
	}
	return connect.NewResponse(&v1.GetModelConfigResponse{Model: c}), nil
}

func (f *fakeClient) UpsertModel(_ context.Context, req *connect.Request[v1.UpsertModelRequest]) (*connect.Response[v1.UpsertModelResponse], error) {
	c := req.Msg.Model
	f.lastUpsert = c
	f.upserts = append(f.upserts, c)
	f.lastPersist = req.Msg.Persist
	if _, ok := f.models[c.Name]; !ok {
		f.order = append(f.order, c.Name)
	}
	f.models[c.Name] = c
	return connect.NewResponse(&v1.UpsertModelResponse{}), nil
}

func (f *fakeClient) DiscoverModels(_ context.Context, req *connect.Request[v1.DiscoverModelsRequest]) (*connect.Response[v1.DiscoverModelsResponse], error) {
	f.lastDiscover = req.Msg
	return connect.NewResponse(&v1.DiscoverModelsResponse{
		ModelIds: f.discoverIDs, Note: f.discoverNote, FromNetwork: len(f.discoverIDs) > 0,
	}), nil
}

func (f *fakeClient) RemoveModel(_ context.Context, req *connect.Request[v1.RemoveModelRequest]) (*connect.Response[v1.RemoveModelResponse], error) {
	name := req.Msg.Name
	f.lastRemove = name
	if f.referenced[name] {
		return nil, fmt.Errorf("model %s is referenced by role coordinator", name)
	}
	if _, ok := f.models[name]; !ok {
		return nil, fmt.Errorf("no such model %q", name)
	}
	delete(f.models, name)
	out := f.order[:0]
	for _, n := range f.order {
		if n != name {
			out = append(out, n)
		}
	}
	f.order = out
	return connect.NewResponse(&v1.RemoveModelResponse{}), nil
}

// ListSessionHistory and ResumeSession back the previous-sessions screen (spec
// §18.6). Subscribe returns an error so the post-reopen subscribe cmd resolves to
// an errMsg instead of panicking on the embedded nil interface.
func (f *fakeClient) ListSessionHistory(_ context.Context, _ *connect.Request[v1.ListSessionHistoryRequest]) (*connect.Response[v1.ListSessionHistoryResponse], error) {
	return connect.NewResponse(&v1.ListSessionHistoryResponse{Sessions: f.history}), nil
}

func (f *fakeClient) ResumeSession(_ context.Context, req *connect.Request[v1.ResumeSessionRequest]) (*connect.Response[v1.ResumeSessionResponse], error) {
	f.lastReopened = req.Msg.SessionId
	mode := "work"
	for _, s := range f.history {
		if s.SessionId == req.Msg.SessionId {
			mode = s.Mode
		}
	}
	return connect.NewResponse(&v1.ResumeSessionResponse{SessionId: req.Msg.SessionId, Mode: mode, Status: "idle"}), nil
}

func (f *fakeClient) Subscribe(_ context.Context, _ *connect.Request[v1.SubscribeRequest]) (*connect.ServerStreamForClient[v1.Event], error) {
	return nil, fmt.Errorf("subscribe not supported in fakeClient")
}

// GetSessionTranscript backs the read-only transcript drill-in (spec §18.6).
func (f *fakeClient) GetSessionTranscript(_ context.Context, req *connect.Request[v1.GetSessionTranscriptRequest]) (*connect.Response[v1.GetSessionTranscriptResponse], error) {
	f.lastTransID = req.Msg.SessionId
	return connect.NewResponse(&v1.GetSessionTranscriptResponse{Events: f.transcript}), nil
}

// ListBacklog backs the backlog browser route from the browse selector; the
// browse tests only need it to not panic, so it returns f.backlogList (empty by
// default; grooming tests set it so a post-update refresh has something to return).
func (f *fakeClient) ListBacklog(_ context.Context, _ *connect.Request[v1.ListBacklogRequest]) (*connect.Response[v1.ListBacklogResponse], error) {
	return connect.NewResponse(&v1.ListBacklogResponse{Tasks: f.backlogList}), nil
}

// GetTask backs the backlog detail drill-in and the batch digest's blocked-reason
// fetch (task 0098). It returns canned detail keyed by id, or an empty detail.
func (f *fakeClient) GetTask(_ context.Context, req *connect.Request[v1.GetTaskRequest]) (*connect.Response[v1.GetTaskResponse], error) {
	f.lastGetTask = req.Msg.Id
	if t, ok := f.taskDetails[req.Msg.Id]; ok {
		return connect.NewResponse(&v1.GetTaskResponse{Task: t}), nil
	}
	return connect.NewResponse(&v1.GetTaskResponse{Task: &v1.TaskDetail{Id: req.Msg.Id}}), nil
}

// UpdateTask backs the backlog grooming keys (task 0099). It records the request
// and returns the (optionally canned) task detail with the mutation applied.
func (f *fakeClient) UpdateTask(_ context.Context, req *connect.Request[v1.UpdateTaskRequest]) (*connect.Response[v1.UpdateTaskResponse], error) {
	f.lastUpdateTask = req.Msg
	t := f.taskDetails[req.Msg.Id]
	if t == nil {
		t = &v1.TaskDetail{Id: req.Msg.Id}
	}
	if req.Msg.Status != nil {
		t.Status = req.Msg.GetStatus()
	}
	if req.Msg.Priority != nil {
		t.Priority = req.Msg.GetPriority()
	}
	if req.Msg.Title != nil {
		t.Title = req.Msg.GetTitle()
	}
	return connect.NewResponse(&v1.UpdateTaskResponse{Task: t}), nil
}

// ListPlans / GetPlan back the plan library browser route (task 0077). They
// return canned data so the browse selector → plans route can be driven.
func (f *fakeClient) ListPlans(_ context.Context, _ *connect.Request[v1.ListPlansRequest]) (*connect.Response[v1.ListPlansResponse], error) {
	return connect.NewResponse(&v1.ListPlansResponse{Plans: f.plans}), nil
}

func (f *fakeClient) GetPlan(_ context.Context, req *connect.Request[v1.GetPlanRequest]) (*connect.Response[v1.GetPlanResponse], error) {
	return connect.NewResponse(&v1.GetPlanResponse{Name: req.Msg.Name, Title: req.Msg.Name, Content: "# " + req.Msg.Name + "\nbody"}), nil
}

// GetUsage backs the cost view route (spec §20.5, task 0039). It records the
// requested group-by so tests can assert the "g" cycle re-fetches with a new
// dimension, and returns canned priced/unpriced rows plus a total.
func (f *fakeClient) GetUsage(_ context.Context, req *connect.Request[v1.GetUsageRequest]) (*connect.Response[v1.GetUsageResponse], error) {
	f.lastGroupBy = req.Msg.GroupBy
	return connect.NewResponse(&v1.GetUsageResponse{
		Rows:      f.usageRows,
		Total:     f.usageTotal,
		Workspace: f.usageWksp,
	}), nil
}

// drive feeds a key through Update and, if a command is returned, runs it and
// feeds the resulting message back through Update (recursing until no command).
// It threads the model value through, mirroring the Bubble Tea runtime.
func drive(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.Update(keyMsg(key))
	m = updated.(model)
	return runCmds(t, m, cmd)
}

// keyMsg builds a v2 KeyPressMsg from a key name ("enter", "ctrl+n", …) or, for
// anything else, a run of printable runes to type.
func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+o":
		return tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
	}
}

// runCmds executes a command (and any follow-ups it yields) by feeding each
// returned message back through Update.
func runCmds(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return m
		}
		updated, next := m.Update(msg)
		m = updated.(model)
		cmd = next
	}
	return m
}

// typeText sends each rune of s through Update so the focused text input edits.
// Text editing returns a cursor-blink command that would block if executed
// synchronously, so the returned cmds are intentionally ignored here.
func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		km := tea.KeyPressMsg{Code: r, Text: string(r)}
		updated, _ := m.Update(km)
		m = updated.(model)
	}
	return m
}

func newBackendsModel(f *fakeClient) model {
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.mbOpen = true
	m.mbView = 0
	return m
}

// TestOverlayCoordinatorAppliesImmediately covers the fix for the "role change
// didn't stick" bug: cycling the coordinator with →  in the settings overlay must
// issue SetRoleConfig right away (no separate "apply" step), so the daemon
// persists it. It works even with no active session (empty session_id).
func TestOverlayCoordinatorAppliesImmediately(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-x"},
		&v1.ModelConfig{Name: "fable", Backend: "anthropic", Model: "claude-fable-5"},
	)
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = runCmds(t, m, m.fetchModels) // populate the model list + seed roles
	m.openOverlay()
	// Move the cursor to the coordinator row and cycle it.
	m = drive(t, m, "down") // interaction level -> coordinator
	if m.ovCursor != ovCoord {
		t.Fatalf("cursor = %d, want ovCoord(%d)", m.ovCursor, ovCoord)
	}
	before := m.roleCoord
	m = drive(t, m, "right")
	if m.roleCoord == before {
		t.Fatalf("coordinator did not change from %q", before)
	}
	if f.lastRoleReq == nil {
		t.Fatal("cycling coordinator did not issue SetRoleConfig")
	}
	if f.lastRoleReq.Coordinator != m.roleCoord {
		t.Fatalf("SetRoleConfig coordinator = %q, want %q", f.lastRoleReq.Coordinator, m.roleCoord)
	}
}

// t_tempWorkspace is an empty path; the modal tests don't touch the filesystem.
const t_tempWorkspace = ""

func TestModelBackendsAdd(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels) // populate the list
	if len(m.models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(m.models))
	}
	// Open a blank add form.
	m = drive(t, m, "a")
	if m.mbView != 1 || m.mbFormMode != mbAdd {
		t.Fatalf("after 'a' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	// name (focused) -> backend default anthropic; type name.
	m = typeText(t, m, "gpt")
	// move to backend, cycle to openai.
	m = drive(t, m, "tab")
	m = drive(t, m, "right") // anthropic -> openai
	// move to model field (backend->base_url->model) and type.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	// The add form seeds the model field with the backend's curated ids; clear it
	// and enter a single id so this exercises the single-model path.
	m.mbInputs[mbFieldModel].SetValue("")
	m = typeText(t, m, "gpt-5")
	// move to key_env and type.
	m = drive(t, m, "tab")
	m = typeText(t, m, "OPENAI_API_KEY")
	// Submit. Backend edits always persist to ycc.toml (no opt-out).
	m = drive(t, m, "enter")
	if f.lastUpsert == nil {
		t.Fatal("UpsertModel was not called")
	}
	if f.lastUpsert.Name != "gpt" || f.lastUpsert.Backend != "openai" || f.lastUpsert.Model != "gpt-5" {
		t.Fatalf("UpsertModel got name=%q backend=%q model=%q", f.lastUpsert.Name, f.lastUpsert.Backend, f.lastUpsert.Model)
	}
	if f.lastUpsert.KeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("UpsertModel key_env=%q", f.lastUpsert.KeyEnv)
	}
	if !f.lastPersist {
		t.Fatal("expected persist=true")
	}
	if m.mbView != 0 {
		t.Fatalf("after submit mbView=%d, want 0 (list)", m.mbView)
	}
	// The list refreshed so role pickers see the new model.
	if len(m.models) != 2 {
		t.Fatalf("after add list has %d models, want 2", len(m.models))
	}
}

func TestModelBackendsEdit(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Edit the selected (only) model: GetModelConfig prefill, then change model id.
	m = drive(t, m, "e")
	if m.mbView != 1 || m.mbFormMode != mbEdit {
		t.Fatalf("after 'e' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	if got := m.mbInputs[mbFieldName].Value(); got != "claude" {
		t.Fatalf("prefill name=%q, want claude", got)
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "claude-3" {
		t.Fatalf("prefill model=%q, want claude-3", got)
	}
	// Focus starts on backend (name is read-only in edit). Move to model and edit.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	m = typeText(t, m, "-opus")
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Name != "claude" {
		t.Fatalf("edit UpsertModel name=%v", f.lastUpsert)
	}
	if f.lastUpsert.Model != "claude-3-opus" {
		t.Fatalf("edit UpsertModel model=%q, want claude-3-opus", f.lastUpsert.Model)
	}
}

func TestModelBackendsDuplicate(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "d")
	if m.mbView != 1 || m.mbFormMode != mbDuplicate {
		t.Fatalf("after 'd' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	if got := m.mbInputs[mbFieldName].Value(); got != "claude-copy" {
		t.Fatalf("duplicate name=%q, want claude-copy", got)
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "claude-3" {
		t.Fatalf("duplicate kept model=%q, want claude-3", got)
	}
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Name != "claude-copy" {
		t.Fatalf("duplicate UpsertModel name=%v", f.lastUpsert)
	}
	if f.lastUpsert.Model != "claude-3" || f.lastUpsert.Backend != "anthropic" {
		t.Fatalf("duplicate kept fields: model=%q backend=%q", f.lastUpsert.Model, f.lastUpsert.Backend)
	}
	if len(m.models) != 2 {
		t.Fatalf("after duplicate list has %d models, want 2", len(m.models))
	}
}

// TestModelBackendsDuplicatePricing strengthens duplicate coverage: a model with
// pricing pointers is duplicated, and the resulting UpsertModel carries the same
// pricing values plus the shared base_url/key_env under a new name (task 0042 —
// a credential-sharing sibling that differs only in name + model id).
func TestModelBackendsDuplicatePricing(t *testing.T) {
	pi, po, cr, cw := 3.0, 15.0, 0.3, 3.75
	f := newFakeClient(&v1.ModelConfig{
		Name: "claude", Backend: "anthropic",
		BaseUrl: "https://api.anthropic.com", Model: "claude-opus-4-8",
		KeyEnv:     "ANTHROPIC_API_KEY",
		PriceInput: &pi, PriceOutput: &po, PriceCacheRead: &cr, PriceCacheWrite: &cw,
	})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "d")
	if m.mbView != 1 || m.mbFormMode != mbDuplicate {
		t.Fatalf("after 'd' mbView=%d mbFormMode=%d", m.mbView, m.mbFormMode)
	}
	// Change only the model id to a sibling (sonnet) — credentials are untouched.
	m.mbInputs[mbFieldModel].SetValue("claude-sonnet-4-5")
	m = drive(t, m, "enter")
	u := f.lastUpsert
	if u == nil {
		t.Fatal("duplicate UpsertModel not called")
	}
	if u.Name != "claude-copy" || u.Model != "claude-sonnet-4-5" {
		t.Fatalf("sibling name=%q model=%q, want claude-copy / claude-sonnet-4-5", u.Name, u.Model)
	}
	// Shared credential/endpoint carried over without re-entry.
	if u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" || u.Backend != "anthropic" {
		t.Fatalf("sibling did not inherit credentials: base=%q key=%q backend=%q", u.BaseUrl, u.KeyEnv, u.Backend)
	}
	// Pricing pointers carried over identically.
	if u.PriceInput == nil || *u.PriceInput != pi ||
		u.PriceOutput == nil || *u.PriceOutput != po ||
		u.PriceCacheRead == nil || *u.PriceCacheRead != cr ||
		u.PriceCacheWrite == nil || *u.PriceCacheWrite != cw {
		t.Fatalf("sibling pricing mismatch: in=%v out=%v cr=%v cw=%v",
			u.PriceInput, u.PriceOutput, u.PriceCacheRead, u.PriceCacheWrite)
	}
}

// TestModelBackendsAddConnectionSiblings exercises the connection-centric add
// flow (spec §13): the anthropic add form seeds the model field with curated ids,
// and submitting creates one sibling logical model per id, each named after its
// model id and sharing the connection's credentials.
func TestModelBackendsAddConnectionSiblings(t *testing.T) {
	f := newFakeClient()
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a") // add form, backend defaults to anthropic
	// The model field is prefilled with anthropic curated ids.
	if got := m.mbInputs[mbFieldModel].Value(); got == "" {
		t.Fatal("expected model field prefilled with curated ids")
	}
	// Set an explicit connection + a specific set of ids.
	m.mbInputs[mbFieldBaseURL].SetValue("https://api.anthropic.com")
	m.mbInputs[mbFieldKeyEnv].SetValue("ANTHROPIC_API_KEY")
	m.mbInputs[mbFieldModel].SetValue("claude-opus-4-8 claude-sonnet-4-5 claude-fable-5")
	m = drive(t, m, "enter")

	if len(f.upserts) != 3 {
		t.Fatalf("expected 3 sibling upserts, got %d", len(f.upserts))
	}
	want := map[string]bool{"claude-opus-4-8": true, "claude-sonnet-4-5": true, "claude-fable-5": true}
	for _, u := range f.upserts {
		if u.Name != u.Model {
			t.Errorf("sibling name=%q should equal model id=%q", u.Name, u.Model)
		}
		if !want[u.Model] {
			t.Errorf("unexpected model %q", u.Model)
		}
		if u.Backend != "anthropic" || u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" {
			t.Errorf("sibling %q did not share connection: backend=%q base=%q key=%q", u.Model, u.Backend, u.BaseUrl, u.KeyEnv)
		}
	}
}

// TestModelBackendsFetchModels exercises ctrl+f discovery: the fetched ids
// populate the model-id field so the whole connection's models become siblings.
func TestModelBackendsFetchModels(t *testing.T) {
	f := newFakeClient()
	f.discoverIDs = []string{"gpt-5.5", "gpt-4o", "o3"}
	f.discoverNote = "3 models from openai"
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a")
	// Move focus to the model field and fetch.
	m.mbFocus = mbFieldModel
	m = drive(t, m, "ctrl+f")
	if m.lastDiscoverBackend(f) == "" {
		t.Fatal("DiscoverModels was not called")
	}
	if got := m.mbInputs[mbFieldModel].Value(); got != "gpt-5.5 gpt-4o o3" {
		t.Fatalf("model field after fetch = %q, want the discovered ids", got)
	}
	if m.mbInfo != "3 models from openai" {
		t.Fatalf("mbInfo = %q, want the discovery note", m.mbInfo)
	}
}

// lastDiscoverBackend is a tiny helper for the fetch test.
func (m model) lastDiscoverBackend(f *fakeClient) string {
	if f.lastDiscover == nil {
		return ""
	}
	return f.lastDiscover.Backend
}

// TestModelBackendsEditMultiID covers editing a model and entering (or fetching)
// multiple ids: the edited model keeps its logical name for its own id, and any
// extra ids become new siblings on the same connection — instead of erroring.
func TestModelBackendsEditMultiID(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{
		Name: "claude", Backend: "anthropic",
		BaseUrl: "https://api.anthropic.com", Model: "claude-opus-4-8", KeyEnv: "ANTHROPIC_API_KEY",
	})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e")
	if m.mbFormMode != mbEdit {
		t.Fatalf("mbFormMode=%d, want mbEdit", m.mbFormMode)
	}
	// Simulate fetching/typing several ids (the original id plus two more).
	m.mbInputs[mbFieldModel].SetValue("claude-opus-4-8 claude-sonnet-4-5 claude-fable-5")
	m = drive(t, m, "enter")
	if m.mbErr != "" {
		t.Fatalf("unexpected error: %q", m.mbErr)
	}
	if len(f.upserts) != 3 {
		t.Fatalf("expected 3 upserts, got %d", len(f.upserts))
	}
	names := map[string]string{} // model id -> logical name
	for _, u := range f.upserts {
		names[u.Model] = u.Name
		if u.BaseUrl != "https://api.anthropic.com" || u.KeyEnv != "ANTHROPIC_API_KEY" {
			t.Errorf("sibling %q lost connection: base=%q key=%q", u.Model, u.BaseUrl, u.KeyEnv)
		}
	}
	// The edited model keeps its name for its own id; the extras are self-named.
	if names["claude-opus-4-8"] != "claude" {
		t.Errorf("edited model name = %q, want claude", names["claude-opus-4-8"])
	}
	if names["claude-sonnet-4-5"] != "claude-sonnet-4-5" || names["claude-fable-5"] != "claude-fable-5" {
		t.Errorf("extra siblings not self-named: %v", names)
	}
}

// TestModelBackendsModelPresets exercises the per-backend model-id presets
// (task 0042 nice-to-have): ctrl+n/ctrl+p cycle the suggestions on the model
// field while free-text entry is retained.
func TestModelBackendsModelPresets(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Blank add form: backend defaults to anthropic.
	m = drive(t, m, "a")
	// Focus the model field (name -> backend -> base_url -> model).
	m = drive(t, m, "tab")
	m = drive(t, m, "tab")
	m = drive(t, m, "tab")
	if m.mbFocus != mbFieldModel {
		t.Fatalf("focus=%d, want mbFieldModel(%d)", m.mbFocus, mbFieldModel)
	}
	anthropic := mbModelPresets["anthropic"]
	// ctrl+n selects the first preset.
	m = drive(t, m, "ctrl+n")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[0] {
		t.Fatalf("after ctrl+n model=%q, want %q", got, anthropic[0])
	}
	// ctrl+n again advances to the second.
	m = drive(t, m, "ctrl+n")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[1] {
		t.Fatalf("after 2x ctrl+n model=%q, want %q", got, anthropic[1])
	}
	// ctrl+p steps back to the first.
	m = drive(t, m, "ctrl+p")
	if got := m.mbInputs[mbFieldModel].Value(); got != anthropic[0] {
		t.Fatalf("after ctrl+p model=%q, want %q", got, anthropic[0])
	}
	// Free text is still accepted: clear and type a custom id.
	m.mbInputs[mbFieldModel].SetValue("")
	m = typeText(t, m, "my-custom-model")
	if got := m.mbInputs[mbFieldModel].Value(); got != "my-custom-model" {
		t.Fatalf("free text not retained: model=%q", got)
	}
}

func TestModelBackendsRemoveValidationError(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"},
		&v1.ModelConfig{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	)
	f.referenced["claude"] = true // a role still references it
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Remove the first (referenced) model.
	m.mbCursor = 0
	m = drive(t, m, "x")
	if m.mbView != 2 {
		t.Fatalf("after 'x' mbView=%d, want 2 (confirm)", m.mbView)
	}
	m = drive(t, m, "enter")
	if f.lastRemove != "claude" {
		t.Fatalf("RemoveModel got %q, want claude", f.lastRemove)
	}
	if m.mbErr == "" || !strings.Contains(m.mbErr, "referenced") {
		t.Fatalf("expected an inline validation error, got mbErr=%q", m.mbErr)
	}
	// The model is still present because removal was rejected.
	if len(m.models) != 2 {
		t.Fatalf("after rejected remove list has %d models, want 2", len(m.models))
	}

	// Removing an unreferenced model succeeds and refreshes the list.
	m.mbCursor = 1 // gpt
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "gpt" {
		t.Fatalf("RemoveModel got %q, want gpt", f.lastRemove)
	}
	if m.mbErr != "" {
		t.Fatalf("unexpected mbErr after successful remove: %q", m.mbErr)
	}
	if len(m.models) != 1 {
		t.Fatalf("after remove list has %d models, want 1", len(m.models))
	}
}

// Removing the last-listed model with the cursor on it must clamp mbCursor so a
// subsequent action does not index m.models out of range (task 0044 regression).
func TestModelBackendsRemoveLastClampsCursor(t *testing.T) {
	f := newFakeClient(
		&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"},
		&v1.ModelConfig{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	)
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	// Put the cursor on the last entry and remove it.
	m.mbCursor = len(m.models) - 1 // "gpt"
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "gpt" {
		t.Fatalf("RemoveModel got %q, want gpt", f.lastRemove)
	}
	if len(m.models) != 1 {
		t.Fatalf("after remove list has %d models, want 1", len(m.models))
	}
	// Cursor must have been clamped back into range.
	if m.mbCursor >= len(m.models) {
		t.Fatalf("mbCursor=%d out of range for %d models", m.mbCursor, len(m.models))
	}
	// A subsequent action on the (now last) cursor must not panic and must target
	// the remaining model.
	m = drive(t, m, "x")
	m = drive(t, m, "enter")
	if f.lastRemove != "claude" {
		t.Fatalf("second RemoveModel got %q, want claude", f.lastRemove)
	}
	if len(m.models) != 0 {
		t.Fatalf("after second remove list has %d models, want 0", len(m.models))
	}
	if m.mbCursor != 0 {
		t.Fatalf("mbCursor=%d for empty list, want 0", m.mbCursor)
	}
	// An edit/remove on an empty list must be a no-op (no panic).
	m = drive(t, m, "e")
	m = drive(t, m, "x")
}

func TestBacklogHidesDoneByDefault(t *testing.T) {
	tasks := []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
		{Id: "0002", Status: "in_progress", Title: "b"},
		{Id: "0003", Status: "done", Title: "c"},
		{Id: "0004", Status: "blocked", Title: "d"},
		{Id: "0005", Status: "done", Title: "e"},
	}
	m := model{backlog: true, backlogTasks: tasks}

	// Default: done tasks are hidden.
	vis := m.visibleBacklogTasks()
	if len(vis) != 3 {
		t.Fatalf("default visible=%d, want 3 (done hidden)", len(vis))
	}
	for _, tk := range vis {
		if tk.Status == "done" {
			t.Fatalf("done task %s visible by default", tk.Id)
		}
	}

	// Toggle with "d": done tasks become visible.
	updated, _ := m.updateBacklog(keyMsg("d"))
	m = updated.(model)
	if !m.backlogShowDone {
		t.Fatalf("backlogShowDone not set after toggle")
	}
	if len(m.visibleBacklogTasks()) != len(tasks) {
		t.Fatalf("after toggle visible=%d, want %d", len(m.visibleBacklogTasks()), len(tasks))
	}

	// Non-done tasks always present regardless of toggle.
	for _, showDone := range []bool{true, false} {
		m.backlogShowDone = showDone
		got := map[string]bool{}
		for _, tk := range m.visibleBacklogTasks() {
			got[tk.Id] = true
		}
		for _, id := range []string{"0001", "0002", "0004"} {
			if !got[id] {
				t.Fatalf("non-done task %s missing (showDone=%v)", id, showDone)
			}
		}
	}

	// Cursor stays in range when toggling show->hide while pointing at a done row.
	m.backlogShowDone = true
	m.backlogCursor = len(m.visibleBacklogTasks()) - 1 // last (done) row
	updated, _ = m.updateBacklog(keyMsg("d"))
	m = updated.(model)
	if m.backlogShowDone {
		t.Fatalf("expected toggle back to hide done")
	}
	if m.backlogCursor >= len(m.visibleBacklogTasks()) {
		t.Fatalf("cursor=%d out of range for %d visible", m.backlogCursor, len(m.visibleBacklogTasks()))
	}
}

// TestBlockedIndicator verifies the home-menu "waiting on you" indicator appears
// only when a backlog task is blocked, and that pressing "w" opens the backlog
// browser filtered to the blocked tasks (task 0101).
func TestBlockedIndicator(t *testing.T) {
	// No blocked tasks: indicator absent.
	m := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
		{Id: "0002", Status: "done", Title: "b"},
	}}
	m.prompt = newChatInput("test")
	if m.blockedTaskCount() != 0 {
		t.Fatalf("blockedTaskCount=%d, want 0", m.blockedTaskCount())
	}
	if strings.Contains(m.menuView(), "waiting on you") {
		t.Fatalf("menu shows blocked indicator when nothing is blocked:\n%s", m.menuView())
	}

	// Add blocked tasks: indicator present with a count.
	m.backlogTasks = append(m.backlogTasks,
		&v1.BacklogTaskSummary{Id: "0003", Status: "blocked", Title: "c"},
		&v1.BacklogTaskSummary{Id: "0004", Status: "blocked", Title: "d"},
	)
	if m.blockedTaskCount() != 2 {
		t.Fatalf("blockedTaskCount=%d, want 2", m.blockedTaskCount())
	}
	view := m.menuView()
	if !strings.Contains(view, "waiting on you") {
		t.Fatalf("menu missing blocked indicator:\n%s", view)
	}
	if !strings.Contains(view, "2 tasks blocked") {
		t.Fatalf("menu missing blocked count:\n%s", view)
	}

	// Press "w": opens the backlog browser filtered to blocked tasks.
	updated, _ := m.updateMenu(keyMsg("w"))
	m = updated.(model)
	if !m.backlog {
		t.Fatalf("after w, backlog browser not open")
	}
	if !m.backlogBlockedOnly {
		t.Fatalf("after w, backlogBlockedOnly not set")
	}
	vis := m.visibleBacklogTasks()
	if len(vis) != 2 {
		t.Fatalf("blocked-only visible=%d, want 2", len(vis))
	}
	for _, tk := range vis {
		if tk.Status != "blocked" {
			t.Fatalf("blocked-only view shows non-blocked task %s (%s)", tk.Id, tk.Status)
		}
	}

	// esc closes and clears the filter.
	updated, _ = m.updateBacklog(keyMsg("esc"))
	m = updated.(model)
	if m.backlog || m.backlogBlockedOnly {
		t.Fatalf("esc did not close/clear blocked filter: backlog=%v blockedOnly=%v", m.backlog, m.backlogBlockedOnly)
	}
}

// TestBlockedIndicatorWNoOpWhenNothingBlocked ensures "w" types into the prompt
// (not intercepted) when nothing is blocked (task 0101).
func TestBlockedIndicatorWNoOpWhenNothingBlocked(t *testing.T) {
	m := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
	}}
	m.prompt = newChatInput("test")
	m.prompt.Focus()
	updated, _ := m.updateMenu(keyMsg("w"))
	m = updated.(model)
	if m.backlog {
		t.Fatalf("w opened backlog browser when nothing is blocked")
	}
	if m.prompt.Value() != "w" {
		t.Fatalf("w not typed into prompt, got %q", m.prompt.Value())
	}
}

// TestPreviousSessionsReopen drives the menu -> session browser -> reopen flow
// (spec §18.6): ctrl+r opens the browser and loads the list, ↓ moves the cursor,
// and `o` reopens the selected session via ResumeSession, entering the session
// view. (Enter now drills into the transcript; see TestSessionBrowserTranscript.)
func TestPreviousSessionsReopen(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_newer", Mode: "work", Status: "idle", Title: "build the thing", LastActivity: "2024-01-02T10:00:00Z"},
		{SessionId: "s_older", Mode: "chat", Status: "stopped", Title: "ask questions", LastActivity: "2024-01-01T10:00:00Z"},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	if m.state != stateMenu {
		t.Fatalf("initial state=%v, want stateMenu", m.state)
	}

	// ctrl+r opens the session browser and loads history.
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("after ctrl+r state=%v, want stateHistory", m.state)
	}
	if len(m.history) != 2 {
		t.Fatalf("history len=%d, want 2", len(m.history))
	}

	// Navigate to the second row and reopen it with `o`.
	m = drive(t, m, "down")
	if m.historyCursor != 1 {
		t.Fatalf("historyCursor=%d, want 1", m.historyCursor)
	}
	m = drive(t, m, "o")

	if f.lastReopened != "s_older" {
		t.Fatalf("reopened %q, want s_older", f.lastReopened)
	}
	if m.sessionID != "s_older" {
		t.Fatalf("sessionID=%q, want s_older", m.sessionID)
	}
	if m.mode != "chat" {
		t.Fatalf("mode=%q, want chat", m.mode)
	}
	if m.state != stateSession {
		t.Fatalf("state=%v, want stateSession", m.state)
	}
}

// TestSessionBrowserTranscript drives the session browser transcript drill-in
// (spec §18.6): Enter on a row fetches the transcript via GetSessionTranscript
// and loads it into the event-rendering pipeline (read-only), Esc backs out to
// the list, and `o` from the transcript reopens the session.
func TestSessionBrowserTranscript(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s1", Mode: "work", Status: "idle", Title: "do the thing", LastActivity: "2024-01-02T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 1, Type: "user_input", Actor: "user", DataJson: `{"text":"go"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"on it"}`},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("state=%v, want stateHistory", m.state)
	}

	// Enter drills into the read-only transcript (no reopen).
	m = drive(t, m, "enter")
	if !m.historyTranscript {
		t.Fatal("enter should open the transcript drill-in")
	}
	if f.lastTransID != "s1" {
		t.Fatalf("GetSessionTranscript id=%q, want s1", f.lastTransID)
	}
	if f.lastReopened != "" {
		t.Fatalf("transcript drill-in must not reopen the session (lastReopened=%q)", f.lastReopened)
	}
	if len(m.evs) != 2 {
		t.Fatalf("transcript loaded %d events into the pipeline, want 2", len(m.evs))
	}
	// The transcript renders via the shared event components.
	view := m.transcriptView()
	if !strings.Contains(view, "transcript") {
		t.Fatalf("transcriptView missing title:\n%s", view)
	}

	// Esc backs out to the list and clears the transient transcript state.
	m = drive(t, m, "esc")
	if m.historyTranscript {
		t.Fatal("esc should leave the transcript drill-in")
	}
	if m.state != stateHistory {
		t.Fatalf("after esc state=%v, want stateHistory (back to list)", m.state)
	}
	if len(m.evs) != 0 {
		t.Fatalf("esc should clear transient transcript events, got %d", len(m.evs))
	}

	// Re-enter the transcript and reopen via `o`.
	m = drive(t, m, "enter")
	m = drive(t, m, "o")
	if f.lastReopened != "s1" {
		t.Fatalf("reopen from transcript: lastReopened=%q, want s1", f.lastReopened)
	}
	if m.state != stateSession || m.sessionID != "s1" {
		t.Fatalf("after reopen state=%v sessionID=%q, want stateSession/s1", m.state, m.sessionID)
	}
}

// TestBrowserNav exercises the shared list+detail component's cursor navigation:
// up/down move with clamping at both bounds and clamp() repairs an out-of-range
// cursor after the row set shrinks.
func TestBrowserNav(t *testing.T) {
	b := browser{rows: []browserRow{{text: "a"}, {text: "b"}, {text: "c"}}}
	b.up() // already at top: no-op
	if b.cursor != 0 {
		t.Fatalf("up at top: cursor=%d, want 0", b.cursor)
	}
	b.down()
	b.down()
	if b.cursor != 2 {
		t.Fatalf("two downs: cursor=%d, want 2", b.cursor)
	}
	b.down() // at bottom: clamped
	if b.cursor != 2 {
		t.Fatalf("down at bottom: cursor=%d, want 2", b.cursor)
	}
	// Shrink the row set out from under the cursor; clamp repairs it.
	b.rows = b.rows[:1]
	b.clamp()
	if b.cursor != 0 {
		t.Fatalf("clamp after shrink: cursor=%d, want 0", b.cursor)
	}
	// Empty list clamps to 0 (never negative).
	b.rows = nil
	b.clamp()
	if b.cursor != 0 {
		t.Fatalf("clamp on empty: cursor=%d, want 0", b.cursor)
	}
}

// TestBrowseMenuRoutes verifies the browse selector (ctrl+o) routes to the
// backlog and session browsers (spec §18.6/§20.5), and esc dismisses it.
func TestBrowseMenuRoutes(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	// ctrl+o opens the browse selector.
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector")
	}
	// First entry routes to the backlog browser.
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.backlog {
		t.Fatalf("first browse entry should open the backlog browser")
	}

	// Re-open and route to the plan library browser (second entry).
	m.backlog = false
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.plans {
		t.Fatalf("second browse entry should open the plan library browser")
	}

	// Re-open and route to the sessions browser (third entry).
	m.plans = false
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if m.state != stateHistory {
		t.Fatalf("third browse entry should open the session browser (state=%v)", m.state)
	}

	// Esc dismisses the selector without routing.
	m.state = stateMenu
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "esc")
	if m.browse {
		t.Fatal("esc should dismiss the browse selector")
	}
	if m.state != stateMenu || m.backlog {
		t.Fatalf("esc must not route anywhere (state=%v backlog=%v)", m.state, m.backlog)
	}
}

// TestPlansBrowser verifies the plan library browser (task 0077): the browse
// selector → plans route lists saved plans, and enter drills into a plan's
// markdown detail; esc/← backs out to the list and esc closes the browser.
func TestPlansBrowser(t *testing.T) {
	f := newFakeClient()
	f.plans = []*v1.PlanSummary{
		{Name: "build-and-test", Title: "Build and test"},
		{Name: "release", Title: "Cut a release"},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	// Open the browse selector and route to plans (second entry).
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if !m.plans {
		t.Fatal("plans route should open the plan library browser")
	}
	if len(m.plansList) != 2 {
		t.Fatalf("plansList = %d, want 2", len(m.plansList))
	}
	if got := m.plansView(); !strings.Contains(got, "build-and-test") || !strings.Contains(got, "Cut a release") {
		t.Fatalf("plansView missing entries: %q", got)
	}

	// Enter drills into the first plan's detail.
	m = drive(t, m, "enter")
	if m.planDetail == nil || m.planDetail.Name != "build-and-test" {
		t.Fatalf("enter should load plan detail, got %+v", m.planDetail)
	}
	if got := m.plansView(); !strings.Contains(got, "build-and-test") {
		t.Fatalf("planDetailView missing plan name: %q", got)
	}

	// Esc backs out to the list, then esc closes the browser.
	m = drive(t, m, "esc")
	if m.planDetail != nil {
		t.Fatal("esc should clear plan detail")
	}
	m = drive(t, m, "esc")
	if m.plans {
		t.Fatal("esc should close the plan library browser")
	}
}

// newCostFakeClient returns a fakeClient with canned usage rows for the cost view
// tests: one priced row, one unpriced row, a total, and a workspace name.
func newCostFakeClient() *fakeClient {
	f := newFakeClient()
	f.usageWksp = "demo-workspace"
	f.usageRows = []*v1.UsageRow{
		{Task: "0001", Model: "sonnet", Input: 1000, Output: 200, CacheRead: 50, CacheWrite: 10, Total: 1260, Cost: 0.1234, PriceStatus: "priced"},
		{Task: "", Model: "local", Input: 500, Output: 100, Total: 600, PriceStatus: "unpriced"},
	}
	f.usageTotal = &v1.UsageRow{Input: 1500, Output: 300, CacheRead: 50, CacheWrite: 10, Total: 1860, Cost: 0.1234, PriceStatus: "partial"}
	return f
}

// TestCostViewRoute verifies the browse selector (ctrl+o) routes to the cost view
// and that driving the fetch populates the rows (spec §20.5, task 0039).
func TestCostViewRoute(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)

	m = drive(t, m, "ctrl+o")
	// Cost is the fourth browse entry.
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")
	if m.browse {
		t.Fatal("enter should dismiss the browse selector")
	}
	if !m.cost {
		t.Fatal("fourth browse entry should open the cost view")
	}
	if len(m.costRows) != 2 {
		t.Fatalf("cost rows not populated: got %d, want 2", len(m.costRows))
	}
	if got := m.costGroupBy; len(got) != 1 || got[0] != "task" {
		t.Fatalf("default group-by = %v, want [task]", got)
	}
}

// TestCostView exercises navigation, the group-by cycle, esc, and rendering.
func TestCostView(t *testing.T) {
	f := newCostFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter")

	// "g" cycles task -> model and re-fetches with the new dimension.
	m = drive(t, m, "g")
	if got := m.costGroupBy; len(got) != 1 || got[0] != "model" {
		t.Fatalf("after g, group-by = %v, want [model]", got)
	}
	if got := f.lastGroupBy; len(got) != 1 || got[0] != "model" {
		t.Fatalf("g should re-fetch with [model], lastGroupBy = %v", got)
	}

	// Down then up should clamp within bounds (2 rows -> max cursor 1).
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	if m.costCursor != 1 {
		t.Fatalf("cursor should clamp at 1, got %d", m.costCursor)
	}
	m = drive(t, m, "up")
	m = drive(t, m, "up")
	if m.costCursor != 0 {
		t.Fatalf("cursor should clamp at 0, got %d", m.costCursor)
	}

	// Render: priced cell, unpriced marker, and TOTAL line present.
	out := m.costView()
	if !strings.Contains(out, "$0.1234") {
		t.Errorf("costView should show a priced $ cell:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("costView should show the unpriced — marker:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("costView should show a TOTAL line:\n%s", out)
	}

	// Esc closes the cost view.
	m = drive(t, m, "esc")
	if m.cost {
		t.Fatal("esc should close the cost view")
	}
}

// TestPreviousSessionsEscReturnsToMenu verifies Esc on the history screen returns
// to the menu rather than opening the settings overlay.
func TestPreviousSessionsEscReturnsToMenu(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("state=%v, want stateHistory", m.state)
	}
	m = drive(t, m, "esc")
	if m.state != stateMenu {
		t.Fatalf("after esc state=%v, want stateMenu", m.state)
	}
	if m.overlay {
		t.Fatalf("esc on history opened the settings overlay")
	}
}

// TestWizardFreeTextAfterPickerFocusesInput guards the mixed-batch focus
// regression: when a picker question precedes a free-text question in one
// multi-question ask_user batch, advancing past the picker (which blurs the
// textarea) must re-focus the textarea so the next free-text answer is typable.
func TestWizardFreeTextAfterPickerFocusesInput(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Drive a multi-question batch: question 1 is a picker, question 2 is free text.
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive {
		t.Fatal("wizard should be active after a batch question_asked")
	}
	if !m.picking || m.wizIdx != 0 {
		t.Fatalf("first question should be a picker at idx 0 (picking=%v idx=%d)", m.picking, m.wizIdx)
	}

	// Answer the first (picker) question by selecting the highlighted option.
	// Update is called directly (not via drive) because advancing to the free-text
	// question returns the textarea's cursor-blink cmd, which would block if run
	// synchronously (see typeText).
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)

	if m.wizIdx != 1 {
		t.Fatalf("after answering Q1, wizIdx=%d, want 1", m.wizIdx)
	}
	if m.picking {
		t.Fatal("Q2 is free text; picking should be false")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused for the free-text question following a picker")
	}
}

// While the wizard is collecting answers for a multi-question batch, the inline
// event-log body for that question_asked event must NOT dump every question
// (which competed with / obscured the one-at-a-time wizard). It should show a
// concise summary pointing down at the wizard. Once the wizard is dismissed, the
// same event reverts to its full enumerated form.
func TestWizardCondensesInlineQuestionDump(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	ev := &v1.Event{
		Seq: 7, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"which database?","options":["postgres","sqlite"]},{"question":"service name?"}]}`,
	}
	m.appendEvent(ev)
	if !m.wizActive {
		t.Fatal("wizard should be active after a batch question_asked")
	}

	// Active: condensed summary, no inline enumeration of the second question.
	body := m.bodyFor(ev)
	if !strings.Contains(body, "questions — answer below") {
		t.Fatalf("active wizard body should show condensed summary, got:\n%s", body)
	}
	if strings.Contains(body, "service name?") {
		t.Fatalf("active wizard body should not enumerate questions inline, got:\n%s", body)
	}

	// Dismiss the wizard; the same event should re-render its full enumerated form.
	m.clearWizard()
	m.bodyCache = map[int]string{}
	body = stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "which database?") || !strings.Contains(body, "service name?") {
		t.Fatalf("after clearWizard the body should enumerate all questions, got:\n%s", body)
	}
}

// The wizard surfaces the active question's options via the picker and an
// obvious free-text escape, and the footer help spells out the interaction.
func TestWizardPickerAndFooterAffordances(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	m.appendEvent(&v1.Event{
		Seq: 3, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive || !m.picking {
		t.Fatalf("expected an active picker wizard (active=%v picking=%v)", m.wizActive, m.picking)
	}

	picker := m.pickerView()
	if !strings.Contains(picker, "postgres") || !strings.Contains(picker, "sqlite") {
		t.Fatalf("picker should list the active question's options, got:\n%s", picker)
	}
	if !strings.Contains(picker, "other… (type your own)") {
		t.Fatalf("picker should offer an obvious free-text escape, got:\n%s", picker)
	}

	view := m.sessionView()
	if !strings.Contains(view, "choose") || !strings.Contains(view, "other…") {
		t.Fatalf("wizard footer should explain choosing + free-text, got:\n%s", view)
	}
}

// newPickerModel builds a session model with a single pending options question so
// the picker footer (m.picking) is active. Returned ready to feed keys/mouse.
func newPickerModel(t *testing.T, f *fakeClient) model {
	t.Helper()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"db?","options":["postgres","sqlite","mysql"]}`,
	})
	if !m.picking || m.wizActive {
		t.Fatalf("expected a single-question picker (picking=%v wizActive=%v)", m.picking, m.wizActive)
	}
	return m
}

// A number key selects the corresponding option directly (spec §18.3): the
// pending question clears and an answer command is issued without touching the
// highlighted cursor first.
func TestPickerNumberKeySelectsOption(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)

	// Press "2" → selects the 2nd option (index 1). The answer cmd is not run
	// here (the fake has no AnswerQuestion), only the state transition is checked.
	updated, cmd := m.Update(keyMsg("2"))
	m = updated.(model)
	if m.picking {
		t.Fatal("number key should have dismissed the picker")
	}
	if m.pending != "" || m.pickerOpts != nil {
		t.Fatalf("selecting an option should clear pending/pickerOpts, got pending=%q opts=%v", m.pending, m.pickerOpts)
	}
	if cmd == nil {
		t.Fatal("selecting an option should return an answer command")
	}
}

// In a multi-question wizard a number key selects the active question's option
// and advances to the next question.
func TestWizardNumberKeyAdvances(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?","options":["a","b"]}]}`,
	})
	if !m.wizActive || !m.picking || m.wizIdx != 0 {
		t.Fatalf("expected wizard picker at idx 0 (active=%v picking=%v idx=%d)", m.wizActive, m.picking, m.wizIdx)
	}

	updated, _ = m.Update(keyMsg("1"))
	m = updated.(model)
	if m.wizIdx != 1 {
		t.Fatalf("number key should advance the wizard: wizIdx=%d, want 1", m.wizIdx)
	}
	if got := m.wizAnswers[0]; !got.done || got.idx != 0 {
		t.Fatalf("Q1 answer should record option 0: %+v", got)
	}
}

// A digit past the number of options is ignored: the picker stays open and the
// cursor doesn't move.
func TestPickerNumberBeyondOptionsNoop(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f) // 3 options
	updated, cmd := m.Update(keyMsg("7"))
	m = updated.(model)
	if !m.picking {
		t.Fatal("a digit beyond the option count must not dismiss the picker")
	}
	if m.pickerCursor != 0 {
		t.Fatalf("stray digit moved the cursor to %d", m.pickerCursor)
	}
	if cmd != nil {
		t.Fatal("stray digit should not return a command")
	}
}

// pgup/pgdown scroll the transcript while a picker is active instead of being
// swallowed, so the question's context can be re-read.
func TestPickerScrollsTranscript(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	// Overflow the viewport so there is something to scroll.
	for i := 2; i < 60; i++ {
		m.appendEvent(&v1.Event{Seq: int64(i), Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"line"}`})
	}
	m.rebuild()
	m.vp.GotoBottom()
	m.follow = m.vp.AtBottom()
	if !m.follow {
		t.Fatal("setup: viewport should start at bottom (following)")
	}

	updated, _ := m.Update(keyMsg("pgup"))
	m = updated.(model)
	if m.follow {
		t.Fatal("pgup while picking should scroll up (follow=false), not be swallowed")
	}
	if !m.picking {
		t.Fatal("scrolling must not dismiss the picker")
	}
}

// A mouse wheel scroll reaches the viewport even while the picker is active.
func TestPickerMouseWheelScrolls(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	for i := 2; i < 60; i++ {
		m.appendEvent(&v1.Event{Seq: int64(i), Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"line"}`})
	}
	m.rebuild()
	m.vp.GotoBottom()
	m.follow = m.vp.AtBottom()

	before := m.vp.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = updated.(model)
	if m.vp.YOffset() >= before {
		t.Fatalf("mouse wheel should scroll the viewport up: before=%d after=%d", before, m.vp.YOffset())
	}
	if !m.picking {
		t.Fatal("mouse wheel must not dismiss the picker")
	}
}

// ctrl+b opens the backlog browser while a question is pending, and the picker
// state survives so sessionView restores it when the browser closes.
func TestPickerCtrlBOpensBacklog(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	updated, cmd := m.Update(keyMsg("ctrl+b"))
	m = updated.(model)
	if !m.backlog {
		t.Fatal("ctrl+b while picking should open the backlog browser")
	}
	if !m.picking {
		t.Fatal("opening the backlog browser must not drop the pending picker")
	}
	if cmd == nil {
		t.Fatal("ctrl+b should return the fetchBacklog command")
	}
}

// ctrl+n opens the quick-capture overlay while a question is pending; the picker
// state survives underneath.
func TestPickerCtrlNOpensCapture(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	updated, _ := m.Update(keyMsg("ctrl+n"))
	m = updated.(model)
	if !m.capture {
		t.Fatal("ctrl+n while picking should open the capture overlay")
	}
	if !m.picking {
		t.Fatal("opening capture must not drop the pending picker")
	}
}

// The picker footer advertises number selection (spec §18.3).
func TestPickerFooterMentionsNumbers(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if view := m.sessionView(); !strings.Contains(view, "1–9") {
		t.Fatalf("picker footer should advertise number selection, got:\n%s", view)
	}
}

// The quick-add capture overlay (task 0049) streams the capture agent's action
// log live: each captureEvMsg appends to captureLog and is rendered in
// captureView, and a terminal capture_result event drives the overlay to its
// created/answered/error state.
func TestCaptureStreamsActionLog(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 0, captureBusy: true}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureEvents = make(chan *v1.Event, 8)

	// Feed two action-log events; each should append and rearm the waiter.
	turn := &v1.Event{Seq: 1, Actor: "capture", Type: "model_turn", DataJson: `{"text":"drafting the task"}`}
	tc := &v1.Event{Seq: 2, Actor: "capture", Type: "tool_call", DataJson: `{"name":"create_task","args":"{\"title\":\"x\"}"}`}

	nm, _ := m.Update(captureEvMsg{turn})
	m = nm.(model)
	nm, _ = m.Update(captureEvMsg{tc})
	m = nm.(model)

	if len(m.captureLog) != 2 {
		t.Fatalf("captureLog len = %d, want 2", len(m.captureLog))
	}
	if !m.captureBusy {
		t.Fatal("expected captureBusy to remain true while streaming")
	}
	view := m.captureView()
	if !strings.Contains(view, "drafting the task") {
		t.Fatalf("captureView missing model_turn detail:\n%s", view)
	}
	if !strings.Contains(view, "create_task") {
		t.Fatalf("captureView missing tool_call detail:\n%s", view)
	}

	// Terminal capture_result with a created task: stage 2, not busy, msg set.
	done := &v1.Event{Actor: "capture", Type: "capture_result", DataJson: `{"task_id":"0050","title":"Add x","question":""}`}
	nm, _ = m.Update(captureEvMsg{done})
	m = nm.(model)
	if m.captureBusy {
		t.Fatal("expected captureBusy=false after capture_result")
	}
	if m.captureStage != 2 {
		t.Fatalf("captureStage = %d, want 2", m.captureStage)
	}
	if !strings.Contains(m.captureMsg, "0050") {
		t.Fatalf("captureMsg = %q, want it to mention the created id", m.captureMsg)
	}
}

// TestCaptureEchoesUserMessage verifies that submitting a message in the
// quick-add capture overlay appends the user's own text to the transcript log
// (so the conversation history stays visible) and that captureView shows it.
func TestCaptureEchoesUserMessage(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 0, state: stateMenu}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureInput.Focus()
	m.captureInput.SetValue("add a dark mode toggle")

	nm, _ := m.Update(keyMsg("enter"))
	m = nm.(model)

	if len(m.captureLog) == 0 {
		t.Fatalf("captureLog empty, want a user_input event")
	}
	last := m.captureLog[len(m.captureLog)-1]
	if last.Actor != "you" || last.Type != "user_input" {
		t.Fatalf("last event actor/type = %q/%q, want you/user_input", last.Actor, last.Type)
	}
	if dataField(last, "text") != "add a dark mode toggle" {
		t.Fatalf("echoed text = %q, want %q", dataField(last, "text"), "add a dark mode toggle")
	}
	view := m.captureView()
	if !strings.Contains(view, "add a dark mode toggle") {
		t.Fatalf("captureView missing echoed user message:\n%s", view)
	}
}

// TestCaptureLogWraps verifies that long capture-log lines wrap to the modal
// inner width instead of being truncated with an ellipsis or overflowing.
func TestCaptureLogWraps(t *testing.T) {
	m := model{w: 40, h: 24, capture: true, captureStage: 0}
	m.captureInput = newChatInput("describe a new backlog item…")
	m.captureEvents = make(chan *v1.Event, 8)

	long := strings.Repeat("wrapme ", 30)
	ev := &v1.Event{Seq: 1, Actor: "you", Type: "user_input", DataJson: `{"text":"` + strings.TrimSpace(long) + `"}`}
	nm, _ := m.Update(captureEvMsg{ev})
	m = nm.(model)

	view := m.captureView()
	for _, ln := range strings.Split(view, "\n") {
		if lipgloss.Width(ln) > m.w {
			t.Fatalf("rendered line width %d exceeds terminal width %d: %q", lipgloss.Width(ln), m.w, ln)
		}
	}
	// The full text should be present (wrapped across lines), not truncated:
	// every "wrapme" token survives.
	joined := strings.ReplaceAll(stripANSI(view), "\n", " ")
	if got := strings.Count(joined, "wrapme"); got != 30 {
		t.Fatalf("found %d wrapme tokens in wrapped log, want 30 (truncated?):\n%s", got, view)
	}
}

// TestCaptureQuestionUsesSharedBadge verifies the stage-1 clarifying question
// reuses the shared interactive-question UI badge (askStyle " ? ") that the
// main agents use, rather than a bespoke header.
func TestCaptureQuestionUsesSharedBadge(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 1, captureQuestion: "Which platform?"}
	m.captureInput = newChatInput("describe a new backlog item…")

	view := m.captureView()
	if !strings.Contains(view, askStyle.Render(" ? ")) {
		t.Fatalf("captureView missing shared question badge:\n%s", view)
	}
	if strings.Contains(view, "The capture agent asks:") {
		t.Fatalf("captureView still uses bespoke clarification header:\n%s", view)
	}
	if !strings.Contains(view, "Which platform?") {
		t.Fatalf("captureView missing the question text:\n%s", view)
	}
}

// TestEventExpandedDefaults verifies default expansion logic with and without
// the auto-expand-agent-logs preference, and that manual per-row overrides win
// in both directions.
func TestEventExpandedDefaults(t *testing.T) {
	m := &model{expanded: map[int]bool{}}

	// Auto-expand off: normal events collapsed, auto-expand types expanded.
	m.prefs.AutoExpandLogs = false
	if m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected normal event collapsed by default with auto-expand off")
	}
	if !m.eventExpanded(2, "session_idle") {
		t.Fatalf("expected session_idle auto-expanded regardless of preference")
	}

	// Auto-expand on: normal events expanded by default.
	m.prefs.AutoExpandLogs = true
	if !m.eventExpanded(3, "tool_call") {
		t.Fatalf("expected normal event expanded by default with auto-expand on")
	}

	// Manual override beats default: collapse with auto-expand on.
	m.expanded[3] = false
	if m.eventExpanded(3, "tool_call") {
		t.Fatalf("expected manual collapse override to win over auto-expand on")
	}

	// Manual override beats default: expand with auto-expand off.
	m.prefs.AutoExpandLogs = false
	m.expanded[4] = true
	if !m.eventExpanded(4, "tool_call") {
		t.Fatalf("expected manual expand override to win over auto-expand off")
	}
}

// TestToggleWithAutoExpand verifies that toggling a row whose effective state is
// expanded-by-default (auto-expand on) records an explicit collapse override,
// and toggling again re-expands it.
func TestToggleWithAutoExpand(t *testing.T) {
	m := &model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}
	m.prefs.AutoExpandLogs = true
	m.evs = []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read","args":"{}"}`},
	}
	m.rebuild()

	if !m.eventExpanded(1, "tool_call") {
		t.Fatalf("precondition: event should be expanded by default with auto-expand on")
	}
	m.toggle(0)
	if m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected toggle to collapse an auto-expanded row")
	}
	m.toggle(0)
	if !m.eventExpanded(1, "tool_call") {
		t.Fatalf("expected second toggle to re-expand the row")
	}
}

// --- live status bar (task 0062) ---

// turnEvent builds a model_turn proto Event carrying a usage block for model name.
func turnEvent(seq int, name string, u event.Usage) *v1.Event {
	return &v1.Event{
		Seq: int64(seq), Type: "model_turn", Actor: "coordinator",
		DataJson: fmt.Sprintf(
			`{"model_name":%q,"usage":{"input":%d,"output":%d,"cache_read":%d,"cache_write":%d,"total":%d}}`,
			name, u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Total),
	}
}

// TestSessionUsageSummation checks that model_turn usage blocks accumulate per
// model and price correctly: a priced + unpriced mix is "partial" (cost from the
// priced model only), an all-priced mix is "priced", and an all-unpriced mix is
// "unpriced" with no invented cost.
func TestSessionUsageSummation(t *testing.T) {
	newModel := func(pricing map[string]config.Pricing) *model {
		return &model{
			expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
			usageByModel: map[string]event.Usage{}, pricing: pricing,
		}
	}

	// 1) partial: "claude" priced, "local" unpriced.
	priced := config.Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, Configured: true}
	m := newModel(map[string]config.Pricing{"claude": priced})
	m.appendEvent(turnEvent(1, "claude", event.Usage{Input: 1000, Output: 500, Total: 1500}))
	m.appendEvent(turnEvent(2, "claude", event.Usage{Input: 2000, Output: 1000, Total: 3000}))
	m.appendEvent(turnEvent(3, "local", event.Usage{Input: 4000, Output: 0, Total: 4000}))
	tokens, cost, status := m.sessionUsage()
	if tokens != 1500+3000+4000 {
		t.Fatalf("tokens = %d, want %d", tokens, 1500+3000+4000)
	}
	// claude: input 3000 * $3/Mtok + output 1500 * $15/Mtok = 0.009 + 0.0225 = 0.0315
	wantCost := (3000*3 + 1500*15) / 1e6
	if d := cost - wantCost; d < -1e-9 || d > 1e-9 {
		t.Fatalf("cost = %v, want %v", cost, wantCost)
	}
	if status != "partial" {
		t.Fatalf("status = %q, want partial", status)
	}

	// 2) fully priced.
	m = newModel(map[string]config.Pricing{"claude": priced})
	m.appendEvent(turnEvent(1, "claude", event.Usage{Input: 1000, Output: 1000, Total: 2000}))
	tokens, cost, status = m.sessionUsage()
	if tokens != 2000 || status != "priced" {
		t.Fatalf("priced case: tokens=%d status=%q", tokens, status)
	}
	if want := (1000*3 + 1000*15) / 1e6; cost != want {
		t.Fatalf("priced cost = %v, want %v", cost, want)
	}

	// 3) fully unpriced: tokens surface but cost stays 0 and status unpriced.
	m = newModel(map[string]config.Pricing{})
	m.appendEvent(turnEvent(1, "local", event.Usage{Input: 500, Output: 500, Total: 1000}))
	tokens, cost, status = m.sessionUsage()
	if tokens != 1000 || cost != 0 || status != "unpriced" {
		t.Fatalf("unpriced case: tokens=%d cost=%v status=%q", tokens, cost, status)
	}

	// 4) empty: no usage at all.
	m = newModel(map[string]config.Pricing{})
	if tk, c, st := m.sessionUsage(); tk != 0 || c != 0 || st != "unpriced" {
		t.Fatalf("empty case: %d %v %q", tk, c, st)
	}
}

// TestStatusBarSegments renders the status bar with a fully-populated session and
// asserts the distinct segments (mode, level, thinking, token readout) appear, and
// that the bar stays exactly one physical row at a narrow width (no wrap).
func TestStatusBarSegments(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "implement", level: "judgement",
		sessionID: "sess12345678", w: 120,
		thinkLevels:  map[string]string{"coordinator": "high"},
		usageByModel: map[string]event.Usage{"claude": {Input: 12000, Output: 6000, Total: 18000}},
		pricing:      map[string]config.Pricing{"claude": {Input: 3, Output: 15, Configured: true}},
	}
	bar := m.statusBar()
	for _, want := range []string{"implement", "judgement", "high", "18.0k", "$"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("status bar missing %q:\n%s", want, bar)
		}
	}

	// Single physical row at a narrow width: no newline and width within bound.
	m.w = 40
	bar = m.statusBar()
	if strings.Contains(bar, "\n") {
		t.Fatalf("status bar wrapped to multiple rows: %q", bar)
	}
	if w := lipgloss.Width(bar); w > 40 {
		t.Fatalf("status bar width %d exceeds 40: %q", w, bar)
	}

	// Unpriced session: tokens shown, never a bogus cost.
	m.w = 120
	m.pricing = map[string]config.Pricing{}
	bar = m.statusBar()
	if strings.Contains(bar, "$") {
		t.Fatalf("unpriced bar must not show a cost: %s", bar)
	}
	if !strings.Contains(bar, "18.0k") {
		t.Fatalf("unpriced bar should still show tokens: %s", bar)
	}
}

// TestRenderBodySessionErrorWraps verifies a long single-line session_error
// message (e.g. a backend 400 invalid_request_error with a JSON body) is wrapped
// to the body width instead of running off the right edge. Regression for the
// truncated/unwrapped error display.
func TestRenderBodySessionErrorWraps(t *testing.T) {
	long := "invalid_request_error: " + strings.Repeat("abcdefghij0123456789", 12) + " end"
	ev := &v1.Event{Seq: 1, Type: "session_error", DataJson: `{"msg":` + jsonQuote(long) + `}`}

	m := &model{w: 40}
	body := m.renderBody(ev)
	if body == "" {
		t.Fatal("renderBody returned empty for session_error")
	}
	for _, line := range strings.Split(body, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Fatalf("error line width %d exceeds terminal width 40: %q", w, line)
		}
	}
	// The full message must survive wrapping (no content dropped): stripping the
	// indent/bar prefix and joining should recover the original characters.
	if !strings.Contains(stripANSI(body), "end") {
		t.Fatalf("wrapped error dropped trailing content: %q", body)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestDropMouseFragment verifies the defensive filter that swallows leaked
// SGR mouse-report bytes (a hardening kept across the bubbletea v2 upgrade)
// without eating real typing. See model.dropMouseFragment.
func TestDropMouseFragment(t *testing.T) {
	runes := func(s string) tea.KeyMsg {
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	altBracket := tea.KeyPressMsg{Code: '[', Text: "[", Mod: tea.ModAlt}

	// With recent mouse activity, mouse-report fragments are dropped.
	recent := model{lastMouse: time.Now()}
	for _, k := range []tea.KeyMsg{runes("<65;80;12M"), runes("35;86;14"), runes("65;80"), altBracket} {
		if !recent.dropMouseFragment(k) {
			t.Errorf("expected fragment %q to be dropped", k.String())
		}
	}
	// Real typing is never dropped, even right after scrolling.
	for _, k := range []tea.KeyMsg{runes("hello"), runes("a"), runes("<3"), runes("5"), runes(";")} {
		if recent.dropMouseFragment(k) {
			t.Errorf("expected real input %q to be kept", k.String())
		}
	}
	// Without recent mouse activity, nothing is dropped.
	stale := model{lastMouse: time.Now().Add(-time.Second)}
	if stale.dropMouseFragment(runes("<65;80;12M")) {
		t.Error("expected no drop when no recent mouse activity")
	}
}

func TestUnifiedDiff(t *testing.T) {
	oldStr := "line one\n\tindented\nmiddle old\nfour\nfive"
	newStr := "line one\n\tindented\nmiddle new\nfour\nfive"
	out := unifiedDiff(oldStr, newStr, 3)
	if !strings.Contains(out, "@@") {
		t.Fatalf("expected hunk header, got:\n%s", out)
	}
	if !strings.Contains(out, "-middle old") {
		t.Errorf("expected removed line, got:\n%s", out)
	}
	if !strings.Contains(out, "+middle new") {
		t.Errorf("expected added line, got:\n%s", out)
	}
	if !strings.Contains(out, " line one") {
		t.Errorf("expected context line prefixed with space, got:\n%s", out)
	}
	// Indentation preserved after the prefix.
	if !strings.Contains(out, " \tindented") {
		t.Errorf("expected indentation preserved, got:\n%q", out)
	}
}

func TestUnifiedDiffTruncation(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&oldB, "old %d\n", i)
		fmt.Fprintf(&newB, "new %d\n", i)
	}
	out := unifiedDiff(oldB.String(), newB.String(), 3)
	if !strings.Contains(out, "diff truncated") {
		t.Errorf("expected truncation marker for very large input")
	}
	if n := strings.Count(out, "\n"); n > 420 {
		t.Errorf("expected output bounded, got %d lines", n)
	}
}

func TestEditCardParamsDiff(t *testing.T) {
	m := &model{w: 100}
	args := `{"file_path":"x.go","old_string":"foo\nbar\nbaz","new_string":"foo\nqux\nbaz"}`
	call := &v1.Event{Seq: 1, Type: "tool_call", Actor: "coordinator",
		DataJson: `{"id":"c1","name":"Edit","args":` + mustJSONString(args) + `}`}
	out := stripANSI(m.cardParams(call))
	if !strings.Contains(out, "-bar") || !strings.Contains(out, "+qux") {
		t.Errorf("expected diff lines, got:\n%s", out)
	}
	if !strings.Contains(out, "@@") {
		t.Errorf("expected hunk header, got:\n%s", out)
	}
	if strings.Contains(out, "old_string:") || strings.Contains(out, "new_string:") {
		t.Errorf("expected raw key labels to be suppressed, got:\n%s", out)
	}
	if !strings.Contains(out, "x.go") {
		t.Errorf("expected file_path shown, got:\n%s", out)
	}
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestTopReadyTask(t *testing.T) {
	tasks := []*v1.BacklogTaskSummary{
		{Id: "0003", Status: "todo", Priority: 1, Ready: false}, // blocked by deps
		{Id: "0004", Status: "done", Priority: 1, Ready: true},  // already done
		{Id: "0005", Status: "todo", Priority: 2, Ready: true},  // ready, p2
		{Id: "0002", Status: "todo", Priority: 1, Ready: true},  // ready, p1 -> winner
		{Id: "0006", Status: "in_review", Priority: 1, Ready: true},
		{Id: "0007", Status: "blocked", Priority: 1, Ready: true},
	}
	if got := topReadyTask(tasks); got != "0002" {
		t.Fatalf("expected highest-priority ready todo 0002, got %q", got)
	}
	// In-progress is resumable and ready; with no todo it should be picked.
	resume := []*v1.BacklogTaskSummary{
		{Id: "0009", Status: "in_progress", Priority: 3, Ready: true},
		{Id: "0008", Status: "in_review", Priority: 1, Ready: true},
		{Id: "0010", Status: "blocked", Priority: 1, Ready: true},
	}
	if got := topReadyTask(resume); got != "0009" {
		t.Fatalf("expected resumable in_progress 0009, got %q", got)
	}
	// Nothing actionable -> empty.
	none := []*v1.BacklogTaskSummary{
		{Id: "0011", Status: "done", Priority: 1, Ready: true},
		{Id: "0012", Status: "blocked", Priority: 1, Ready: true},
		{Id: "0013", Status: "todo", Priority: 1, Ready: false},
	}
	if got := topReadyTask(none); got != "" {
		t.Fatalf("expected no ready task, got %q", got)
	}
}

func TestLoopDecisionStopsOnNoProgress(t *testing.T) {
	// A session ran (loopStarted) but the backlog fingerprint is unchanged from
	// before it: nothing advanced, so the loop must stop instead of spinning. This
	// is fingerprint-based, NOT a guess at which task ran, so a session that worked
	// a different task than the driver might have predicted is NOT a false stall —
	// any status change yields a different fingerprint and keeps the loop going.
	m := &model{looping: true, loopStarted: true, loopPrevFP: "0001:done,0002:todo"}
	next, _ := m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done,0002:todo"})
	mm := next.(model)
	if mm.looping {
		t.Fatalf("expected loop to stop on no-progress, still looping")
	}
	if mm.state != stateMenu {
		t.Fatalf("expected return to menu, got state %v", mm.state)
	}
}

// A finished session that changed the backlog (even if it worked a different
// ready task than the driver would have guessed) must NOT be treated as a stall:
// the fingerprint differs, so the loop continues to the next task.
func TestLoopDecisionContinuesWhenBacklogChanged(t *testing.T) {
	fc := newFakeClient()
	m := &model{looping: true, loopStarted: true, loopPrevFP: "0001:todo,0002:todo", client: fc, ctx: context.Background()}
	next, cmd := m.applyLoopDecision(loopDecisionMsg{next: "0001", fp: "0001:todo,0002:done"})
	mm := next.(model)
	if !mm.looping || mm.loopPrevFP != "0001:todo,0002:done" {
		t.Fatalf("expected loop to continue with new fingerprint, looping=%v fp=%q", mm.looping, mm.loopPrevFP)
	}
	if cmd == nil {
		t.Fatal("expected a startSession command")
	}
}

func TestLoopDecisionStopsWhenEmpty(t *testing.T) {
	m := &model{looping: true, loopStarted: true, loopPrevFP: "0002:todo"}
	next, _ := m.applyLoopDecision(loopDecisionMsg{next: "", fp: "0002:done"})
	mm := next.(model)
	if mm.looping || mm.state != stateMenu {
		t.Fatalf("expected loop to stop and return to menu, looping=%v state=%v", mm.looping, mm.state)
	}
}

// A loop iteration that has a ready task starts the next work session in
// AUTONOMOUS mode so an unattended run never blocks on ask_user (a genuinely
// stuck task is marked blocked by the coordinator and skipped).
func TestLoopDecisionStartsAutonomousSession(t *testing.T) {
	fc := newFakeClient()
	m := &model{looping: true, loopStarted: false, loopPrevFP: "", client: fc, ctx: context.Background()}
	next, cmd := m.applyLoopDecision(loopDecisionMsg{next: "0003", fp: "0003:todo"})
	mm := next.(model)
	if !mm.looping || !mm.loopStarted || mm.loopPrevFP != "0003:todo" {
		t.Fatalf("expected loop to continue, looping=%v started=%v fp=%q", mm.looping, mm.loopStarted, mm.loopPrevFP)
	}
	if cmd == nil {
		t.Fatal("expected a startSession command")
	}
	cmd() // executes the StartSession RPC against the fake client
	if fc.lastStartLevel != "autonomous" {
		t.Fatalf("loop session must start autonomous, got %q", fc.lastStartLevel)
	}
}

// A finished work session goes idle and blocks for input rather than
// self-terminating, so while looping the driver must stop it explicitly (closing
// its stream to advance the loop). A second idle event must not re-trigger.
func TestLoopStopsIdleSession(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.looping = true
	m.sessionID = "s1"
	m.client = newFakeClient()
	m.ctx = context.Background()
	m.events = make(chan *v1.Event, 4)

	idle := &v1.Event{Seq: 1, Type: "session_idle", DataJson: `{"report":"shipped task 0002"}`}
	nm, cmd := m.Update(evMsg{idle})
	m = nm.(model)
	if !m.loopStopping {
		t.Fatal("expected loopStopping=true after a looping session goes idle")
	}
	if cmd == nil {
		t.Fatal("expected a command (StopSession) to be issued on idle while looping")
	}

	// A subsequent idle event must not re-arm the stop (guard against repeats).
	idle2 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"again"}`}
	nm, _ = m.Update(evMsg{idle2})
	m = nm.(model)
	if !m.loopStopping {
		t.Fatal("loopStopping should remain set across further idle events")
	}

	// The returned command must re-arm a reader on m.events. The idle branch
	// batches stopSession()+waitEvent()+spin; StopSession closes the event stream,
	// and only an armed waitEvent surfaces that close as streamClosedMsg to drive
	// the loop advance. Verify the batch issues StopSession AND contains a command
	// that yields streamClosedMsg once the channel is closed (fails if waitEvent is
	// dropped from the batch).
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a tea.BatchMsg from the idle-looping command, got %T", cmd())
	}
	close(m.events)
	fc := m.client.(*fakeClient)
	sawStreamClosed := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, isClosed := c().(streamClosedMsg); isClosed {
			sawStreamClosed = true
		}
	}
	if fc.lastStopped != "s1" {
		t.Fatalf("expected StopSession to be issued for s1, got %q", fc.lastStopped)
	}
	if !sawStreamClosed {
		t.Fatal("idle-looping command must re-arm waitEvent(m.events) so the closed stream yields streamClosedMsg")
	}
}

// When the loop is NOT active, an idle session must stay put (no auto-stop) so a
// normal work session remains usable after finishing a task.
func TestNonLoopIdleStaysPut(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.looping = false
	m.sessionID = "s1"
	m.events = make(chan *v1.Event, 4)

	idle := &v1.Event{Seq: 1, Type: "session_idle", DataJson: `{"report":"done"}`}
	nm, _ := m.Update(evMsg{idle})
	m = nm.(model)
	if m.loopStopping {
		t.Fatal("non-loop session must not arm loopStopping on idle")
	}
	if m.status != "idle" {
		t.Fatalf("expected status idle, got %q", m.status)
	}
}

// --- work-loop batch digest (task 0098) ---

// evJSON builds an event of the given type with a JSON data payload.
func evJSON(seq int64, typ, data string) *v1.Event {
	return &v1.Event{Seq: seq, Type: typ, DataJson: data}
}

// TestBlockedReasonFromBody covers the pure work-log reason extractor: it prefers
// the last bullet mentioning "blocked", else the last bullet, else "".
func TestBlockedReasonFromBody(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"prefers last blocked", "## Work log\n- did a thing\n- blocked: need the API key\n- (later) tidied up", "blocked: need the API key"},
		{"prefers blocked", "## Work log\n- blocked: waiting on review\n- noted", "blocked: waiting on review"},
		{"last bullet", "## Work log\n- first\n- second", "second"},
		{"star bullets", "# Task\n## Work log\n* only bullet here", "only bullet here"},
		{"no work log", "## Description\n- not a work log bullet", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := blockedReasonFromBody(c.body); got != c.want {
			t.Errorf("%s: blockedReasonFromBody = %q, want %q", c.name, got, c.want)
		}
	}
}

// digestLoopModel builds a model wired to the given fake client ready to drive a
// scripted loop run for the digest tests.
func digestLoopModel(fc *fakeClient) model {
	return model{
		client: fc, ctx: context.Background(),
		looping: true, loopStarted: false,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		usageByModel: map[string]event.Usage{},
	}
}

// TestLoopDigestRollup drives a scripted two-session loop and asserts the digest
// classifies tasks, records commit sha + verdict tally, and per-session records.
func TestLoopDigestRollup(t *testing.T) {
	fc := newFakeClient()
	fc.taskDetails = map[string]*v1.TaskDetail{
		"0002": {Id: "0002", Status: "blocked", Body: "## Work log\n- started\n- blocked: needs the staging DB password"},
	}
	m := digestLoopModel(fc)

	// First decision initialises the run baseline (0001,0002 todo) and starts s1.
	baseline := []*v1.BacklogTaskSummary{
		{Id: "0001", Title: "First task", Status: "todo", Ready: true},
		{Id: "0002", Title: "Second task", Status: "todo", Ready: true},
	}
	nm, _ := m.applyLoopDecision(loopDecisionMsg{next: "0001", fp: "0001:todo,0002:todo", tasks: baseline})
	m = nm.(model)
	if m.loopRun == nil || len(m.loopRun.baseline) != 2 {
		t.Fatalf("expected run baseline of 2 tasks, got %+v", m.loopRun)
	}

	// Session 1 works 0001: focuses it, commits, two approvals, spends 1000 tokens.
	m.sessionID = "s1"
	m.sessionStart = time.Now().Add(-90 * time.Second)
	m.usageByModel = map[string]event.Usage{"m": {Total: 1000}}
	m.evs = []*v1.Event{
		evJSON(1, "task_focus", `{"task":"0001"}`),
		evJSON(2, "commit_made", `{"task":"0001","sha":"abcdef1234567","message":"do the thing"}`),
		evJSON(3, "review_submitted", `{"verdict":"approve"}`),
		evJSON(4, "review_submitted", `{"verdict":"approve"}`),
	}
	nm, _ = m.Update(streamClosedMsg{})
	m = nm.(model)
	if len(m.loopRun.sessions) != 1 {
		t.Fatalf("expected 1 session recorded after s1, got %d", len(m.loopRun.sessions))
	}

	// Second decision (0002 ready) continues the loop; start s2.
	nm, _ = m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done,0002:todo", tasks: baseline})
	m = nm.(model)

	// Session 2 works 0002 and blocks it, spending 500 tokens.
	m.sessionID = "s2"
	m.sessionStart = time.Now().Add(-30 * time.Second)
	m.usageByModel = map[string]event.Usage{"m": {Total: 500}}
	m.evs = []*v1.Event{
		evJSON(1, "task_focus", `{"task":"0002"}`),
	}
	nm, _ = m.Update(streamClosedMsg{})
	m = nm.(model)
	if len(m.loopRun.sessions) != 2 {
		t.Fatalf("expected 2 sessions recorded, got %d", len(m.loopRun.sessions))
	}

	// Final decision: nothing ready. 0001 done, 0002 blocked, 0003 newly created.
	final := []*v1.BacklogTaskSummary{
		{Id: "0001", Title: "First task", Status: "done"},
		{Id: "0002", Title: "Second task", Status: "blocked"},
		{Id: "0003", Title: "Follow-up", Status: "todo"},
	}
	nm, cmd := m.applyLoopDecision(loopDecisionMsg{next: "", fp: "", tasks: final})
	m = nm.(model)

	if !m.digest {
		t.Fatal("expected the digest modal to open when the loop ends")
	}
	d := m.loopDigest
	if d == nil {
		t.Fatal("expected a loopDigest to be built")
	}
	if len(d.completed) != 1 || d.completed[0].id != "0001" {
		t.Fatalf("expected 0001 completed, got %+v", d.completed)
	}
	if d.completed[0].sha != "abcdef1234567" {
		t.Fatalf("expected commit sha recorded, got %q", d.completed[0].sha)
	}
	if d.completed[0].verdictTally != "approve×2" {
		t.Fatalf("expected verdict tally approve×2, got %q", d.completed[0].verdictTally)
	}
	if d.completed[0].tokens != 1000 {
		t.Fatalf("expected 1000 tokens for 0001, got %d", d.completed[0].tokens)
	}
	if len(d.blocked) != 1 || d.blocked[0].id != "0002" {
		t.Fatalf("expected 0002 blocked, got %+v", d.blocked)
	}
	if len(d.created) != 1 || d.created[0].id != "0003" {
		t.Fatalf("expected 0003 created, got %+v", d.created)
	}
	if d.totalTokens != 1500 {
		t.Fatalf("expected total 1500 tokens, got %d", d.totalTokens)
	}

	// The finish batch fetches each blocked task's reason; drive that fetch and
	// feed the digestTaskMsg back so the reason fills in.
	_ = cmd
	dm := m.fetchDigestTask("0002")()
	nm, _ = m.Update(dm)
	m = nm.(model)
	if got := m.loopDigest.blocked[0].reason; got != "blocked: needs the staging DB password" {
		t.Fatalf("expected blocked reason from work log, got %q", got)
	}
}

// TestLoopDigestPricingAndReopen covers cost fill (unpriced renders "—" until
// priced) and that the digest is re-openable from the browse selector.
func TestLoopDigestPricingAndReopen(t *testing.T) {
	d := &loopDigest{
		outcome:    "loop complete: no ready tasks remain",
		costStatus: "unpriced",
		sessions:   []loopSessRec{{id: "s1", focus: "0001", tokens: 1000, priceStatus: "unpriced"}},
		completed:  []digestTask{{id: "0001", title: "First", tokens: 1000, priceStatus: "unpriced"}},
	}
	m := model{loopDigest: d, digest: true}

	// Before pricing, cost renders "—" (§20.4).
	if !strings.Contains(m.digestView(), "—") {
		t.Fatalf("expected unpriced cost to render as em dash, got:\n%s", m.digestView())
	}

	// Price it from a per-session usage row; cost fills for the session's focus task.
	d.applyUsage([]*v1.UsageRow{{Session: "s1", Cost: 0.42, PriceStatus: "priced", Total: 1000}})
	if d.completed[0].priceStatus != "priced" || d.completed[0].cost != 0.42 {
		t.Fatalf("expected 0001 priced at 0.42, got status=%q cost=%v", d.completed[0].priceStatus, d.completed[0].cost)
	}
	if d.totalCost != 0.42 || d.costStatus != "priced" {
		t.Fatalf("expected total 0.42 priced, got cost=%v status=%q", d.totalCost, d.costStatus)
	}
	if !strings.Contains(m.digestView(), "$0.4200") {
		t.Fatalf("expected priced cost in digest view, got:\n%s", m.digestView())
	}

	// Re-openable: from the menu, browse selector → "digest" reopens it.
	fc := newFakeClient()
	mm := model{
		client: fc, ctx: context.Background(), state: stateMenu,
		loopDigest: d, browse: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	// Move the browse cursor to the "digest" target and open it.
	for i, tgt := range browseTargets {
		if tgt.label == "digest" {
			mm.browseCursor = i
		}
	}
	nm, _ := mm.updateBrowse(keyMsg("enter"))
	mm = nm.(model)
	if !mm.digest || mm.browse {
		t.Fatalf("expected browse selector to reopen the digest, digest=%v browse=%v", mm.digest, mm.browse)
	}
}

// --- session textarea input behavior (task 0058) ---

// newSessionTextareaModel builds a sized session model whose input is a real
// textarea, mirroring how the running TUI is constructed.
func newSessionTextareaModel(t *testing.T) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", mode: "implement",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		input: newSessionInput(),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	return m
}

func TestSessionInputEnterSendsAndClears(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "hello agent")
	if m.input.Value() != "hello agent" {
		t.Fatalf("setup: value = %q, want %q", m.input.Value(), "hello agent")
	}
	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.input.Value() != "" {
		t.Fatalf("enter did not clear input: %q", m.input.Value())
	}
	if cmd == nil {
		t.Fatalf("enter on non-empty input should issue a send command")
	}
}

func TestSessionInputShiftEnterInsertsNewline(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "ab")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated.(model)
	m = typeText(t, m, "cd")
	if m.input.Value() != "ab\ncd" {
		t.Fatalf("shift+enter: value = %q, want %q", m.input.Value(), "ab\ncd")
	}
	if m.input.LineCount() != 2 {
		t.Fatalf("shift+enter: LineCount = %d, want 2", m.input.LineCount())
	}
}

func TestSessionInputCtrlJInsertsNewline(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "ab")
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m = updated.(model)
	m = typeText(t, m, "cd")
	if m.input.Value() != "ab\ncd" {
		t.Fatalf("ctrl+j: value = %q, want %q", m.input.Value(), "ab\ncd")
	}
	if m.input.LineCount() != 2 {
		t.Fatalf("ctrl+j: LineCount = %d, want 2", m.input.LineCount())
	}
}

// TestInterruptKeyHintReflectsEnhancement checks the footer advertises ctrl+x
// (the universal chord) until the terminal reports kitty keyboard disambiguation,
// after which it shows ctrl+i (byte-identical to Tab, so only usable there).
func TestInterruptKeyHintReflectsEnhancement(t *testing.T) {
	m := newSessionTextareaModel(t)
	// Widen the terminal so the footer isn't clamped/truncated before the hint.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})
	m = updated.(model)
	if got := m.interruptKeyHint(); got != "ctrl+x" {
		t.Fatalf("without enhancement: hint = %q, want %q", got, "ctrl+x")
	}
	if v := m.render(); !strings.Contains(v, "ctrl+x interrupt") {
		t.Fatalf("footer should advertise ctrl+x interrupt without enhancement:\n%s", v)
	}

	updated, _ = m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m = updated.(model)
	if got := m.interruptKeyHint(); got != "ctrl+i" {
		t.Fatalf("with enhancement: hint = %q, want %q", got, "ctrl+i")
	}
	if v := m.render(); !strings.Contains(v, "ctrl+i interrupt") {
		t.Fatalf("footer should advertise ctrl+i interrupt with enhancement:\n%s", v)
	}
}

// TestSessionCtrlXInterruptsWithoutEditingInput ensures ctrl+x triggers the
// interrupt path on every terminal and does not leak into the session textarea.
func TestSessionCtrlXInterruptsWithoutEditingInput(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, "steer me")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updated.(model)
	if cmd == nil {
		t.Fatalf("ctrl+x while running should issue an interrupt command")
	}
	if m.input.Value() != "steer me" {
		t.Fatalf("ctrl+x should not edit the input: value = %q", m.input.Value())
	}
}

func TestSessionInputHeightCapsWithNewlines(t *testing.T) {
	m := newSessionTextareaModel(t)
	for i := 0; i < maxInputRows+3; i++ {
		m = typeText(t, m, "x")
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		m = updated.(model)
	}
	if m.input.Height() != maxInputRows {
		t.Fatalf("height with many newlines = %d, want %d", m.input.Height(), maxInputRows)
	}
}

func TestSessionInputGrowsOnSoftWrap(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, strings.Repeat("a", 200))
	if m.input.Height() <= 1 {
		t.Fatalf("soft-wrapped long line height = %d, want > 1", m.input.Height())
	}
	m2 := newSessionTextareaModel(t)
	m2 = typeText(t, m2, strings.Repeat("a", 600))
	if m2.input.Height() != maxInputRows {
		t.Fatalf("very long wrapped line height = %d, want %d (capped)", m2.input.Height(), maxInputRows)
	}
}

func TestSessionInputRelayoutFitsTerminal(t *testing.T) {
	m := newSessionTextareaModel(t)
	m = typeText(t, m, strings.Repeat("a", 600))
	if got := len(strings.Split(m.sessionView(), "\n")); got != 24 {
		t.Fatalf("sessionView produced %d lines, want 24", got)
	}
}

func TestListWindow(t *testing.T) {
	// n<=size: no clipping.
	if s, e := listWindow(0, 3, 5); s != 0 || e != 3 {
		t.Fatalf("n<=size: got (%d,%d), want (0,3)", s, e)
	}
	// size<=0: no clipping.
	if s, e := listWindow(2, 10, 0); s != 0 || e != 10 {
		t.Fatalf("size<=0: got (%d,%d), want (0,10)", s, e)
	}
	// cursor at top → start 0.
	if s, e := listWindow(0, 30, 10); s != 0 || e != 10 {
		t.Fatalf("cursor top: got (%d,%d), want (0,10)", s, e)
	}
	// cursor in middle → cursor within window and centered.
	s, e := listWindow(15, 30, 10)
	if !(s <= 15 && 15 < e) {
		t.Fatalf("cursor middle: 15 not in [%d,%d)", s, e)
	}
	if e-s != 10 {
		t.Fatalf("cursor middle: window len=%d, want 10", e-s)
	}
	if s != 15-10/2 {
		t.Fatalf("cursor middle: start=%d, want %d (centered)", s, 15-10/2)
	}
	// cursor at last index → end==n and last visible.
	s, e = listWindow(29, 30, 10)
	if e != 30 {
		t.Fatalf("cursor last: end=%d, want 30", e)
	}
	if !(s <= 29 && 29 < e) {
		t.Fatalf("cursor last: 29 not in [%d,%d)", s, e)
	}
	if e-s != 10 {
		t.Fatalf("cursor last: window len=%d, want 10", e-s)
	}
}

func TestBacklogViewScrollsWithinViewport(t *testing.T) {
	var tasks []*v1.BacklogTaskSummary
	for i := 1; i <= 30; i++ {
		tasks = append(tasks, &v1.BacklogTaskSummary{
			Id:     fmt.Sprintf("%04d", i),
			Status: "todo",
			Title:  fmt.Sprintf("task %d", i),
			Ready:  true,
		})
	}
	m := model{backlog: true, backlogTasks: tasks}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)

	// Cursor at top: list clipped to viewport, first task visible.
	out := m.backlogView()
	if lines := len(strings.Split(out, "\n")); lines > 12 {
		t.Fatalf("backlogView produced %d lines, want <= 12", lines)
	}
	if !strings.Contains(out, "0001") {
		t.Fatalf("first task 0001 not visible at cursor top:\n%s", out)
	}
	if strings.Contains(out, "0030") {
		t.Fatalf("last task 0030 unexpectedly visible at cursor top:\n%s", out)
	}

	// Move cursor to the last task: window scrolls.
	m.backlogCursor = len(m.visibleBacklogTasks()) - 1
	out = m.backlogView()
	if lines := len(strings.Split(out, "\n")); lines > 12 {
		t.Fatalf("backlogView (last) produced %d lines, want <= 12", lines)
	}
	if !strings.Contains(out, "0030") {
		t.Fatalf("last task 0030 not visible at cursor bottom:\n%s", out)
	}
	if strings.Contains(out, "0001") {
		t.Fatalf("first task 0001 still visible after scrolling to bottom:\n%s", out)
	}
}

// TestBacklogDetailScrolls verifies the backlog task detail view is a scrollable
// viewport: opening a task starts at the top, scroll keys advance the offset so
// long content is reachable, and opening a different task resets to the top.
func TestBacklogDetailScrolls(t *testing.T) {
	m := model{
		state: stateMenu, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{"coordinator": "high", "implementer": "high", "reviewers": "high"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.backlog = true

	// A body far longer than one viewport page.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "line %03d\n", i)
	}
	updated, _ = m.Update(taskDetailMsg{task: &v1.TaskDetail{Id: "0001", Title: "t", Body: sb.String()}})
	m = updated.(model)

	if !m.backlogVP.AtTop() {
		t.Fatalf("detail viewport did not start at top: YOffset=%d", m.backlogVP.YOffset())
	}
	// Render once (detail view) to ensure it does not panic and produces output.
	if out := m.render(); out == "" {
		t.Fatalf("detail render produced no output")
	}

	// Scroll down several times; the offset must increase.
	before := m.backlogVP.YOffset()
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(model)
	}
	if m.backlogVP.YOffset() <= before {
		t.Fatalf("scrolling did not advance offset: before=%d after=%d", before, m.backlogVP.YOffset())
	}

	// Opening a different task resets scroll to the top.
	updated, _ = m.Update(taskDetailMsg{task: &v1.TaskDetail{Id: "0002", Title: "u", Body: sb.String()}})
	m = updated.(model)
	if !m.backlogVP.AtTop() {
		t.Fatalf("opening a new task did not reset to top: YOffset=%d", m.backlogVP.YOffset())
	}
}

// TestTransientErrorKeepsSessionUsable verifies that a failed RPC on a live,
// connected session surfaces as an inline status-bar flash while the session
// view keeps rendering its events and accepting input — never the full-screen
// fatal error (task 0104).
func TestTransientErrorKeepsSessionUsable(t *testing.T) {
	m := model{
		state: stateSession, status: "running", follow: true, connected: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{"coordinator": "high"},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hello world"}`})
	m.rebuild()

	// A failed SendInput/etc. produces errMsg. Because the client is connected
	// this must NOT set the fatal error.
	updated, _ = m.Update(errMsg{err: fmt.Errorf("send failed: boom")})
	m = updated.(model)
	if m.err != nil {
		t.Fatalf("transient errMsg set fatal m.err = %v, want nil", m.err)
	}
	if m.flashErr == "" {
		t.Fatalf("transient errMsg did not set flashErr")
	}

	view := m.render()
	if strings.Contains(view, "r to retry") || strings.Contains(view, "ctrl+c to quit") {
		t.Fatalf("transient error rendered the fatal screen:\n%s", view)
	}
	if !strings.Contains(view, "hello world") {
		t.Fatalf("session events no longer render after transient error:\n%s", view)
	}
	if !strings.Contains(view, "send failed: boom") {
		t.Fatalf("status bar does not surface the inline error:\n%s", view)
	}

	// The input still accepts text.
	updated, _ = m.Update(keyMsg("x"))
	m = updated.(model)
	if !strings.Contains(m.input.Value(), "x") {
		t.Fatalf("input did not accept text after transient error: value=%q", m.input.Value())
	}

	// The clear tick dismisses the flash (a stale tick with an old seq would not).
	updated, _ = m.Update(flashClearMsg{seq: m.flashSeq})
	m = updated.(model)
	if m.flashErr != "" {
		t.Fatalf("flash did not clear on the matching tick: %q", m.flashErr)
	}
}

// TestTransientErrorClearsOnSuccess verifies the inline flash clears when the
// next successful RPC result arrives (task 0104).
func TestTransientErrorClearsOnSuccess(t *testing.T) {
	m := model{
		state: stateSession, status: "running", connected: true,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(errMsg{err: fmt.Errorf("hiccup")})
	m = updated.(model)
	if m.flashErr == "" {
		t.Fatalf("errMsg did not arm flash")
	}
	// A successful backlog fetch result clears the flash.
	updated, _ = m.Update(backlogMsg{tasks: nil})
	m = updated.(model)
	if m.flashErr != "" {
		t.Fatalf("flash did not clear on a successful RPC result: %q", m.flashErr)
	}
}

// TestFatalStartupErrorRendersRetry verifies that an RPC failure before the
// client has ever reached the daemon is fatal, renders the full-screen error,
// offers a retry, and that "r" re-runs Init while leaving quit intact (task
// 0104).
func TestFatalStartupErrorRendersRetry(t *testing.T) {
	m := model{
		state: stateMenu, connected: false,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(errMsg{err: fmt.Errorf("dial daemon: connection refused")})
	m = updated.(model)
	if m.err == nil {
		t.Fatalf("startup errMsg (not connected) did not set fatal m.err")
	}
	view := m.render()
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("fatal screen missing error text:\n%s", view)
	}
	if !strings.Contains(view, "retry") {
		t.Fatalf("fatal screen missing retry affordance:\n%s", view)
	}

	// "r" clears the fatal error and re-runs the Init fetches.
	updated, cmd := m.Update(keyMsg("r"))
	m = updated.(model)
	if m.err != nil {
		t.Fatalf("retry did not clear fatal m.err = %v", m.err)
	}
	if cmd == nil {
		t.Fatalf("retry did not return a command to re-run Init")
	}
}

// TestQuestionPickerFitsOnScreen guards the ask_user layout regression: when a
// question_asked event carries options (single question or a multi-question
// batch), the rendered session view must still fit the terminal height with the
// picker options and help footer visible. Previously relayout only accounted
// for the input box, so the wizard overview / option picker stacked BELOW a
// full-height viewport and were clipped off the bottom of the screen — the user
// never saw the options and the question faded off the bottom.
func TestQuestionPickerFitsOnScreen(t *testing.T) {
	cases := map[string]string{
		"single": `{"question":"db?","options":["postgres","sqlite","mysql"]}`,
		"batch":  `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	}
	for name, dataJSON := range cases {
		t.Run(name, func(t *testing.T) {
			m := newSessionTextareaModel(t)
			m.client = newFakeClient()
			m.ctx = context.Background()
			m.sessionID = "s1"
			m.events = make(chan *v1.Event, 4)
			m.follow = true

			// Fill the log with enough turns that the viewport content alone
			// exceeds the 24-row terminal — the situation where an unshrunk
			// viewport pushes the picker off-screen.
			var seq int64
			for seq = 1; seq <= 40; seq++ {
				nm, _ := m.Update(evMsg{&v1.Event{
					Seq: seq, Type: "model_turn", Actor: "coordinator",
					DataJson: fmt.Sprintf(`{"text":"working on step %d"}`, seq),
				}})
				m = nm.(model)
			}
			nm, _ := m.Update(evMsg{&v1.Event{
				Seq: seq, Type: "question_asked", Actor: "coordinator", DataJson: dataJSON,
			}})
			m = nm.(model)
			if !m.picking {
				t.Fatal("expected the option picker to be active after question_asked with options")
			}

			view := m.render()
			lines := strings.Split(view, "\n")
			if len(lines) > 24 {
				t.Fatalf("session view is %d rows tall; must fit the 24-row terminal", len(lines))
			}
			if !strings.Contains(view, "postgres") {
				t.Fatalf("picker options not visible in the rendered view:\n%s", view)
			}
			if !strings.Contains(view, "other…") {
				t.Fatalf("picker 'other…' escape not visible in the rendered view:\n%s", view)
			}
		})
	}
}

// driveBacklog feeds a key through the backlog browser handler and runs any
// resulting commands (task 0099 grooming keys).
func driveBacklog(t *testing.T, m model, key string) model {
	t.Helper()
	updated, cmd := m.updateBacklog(keyMsg(key))
	m = updated.(model)
	return runCmds(t, m, cmd)
}

func newBacklogModel(f *fakeClient, tasks []*v1.BacklogTaskSummary) model {
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.backlog = true
	m.backlogTasks = tasks
	m.backlogCursor = 0
	f.backlogList = tasks
	return m
}

// TestBacklogPriorityKeys verifies +/- reprioritize the cursor row via UpdateTask,
// clamped to 1..5 (no RPC at the clamp edge) — task 0099.
func TestBacklogPriorityKeys(t *testing.T) {
	f := newFakeClient()
	m := newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})

	// "+" raises priority toward p1 (3 -> 2).
	driveBacklog(t, m, "+")
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetId() != "0001" {
		t.Fatalf("+ did not fire UpdateTask for the cursor row: %+v", f.lastUpdateTask)
	}
	if f.lastUpdateTask.Priority == nil || f.lastUpdateTask.GetPriority() != 2 {
		t.Fatalf("+ priority = %v, want 2", f.lastUpdateTask.Priority)
	}

	// "-" lowers priority toward p5 (3 -> 4).
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})
	driveBacklog(t, m, "-")
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetPriority() != 4 {
		t.Fatalf("- priority = %v, want 4", f.lastUpdateTask)
	}

	// Clamp edges: p1 "+" and p5 "-" are no-ops (no RPC).
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Priority: 1, Title: "a"}})
	driveBacklog(t, m, "+")
	if f.lastUpdateTask != nil {
		t.Fatalf("+ at p1 fired an RPC, want no-op: %+v", f.lastUpdateTask)
	}
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Priority: 5, Title: "a"}})
	driveBacklog(t, m, "-")
	if f.lastUpdateTask != nil {
		t.Fatalf("- at p5 fired an RPC, want no-op: %+v", f.lastUpdateTask)
	}
}

// TestBacklogStatusPrompt verifies "s" then a digit changes status via UpdateTask,
// and "s" then esc cancels without an RPC (task 0099).
func TestBacklogStatusPrompt(t *testing.T) {
	f := newFakeClient()
	m := newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Priority: 3, Title: "a"}})

	m = driveBacklog(t, m, "s")
	if !m.backlogStatusPrompt {
		t.Fatal("s did not enter the status prompt")
	}
	if f.lastUpdateTask != nil {
		t.Fatalf("s alone fired an RPC: %+v", f.lastUpdateTask)
	}
	m = driveBacklog(t, m, "3") // in_review
	if m.backlogStatusPrompt {
		t.Fatal("status prompt still active after selecting a digit")
	}
	if f.lastUpdateTask == nil || f.lastUpdateTask.GetStatus() != "in_review" {
		t.Fatalf("status digit = %+v, want status=in_review", f.lastUpdateTask)
	}

	// esc cancels the prompt without an RPC.
	f.lastUpdateTask = nil
	m = newBacklogModel(f, []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Title: "a"}})
	m = driveBacklog(t, m, "s")
	m = driveBacklog(t, m, "esc")
	if m.backlogStatusPrompt {
		t.Fatal("esc did not cancel the status prompt")
	}
	if f.lastUpdateTask != nil {
		t.Fatalf("esc after s fired an RPC: %+v", f.lastUpdateTask)
	}
}

// TestBacklogEditorGating verifies the open-in-editor affordance is only offered
// when the task file is local, and degrades to a footer notice otherwise (task 0099).
func TestBacklogEditorGating(t *testing.T) {
	// A remote/non-local path: taskFileLocal is false, "e" degrades to a notice
	// (and never execs an editor).
	f := newFakeClient()
	m := newBacklogModel(f, nil)
	m.backlogDetail = &v1.TaskDetail{Id: "0001", Title: "a", Path: "/no/such/task/file.md"}
	if taskFileLocal(m.backlogDetail.Path) {
		t.Fatal("taskFileLocal true for a nonexistent path")
	}
	updated, cmd := m.updateBacklog(keyMsg("e"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("e on a non-local task returned a command (would exec an editor)")
	}
	if m.backlogNotice == "" {
		t.Fatal("e on a non-local task did not set a footer notice")
	}

	// A real file: taskFileLocal is true and the detail footer advertises "e edit".
	dir := t.TempDir()
	p := filepath.Join(dir, "0001-x.md")
	if err := os.WriteFile(p, []byte("# task\n"), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	if !taskFileLocal(p) {
		t.Fatal("taskFileLocal false for an existing file")
	}
	m2 := model{ready: true}
	m2.w, m2.h = 80, 24
	if got := m2.taskDetailView(&v1.TaskDetail{Id: "0001", Title: "a", Path: p}); !strings.Contains(got, "e edit") {
		t.Fatalf("detail footer missing 'e edit' for a local task:\n%s", got)
	}
	if got := m2.taskDetailView(&v1.TaskDetail{Id: "0001", Title: "a", Path: "/nope.md"}); strings.Contains(got, "e edit") {
		t.Fatalf("detail footer advertised 'e edit' for a non-local task:\n%s", got)
	}
}

// TestEditorCommand covers the $EDITOR → $VISUAL → vi resolution order (task 0099).
func TestEditorCommand(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")
	if got := editorCommand(); got != "vi" {
		t.Fatalf("default editor = %q, want vi", got)
	}
	t.Setenv("VISUAL", "nano")
	if got := editorCommand(); got != "nano" {
		t.Fatalf("VISUAL editor = %q, want nano", got)
	}
	t.Setenv("EDITOR", "emacs")
	if got := editorCommand(); got != "emacs" {
		t.Fatalf("EDITOR editor = %q, want emacs (takes precedence over VISUAL)", got)
	}
}

// TestChatInputWordMotion verifies Ctrl+Left/Ctrl+Right perform word-wise
// cursor movement in the multi-line chat-input textarea (task 0102). The
// bubbles v2 textarea binds word motions to alt-arrows by default; newChatInput
// additionally binds ctrl+left/ctrl+right (matching the single-line textinput).
func TestChatInputWordMotion(t *testing.T) {
	ta := newChatInput("")
	ta.Focus()
	ta.SetWidth(80) // wide enough that the value never soft-wraps
	// "hello world foo": hello=[0,4], space 5, world=[6,10], space 11, foo=[12,14]
	ta.SetValue("hello world foo")
	ta.CursorEnd()

	col := func() int {
		li := ta.LineInfo()
		return li.StartColumn + li.ColumnOffset
	}
	ctrlLeft := tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}
	ctrlRight := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModCtrl}

	// Ctrl+Left steps back to the start of each previous word.
	for _, want := range []int{12, 6, 0} {
		ta, _ = ta.Update(ctrlLeft)
		if got := col(); got != want {
			t.Fatalf("ctrl+left cursor col = %d, want %d", got, want)
		}
	}
	// Extra Ctrl+Left at the start of the buffer must not crash or move.
	ta, _ = ta.Update(ctrlLeft)
	if got := col(); got != 0 {
		t.Fatalf("ctrl+left at buffer start moved cursor to %d, want 0", got)
	}

	// Ctrl+Right steps forward to each next word boundary (end of word).
	for _, want := range []int{5, 11, 15} {
		ta, _ = ta.Update(ctrlRight)
		if got := col(); got != want {
			t.Fatalf("ctrl+right cursor col = %d, want %d", got, want)
		}
	}
	// Extra Ctrl+Right at the end of the buffer must not crash or move.
	ta, _ = ta.Update(ctrlRight)
	if got := col(); got != 15 {
		t.Fatalf("ctrl+right at buffer end moved cursor to %d, want 15", got)
	}

	// Word motion left the text untouched.
	if got := ta.Value(); got != "hello world foo" {
		t.Fatalf("word motion mutated value: %q", got)
	}
}
