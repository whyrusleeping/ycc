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

	"charm.land/bubbles/v2/textinput"
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
	lastPersist bool
	lastRemove  string

	// previous-sessions screen (spec §18.6)
	history      []*v1.SessionSummary
	lastReopened string
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
	f.lastPersist = req.Msg.Persist
	if _, ok := f.models[c.Name]; !ok {
		f.order = append(f.order, c.Name)
	}
	f.models[c.Name] = c
	return connect.NewResponse(&v1.UpsertModelResponse{}), nil
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
	m = typeText(t, m, "gpt-5")
	// move to key_env and type.
	m = drive(t, m, "tab")
	m = typeText(t, m, "OPENAI_API_KEY")
	// toggle persist on by enabling it via the list-level toggle before submit:
	m.mbPersist = true
	// Submit.
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

// TestPreviousSessionsReopen drives the menu -> previous-sessions -> reopen flow
// (spec §18.6): ctrl+r opens the history screen and loads the list, ↓ moves the
// cursor, and Enter reopens the selected session via ResumeSession, entering the
// session view.
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

	// ctrl+r opens the previous-sessions screen and loads history.
	m = drive(t, m, "ctrl+r")
	if m.state != stateHistory {
		t.Fatalf("after ctrl+r state=%v, want stateHistory", m.state)
	}
	if len(m.history) != 2 {
		t.Fatalf("history len=%d, want 2", len(m.history))
	}

	// Navigate to the second row and reopen it.
	m = drive(t, m, "down")
	if m.historyCursor != 1 {
		t.Fatalf("historyCursor=%d, want 1", m.historyCursor)
	}
	m = drive(t, m, "enter")

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

// The quick-add capture overlay (task 0049) streams the capture agent's action
// log live: each captureEvMsg appends to captureLog and is rendered in
// captureView, and a terminal capture_result event drives the overlay to its
// created/answered/error state.
func TestCaptureStreamsActionLog(t *testing.T) {
	m := model{w: 80, capture: true, captureStage: 0, captureBusy: true}
	m.captureInput = textinput.New()
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
