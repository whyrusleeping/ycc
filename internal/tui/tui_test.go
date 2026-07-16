package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"connectrpc.com/connect"
	"github.com/charmbracelet/x/ansi"

	"github.com/whyrusleeping/ycc/internal/clientconfig"
	"github.com/whyrusleeping/ycc/internal/codex"
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
	ev := &v1.Event{Type: "thinking", DataJson: `{"text":"first I will read the file","blocks":1,"reasoning_tokens":384}`}
	if d := detailLine(ev); !strings.Contains(d, "reasoning summary") || !strings.Contains(d, "384 hidden tokens") || !strings.Contains(d, "read the file") {
		t.Fatalf("detailLine = %q", d)
	}
	m := &model{w: 80}
	body := m.renderBody(ev)
	if !strings.Contains(body, "read the file") {
		t.Fatalf("renderBody = %q", body)
	}
	if d := expandedDetailLine(ev); !strings.Contains(d, "384 hidden reasoning tokens") {
		t.Fatalf("expandedDetailLine = %q", d)
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

// A session_idle report is the canonical human-facing finish message and renders
// in full, whether it echoes the final turn, adds details, or differs entirely.
func TestIdleReportRenderedInFull(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All done — shipped the feature and tests pass."}`}

	// Exact echo: the finish report remains the canonical, visible body.
	idle := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass."}`}
	m := mk(turn, idle)
	if b := m.renderBody(idle); !strings.Contains(b, "shipped the feature") {
		t.Fatalf("echoing finish report should render in full, got %q", b)
	}

	// Echo + appended assumptions: the full report remains.
	idle2 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All done — shipped the feature and tests pass.\n\nAssumptions made without consulting the user (autonomous mode):\n- used port 8080\n"}`}
	m = mk(turn, idle2)
	b := m.renderBody(idle2)
	if !strings.Contains(b, "shipped the feature") || !strings.Contains(b, "Assumptions") || !strings.Contains(b, "port 8080") {
		t.Fatalf("finish body should keep the complete report, got %q", b)
	}

	// Different control-tool report: rendered in full.
	idle3 := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042 and committed the change."}`}
	m = mk(turn, idle3)
	if b := m.renderBody(idle3); !strings.Contains(b, "task 0042") {
		t.Fatalf("a differing report should render in full, got %q", b)
	}
}

// A final model_turn echoed by session_idle is folded into the canonical finish
// report. Additive reports coalesce too; genuinely different rows both remain.
func TestFinishReportCoalescesPrecedingTurn(t *testing.T) {
	mk := func(evs ...*v1.Event) *model {
		m := &model{w: 80, bodyCache: map[int]string{}}
		m.evs = evs
		return m
	}
	turn := &v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"All green. Shipped it."}`}

	echo := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it."}`}
	m := mk(turn, echo)
	if !m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("echoed final turn should fold into a visible finish report")
	}

	added := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"All green. Shipped it.\n\nAssumptions:\n- used port 8080"}`}
	m = mk(turn, added)
	if !m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("a final-turn prefix should fold into the visible additive finish report")
	}

	diff := &v1.Event{Seq: 2, Type: "session_idle", DataJson: `{"report":"Completed task 0042."}`}
	m = mk(turn, diff)
	if m.hiddenRow(0) || m.hiddenRow(1) {
		t.Fatal("a differing finish report should preserve both rows")
	}
}

func TestFinishReportAlwaysExpandedAndCannotCollapse(t *testing.T) {
	m := &model{
		w: 80, ready: true, prefs: clientconfig.Prefs{AutoExpandLogs: false},
		expanded: map[int]bool{2: false}, bodyCache: map[int]string{},
		blockCache: map[int]string{}, hiddenCache: map[int]bool{},
	}
	idle := &v1.Event{Seq: 2, Type: "session_idle", Actor: "coordinator", DataJson: `{"report":"## Finished\n\n- shipped"}`}
	m.evs = []*v1.Event{idle}
	if !m.eventExpanded(2, "session_idle") {
		t.Fatal("finish report must ignore auto-expand=false and manual collapse overrides")
	}
	m.toggle(0)
	if !m.eventExpanded(2, "session_idle") {
		t.Fatal("finish report must remain expanded after toggle")
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
	t.Run("only stray non-task .md, no tasks", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, "backlog/notes.md", "# Notes\n")
		if !needsOnboarding(ws) {
			t.Fatal("stray non-task .md without tasks should still need onboarding")
		}
	})
	t.Run("configured spec entry point with content", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, ".ycc/config.toml", "spec_path = \"docs/index.md\"\n")
		writeFile(t, ws, "docs/index.md", "# Spec\n\n## Goals\nship it\n")
		if specIsEmpty(ws) {
			t.Fatal("configured entry point with real content should not be empty")
		}
		if needsOnboarding(ws) {
			t.Fatal("workspace with a real configured spec should not need onboarding")
		}
	})
	t.Run("configured spec entry point missing", func(t *testing.T) {
		ws := t.TempDir()
		writeFile(t, ws, ".ycc/config.toml", "spec_path = \"docs/index.md\"\n")
		// Root spec.md exists but is not the configured entry point.
		writeFile(t, ws, "spec.md", "# Real spec\n\ncontent\n")
		if !specIsEmpty(ws) {
			t.Fatal("missing configured entry point should be empty (root spec.md must not count)")
		}
		if !needsOnboarding(ws) {
			t.Fatal("missing configured spec + no backlog should need onboarding")
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

// The status header must not latch on "idle" either: prodding a finished
// session emits a user_input echo as soon as the daemon accepts it, but the
// first model event can lag far behind (long context + thinking). The echo —
// and any later activity — must flip the status back to running so the spinner
// arms and the footer stops claiming "session finished" while the agent is
// actually working on the follow-up.
func TestAppendEventClearsLatchedIdle(t *testing.T) {
	for _, activity := range []string{"user_input", "thinking", "model_turn"} {
		m := &model{w: 80, follow: true, expanded: map[int]bool{}, bodyCache: map[int]string{}}
		m.appendEvent(&v1.Event{Type: "session_idle", DataJson: `{"report":"done"}`})
		if m.status != "idle" {
			t.Fatalf("after session_idle status = %q, want idle", m.status)
		}
		m.appendEvent(&v1.Event{Type: activity, DataJson: `{"text":"follow-up"}`})
		if m.status != "running" {
			t.Fatalf("after %s status = %q, want running", activity, m.status)
		}
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
	stopCount   int

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

	// commit-diff drill-in (task 0140): canned diff returned by GetCommitDiff and
	// the last sha requested.
	commitDiff      string
	commitDiffTrunc bool
	commitDiffErr   error
	lastCommitSha   string

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

	// workstreams panel (task 0085): canned list returned by ListWorkstreams, the
	// SpawnWorkstream requests recorded in order, canned PreviewMerge/MergeWorkstream
	// responses, and the last discard id.
	workstreams   []*v1.WorkstreamInfo
	spawnReqs     []*v1.SpawnWorkstreamRequest
	previewResp   *v1.PreviewMergeResponse
	mergeResp     *v1.MergeWorkstreamResponse
	lastPreviewID string
	lastMergeID   string
	lastDiscardID string
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
	f.stopCount++
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

// GetCommitDiff backs the commit-diff drill-in overlay (task 0140): it records
// the requested sha and returns the canned diff (or a canned error).
func (f *fakeClient) GetCommitDiff(_ context.Context, req *connect.Request[v1.GetCommitDiffRequest]) (*connect.Response[v1.GetCommitDiffResponse], error) {
	f.lastCommitSha = req.Msg.Sha
	if f.commitDiffErr != nil {
		return nil, f.commitDiffErr
	}
	return connect.NewResponse(&v1.GetCommitDiffResponse{Diff: f.commitDiff, Truncated: f.commitDiffTrunc}), nil
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

// --- workstreams panel RPCs (task 0085, design §8) ---

func (f *fakeClient) ListWorkstreams(_ context.Context, _ *connect.Request[v1.ListWorkstreamsRequest]) (*connect.Response[v1.ListWorkstreamsResponse], error) {
	return connect.NewResponse(&v1.ListWorkstreamsResponse{Workstreams: f.workstreams}), nil
}

func (f *fakeClient) SpawnWorkstream(_ context.Context, req *connect.Request[v1.SpawnWorkstreamRequest]) (*connect.Response[v1.SpawnWorkstreamResponse], error) {
	f.spawnReqs = append(f.spawnReqs, req.Msg)
	ws := &v1.WorkstreamInfo{
		Id:        fmt.Sprintf("ws_%d", len(f.spawnReqs)),
		Project:   req.Msg.Project,
		TaskId:    req.Msg.TaskId,
		Branch:    "ycc/ws/spawn-" + req.Msg.TaskId,
		SessionId: fmt.Sprintf("s-ws-%d", len(f.spawnReqs)),
		Status:    "active",
	}
	f.workstreams = append(f.workstreams, ws)
	return connect.NewResponse(&v1.SpawnWorkstreamResponse{Workstream: ws}), nil
}

func (f *fakeClient) PreviewMerge(_ context.Context, req *connect.Request[v1.PreviewMergeRequest]) (*connect.Response[v1.PreviewMergeResponse], error) {
	f.lastPreviewID = req.Msg.WorkstreamId
	resp := f.previewResp
	if resp == nil {
		resp = &v1.PreviewMergeResponse{Clean: true, Diff: "diff --git a/x b/x\n+added\n"}
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeClient) MergeWorkstream(_ context.Context, req *connect.Request[v1.MergeWorkstreamRequest]) (*connect.Response[v1.MergeWorkstreamResponse], error) {
	f.lastMergeID = req.Msg.WorkstreamId
	resp := f.mergeResp
	if resp == nil {
		resp = &v1.MergeWorkstreamResponse{Merged: true, Commit: "abc1234"}
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeClient) DiscardWorkstream(_ context.Context, req *connect.Request[v1.DiscardWorkstreamRequest]) (*connect.Response[v1.DiscardWorkstreamResponse], error) {
	f.lastDiscardID = req.Msg.WorkstreamId
	return connect.NewResponse(&v1.DiscardWorkstreamResponse{}), nil
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

func (f *fakeClient) GetSubscriptionUsage(_ context.Context, _ *connect.Request[v1.GetSubscriptionUsageRequest]) (*connect.Response[v1.GetSubscriptionUsageResponse], error) {
	return connect.NewResponse(&v1.GetSubscriptionUsageResponse{}), nil
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
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+r":
		return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+o":
		return tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	case "ctrl+h":
		return tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}
	case "ctrl+_":
		return tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl}
	default:
		if r, ok := strings.CutPrefix(key, "ctrl+"); ok && len([]rune(r)) == 1 {
			return tea.KeyPressMsg{Code: []rune(r)[0], Mod: tea.ModCtrl}
		}
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

// overlayToReviewers opens the settings overlay on a client with the given
// models and moves the cursor to the reviewers row.
func overlayToReviewers(t *testing.T, extra ...*v1.ModelConfig) model {
	t.Helper()
	cfgs := []*v1.ModelConfig{
		{Name: "claude", Backend: "anthropic", Model: "claude-x"},
		{Name: "fable", Backend: "anthropic", Model: "claude-fable-5"},
		{Name: "gpt", Backend: "openai", Model: "gpt-5"},
	}
	cfgs = append(cfgs, extra...)
	f := newFakeClient(cfgs...)
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = runCmds(t, m, m.fetchModels)
	m.openOverlay()
	m = drive(t, m, "down") // level -> coord
	m = drive(t, m, "down") // coord -> impl
	m = drive(t, m, "down") // impl -> reviewers
	if m.ovCursor != ovReviewers {
		t.Fatalf("cursor = %d, want ovReviewers(%d)", m.ovCursor, ovReviewers)
	}
	return m
}

func reviewerNames(m model) []string { return append([]string(nil), m.roleReviewrs...) }

// TestOverlayReviewerSubCursorMoves verifies that ←/→ on the reviewers row moves
// the visible sub-cursor with wraparound and does not change the reviewer set.
func TestOverlayReviewerSubCursorMoves(t *testing.T) {
	m := overlayToReviewers(t)
	if m.reviewerSub != 0 {
		t.Fatalf("initial reviewerSub = %d, want 0", m.reviewerSub)
	}
	before := reviewerNames(m)
	m = drive(t, m, "right")
	if m.reviewerSub != 1 {
		t.Fatalf("after right reviewerSub = %d, want 1", m.reviewerSub)
	}
	m = drive(t, m, "right")
	if m.reviewerSub != 2 {
		t.Fatalf("after right reviewerSub = %d, want 2", m.reviewerSub)
	}
	m = drive(t, m, "right") // wrap 2 -> 0
	if m.reviewerSub != 0 {
		t.Fatalf("after wrap reviewerSub = %d, want 0", m.reviewerSub)
	}
	m = drive(t, m, "left") // wrap 0 -> 2
	if m.reviewerSub != 2 {
		t.Fatalf("after left wrap reviewerSub = %d, want 2", m.reviewerSub)
	}
	if got := reviewerNames(m); !reflect.DeepEqual(got, before) {
		t.Fatalf("moving the sub-cursor changed reviewers: %v -> %v", before, got)
	}
}

// TestOverlayReviewerSpaceToggles verifies that space toggles exactly the
// highlighted model, the highlight does not advance, and the change persists.
func TestOverlayReviewerSpaceToggles(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	// Default reviewer set is the first model ("claude") via openOverlay.
	if !m.contains("claude") {
		t.Fatalf("expected default reviewers to contain claude, got %v", m.roleReviewrs)
	}
	// Highlight the second model and add it.
	m = drive(t, m, "right") // sub -> 1 (fable)
	m = drive(t, m, "space")
	if m.reviewerSub != 1 {
		t.Fatalf("highlight advanced after toggle: reviewerSub = %d, want 1", m.reviewerSub)
	}
	if !m.contains("fable") {
		t.Fatalf("space did not add fable: %v", m.roleReviewrs)
	}
	if f.lastRoleReq == nil {
		t.Fatal("toggling reviewer did not persist via SetRoleConfig")
	}
	if !reflect.DeepEqual(f.lastRoleReq.Reviewers, m.roleReviewrs) {
		t.Fatalf("persisted reviewers = %v, want %v", f.lastRoleReq.Reviewers, m.roleReviewrs)
	}
	// Toggle it off again — highlight stays on fable.
	m = drive(t, m, "space")
	if m.contains("fable") {
		t.Fatalf("second space did not remove fable: %v", m.roleReviewrs)
	}
	if m.reviewerSub != 1 {
		t.Fatalf("highlight moved: reviewerSub = %d, want 1", m.reviewerSub)
	}
}

// TestOverlayReviewerInvariant verifies untoggling the last reviewer restores a
// model so a session never points at zero reviewers.
func TestOverlayReviewerInvariant(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	// Default set is just ["claude"], highlighted at index 0. Untoggle it.
	m = drive(t, m, "space")
	if len(m.roleReviewrs) == 0 {
		t.Fatal("non-empty reviewer invariant violated: reviewers is empty")
	}
	if f.lastRoleReq == nil || len(f.lastRoleReq.Reviewers) == 0 {
		t.Fatalf("invariant restore did not persist a non-empty set: %+v", f.lastRoleReq)
	}
}

// TestOverlayReviewerEnterPersists is a regression test: enter on the reviewers
// row must both toggle and persist (previously it toggled without persisting).
func TestOverlayReviewerEnterPersists(t *testing.T) {
	m := overlayToReviewers(t)
	f := m.client.(*fakeClient)
	m = drive(t, m, "right") // highlight fable
	m = drive(t, m, "enter")
	if !m.contains("fable") {
		t.Fatalf("enter did not toggle fable: %v", m.roleReviewrs)
	}
	if f.lastRoleReq == nil {
		t.Fatal("enter on reviewers row did not persist via SetRoleConfig")
	}
	if !reflect.DeepEqual(f.lastRoleReq.Reviewers, m.roleReviewrs) {
		t.Fatalf("persisted reviewers = %v, want %v", f.lastRoleReq.Reviewers, m.roleReviewrs)
	}
}

// TestOverlayReviewerHighlightVisible verifies overlayView highlights the chip
// the next toggle affects when the cursor is on the reviewers row, and renders
// the chips plain when it is not.
func TestOverlayReviewerHighlightVisible(t *testing.T) {
	m := overlayToReviewers(t)
	m = drive(t, m, "right") // highlight fable (index 1)
	// Distinct styling means the raw view differs from the ANSI-stripped view
	// around the highlighted chip.
	view := m.overlayView()
	if !strings.Contains(stripANSI(view), "[ ] fable") {
		t.Fatalf("reviewers row missing fable chip:\n%s", stripANSI(view))
	}
	styled := selStyle.Render("[ ] fable")
	if !strings.Contains(view, styled) {
		t.Fatalf("highlighted chip not styled with selStyle when cursor on reviewers row:\n%s", view)
	}
	// Move the cursor off the reviewers row: the chip should no longer be styled.
	m = drive(t, m, "up") // reviewers -> impl
	view = m.overlayView()
	if strings.Contains(view, styled) {
		t.Fatalf("chip still highlighted when cursor is off the reviewers row:\n%s", view)
	}
}

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

// TestModelBackendsAuthPicker switches an anthropic model to subscription
// (oauth) auth via the form's auth field and verifies the choice round-trips
// (spec §13, task 0194).
func TestModelBackendsAuthPicker(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", KeyEnv: "ANTHROPIC_API_KEY"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e") // edit; focus starts on backend
	// Move to the auth field (backend -> base_url -> model -> key_env -> auth).
	for i := 0; i < 4; i++ {
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right") // api key -> oauth
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("after right auth=%q, want oauth", got)
	}
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Auth != "oauth" {
		t.Fatalf("UpsertModel auth=%v, want oauth", f.lastUpsert)
	}
}

// TestModelBackendsAuthPrefillAndEditPreserved: an oauth model loads with the
// picker on oauth, and an unrelated edit keeps the auth value (no silent
// downgrade to api-key).
func TestModelBackendsAuthPrefillAndEditPreserved(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3", Auth: "oauth"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "e")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("prefill auth=%q, want oauth", got)
	}
	// Edit only the model id, then save: auth must survive.
	m = drive(t, m, "tab") // base_url
	m = drive(t, m, "tab") // model
	m = typeText(t, m, "-opus")
	m = drive(t, m, "enter")
	if f.lastUpsert == nil || f.lastUpsert.Auth != "oauth" {
		t.Fatalf("UpsertModel auth=%v, want oauth preserved", f.lastUpsert)
	}
}

// TestModelBackendsAuthCodexPresets: in add mode, selecting oauth on an
// openai connection re-seeds the curated model ids with the codex catalog
// (the platform ids don't exist on the subscription backend), and switching
// back restores the platform set.
func TestModelBackendsAuthCodexPresets(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a")
	m = drive(t, m, "tab")   // -> backend
	m = drive(t, m, "right") // anthropic -> openai (reseeds platform ids)
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(mbModelPresets["openai"], " ") {
		t.Fatalf("model ids = %q, want openai presets", got)
	}
	for i := 0; i < 4; i++ { // backend -> base_url -> model -> key_env -> auth
		m = drive(t, m, "tab")
	}
	m = drive(t, m, "right") // api key -> oauth: codex catalog
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(codex.Models, " ") {
		t.Fatalf("model ids = %q, want codex catalog", got)
	}
	m = drive(t, m, "left") // back to api key: platform ids restored
	if got := m.mbInputs[mbFieldModel].Value(); got != strings.Join(mbModelPresets["openai"], " ") {
		t.Fatalf("model ids = %q, want openai presets restored", got)
	}
}

// TestModelBackendsAuthSupportedBackendsOnly: the auth picker is pinned to
// api-key on backends without subscription support (ollama), and switching
// the backend to one of those resets a selected oauth back to api-key.
// Anthropic → openai keeps the selection (both support oauth).
func TestModelBackendsAuthSupportedBackendsOnly(t *testing.T) {
	f := newFakeClient(&v1.ModelConfig{Name: "claude", Backend: "anthropic", Model: "claude-3"})
	m := newBackendsModel(f)
	m = runCmds(t, m, m.fetchModels)
	m = drive(t, m, "a") // add form, backend defaults to anthropic, focus on name
	// Select oauth on the anthropic backend.
	for i := 0; i < 5; i++ { // name -> backend -> base_url -> model -> key_env -> auth
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("auth=%q, want oauth", got)
	}
	// Back to the backend field.
	for i := 0; i < 4; i++ {
		m = drive(t, m, "shift+tab")
	}
	if m.mbFocus != mbFieldBackend {
		t.Fatalf("focus=%d, want mbFieldBackend(%d)", m.mbFocus, mbFieldBackend)
	}
	// anthropic -> openai keeps oauth (both subscription-capable).
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "oauth" {
		t.Fatalf("after anthropic->openai auth=%q, want oauth kept", got)
	}
	// openai -> ollama resets to the api-key default.
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "" {
		t.Fatalf("after switch to ollama auth=%q, want api-key default", got)
	}
	// And the picker no longer cycles while on ollama.
	for i := 0; i < 4; i++ {
		m = drive(t, m, "tab")
	}
	if m.mbFocus != mbFieldAuth {
		t.Fatalf("focus=%d, want mbFieldAuth(%d)", m.mbFocus, mbFieldAuth)
	}
	m = drive(t, m, "right")
	if got := mbAuthList[m.mbAuthIdx]; got != "" {
		t.Fatalf("auth cycled on ollama backend: %q", got)
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
// only when a backlog task is blocked, and that pressing ctrl+w opens the backlog
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

	// Press ctrl+w: opens the backlog browser filtered to blocked tasks.
	updated, _ := m.updateMenu(keyMsg("ctrl+w"))
	m = updated.(model)
	if !m.backlog {
		t.Fatalf("after ctrl+w, backlog browser not open")
	}
	if !m.backlogBlockedOnly {
		t.Fatalf("after ctrl+w, backlogBlockedOnly not set")
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

// TestBlockedIndicatorBareWTypes ensures a naked "w" always types into the
// prompt — menu affordances are ctrl-chords, so a bare letter never triggers
// anything even when tasks are blocked — and that ctrl+w is a no-op (falls
// through) when nothing is blocked (task 0101).
func TestBlockedIndicatorBareWTypes(t *testing.T) {
	// Blocked tasks present: a naked "w" still just types.
	m := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "blocked", Title: "a"},
	}}
	m.prompt = newChatInput("test")
	m.prompt.Focus()
	updated, _ := m.updateMenu(keyMsg("w"))
	m = updated.(model)
	if m.backlog {
		t.Fatalf("naked w opened the backlog browser")
	}
	if m.prompt.Value() != "w" {
		t.Fatalf("w not typed into prompt, got %q", m.prompt.Value())
	}

	// Nothing blocked: ctrl+w does not open the browser.
	m2 := model{state: stateMenu, backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a"},
	}}
	m2.prompt = newChatInput("test")
	m2.prompt.Focus()
	updated, _ = m2.updateMenu(keyMsg("ctrl+w"))
	m2 = updated.(model)
	if m2.backlog {
		t.Fatalf("ctrl+w opened backlog browser when nothing is blocked")
	}
}

// TestWaitingSessionIndicator verifies the home-menu "session waiting for you"
// indicator: absent when nothing needs the user, present with a count when a
// live session has a pending question or is paused, and that pressing ctrl+s
// attaches directly (one session) or opens the filtered browser (several), while
// a naked "s" types into the prompt and never triggers anything (task 0107).
func TestWaitingSessionIndicator(t *testing.T) {
	// (a) No waiting sessions: line absent.
	m := model{state: stateMenu}
	m.prompt = newChatInput("test")
	if strings.Contains(m.menuView(), "waiting for you") || strings.Contains(m.menuView(), "waiting for your answer") {
		t.Fatalf("menu shows waiting-session line when nothing needs the user:\n%s", m.menuView())
	}

	// (b) One session with a pending question: line present with count + "press ctrl+s".
	m.waitingSessions = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true},
	}
	view := m.menuView()
	if !strings.Contains(view, "1 session waiting for your answer") {
		t.Fatalf("menu missing waiting-session line:\n%s", view)
	}
	if !strings.Contains(view, "press ctrl+s to open") {
		t.Fatalf("menu missing 'ctrl+s' route hint:\n%s", view)
	}

	// (c) ctrl+s with exactly one waiting session attaches directly (reopen).
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true},
	}
	one := initialModel(context.Background(), f, t_tempWorkspace, false)
	one.waitingSessions = f.history
	one = drive(t, one, "ctrl+s")
	if f.lastReopened != "s_q" {
		t.Fatalf("ctrl+s with one waiting session reopened %q, want s_q", f.lastReopened)
	}
	if one.state != stateSession || one.sessionID != "s_q" {
		t.Fatalf("ctrl+s did not attach to the waiting session: state=%v id=%q", one.state, one.sessionID)
	}

	// (d) ctrl+s with two waiting sessions opens the browser filtered to them.
	f2 := newFakeClient()
	f2.history = []*v1.SessionSummary{
		{SessionId: "s_q", Mode: "work", Status: "running", Live: true, WaitingInput: true, LastActivity: "2024-01-02T10:00:00Z"},
		{SessionId: "s_p", Mode: "chat", Status: "paused", Live: true, LastActivity: "2024-01-01T10:00:00Z"},
		{SessionId: "s_done", Mode: "work", Status: "idle", Live: false, LastActivity: "2024-01-03T10:00:00Z"},
	}
	two := initialModel(context.Background(), f2, t_tempWorkspace, false)
	two.waitingSessions = []*v1.SessionSummary{f2.history[0], f2.history[1]}
	two = drive(t, two, "ctrl+s")
	if two.state != stateHistory {
		t.Fatalf("ctrl+s with two waiting sessions state=%v, want stateHistory", two.state)
	}
	if !two.historyWaitingOnly {
		t.Fatalf("ctrl+s with two waiting sessions did not set historyWaitingOnly")
	}
	if len(two.history) != 2 {
		t.Fatalf("waiting-only browser shows %d rows, want 2 (filtered): %+v", len(two.history), two.history)
	}
	for _, s := range two.history {
		if !s.Live || (!s.WaitingInput && s.Status != "paused") {
			t.Fatalf("waiting-only browser shows a non-waiting session: %+v", s)
		}
	}

	// (e) A naked "s" always types into the prompt, even with sessions waiting.
	typing := model{state: stateMenu, waitingSessions: f.history}
	typing.prompt = newChatInput("test")
	typing.prompt.Focus()
	typing = typeText(t, typing, "ba")
	updated, _ := typing.updateMenu(keyMsg("s"))
	typing = updated.(model)
	if typing.state != stateMenu {
		t.Fatalf("s hijacked typing: state=%v", typing.state)
	}
	if typing.prompt.Value() != "bas" {
		t.Fatalf("s not typed into prompt, got %q", typing.prompt.Value())
	}
}

// TestMenuContextHeader verifies the home-menu project-context header (task
// 0139): the project name and ready/blocked backlog counts render from the
// backlog, the git and today's-spend segments appear only when their data has
// arrived, and the spend segment stays hidden at zero cost.
func TestMenuContextHeader(t *testing.T) {
	m := model{state: stateMenu, w: 200, workspace: "/home/user/myproj", backlogTasks: []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "a", Ready: true},
		{Id: "0002", Status: "todo", Title: "b", Ready: true},
		{Id: "0003", Status: "in_progress", Title: "c", Ready: true},
		{Id: "0004", Status: "todo", Title: "d", Ready: false},
		{Id: "0005", Status: "blocked", Title: "e"},
	}}
	m.prompt = newChatInput("test")

	view := ansi.Strip(m.menuView())
	if !strings.Contains(view, "myproj") {
		t.Fatalf("header missing project name:\n%s", view)
	}
	if !strings.Contains(view, "3 ready") {
		t.Fatalf("header missing ready count (want 3):\n%s", view)
	}
	if !strings.Contains(view, "1 blocked") {
		t.Fatalf("header missing blocked count (want 1):\n%s", view)
	}
	// Git + spend segments absent until their data arrives.
	if strings.Contains(view, "⎇") {
		t.Fatalf("git segment present before git info loaded:\n%s", view)
	}
	if strings.Contains(view, "today") {
		t.Fatalf("spend segment present before spend loaded:\n%s", view)
	}

	// Git info arrives -> branch + dirty marker shown.
	updated, _ := m.Update(menuGitMsg{branch: "main", dirty: true})
	m = updated.(model)
	view = ansi.Strip(m.menuView())
	if !strings.Contains(view, "main") || !strings.Contains(view, "⎇") {
		t.Fatalf("header missing git branch after menuGitMsg:\n%s", view)
	}

	// Zero-cost spend still hides the segment.
	updated, _ = m.Update(menuSpendMsg{cost: 0, status: "priced"})
	m = updated.(model)
	if strings.Contains(ansi.Strip(m.menuView()), "today") {
		t.Fatalf("spend segment shown at zero cost:\n%s", m.menuView())
	}

	// Positive spend -> segment appears.
	updated, _ = m.Update(menuSpendMsg{cost: 1.23, status: "priced"})
	m = updated.(model)
	view = ansi.Strip(m.menuView())
	if !strings.Contains(view, "$1.23 today") {
		t.Fatalf("header missing spend segment after menuSpendMsg:\n%s", view)
	}
}

// TestMenuHeaderFitsNarrowTerminal checks the context header stays on exactly one
// physical row and never exceeds the terminal width, dropping segments by
// priority on a narrow terminal (task 0139).
func TestMenuHeaderFitsNarrowTerminal(t *testing.T) {
	for _, w := range []int{80, 40, 20, 10} {
		m := model{state: stateMenu, w: w, workspace: "/home/user/a-rather-long-project-name",
			gitBranch: "feature/some-long-branch-name", gitDirty: true,
			todaySpend: 12.34, todaySpendStatus: "priced", todaySpendLoaded: true,
			backlogTasks: []*v1.BacklogTaskSummary{
				{Id: "0001", Status: "todo", Ready: true},
				{Id: "0002", Status: "blocked"},
			}}
		header := m.menuHeader()
		if strings.Contains(header, "\n") {
			t.Fatalf("w=%d: header spans multiple rows:\n%q", w, header)
		}
		if got := lipgloss.Width(header); got > w {
			t.Fatalf("w=%d: header width %d exceeds terminal width", w, got)
		}
	}
}

// TestMenuContinueLastSession verifies the "ctrl+l continue last session"
// affordance (task 0139): with a lastSession and an empty prompt, ctrl+l reopens
// it; the footer and body advertise the affordance; and a naked "c" always types
// into the prompt.
func TestMenuContinueLastSession(t *testing.T) {
	// No last session: affordance absent.
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_last", Mode: "work", Title: "wire up the header", Status: "idle", Live: false},
	}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.w = 200
	if strings.Contains(m.menuView(), "continue last session") {
		t.Fatalf("continue affordance shown with no last session:\n%s", m.menuView())
	}

	// Deliver the recent session via the waiting-sessions message path.
	updated, _ := m.Update(waitingSessionsMsg{recent: f.history[0]})
	m = updated.(model)
	if m.lastSession == nil || m.lastSession.SessionId != "s_last" {
		t.Fatalf("lastSession not set from waitingSessionsMsg: %+v", m.lastSession)
	}
	view := m.menuView()
	if !strings.Contains(view, "continue last session") {
		t.Fatalf("menu missing continue affordance:\n%s", view)
	}
	if !strings.Contains(view, "wire up the header") {
		t.Fatalf("continue affordance missing session title:\n%s", view)
	}
	if !strings.Contains(ansi.Strip(view), "ctrl+l continue last session") {
		t.Fatalf("body missing ctrl+l continue hint:\n%s", view)
	}

	// ctrl+l with an empty prompt reopens the last session.
	m = drive(t, m, "ctrl+l")
	if f.lastReopened != "s_last" {
		t.Fatalf("ctrl+l reopened %q, want s_last", f.lastReopened)
	}
	if m.state != stateSession || m.sessionID != "s_last" {
		t.Fatalf("ctrl+l did not attach to the last session: state=%v id=%q", m.state, m.sessionID)
	}

	// A naked "c" always types into the prompt, even with a last session set.
	typing := model{state: stateMenu, lastSession: f.history[0]}
	typing.prompt = newChatInput("test")
	typing.prompt.Focus()
	typing = typeText(t, typing, "ab")
	updated, _ = typing.updateMenu(keyMsg("c"))
	typing = updated.(model)
	if typing.state != stateMenu {
		t.Fatalf("c hijacked typing: state=%v", typing.state)
	}
	if typing.prompt.Value() != "abc" {
		t.Fatalf("c not typed into prompt, got %q", typing.prompt.Value())
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

// TestSessionBrowseParity verifies task 0112: the browse selector (ctrl+o) and
// the read-only session browser modal (ctrl+r / browse → sessions) are reachable
// from within a live session, browsing there never disturbs (or reopens over) the
// live session, and esc unwinds transcript → list → session.
func TestSessionBrowseParity(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_hist", Mode: "chat", Status: "idle", Title: "old chat", LastActivity: "2024-01-01T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 10, Type: "user_input", Actor: "user", DataJson: `{"text":"replayed"}`},
		{Seq: 11, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"replayed reply"}`},
	}
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s_live", mode: "work", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	// Give the live session some events so we can detect clobbering.
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live one"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live two"}`})
	m.rebuild()
	liveDelivered := len(m.deliveredSeqs)

	// ctrl+o opens the browse selector from a session; esc returns to the session.
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector from a session")
	}
	m = drive(t, m, "esc")
	if m.browse {
		t.Fatal("esc should dismiss the browse selector")
	}
	if m.state != stateSession {
		t.Fatalf("esc from the browse selector should return to the session (state=%v)", m.state)
	}
	if len(m.evs) != 2 {
		t.Fatalf("browse selector must not touch the live session events (evs=%d, want 2)", len(m.evs))
	}

	// browse → sessions from a session opens the read-only modal, NOT stateHistory.
	m = drive(t, m, "ctrl+o")
	m = drive(t, m, "down")
	m = drive(t, m, "down")
	m = drive(t, m, "enter") // "sessions" is the third browse target
	if m.state != stateSession {
		t.Fatalf("browse → sessions from a session must stay stateSession (state=%v)", m.state)
	}
	if !m.histModal {
		t.Fatal("browse → sessions from a session should open the session browser modal")
	}
	if len(m.history) != 1 {
		t.Fatalf("session browser modal should load history (len=%d, want 1)", len(m.history))
	}
	// esc closes the modal back to the live session.
	m = drive(t, m, "esc")
	if m.histModal {
		t.Fatal("esc should close the session browser modal")
	}

	// ctrl+r from a session opens the same read-only modal directly.
	m = drive(t, m, "ctrl+r")
	if !m.histModal || m.state != stateSession {
		t.Fatalf("ctrl+r from a session should open the modal over the session (histModal=%v state=%v)", m.histModal, m.state)
	}

	// enter loads the transcript into the modal viewport WITHOUT clobbering the
	// live session's event pipeline.
	m = drive(t, m, "enter")
	if !m.histModalTranscript {
		t.Fatal("enter should drill into the read-only transcript modal")
	}
	if f.lastTransID != "s_hist" {
		t.Fatalf("GetSessionTranscript id=%q, want s_hist", f.lastTransID)
	}
	if len(m.evs) != 2 {
		t.Fatalf("modal transcript must not clobber live session evs (evs=%d, want 2)", len(m.evs))
	}
	if len(m.deliveredSeqs) != liveDelivered {
		t.Fatalf("modal transcript must not clobber live deliveredSeqs (%d, want %d)", len(m.deliveredSeqs), liveDelivered)
	}
	// The modal transcript renders the replayed events into its own viewport.
	if view := m.histModalView(); !strings.Contains(view, "transcript") {
		t.Fatalf("histModalView (transcript) missing title:\n%s", view)
	}

	// `o` in the modal transcript is a no-op (no reopen-over-live-session footgun).
	m = drive(t, m, "o")
	if f.lastReopened != "" {
		t.Fatalf("`o` in the modal transcript must not reopen (lastReopened=%q)", f.lastReopened)
	}
	if !m.histModalTranscript {
		t.Fatal("`o` in the modal transcript should be a no-op (still on the transcript)")
	}

	// esc unwinds: transcript → list.
	m = drive(t, m, "esc")
	if m.histModalTranscript {
		t.Fatal("esc should leave the transcript back to the list")
	}
	if !m.histModal {
		t.Fatal("esc from the transcript should return to the list, not close the modal")
	}

	// `o` in the modal list is also a no-op.
	m = drive(t, m, "o")
	if f.lastReopened != "" {
		t.Fatalf("`o` in the modal list must not reopen (lastReopened=%q)", f.lastReopened)
	}
	if !m.histModal {
		t.Fatal("`o` in the modal list should be a no-op (still open)")
	}

	// esc from the list closes the modal, back to the live session intact.
	m = drive(t, m, "esc")
	if m.histModal {
		t.Fatal("esc should close the session browser modal")
	}
	if m.state != stateSession || m.sessionID != "s_live" {
		t.Fatalf("after closing the modal, back to the live session (state=%v id=%q)", m.state, m.sessionID)
	}
	if len(m.evs) != 2 {
		t.Fatalf("live session events must be intact after browsing (evs=%d, want 2)", len(m.evs))
	}
}

// TestSessionBrowserModalTranscriptNav verifies task 0119: the session-browser
// modal transcript (opened OVER a live session) supports line-based `/` search
// (n/N wrap, esc-clear) and {}()<>[] jump-to-event keys, and NONE of it disturbs
// the live session behind the modal (m.evs/m.vp/m.searching/m.searchQuery).
func TestSessionBrowserModalTranscriptNav(t *testing.T) {
	f := newFakeClient()
	f.history = []*v1.SessionSummary{
		{SessionId: "s_hist", Mode: "chat", Status: "idle", Title: "old chat", LastActivity: "2024-01-01T10:00:00Z"},
	}
	f.transcript = []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"intro alpha line"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"pick something"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"middle beta content"}`},
		{Seq: 4, Type: "review_submitted", Actor: "reviewer", DataJson: `{"model":"claude","verdict":"approve","summary":"looks good"}`},
		{Seq: 5, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123","message":"do the thing"}`},
		{Seq: 6, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom failure"}`},
		{Seq: 7, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"tail alpha zebra"}`},
	}
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s_live", mode: "work", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live one"}`})
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"live two"}`})
	m.rebuild()
	// Seed a kept LIVE-session search + selection to prove they survive the modal.
	m.searchQuery = "live"
	m.selected = 1
	liveOffset := m.vp.YOffset()

	assertLiveIntact := func(where string) {
		t.Helper()
		if len(m.evs) != 2 {
			t.Fatalf("%s: live evs = %d, want 2", where, len(m.evs))
		}
		if m.searchQuery != "live" || m.searching {
			t.Fatalf("%s: live search clobbered (query=%q searching=%v)", where, m.searchQuery, m.searching)
		}
		if m.selected != 1 {
			t.Fatalf("%s: live selection clobbered (selected=%d, want 1)", where, m.selected)
		}
		if m.vp.YOffset() != liveOffset {
			t.Fatalf("%s: live viewport offset moved (%d, want %d)", where, m.vp.YOffset(), liveOffset)
		}
	}

	// Open the modal and drill into the transcript.
	m = drive(t, m, "ctrl+r")
	m = drive(t, m, "enter")
	if !m.histModalTranscript {
		t.Fatal("enter should drill into the modal transcript")
	}
	if len(m.histModalLines) == 0 {
		t.Fatal("modal transcript should capture rendered lines")
	}
	if len(m.histModalEventLines) != 7 {
		t.Fatalf("modal transcript should record 7 event start lines, got %d", len(m.histModalEventLines))
	}
	if m.histModalCurLine != -1 {
		t.Fatalf("a fresh transcript has no cursor line (got %d)", m.histModalCurLine)
	}
	assertLiveIntact("after loading the modal transcript")

	lineForType := func(typ string) int {
		for _, el := range m.histModalEventLines {
			if el.typ == typ {
				return el.line
			}
		}
		return -2
	}
	curText := func() string { return ansi.Strip(m.histModalLines[m.histModalCurLine]) }

	// Jump keys land on the recorded start line of the right event type.
	m = drive(t, m, "}") // forward to question_asked
	if m.histModalCurLine != lineForType("question_asked") {
		t.Fatalf("} should jump to question_asked (line=%d, want %d)", m.histModalCurLine, lineForType("question_asked"))
	}
	m = drive(t, m, ")") // forward to review_submitted
	if m.histModalCurLine != lineForType("review_submitted") {
		t.Fatalf(") should jump to review_submitted (line=%d, want %d)", m.histModalCurLine, lineForType("review_submitted"))
	}
	m = drive(t, m, ">") // forward to commit_made
	if m.histModalCurLine != lineForType("commit_made") {
		t.Fatalf("> should jump to commit_made (line=%d, want %d)", m.histModalCurLine, lineForType("commit_made"))
	}
	m = drive(t, m, "]") // forward to session_error
	if m.histModalCurLine != lineForType("session_error") {
		t.Fatalf("] should jump to session_error (line=%d, want %d)", m.histModalCurLine, lineForType("session_error"))
	}
	// No-wrap: another forward session_error jump is a no-op.
	atError := m.histModalCurLine
	m = drive(t, m, "]")
	if m.histModalCurLine != atError {
		t.Fatalf("] with no further error should be a no-op (line=%d, want %d)", m.histModalCurLine, atError)
	}
	// Backward jump to the (earlier) question_asked.
	m = drive(t, m, "{")
	if m.histModalCurLine != lineForType("question_asked") {
		t.Fatalf("{ should jump back to question_asked (line=%d)", m.histModalCurLine)
	}
	assertLiveIntact("after jump keys")

	// `/` search: "alpha" matches exactly two lines (intro + tail).
	var matchLines []int
	for i, ln := range m.histModalLines {
		if strings.Contains(strings.ToLower(ansi.Strip(ln)), "alpha") {
			matchLines = append(matchLines, i)
		}
	}
	if len(matchLines) != 2 {
		t.Fatalf("expected 2 lines containing alpha, got %v", matchLines)
	}
	inMatches := func(l int) bool { return l == matchLines[0] || l == matchLines[1] }

	m = drive(t, m, "/")
	if !m.histModalSearching {
		t.Fatal("`/` should start the modal search")
	}
	for _, r := range "alpha" {
		m = drive(t, m, string(r))
	}
	first := m.histModalCurLine
	if !inMatches(first) || !strings.Contains(curText(), "alpha") {
		t.Fatalf("typing should jump to a matching line (line=%d text=%q)", first, curText())
	}
	m = drive(t, m, "enter") // keep the query for n/N
	if m.histModalSearching {
		t.Fatal("enter should confirm and stop owning input")
	}
	m = drive(t, m, "n")
	second := m.histModalCurLine
	if second == first || !inMatches(second) {
		t.Fatalf("n should advance to the other match (first=%d second=%d)", first, second)
	}
	m = drive(t, m, "n")
	if m.histModalCurLine != first {
		t.Fatalf("n should wrap back to the first match (got %d, want %d)", m.histModalCurLine, first)
	}
	m = drive(t, m, "N")
	if m.histModalCurLine != second {
		t.Fatalf("N should wrap back to the other match (got %d, want %d)", m.histModalCurLine, second)
	}
	assertLiveIntact("after search n/N")

	// esc with a kept query clears it but stays in the transcript.
	m = drive(t, m, "esc")
	if m.histModalQuery != "" {
		t.Fatalf("esc should clear the kept query (got %q)", m.histModalQuery)
	}
	if !m.histModalTranscript {
		t.Fatal("esc with an active query should NOT back out of the transcript")
	}
	// A second esc backs out to the list.
	m = drive(t, m, "esc")
	if m.histModalTranscript {
		t.Fatal("a second esc should back out of the transcript to the list")
	}
	if !m.histModal {
		t.Fatal("backing out of the transcript should stay in the modal list")
	}
	if len(m.histModalLines) != 0 {
		t.Fatal("backing out should reset the modal transcript nav state")
	}
	assertLiveIntact("after backing out of the transcript")

	// esc-cancel while typing: re-enter the transcript, type, then esc cancels.
	m = drive(t, m, "enter")
	m = drive(t, m, "/")
	m = drive(t, m, "z")
	m = drive(t, m, "esc")
	if m.histModalSearching || m.histModalQuery != "" {
		t.Fatalf("esc should cancel search entry (searching=%v query=%q)", m.histModalSearching, m.histModalQuery)
	}
	if !m.histModalTranscript {
		t.Fatal("esc-cancel should stay in the transcript")
	}
	assertLiveIntact("after esc-cancel while typing")

	// Close the whole modal; the live session behind it is fully intact.
	m = drive(t, m, "esc") // transcript → list
	m = drive(t, m, "esc") // list → close
	if m.histModal {
		t.Fatal("esc from the list should close the modal")
	}
	assertLiveIntact("after closing the modal")
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
func TestSubscriptionUsageTUI(t *testing.T) {
	got := subscriptionUsageTUI([]*v1.SubscriptionUsageAccount{{
		Provider: "anthropic", Models: []string{"claude"}, State: "fresh",
		Windows: []*v1.SubscriptionUsageWindow{{Label: "5 hour", UsedPercent: 72.5, ResetsAtUnix: 1784548800}},
	}})
	for _, want := range []string{"Subscription allowance", "anthropic", "claude", "5 hour", "72.5%", "resets"} {
		if !strings.Contains(got, want) {
			t.Fatalf("subscription usage view missing %q:\n%s", want, got)
		}
	}
}

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

// TestCostViewScrollsWithinTerminal guards the cost-view overflow regression:
// with more usage rows than the terminal is tall, the table must window around
// the cursor (keeping it visible) instead of overrunning the screen, with the
// header and TOTAL rows pinned and a position indicator in the hint.
func TestCostViewScrollsWithinTerminal(t *testing.T) {
	m := model{cost: true, costGroupBy: []string{"task"}}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	m = updated.(model)
	for i := 0; i < 40; i++ {
		m.costRows = append(m.costRows, &v1.UsageRow{
			Task: fmt.Sprintf("task-%04d", i), Input: 10, Output: 5, Total: 15,
			Cost: 0.01, PriceStatus: "priced",
		})
	}
	m.costTotal = &v1.UsageRow{Input: 400, Output: 200, Total: 600, Cost: 0.4, PriceStatus: "priced"}
	m.costCursor = len(m.costRows) - 1

	view := m.costView()
	lines := strings.Split(view, "\n")
	if len(lines) > 14 {
		t.Fatalf("costView produced %d lines for a 14-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "task-0039") {
		t.Errorf("cursor row (last) should stay visible in the window:\n%s", view)
	}
	if !strings.Contains(view, "TOTAL") {
		t.Errorf("TOTAL row should stay pinned when scrolled:\n%s", view)
	}
	if !strings.Contains(view, "/40") {
		t.Errorf("hint should show the scroll position indicator:\n%s", view)
	}
	// Scrolling back to the top brings the first row into view.
	m.costCursor = 0
	if view := m.costView(); !strings.Contains(view, "task-0000") {
		t.Errorf("first row should be visible with the cursor at 0:\n%s", view)
	}
}

// TestModelBackendsListScrollsWithinTerminal guards the same overflow for the
// model-backends list card.
func TestModelBackendsListScrollsWithinTerminal(t *testing.T) {
	m := model{mbOpen: true}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)
	for i := 0; i < 30; i++ {
		m.models = append(m.models, &v1.ModelInfo{
			Name: fmt.Sprintf("backend-%02d", i), Backend: "openai", Model: "gpt",
		})
	}
	m.mbCursor = len(m.models) - 1

	view := m.mbListView()
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("mbListView produced %d lines for a 12-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "backend-29") {
		t.Errorf("cursor row (last) should stay visible in the window:\n%s", view)
	}
}

// TestSettingsOverlayFitsShortTerminal verifies the settings card windows its
// rows around the cursor on terminals shorter than the row list.
func TestSettingsOverlayFitsShortTerminal(t *testing.T) {
	m := model{overlay: true, thinkLevels: map[string]string{}, prefs: clientconfig.Prefs{Theme: "dark"}}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m = updated.(model)
	m.ovCursor = ovQuit // last row

	view := m.overlayView()
	lines := strings.Split(view, "\n")
	if len(lines) > 10 {
		t.Fatalf("overlayView produced %d lines for a 10-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "quit") {
		t.Errorf("cursor row (quit) should stay visible in the window:\n%s", view)
	}
}

// TestPlanDetailScrolls verifies the plan detail view renders through a
// viewport sized to the terminal so long plans scroll instead of overflowing.
func TestPlanDetailScrolls(t *testing.T) {
	m := model{}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = updated.(model)
	var body strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&body, "- plan item %d\n", i)
	}
	m.plans = true
	upd2, _ := m.Update(planDetailMsg{plan: &v1.GetPlanResponse{Name: "big", Title: "Big plan", Content: body.String()}})
	m = upd2.(model)

	view := ansi.Strip(m.plansView())
	lines := strings.Split(view, "\n")
	if len(lines) > 12 {
		t.Fatalf("planDetailView produced %d lines for a 12-row terminal:\n%s", len(lines), view)
	}
	if !strings.Contains(view, "plan item 0") {
		t.Errorf("plan detail should start at the top:\n%s", view)
	}
	if strings.Contains(view, "plan item 59") {
		t.Errorf("the tail of a long plan should be off-screen before scrolling:\n%s", view)
	}
	// Scrolling down moves the window: the top line leaves the viewport.
	for i := 0; i < 10; i++ {
		m2, _ := m.updatePlans(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = m2.(model)
	}
	if view := ansi.Strip(m.plansView()); strings.Contains(view, "plan item 0\n") {
		t.Errorf("pgdown should scroll the plan detail viewport:\n%s", view)
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

// TestPickerAnswerRefocusesInput guards the dead-input regression: answering a
// single options question via the picker blurs the textarea when the question
// arrives; committing the answer must hand focus back, or the input box drops
// every keystroke once the agent finishes (e.g. after onboarding).
func TestPickerAnswerRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if m.input.Focused() {
		t.Fatal("precondition: the picker should have blurred the textarea")
	}

	updated, _ := m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.picking {
		t.Fatal("enter should have dismissed the picker")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused again after answering via the picker")
	}
}

// TestWizardFinalPickerAnswerRefocusesInput is the wizard variant: when the LAST
// question of a multi-question batch is a picker, submitting the batch must
// re-focus the textarea (the mixed picker→free-text case is covered by
// TestWizardFreeTextAfterPickerFocusesInput; this guards the picker-last case).
func TestWizardFinalPickerAnswerRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	// Free-text question first, picker question last.
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"name?"},{"question":"db?","options":["postgres","sqlite"]}]}`,
	})
	if !m.wizActive || m.picking {
		t.Fatalf("Q1 should be free text (active=%v picking=%v)", m.wizActive, m.picking)
	}

	// Answer Q1 as free text, landing on the final picker question.
	m.input.SetValue("svc")
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)
	if !m.picking || m.wizIdx != 1 {
		t.Fatalf("Q2 should be the active picker (picking=%v idx=%d)", m.picking, m.wizIdx)
	}
	if m.input.Focused() {
		t.Fatal("precondition: the picker question should have blurred the textarea")
	}

	// Answer the final picker question: the batch submits and the input box
	// replaces the picker — it must be typable. (wizActive stays set until the
	// daemon confirms with question_answered.)
	updated, _ = m.Update(keyMsg("enter"))
	m = updated.(model)
	if m.picking {
		t.Fatal("picker should collapse once the batch submits")
	}
	if !m.input.Focused() {
		t.Fatal("textarea must be focused after the final picker answer submits the batch")
	}
}

// TestQuestionAnsweredEventRefocusesInput: the daemon's question_answered
// confirmation is the safety net — even if local state missed the re-focus,
// the event must leave the textarea typable.
func TestQuestionAnsweredEventRefocusesInput(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f)
	if m.input.Focused() {
		t.Fatal("precondition: the picker should have blurred the textarea")
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "question_answered", Actor: "user"})
	if m.picking || m.pending != "" {
		t.Fatalf("question_answered should clear the picker (picking=%v pending=%q)", m.picking, m.pending)
	}
	if !m.input.Focused() {
		t.Fatal("question_answered must re-focus the textarea")
	}
}

// TestReopenClearsStaleQuestion guards the reopen-replay variant of the dead
// input regression: a session whose log ends with an unanswered question_asked
// (e.g. it was stopped while blocked on ask_user) is repaired on reopen — the
// daemon gives the dangling ask_user call a synthetic tool result, so the
// question is no longer answerable. The session_reopened marker replayed after
// it must dismiss the stale picker/wizard and re-focus the textarea instead of
// leaving the TUI stuck on a question that can never be answered.
func TestReopenClearsStaleQuestion(t *testing.T) {
	f := newFakeClient()
	m := newPickerModel(t, f) // replayed log ends with an options question_asked
	if m.input.Focused() {
		t.Fatal("precondition: the replayed picker question should have blurred the textarea")
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "session_reopened", Actor: "system"})
	if m.picking || m.pending != "" || m.pickerOpts != nil {
		t.Fatalf("session_reopened should drop the stale picker (picking=%v pending=%q opts=%v)",
			m.picking, m.pending, m.pickerOpts)
	}
	if !m.input.Focused() {
		t.Fatal("session_reopened must re-focus the textarea")
	}
}

// The wizard (multi-question batch) variant of the stale-question reopen.
func TestReopenClearsStaleWizard(t *testing.T) {
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
		DataJson: `{"questions":[{"question":"db?","options":["postgres","sqlite"]},{"question":"name?"}]}`,
	})
	if !m.wizActive || !m.picking {
		t.Fatalf("precondition: wizard picker should be active (active=%v picking=%v)", m.wizActive, m.picking)
	}

	m.appendEvent(&v1.Event{Seq: 2, Type: "session_reopened", Actor: "system"})
	if m.wizActive || m.picking || m.pending != "" {
		t.Fatalf("session_reopened should drop the stale wizard (active=%v picking=%v pending=%q)",
			m.wizActive, m.picking, m.pending)
	}
	if !m.input.Focused() {
		t.Fatal("session_reopened must re-focus the textarea")
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

// A pending question longer than the terminal width must word-wrap in the
// picker footer (task 0149) rather than being clipped/ellipsised: the full text
// is present across wrapped lines and no rendered line overflows the width.
func TestPickerWrapsLongQuestion(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)

	long := strings.TrimSpace(strings.Repeat("wrapme ", 30))
	m.appendEvent(&v1.Event{
		Seq: 1, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"` + long + `","options":["yes","no"]}`,
	})
	if !m.picking || m.wizActive {
		t.Fatalf("expected a single-question picker (picking=%v wizActive=%v)", m.picking, m.wizActive)
	}

	assertPickerWraps := func(width int) {
		picker := m.pickerView()
		for _, ln := range strings.Split(picker, "\n") {
			if w := lipgloss.Width(ln); w > m.w {
				t.Fatalf("width %d: picker line width %d exceeds %d: %q", width, w, m.w, ln)
			}
		}
		joined := strings.ReplaceAll(stripANSI(picker), "\n", " ")
		if got := strings.Count(joined, "wrapme"); got != 30 {
			t.Fatalf("width %d: found %d wrapme tokens (clipped?), want 30:\n%s", width, got, picker)
		}
		// The layout accounting must agree with the rendered footer height so the
		// help line / viewport math stays correct after wrapping.
		if h, want := m.footerStackHeight(), lipgloss.Height(picker); h != want {
			t.Fatalf("width %d: footerStackHeight()=%d, want lipgloss.Height(pickerView())=%d", width, h, want)
		}
	}

	assertPickerWraps(80)

	// Reflow on resize: a narrower terminal must re-wrap and still fit.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 24})
	m = updated.(model)
	assertPickerWraps(48)
}

// The multi-question wizard footer word-wraps a long question prompt (task 0149)
// instead of truncating it, and every rendered line fits the terminal width.
func TestWizardWrapsLongQuestion(t *testing.T) {
	f := newFakeClient()
	m := model{
		client: f, ctx: context.Background(),
		state: stateSession, status: "running", sessionID: "s1", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = updated.(model)

	long := strings.TrimSpace(strings.Repeat("wrapme ", 25))
	m.appendEvent(&v1.Event{
		Seq: 3, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"questions":[{"question":"` + long + `","options":["a","b"]},{"question":"short?"}]}`,
	})
	if !m.wizActive {
		t.Fatalf("expected an active wizard (wizActive=%v)", m.wizActive)
	}

	wiz := m.wizardView()
	for _, ln := range strings.Split(wiz, "\n") {
		if w := lipgloss.Width(ln); w > m.w {
			t.Fatalf("wizard line width %d exceeds %d: %q", w, m.w, ln)
		}
	}
	joined := strings.ReplaceAll(stripANSI(wiz), "\n", " ")
	if got := strings.Count(joined, "wrapme"); got != 25 {
		t.Fatalf("found %d wrapme tokens (truncated?), want 25:\n%s", got, wiz)
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
// asserts the distinct segments (mode, model, thinking, token readout) appear in
// the intended order, and that the interaction-level segment is gone. It also
// verifies the bar stays exactly one physical row at a narrow width (no wrap).
func TestStatusBarSegments(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "implement", level: "judgement",
		sessionID: "sess12345678", w: 120, roleCoord: "claude-opus",
		thinkLevels:  map[string]string{"coordinator": "high"},
		usageByModel: map[string]event.Usage{"claude": {Input: 12000, Output: 6000, Total: 18000}},
		pricing:      map[string]config.Pricing{"claude": {Input: 3, Output: 15, Configured: true}},
	}
	bar := m.statusBar()
	for _, want := range []string{"implement", "claude-opus", "high", "18.0k", "$"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("status bar missing %q:\n%s", want, bar)
		}
	}
	if strings.Contains(bar, "judgement") || strings.Contains(bar, "lvl ") {
		t.Fatalf("status bar must not show interaction level:\n%s", bar)
	}
	if modelAt, reasoningAt := strings.Index(bar, "claude-opus"), strings.Index(bar, "high"); modelAt < 0 || reasoningAt < 0 || modelAt > reasoningAt {
		t.Fatalf("coordinator model must appear before reasoning level:\n%s", bar)
	}

	// A recorded coordinator turn is authoritative and updates the bar on live
	// delivery and replay; non-coordinator turns must not replace it.
	m.expanded, m.bodyCache, m.blockCache, m.hiddenCache = map[int]bool{}, map[int]string{}, map[int]string{}, map[int]bool{}
	m.appendEvent(&v1.Event{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"model_name":"gpt-5.6-sol","text":"hello"}`})
	if got := m.roleCoord; got != "gpt-5.6-sol" {
		t.Fatalf("coordinator turn set roleCoord=%q, want gpt-5.6-sol", got)
	}
	m.appendEvent(&v1.Event{Seq: 2, Type: "model_turn", Actor: "implementer", DataJson: `{"model_name":"claude-sonnet","text":"done"}`})
	if got := m.roleCoord; got != "gpt-5.6-sol" {
		t.Fatalf("implementer turn changed roleCoord=%q", got)
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

// TestStatusBarShowsFocusedTask: a task_focus event surfaces which backlog task
// the session is working on in the header — id plus (truncated) title when the
// event carries one — and a later focus replaces the readout.
func TestStatusBarShowsFocusedTask(t *testing.T) {
	m := model{
		state: stateSession, status: "running", mode: "work", w: 120,
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
	}
	m.appendEvent(&v1.Event{Seq: 1, Type: "task_focus", Actor: "coordinator", DataJson: `{"task":"0007","title":"Fix the frobnicator"}`})
	bar := m.statusBar()
	for _, want := range []string{"task", "0007", "Fix the frobnicator"} {
		if !strings.Contains(bar, want) {
			t.Fatalf("status bar missing %q after task_focus:\n%s", want, bar)
		}
	}

	// A new focus replaces the old one; a title-less event still shows the id.
	m.appendEvent(&v1.Event{Seq: 2, Type: "task_focus", Actor: "coordinator", DataJson: `{"task":"0009"}`})
	bar = m.statusBar()
	if !strings.Contains(bar, "0009") || strings.Contains(bar, "0007") {
		t.Fatalf("status bar should show the new focus 0009 only:\n%s", bar)
	}
}

// TestHistoryRowsPrefixFocusTasks: the session browser prefixes each row's title
// with the backlog task(s) the session worked, so the list shows at a glance
// which task each session was on; sessions with no focus are unprefixed.
func TestHistoryRowsPrefixFocusTasks(t *testing.T) {
	m := model{w: 100, history: []*v1.SessionSummary{
		{SessionId: "sess-a", Title: "make the tests pass", Mode: "work", Status: "done", FocusTasks: []string{"0007", "0009"}},
		{SessionId: "sess-b", Title: "just a chat", Mode: "chat", Status: "idle"},
	}}
	rows := m.historyRows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if !strings.Contains(rows[0].text, "[0007,0009] make the tests pass") {
		t.Fatalf("focused session row should prefix its tasks: %q", rows[0].text)
	}
	if strings.Contains(rows[1].text, "[") {
		t.Fatalf("unfocused session row must not carry a task prefix: %q", rows[1].text)
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
		{Id: "0001", Status: "proposed", Priority: 1, Ready: true}, // ready but not yet accepted -> never picked
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

// A task created while the loop is already running — with status todo and its
// dependencies satisfied — is picked up by the NEXT loop iteration without
// restarting the loop, because the driver re-reads the live backlog each cycle
// (task 0168). Ineligible additions (proposed, dependency-blocked, blocked) are
// skipped, exactly like the initial pick.
func TestLoopPicksUpTaskAddedMidLoop(t *testing.T) {
	fc := newFakeClient()
	fc.backlogList = []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Priority: 2, Ready: true},
	}
	m := model{looping: true, loopStarted: false, loopPrevFP: "", client: fc, ctx: context.Background(), state: stateSession}

	// First decision: pick 0001 and start the loop. loopRun is initialised here so
	// the streamClosedMsg branch below records a per-session snapshot as the real
	// loop does.
	next1, cmd1 := m.applyLoopDecision(loopDecisionMsg{
		next:  topReadyTask(fc.backlogList),
		fp:    backlogFingerprint(fc.backlogList),
		tasks: fc.backlogList,
	})
	m = next1.(model)
	if !m.looping || !m.loopStarted || cmd1 == nil {
		t.Fatalf("expected loop to start on first decision, looping=%v started=%v cmd=%v", m.looping, m.loopStarted, cmd1 != nil)
	}

	// Simulate mid-loop backlog changes: 0001 finished, plus several tasks added
	// while the loop was running. Only 0002 (todo + ready) is eligible.
	fc.backlogList = []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "done", Priority: 2, Ready: true},
		{Id: "0002", Status: "todo", Priority: 3, Ready: true},     // added mid-loop -> next pick
		{Id: "0003", Status: "proposed", Priority: 1, Ready: true}, // not yet accepted -> skip
		{Id: "0004", Status: "todo", Priority: 1, Ready: false},    // dependency-blocked -> skip
		{Id: "0005", Status: "blocked", Priority: 1, Ready: true},  // needs user -> skip
	}

	// Drive the next iteration exactly as the real loop does: the finished session's
	// stream closes while looping, which returns loopNext().
	upd, cmd := m.Update(streamClosedMsg{})
	m = upd.(model)
	if cmd == nil {
		t.Fatal("expected streamClosedMsg (looping) to return a loopNext command")
	}

	// Execute the loopNext command against the fake client to get the decision.
	dmsg, ok := cmd().(loopDecisionMsg)
	if !ok {
		t.Fatalf("expected loopDecisionMsg from loopNext, got %T", cmd())
	}
	if dmsg.next != "0002" {
		t.Fatalf("expected mid-loop-added 0002 to be picked next, got %q", dmsg.next)
	}

	// Applying the decision continues the loop (fingerprint changed since 0001 is
	// now done, so this is not treated as a stall).
	next2, cmd2 := m.applyLoopDecision(dmsg)
	m2 := next2.(model)
	if !m2.looping {
		t.Fatalf("expected loop to continue after picking up mid-loop task, looping=%v", m2.looping)
	}
	if m2.loopPrevFP != dmsg.fp {
		t.Fatalf("expected loopPrevFP updated to %q, got %q", dmsg.fp, m2.loopPrevFP)
	}
	if cmd2 == nil {
		t.Fatal("expected a startSession command for the picked-up task")
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

// The home menu starts at judgement each launch (not persisted — §18.2), and
// ←/→ cycle the selector through interactive/judgement/autonomous when the prompt
// is empty. With a typed prompt, the arrows move the cursor and leave the level
// untouched. The chosen level is passed to StartSession on enter, while a loop
// start always forces autonomous regardless of the selector.
func TestMenuLevelSelector(t *testing.T) {
	// Default is judgement on a fresh model.
	m := newBackendsModel(newFakeClient())
	m.state = stateMenu
	if m.menuLevel != "judgement" {
		t.Fatalf("fresh menuLevel = %q, want judgement", m.menuLevel)
	}

	// right cycles judgement → autonomous; left wraps autonomous → judgement etc.
	step := func(key, want string) {
		t.Helper()
		updated, _ := m.updateMenu(keyMsg(key))
		m = updated.(model)
		if m.menuLevel != want {
			t.Fatalf("after %s, menuLevel = %q, want %q", key, m.menuLevel, want)
		}
	}
	step("right", "autonomous")
	step("right", "interactive") // wraps
	step("left", "autonomous")   // wraps back
	step("left", "judgement")

	// The level pill documents/shows the selector (the footer stays minimal;
	// the full catalog lives in the help modal).
	view := ansi.Strip(m.menuView())
	if !strings.Contains(view, "←/→") {
		t.Fatalf("menu missing ←/→ level hint:\n%s", view)
	}
	if !strings.Contains(view, "judgement") {
		t.Fatalf("menu view missing level pill:\n%s", view)
	}

	// With a typed prompt, ←/→ move the cursor and leave the level unchanged.
	m.mbOpen = false
	m.prompt.Focus()
	m = typeText(t, m, "fix the bug")
	if strings.TrimSpace(m.prompt.Value()) == "" {
		t.Fatalf("prompt did not receive typed text")
	}
	updated, _ := m.updateMenu(keyMsg("right"))
	m = updated.(model)
	if m.menuLevel != "judgement" {
		t.Fatalf("with a typed prompt, right changed menuLevel to %q", m.menuLevel)
	}
}

// The enter path starts the selected session at the level chosen on the home menu.
func TestMenuEnterUsesSelectedLevel(t *testing.T) {
	fc := newFakeClient()
	m := newBackendsModel(fc)
	m.state = stateMenu
	m.prompt = newChatInput("test")
	m.entries = []menuEntry{{label: "chat", description: "chat mode", mode: "chat"}}
	m.cursor = 0
	m.menuLevel = "interactive"

	updated, cmd := m.updateMenu(keyMsg("enter"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected a startSession command from enter")
	}
	cmd() // executes StartSession against the fake client
	if fc.lastStartLevel != "interactive" {
		t.Fatalf("enter started level %q, want interactive", fc.lastStartLevel)
	}
}

// A loop start forces autonomous even when the home-menu selector picked a
// less-autonomous level (the loop path must never block on ask_user).
func TestMenuLoopForcesAutonomousDespiteSelector(t *testing.T) {
	fc := newFakeClient()
	m := &model{looping: true, loopStarted: false, loopPrevFP: "", menuLevel: "interactive", client: fc, ctx: context.Background()}
	_, cmd := m.applyLoopDecision(loopDecisionMsg{next: "0003", fp: "0003:todo"})
	if cmd == nil {
		t.Fatal("expected a startSession command from the loop")
	}
	cmd()
	if fc.lastStartLevel != "autonomous" {
		t.Fatalf("loop start used level %q despite selector, want autonomous", fc.lastStartLevel)
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

// A transient (broadcast-only) event — Seq=0, Transient=true, e.g. turn_delta —
// must be ignored by the event loop: it never enters m.evs, never advances seq
// tracking, and never corrupts reducer-fed state. There is no rendering yet
// (task 0114); the TUI just tolerates the seq-less event safely.
func TestEvMsgIgnoresTransientEvents(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.sessionID = "s1"
	m.client = newFakeClient()
	m.ctx = context.Background()
	m.events = make(chan *v1.Event, 4)

	// A normal persisted event is recorded.
	real := &v1.Event{Seq: 1, Type: "model_turn", DataJson: `{"text":"hi"}`}
	nm, _ := m.Update(evMsg{real})
	m = nm.(model)
	if len(m.evs) != 1 {
		t.Fatalf("after persisted event len(evs) = %d, want 1", len(m.evs))
	}

	// A transient event must be dropped: len(evs) stays 1.
	trans := &v1.Event{Seq: 0, Type: "turn_delta", Transient: true, DataJson: `{"text":"par"}`}
	nm, _ = m.Update(evMsg{trans})
	m = nm.(model)
	if len(m.evs) != 1 {
		t.Fatalf("transient event was not ignored: len(evs) = %d, want 1", len(m.evs))
	}
	if m.evs[0].Seq != 1 || m.evs[0].Type != "model_turn" {
		t.Fatalf("transient event corrupted event list: %+v", m.evs[0])
	}

	// Transient events queued on the stream (batched drain path) are also skipped.
	m.events <- &v1.Event{Seq: 0, Type: "turn_delta", Transient: true}
	m.events <- &v1.Event{Seq: 2, Type: "tool_call", DataJson: `{"name":"x"}`}
	nm, _ = m.Update(evMsg{&v1.Event{Seq: 0, Type: "turn_delta", Transient: true}})
	m = nm.(model)
	if len(m.evs) != 2 {
		t.Fatalf("batched drain mishandled transients: len(evs) = %d, want 2", len(m.evs))
	}
	if m.evs[1].Seq != 2 || m.evs[1].Type != "tool_call" {
		t.Fatalf("persisted event lost/corrupted in batched drain: %+v", m.evs[1])
	}
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

// --- return to menu from a finished session (task 0127) ---

// runBatch executes a command and every sub-command a tea.BatchMsg fans out,
// discarding follow-up messages. It exists so tests can observe RPC side effects
// (e.g. StopSession) issued inside a tea.Batch returned by Update.
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
}

// A finished, idle (non-looping) session offers `q` as a clean way back to the
// menu: it flips to stateMenu and stops the still-alive daemon session exactly
// once with the old id (so no orphaned session is left behind).
func TestSessionQReturnsToMenuAndStopsIdle(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if !m.sessionFinished() {
		t.Fatal("setup: expected an idle non-looping session to be finished")
	}
	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateMenu {
		t.Fatalf("q on a finished idle session: state = %v, want stateMenu", m.state)
	}
	if m.sessionID != "" || m.status != "" {
		t.Fatalf("q should clear session id/status, got id=%q status=%q", m.sessionID, m.status)
	}
	runBatch(cmd)
	if fc.stopCount != 1 {
		t.Fatalf("expected StopSession issued exactly once, got %d", fc.stopCount)
	}
	if fc.lastStopped != "s1" {
		t.Fatalf("expected StopSession for s1, got %q", fc.lastStopped)
	}
}

// A finished session whose stream already closed needs no StopSession — the
// session is already gone, so `q` returns to the menu without an RPC.
func TestSessionQReturnsToMenuStreamClosedNoStop(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "stream closed"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	updated, cmd := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateMenu {
		t.Fatalf("q on a stream-closed session: state = %v, want stateMenu", m.state)
	}
	runBatch(cmd)
	if fc.stopCount != 0 {
		t.Fatalf("stream-closed session should not issue StopSession, got %d calls", fc.stopCount)
	}
}

// While the session is still running, `q` is not a menu-exit — it types into the
// input like any other character and leaves the session untouched.
func TestSessionQTypesWhileRunning(t *testing.T) {
	m := newSessionTextareaModel(t) // status defaults to "running"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if m.sessionFinished() {
		t.Fatal("setup: a running session must not be considered finished")
	}
	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q while running: state = %v, want stateSession", m.state)
	}
	if m.input.Value() != "q" {
		t.Fatalf("q while running should type into the input, got %q", m.input.Value())
	}
	if fc.stopCount != 0 {
		t.Fatalf("q while running must not issue StopSession, got %d calls", fc.stopCount)
	}
}

// Even on a finished session, `q` types normally when the input is non-empty so
// it never hijacks composition mid-message.
func TestSessionQTypesWhenInputNonEmpty(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()
	m = typeText(t, m, "hi")

	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q with non-empty input: state = %v, want stateSession", m.state)
	}
	if m.input.Value() != "hiq" {
		t.Fatalf("q with non-empty input should type, got %q", m.input.Value())
	}
	if fc.stopCount != 0 {
		t.Fatalf("q with non-empty input must not issue StopSession, got %d calls", fc.stopCount)
	}
}

// A looping (idle) session belongs to the work-loop driver, which owns the
// idle→stop→advance transition. The `q` binding must NOT hijack it back to menu.
func TestSessionQIgnoredWhileLooping(t *testing.T) {
	m := newSessionTextareaModel(t)
	m.status = "idle"
	m.looping = true
	m.mode = "work"
	m.sessionID = "s1"
	fc := newFakeClient()
	m.client = fc
	m.ctx = context.Background()

	if m.sessionFinished() {
		t.Fatal("a looping session must never be considered finished (loop owns it)")
	}
	updated, _ := m.Update(keyMsg("q"))
	m = updated.(model)
	if m.state != stateSession {
		t.Fatalf("q while looping: state = %v, want stateSession", m.state)
	}
}

// The footer surfaces the "return to menu" affordance only once the session is
// finished — never while it is still running.
func TestSessionViewFinishedHint(t *testing.T) {
	m := newSessionTextareaModel(t)

	m.status = "running"
	if strings.Contains(m.sessionView(), "return to menu") {
		t.Fatalf("running session must not show the return-to-menu hint:\n%s", m.sessionView())
	}

	m.status = "idle"
	view := m.sessionView()
	if !strings.Contains(view, "session finished") || !strings.Contains(view, "q return to menu") {
		t.Fatalf("finished session should show the return-to-menu hint, got:\n%s", view)
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

// TestMaybeNotify covers the terminal-bell / OSC 9 notification gate (task
// 0108): bell/desktop emitted for genuinely-new trigger events when enabled,
// suppressed for replayed events, disabled prefs, non-trigger types, and for
// session_idle while looping.
func TestMaybeNotify(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	after := base.Add(time.Second)   // newer than notifyAfter → genuine
	before := base.Add(-time.Second) // older → replay

	// swap notifyOut and restore.
	orig := notifyOut
	defer func() { notifyOut = orig }()
	run := func(m *model, ev *v1.Event) string {
		var buf strings.Builder
		notifyOut = &buf
		m.maybeNotify(ev)
		return buf.String()
	}

	newModel := func(bell, desktop, looping bool) *model {
		return &model{
			prefs:       clientconfig.Prefs{NotifyBell: bell, NotifyDesktop: desktop},
			notifyAfter: base,
			looping:     looping,
		}
	}
	ev := func(typ, ts, dataJSON string) *v1.Event {
		return &v1.Event{Type: typ, Ts: ts, DataJson: dataJSON}
	}

	// Bell on, question after subscribe → BEL.
	if got := run(newModel(true, false, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\a" {
		t.Fatalf("bell on question_asked = %q, want BEL", got)
	}
	// Desktop on (bell off) → OSC 9 with question text, no BEL.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\x1b]9;ycc: proceed?\x07" {
		t.Fatalf("desktop on question_asked = %q", got)
	}
	// Both on → BEL then OSC 9, single write.
	if got := run(newModel(true, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "\a\x1b]9;ycc: proceed?\x07" {
		t.Fatalf("both on = %q", got)
	}
	// Replayed event (ts before notifyAfter) → nothing.
	if got := run(newModel(true, true, false), ev("question_asked", before.Format(time.RFC3339), `{"question":"proceed?"}`)); got != "" {
		t.Fatalf("replayed event should be silent, got %q", got)
	}
	// Both prefs off → nothing.
	if got := run(newModel(false, false, false), ev("question_asked", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("prefs off should be silent, got %q", got)
	}
	// Non-trigger type → nothing.
	if got := run(newModel(true, true, false), ev("model_turn", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("non-trigger type should be silent, got %q", got)
	}
	// Looping suppresses session_idle bell.
	if got := run(newModel(true, false, true), ev("session_idle", after.Format(time.RFC3339), "")); got != "" {
		t.Fatalf("looping session_idle should be silent, got %q", got)
	}
	// Looping does NOT suppress session_error.
	if got := run(newModel(true, false, true), ev("session_error", after.Format(time.RFC3339), "")); got != "\a" {
		t.Fatalf("looping session_error should ring, got %q", got)
	}
	// question_asked with no question text → generic desktop label.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), "")); got != "\x1b]9;ycc: question waiting\x07" {
		t.Fatalf("empty question desktop = %q", got)
	}
	// Auto-answered question (autonomous mode) → silent.
	if got := run(newModel(true, true, false), ev("question_asked", after.Format(time.RFC3339), `{"question":"proceed?","auto":true}`)); got != "" {
		t.Fatalf("auto:true question should be silent, got %q", got)
	}
	// Batch (multi-question) ask carries prompts under "questions"; desktop text
	// uses the first prompt.
	if got := run(newModel(false, true, false), ev("question_asked", after.Format(time.RFC3339), `{"questions":[{"question":"first?"},{"question":"second?"}]}`)); got != "\x1b]9;ycc: first?\x07" {
		t.Fatalf("batch questions desktop = %q", got)
	}
	// Unparseable timestamp → nothing (can't tell replay from live).
	if got := run(newModel(true, true, false), ev("question_asked", "not-a-time", "")); got != "" {
		t.Fatalf("bad ts should be silent, got %q", got)
	}
}

// TestSanitizeNotifyRuneBoundary verifies truncation happens on a rune boundary
// so a multibyte rune is never split (task 0108 polish).
func TestSanitizeNotifyRuneBoundary(t *testing.T) {
	// 130 multibyte runes (é is 2 bytes) — must truncate to 120 whole runes.
	in := strings.Repeat("é", 130)
	got := sanitizeNotify(in)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 120 {
		t.Fatalf("rune count after truncation = %d, want 120", n)
	}
}

// isQuit reports whether cmd (or a batch containing it) yields tea.QuitMsg.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if isQuit(c) {
				return true
			}
		}
	}
	return false
}

// TestQuitGuardOneShotRunning covers task 0109: on a one-shot daemon with a live
// running session, the first ctrl+c arms the guard (no quit) and shows a warning;
// a second ctrl+c quits.
func TestQuitGuardOneShotRunning(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false) // one-shot
	m.w, m.h = 80, 24
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"

	updated, cmd := m.Update(keyMsg("ctrl+c"))
	m = updated.(model)
	if isQuit(cmd) {
		t.Fatal("first ctrl+c on a running one-shot session should NOT quit")
	}
	if !m.quitArmed {
		t.Fatal("first ctrl+c should arm the quit guard")
	}
	if view := m.render(); !strings.Contains(view, quitGuardHint) {
		t.Fatalf("armed view should show the quit-guard warning; got:\n%s", view)
	}

	_, cmd = m.Update(keyMsg("ctrl+c"))
	if !isQuit(cmd) {
		t.Fatal("second ctrl+c should quit")
	}
}

// TestQuitGuardIdleImmediate: no live work → ctrl+c quits at once.
func TestQuitGuardIdleImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status, m.sessionID = stateSession, "idle", "sess-1"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on an idle session should quit immediately")
	}
}

// TestQuitGuardPersistentImmediate: on a persistent daemon the work survives, so
// quit stays immediate even while running.
func TestQuitGuardPersistentImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, true) // persistent
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on a persistent daemon should quit immediately")
	}
}

// TestQuitGuardMenuImmediate: home menu with no live session → immediate quit.
func TestQuitGuardMenuImmediate(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status = stateMenu, "idle"
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("ctrl+c on the home menu with no live session should quit immediately")
	}
}

// TestQuitGuardOverlayRow: the settings-overlay Quit row uses the same guard.
func TestQuitGuardOverlayRow(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.w, m.h = 80, 24
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"
	m.openOverlay()
	// Point the cursor at the Quit row.
	m.ovCursor = ovQuit

	updated, cmd := m.Update(keyMsg("enter"))
	m = updated.(model)
	if isQuit(cmd) {
		t.Fatal("first activation of the overlay Quit row should NOT quit while running")
	}
	if !m.quitArmed {
		t.Fatal("first overlay Quit activation should arm the guard")
	}

	_, cmd = m.Update(keyMsg("enter"))
	if !isQuit(cmd) {
		t.Fatal("second overlay Quit activation should quit")
	}
}

// TestQuitGuardDisarm: a matching quitDisarmMsg clears the armed state (so the
// next ctrl+c re-arms instead of quitting), while a stale seq is ignored.
func TestQuitGuardDisarm(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.state, m.status, m.sessionID = stateSession, "running", "sess-1"

	updated, _ := m.Update(keyMsg("ctrl+c"))
	m = updated.(model)
	if !m.quitArmed {
		t.Fatal("ctrl+c should arm the guard")
	}
	seq := m.quitSeq

	// Stale seq: ignored.
	updated, _ = m.Update(quitDisarmMsg{seq: seq - 1})
	m = updated.(model)
	if !m.quitArmed {
		t.Fatal("stale quitDisarmMsg must not disarm the guard")
	}

	// Matching seq: disarms.
	updated, _ = m.Update(quitDisarmMsg{seq: seq})
	m = updated.(model)
	if m.quitArmed {
		t.Fatal("matching quitDisarmMsg should disarm the guard")
	}

	// Next ctrl+c re-arms (does not quit).
	_, cmd := m.Update(keyMsg("ctrl+c"))
	if isQuit(cmd) {
		t.Fatal("after disarm, next ctrl+c should re-arm, not quit")
	}
}

// TestBacklogMultiSelectSpawn covers the multi-select "run in parallel" flow
// (task 0085): space toggles selection on todo tasks, P spawns one workstream per
// selected task with the right project/task ids and opens the Workstreams panel.
func TestBacklogMultiSelectSpawn(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.project = "demo"
	m.backlog = true
	m.backlogTasks = []*v1.BacklogTaskSummary{
		{Id: "0001", Status: "todo", Title: "alpha"},
		{Id: "0002", Status: "todo", Title: "beta"},
		{Id: "0003", Status: "in_progress", Title: "gamma"},
	}

	// Select the first todo task, move down, select the second.
	m = drive(t, m, "space")
	m = drive(t, m, "down")
	m = drive(t, m, "space")
	if len(m.backlogSelected) != 2 || !m.backlogSelected["0001"] || !m.backlogSelected["0002"] {
		t.Fatalf("selection = %v, want {0001,0002}", m.backlogSelected)
	}

	// Move to the in_progress task and try to select it — not spawnable.
	m = drive(t, m, "down")
	m = drive(t, m, "space")
	if m.backlogSelected["0003"] {
		t.Fatal("non-todo task should not be selectable")
	}

	// P spawns one workstream per selected task and opens the panel.
	m = drive(t, m, "P")
	if len(f.spawnReqs) != 2 {
		t.Fatalf("SpawnWorkstream calls = %d, want 2", len(f.spawnReqs))
	}
	gotTasks := map[string]bool{}
	for _, r := range f.spawnReqs {
		if r.Project != "demo" {
			t.Fatalf("spawn project = %q, want demo", r.Project)
		}
		if r.InteractionLevel != "judgement" {
			t.Fatalf("spawn level = %q, want judgement", r.InteractionLevel)
		}
		gotTasks[r.TaskId] = true
	}
	if !gotTasks["0001"] || !gotTasks["0002"] {
		t.Fatalf("spawned task ids = %v, want {0001,0002}", gotTasks)
	}
	if !m.ws {
		t.Fatal("spawn should open the Workstreams panel")
	}
	if m.backlog {
		t.Fatal("spawn should close the backlog browser")
	}
}

// TestSpawnRequiresProject covers the daemon-registry guard: with no project the
// spawn is refused with an explanatory notice and no RPC fires (task 0085).
func TestSpawnRequiresProject(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.project = "" // one-shot: no registered project
	m.backlog = true
	m.backlogTasks = []*v1.BacklogTaskSummary{{Id: "0001", Status: "todo", Title: "alpha"}}

	m = drive(t, m, "space")
	m = drive(t, m, "P")
	if len(f.spawnReqs) != 0 {
		t.Fatalf("SpawnWorkstream should not fire without a project, got %d", len(f.spawnReqs))
	}
	if !strings.Contains(m.backlogNotice, "project") {
		t.Fatalf("notice = %q, want an explanation about needing a project", m.backlogNotice)
	}
}

// TestWorkstreamsPanelConflictRow proves a conflict is a visually distinct row
// state (design §8): a workstream with a locally-known conflict renders loudly,
// never a silent normal status.
func TestWorkstreamsPanelConflictRow(t *testing.T) {
	m := model{
		ws: true,
		wsList: []*v1.WorkstreamInfo{
			{Id: "ws_1", TaskId: "0001", Branch: "ycc/ws/ws_1", CommitCount: 3, SessionStatus: "running", Status: "active"},
			{Id: "ws_2", TaskId: "0002", Branch: "ycc/ws/ws_2", CommitCount: 1, SessionStatus: "idle", Status: "active"},
		},
		wsLocal: map[string]string{"ws_2": "conflict"},
	}

	// wsRowStatus precedence: normal running for ws_1, conflict for ws_2.
	if s, conflict := m.wsRowStatus(m.wsList[0]); conflict || s != "running" {
		t.Fatalf("ws_1 status = (%q,%v), want (running,false)", s, conflict)
	}
	if s, conflict := m.wsRowStatus(m.wsList[1]); !conflict || s != "conflict" {
		t.Fatalf("ws_2 status = (%q,%v), want (conflict,true)", s, conflict)
	}

	view := m.workstreamsView()
	if !strings.Contains(view, "conflict") {
		t.Fatalf("panel view missing a conflict row:\n%s", view)
	}
	if !strings.Contains(view, "running") {
		t.Fatalf("panel view missing the running row:\n%s", view)
	}
}

// TestWorkstreamMergeFlow covers preview → accept → merged (task 0085, design §6).
func TestWorkstreamMergeFlow(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	// m opens the merge overlay via PreviewMerge (clean by default).
	m = drive(t, m, "m")
	if f.lastPreviewID != "ws_1" {
		t.Fatalf("PreviewMerge id = %q, want ws_1", f.lastPreviewID)
	}
	if m.wsMerge == nil || !m.wsMerge.GetClean() {
		t.Fatalf("expected a clean merge overlay, got %+v", m.wsMerge)
	}

	// enter accepts the clean merge; the row merges with a commit sha.
	m = drive(t, m, "enter")
	if f.lastMergeID != "ws_1" {
		t.Fatalf("MergeWorkstream id = %q, want ws_1", f.lastMergeID)
	}
	if m.wsMerge != nil {
		t.Fatal("merge overlay should close after a successful merge")
	}
	if !strings.Contains(m.wsNotice, "merged") {
		t.Fatalf("notice = %q, want a merged confirmation", m.wsNotice)
	}
}

// TestWorkstreamMergeConflict proves a conflicted preview cannot be silently
// merged: the overlay stays, the row is marked conflict, and enter is refused.
func TestWorkstreamMergeConflict(t *testing.T) {
	f := newFakeClient()
	f.previewResp = &v1.PreviewMergeResponse{Clean: false, Conflicts: []string{"shared.txt"}}
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	m = drive(t, m, "m")
	if m.wsMerge == nil || m.wsMerge.GetClean() {
		t.Fatalf("expected a conflicted overlay, got %+v", m.wsMerge)
	}
	if m.wsLocal["ws_1"] != "conflict" {
		t.Fatalf("wsLocal[ws_1] = %q, want conflict", m.wsLocal["ws_1"])
	}

	// enter must not merge a conflicted preview.
	m = drive(t, m, "enter")
	if f.lastMergeID != "" {
		t.Fatalf("MergeWorkstream fired on a conflicted preview (id=%q)", f.lastMergeID)
	}
}

// TestWorkstreamDiscardConfirm covers the two-step discard confirm (task 0085).
func TestWorkstreamDiscardConfirm(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	// d arms the confirm; no RPC yet.
	m = drive(t, m, "d")
	if m.wsDiscardID != "ws_1" {
		t.Fatalf("wsDiscardID = %q, want ws_1", m.wsDiscardID)
	}
	if f.lastDiscardID != "" {
		t.Fatal("discard fired before confirmation")
	}

	// y confirms the discard.
	m = drive(t, m, "y")
	if f.lastDiscardID != "ws_1" {
		t.Fatalf("DiscardWorkstream id = %q, want ws_1", f.lastDiscardID)
	}
	if m.wsDiscardID != "" {
		t.Fatal("discard confirm should clear after firing")
	}
}

// TestWorkstreamDiscardCancel proves any non-y key cancels the discard confirm.
func TestWorkstreamDiscardCancel(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s1", Status: "active"}}

	m = drive(t, m, "d")
	m = drive(t, m, "n")
	if f.lastDiscardID != "" {
		t.Fatal("discard should not fire when cancelled")
	}
	if m.wsDiscardID != "" {
		t.Fatal("discard confirm should clear on cancel")
	}
}

// TestWorkstreamDrillIntoSession proves enter on a row attaches to its session
// via ResumeSession (task 0085, design §8).
func TestWorkstreamDrillIntoSession(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m.ws = true
	m.wsList = []*v1.WorkstreamInfo{{Id: "ws_1", Branch: "ycc/ws/ws_1", SessionId: "s-ws-1", Status: "active"}}

	updated, cmd := m.updateWorkstreams(keyMsg("enter"))
	m = updated.(model)
	_ = runCmds(t, m, cmd)
	if f.lastReopened != "s-ws-1" {
		t.Fatalf("ResumeSession id = %q, want s-ws-1", f.lastReopened)
	}
}

// TestBrowseWorkstreamsRoute proves the browse selector routes to the panel.
func TestBrowseWorkstreamsRoute(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	m = drive(t, m, "ctrl+o")
	if !m.browse {
		t.Fatal("ctrl+o should open the browse selector")
	}
	// Navigate to the "workstreams" route.
	idx := -1
	for i, t := range browseTargets {
		if t.label == "workstreams" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("workstreams route missing from browseTargets")
	}
	for i := 0; i < idx; i++ {
		m = drive(t, m, "down")
	}
	m = drive(t, m, "enter")
	if !m.ws {
		t.Fatal("workstreams route should open the Workstreams panel")
	}
}

// TestHelpModalOpensAndCloses verifies `?` opens the keybinding help modal over
// the menu (empty prompt), the card shows the title and several section
// headings, and esc closes it back to the menu (task 0111).
func TestHelpModalOpensAndCloses(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	// Tall enough for the first four catalog sections to render without scrolling;
	// the catalog grows over time (task 0127 added a session binding, drag-select
	// another), so keep headroom above the "question picker" row.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 46})
	m = updated.(model)

	m = drive(t, m, "?")
	if !m.helpOpen {
		t.Fatal("`?` on the menu with an empty prompt should open the help modal")
	}
	view := m.render()
	if !strings.Contains(view, "keybindings") {
		t.Fatalf("help view missing the title:\n%s", view)
	}
	for _, want := range []string{"global", "home menu", "session", "question picker"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing section %q:\n%s", want, view)
		}
	}
	if !strings.ContainsAny(view, "╭╰│╮╯") {
		t.Fatalf("help modal does not render a rounded card:\n%s", view)
	}
	for i, ln := range strings.Split(view, "\n") {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("help view line %d width %d exceeds terminal width 80: %q", i, w, ln)
		}
	}

	m = drive(t, m, "esc")
	if m.helpOpen {
		t.Fatal("esc should close the help modal")
	}
	if m.state != stateMenu {
		t.Fatalf("closing help should return to the menu, state = %v", m.state)
	}
}

// TestHelpKeyTypesIntoNonEmptyInput verifies `?` types a literal '?' rather than
// opening the modal when the focused input is non-empty — on both the menu and a
// session (task 0111).
func TestHelpKeyTypesIntoNonEmptyInput(t *testing.T) {
	// Menu: prompt has text, so `?` types.
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updated.(model)
	m = typeText(t, m, "write a")
	m = typeText(t, m, "?")
	if m.helpOpen {
		t.Fatal("`?` should not open help while the menu prompt is non-empty")
	}
	if got := m.prompt.Value(); got != "write a?" {
		t.Fatalf("`?` did not type into the prompt: value = %q", got)
	}

	// Session: input has text, so `?` types.
	s := newSessionTextareaModel(t)
	s = typeText(t, s, "fix the")
	s = typeText(t, s, "?")
	if s.helpOpen {
		t.Fatal("`?` should not open help while the session input is non-empty")
	}
	if got := s.input.Value(); got != "fix the?" {
		t.Fatalf("`?` did not type into the session input: value = %q", got)
	}
}

// TestHelpCtrlUnderscoreOpensUnconditionally verifies ctrl+_ opens help even with
// a non-empty session input, and that `?` opens help from the question picker
// (no free-text input focused there) — task 0111.
func TestHelpCtrlUnderscoreOpensUnconditionally(t *testing.T) {
	s := newSessionTextareaModel(t)
	s = typeText(t, s, "some text")
	s = drive(t, s, "ctrl+_")
	if !s.helpOpen {
		t.Fatal("ctrl+_ should open help even with a non-empty session input")
	}

	// Picking state: `?` opens help.
	p := newSessionTextareaModel(t)
	p.picking = true
	p.pickerOpts = []string{"a", "b"}
	p = drive(t, p, "?")
	if !p.helpOpen {
		t.Fatal("`?` should open help from the question picker")
	}
}

// TestFootersMentionHelp verifies the menu and default session footers advertise
// the help key (task 0111).
func TestFootersMentionHelp(t *testing.T) {
	f := newFakeClient()
	m := initialModel(context.Background(), f, t_tempWorkspace, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	m = updated.(model)
	if !strings.Contains(m.menuView(), "? help") {
		t.Fatalf("menu footer should mention the help key:\n%s", m.menuView())
	}

	s := newSessionTextareaModel(t)
	updated, _ = s.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	s = updated.(model)
	if !strings.Contains(s.sessionView(), "? help") {
		t.Fatalf("session footer should mention the help key:\n%s", s.sessionView())
	}
}

// --- transcript search & jump-to-event navigation (task 0116) ---

// press feeds a key through Update and discards any follow-up command (avoiding
// the textarea's repeating blink tick, which drive would loop on).
func press(m model, key string) model {
	updated, _ := m.Update(keyMsg(key))
	return updated.(model)
}

// searchEvsModel builds a ready session model over the given events (input
// focused), with a rebuilt event pipeline.
func searchEvsModel(t *testing.T, evs []*v1.Event) model {
	t.Helper()
	m := model{
		state: stateSession, status: "running", mode: "implement",
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		follow: true, input: newSessionInput(),
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.evs = evs
	m.rebuild()
	return m
}

// typeSearch feeds each rune of s through Update while the search bar is active.
func typeSearch(m model, s string) model {
	for _, r := range s {
		m = press(m, string(r))
	}
	return m
}

func TestSearchableTextMatching(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"hello ALPHA world"}`},
		{Seq: 2, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"c1","name":"Read"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"c1","result":"secret payload"}`},
	})
	// Case-insensitive headline match.
	if !m.matchesQuery(0, "alpha") {
		t.Fatalf("expected case-insensitive headline match for %q", m.searchableText(0))
	}
	// A folded tool_result (hidden row) never matches on its own.
	if !m.hiddenRow(2) {
		t.Fatal("setup: tool_result should be folded (hidden) into its call")
	}
	if m.searchableText(2) != "" {
		t.Fatalf("hidden folded result should have empty searchable text, got %q", m.searchableText(2))
	}
	if m.matchesQuery(2, "secret") {
		t.Fatal("hidden folded result should not match a query")
	}

	// Body text matches only when the row is expanded.
	body := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"# heading\n\nzebra body content here"}`},
	})
	body.expanded[1] = true
	body.bodyCache = map[int]string{}
	if !body.matchesQuery(0, "zebra") {
		t.Fatalf("expanded body should match; text=%q", body.searchableText(0))
	}
}

func TestSessionSearchJumpsAndCycles(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha one"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"beta two"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"gamma ALPHA three"}`},
	})
	// Enter search and type the query: selection jumps to the first match.
	m = press(m, "/")
	if !m.searching {
		t.Fatal("`/` did not enter search mode")
	}
	m = typeSearch(m, "alpha")
	if m.selected != 0 {
		t.Fatalf("after typing, selection = %d, want 0", m.selected)
	}
	// Confirm keeps the query active for n/N.
	m = press(m, "enter")
	if m.searching {
		t.Fatal("enter should confirm and exit search entry")
	}
	if m.searchQuery != "alpha" {
		t.Fatalf("query = %q, want alpha", m.searchQuery)
	}
	// n cycles to the next match, then wraps.
	m = press(m, "n")
	if m.selected != 2 {
		t.Fatalf("n: selection = %d, want 2", m.selected)
	}
	m = press(m, "n")
	if m.selected != 0 {
		t.Fatalf("n wrap: selection = %d, want 0", m.selected)
	}
	// N cycles backward with wrap.
	m = press(m, "N")
	if m.selected != 2 {
		t.Fatalf("N: selection = %d, want 2", m.selected)
	}
	// esc clears the search and re-focuses the input for normal typing.
	m = press(m, "esc")
	if m.searching || m.searchQuery != "" {
		t.Fatalf("esc did not clear search: searching=%v query=%q", m.searching, m.searchQuery)
	}
	m = typeText(t, m, "n")
	if m.input.Value() != "n" {
		t.Fatalf("after esc, typing should reach the textarea, got %q", m.input.Value())
	}
}

func TestSearchDoesNotHijackNonEmptyInput(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha"}`},
	})
	m = typeText(t, m, "hello")
	// `/` with non-empty input types a slash rather than opening search.
	m = press(m, "/")
	if m.searching {
		t.Fatal("`/` opened search despite non-empty input")
	}
	if m.input.Value() != "hello/" {
		t.Fatalf("`/` should type into the textarea, got %q", m.input.Value())
	}
	// n with an active query but non-empty input types rather than cycling.
	m.searchQuery = "alpha"
	m = press(m, "n")
	if m.input.Value() != "hello/n" {
		t.Fatalf("n should type into the textarea, got %q", m.input.Value())
	}
}

func TestJumpToEventKeys(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"a"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"why?"}`},
		{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"b"}`},
		{Seq: 4, Type: "review_submitted", Actor: "reviewer", DataJson: `{"verdict":"approve","summary":"lgtm"}`},
		{Seq: 5, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc","message":"done"}`},
		{Seq: 6, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom"}`},
		{Seq: 7, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"next?"}`},
	})
	m = press(m, "}") // next question
	if m.selected != 1 {
		t.Fatalf("} to first question = %d, want 1", m.selected)
	}
	m = press(m, "}") // next question
	if m.selected != 6 {
		t.Fatalf("} to next question = %d, want 6", m.selected)
	}
	m = press(m, "{") // prev question
	if m.selected != 1 {
		t.Fatalf("{ to prev question = %d, want 1", m.selected)
	}
	m = press(m, ")") // next review
	if m.selected != 3 {
		t.Fatalf(") to review = %d, want 3", m.selected)
	}
	m = press(m, ">") // next commit
	if m.selected != 4 {
		t.Fatalf("> to commit = %d, want 4", m.selected)
	}
	m = press(m, "]") // next error
	if m.selected != 5 {
		t.Fatalf("] to error = %d, want 5", m.selected)
	}
	m = press(m, "[") // prev error: none before index 5 -> no-op
	if m.selected != 5 {
		t.Fatalf("[ with no earlier error should be a no-op, got %d", m.selected)
	}
}

func TestTranscriptSearchAndEsc(t *testing.T) {
	m := searchEvsModel(t, []*v1.Event{
		{Seq: 1, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"alpha one"}`},
		{Seq: 2, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"gamma alpha two"}`},
	})
	m.state = stateHistory
	m.historyTranscript = true
	m.historyTransID = "sess1"

	m = press(m, "/")
	if !m.searching {
		t.Fatal("`/` did not enter search in the transcript")
	}
	m = typeSearch(m, "alpha")
	if m.selected != 0 {
		t.Fatalf("transcript search selection = %d, want 0", m.selected)
	}
	m = press(m, "enter")
	m = press(m, "n")
	if m.selected != 1 {
		t.Fatalf("transcript n = %d, want 1", m.selected)
	}
	// First esc clears the search but stays in the transcript.
	m = press(m, "esc")
	if m.searchQuery != "" {
		t.Fatalf("esc did not clear the transcript search: %q", m.searchQuery)
	}
	if !m.historyTranscript {
		t.Fatal("esc with active search should NOT back out of the transcript")
	}
	// Second esc backs out to the list.
	m = press(m, "esc")
	if m.historyTranscript {
		t.Fatal("second esc should back out of the transcript")
	}
}

func TestSessionViewFitsWhileSearching(t *testing.T) {
	m := searchEvsModel(t, nil)
	for i := 0; i < 40; i++ {
		m.appendEvent(&v1.Event{
			Seq: int64(i), Type: "model_turn", Actor: "coordinator",
			DataJson: fmt.Sprintf(`{"text":"long output line number %d that wraps inside the body region"}`, i),
		})
	}
	m.rebuild()
	m = press(m, "/")
	m = typeSearch(m, "line")
	view := m.sessionView()
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("searching sessionView produced %d lines, want 24", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("line %d width %d exceeds terminal width 80: %q", i, w, ln)
		}
	}
}

// --- ask_user Q&A folding (task 0120) ---

// One ask_user round-trip emits four events (tool_call, question_asked,
// question_answered, tool_result); the transcript must render them as ONE
// block: the plumbing rows fold away and the question_asked body carries the
// question plus the folded answer.
func TestAskUserExchangeFoldsToSingleBlock(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"which db?\"}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"which db?","options":["postgres","sqlite"]}`},
			{Seq: 3, Type: "question_answered", Actor: "coordinator", DataJson: `{"answer":"postgres"}`},
			{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"postgres"}`},
		},
	}
	if !m.hiddenRow(0) {
		t.Fatal("ask_user tool_call should be hidden once its question_asked exists")
	}
	if m.hiddenRow(1) {
		t.Fatal("question_asked row must stay visible (it is the canonical Q&A block)")
	}
	if !m.hiddenRow(2) {
		t.Fatal("question_answered should fold into the question_asked row")
	}
	if !m.hiddenRow(3) {
		t.Fatal("ask_user tool_result (a duplicate of the answer) should be hidden")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	if !strings.Contains(body, "which db?") {
		t.Fatalf("question body should contain the question, got:\n%s", body)
	}
	if !strings.Contains(body, "→ postgres") {
		t.Fatalf("question body should fold in the answer, got:\n%s", body)
	}
}

// While a question is still unanswered, the question row stays visible with no
// answer line, and the (blocked, still-running) ask_user tool_call is already
// hidden — otherwise it would sit there showing a stuck in-flight glyph.
func TestAskUserPendingQuestionVisibleWithoutAnswer(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"which db?\"}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"which db?"}`},
		},
	}
	if !m.hiddenRow(0) {
		t.Fatal("pending ask_user tool_call should be hidden")
	}
	if m.hiddenRow(1) {
		t.Fatal("pending question_asked must be visible")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	if !strings.Contains(body, "which db?") {
		t.Fatalf("pending question body should show the question, got:\n%s", body)
	}
	if strings.Contains(body, "→") {
		t.Fatalf("pending question body must not show an answer arrow, got:\n%s", body)
	}
}

// An ask_user call that never asked (validation error: no question_asked event)
// must NOT be treated as plumbing — the call and its errored result render via
// the normal adjacent-fold tool card so the failure stays visible. An ask that
// was cancelled mid-question keeps its errored result visible too.
func TestAskUserErrorStaysVisible(t *testing.T) {
	bad := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{}"}`},
		{Seq: 2, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"ask_user: provide a 'question'","error":"true"}`},
	}}
	if bad.isAskUserPlumbing(0) || bad.isAskUserPlumbing(1) {
		t.Fatal("an ask_user call with no question_asked is not plumbing")
	}
	// The adjacent pair still folds into one visible tool card (isMergedResult),
	// which is the normal errored-tool rendering.
	if !bad.hiddenRow(1) || bad.hiddenRow(0) {
		t.Fatal("errored ask_user should render as a normal merged tool card")
	}

	cancelled := &model{evs: []*v1.Event{
		{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{\"question\":\"q?\"}"}`},
		{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"q?"}`},
		{Seq: 3, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"ask_user: context canceled","error":"true"}`},
	}}
	if !cancelled.hiddenRow(0) {
		t.Fatal("cancelled ask_user call should still be hidden (its question row shows the ask)")
	}
	if cancelled.hiddenRow(2) {
		t.Fatal("an errored ask_user result must stay visible so the failure is not swallowed")
	}
}

// Autonomous-mode auto-answers must render as one compact dim line, not the
// full canned "no human is available…" paragraph the agent receives.
func TestAutoAnsweredQuestionRendersCompact(t *testing.T) {
	canned := "You are in autonomous mode and no human is available, so this question cannot be answered."
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "question_asked", Actor: "coordinator", DataJson: `{"question":"should I?","auto":true}`},
			{Seq: 2, Type: "question_answered", Actor: "coordinator", DataJson: fmt.Sprintf(`{"answer":%q,"auto":true}`, canned)},
		},
	}
	if !m.hiddenRow(1) {
		t.Fatal("auto question_answered should fold into the question row")
	}
	body := stripANSI(m.bodyFor(m.evs[0]))
	if !strings.Contains(body, "auto-answered (autonomous mode)") {
		t.Fatalf("auto answer should render the compact marker, got:\n%s", body)
	}
	if strings.Contains(body, "no human is available") {
		t.Fatalf("auto answer must not dump the canned paragraph, got:\n%s", body)
	}
}

// A multi-question batch folds each answer beneath its own question in one
// block; the suggested options drop away once answered (only the chosen answer
// matters), and the tool_result's "Q1/A1" dump stays hidden.
func TestBatchQuestionBodyInterleavesAnswers(t *testing.T) {
	m := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: []*v1.Event{
			{Seq: 1, Type: "tool_call", Actor: "coordinator", DataJson: `{"id":"t1","name":"ask_user","args":"{}"}`},
			{Seq: 2, Type: "question_asked", Actor: "coordinator", DataJson: `{"questions":[{"question":"which database?","options":["postgres","sqlite"]},{"question":"service name?"}]}`},
			{Seq: 3, Type: "question_answered", Actor: "coordinator", DataJson: `{"answers":["postgres","ycc"]}`},
			{Seq: 4, Type: "tool_result", Actor: "coordinator", DataJson: `{"id":"t1","result":"Q1: which database?\nA1: postgres\n\nQ2: service name?\nA2: ycc"}`},
		},
	}
	if !m.hiddenRow(0) || !m.hiddenRow(2) || !m.hiddenRow(3) {
		t.Fatal("batch ask_user plumbing and answer rows should be hidden")
	}
	body := stripANSI(m.bodyFor(m.evs[1]))
	for _, want := range []string{"1. which database?", "→ postgres", "2. service name?", "→ ycc"} {
		if !strings.Contains(body, want) {
			t.Fatalf("batch body missing %q, got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sqlite") {
		t.Fatalf("answered batch body should drop the unchosen options, got:\n%s", body)
	}

	// Unanswered: options stay visible, no answer arrows yet.
	pending := &model{
		expanded: map[int]bool{}, bodyCache: map[int]string{},
		evs: m.evs[:2],
	}
	body = stripANSI(pending.bodyFor(pending.evs[1]))
	if !strings.Contains(body, "sqlite") {
		t.Fatalf("unanswered batch body should keep the options, got:\n%s", body)
	}
	if strings.Contains(body, "→") {
		t.Fatalf("unanswered batch body must not show answer arrows, got:\n%s", body)
	}
}

// While the single-question footer picker is echoing the pending prompt, the
// question row's body collapses to a pointer (mirroring the wizard) so the
// question never renders twice on screen at once; choosing "other…" (free
// text) restores the full question since the plain textarea shows no prompt.
func TestPendingPickerCondensesQuestionBody(t *testing.T) {
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
		Seq: 5, Type: "question_asked", Actor: "coordinator",
		DataJson: `{"question":"which database?","options":["postgres","sqlite"]}`,
	}
	m.appendEvent(ev)
	if !m.picking || m.pendingSeq != 5 {
		t.Fatalf("picker should be active for seq 5 (picking=%v pendingSeq=%d)", m.picking, m.pendingSeq)
	}
	body := stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "answer below") {
		t.Fatalf("pending picker body should be a pointer, got:\n%s", body)
	}
	if strings.Contains(body, "which database?") {
		t.Fatalf("pending picker body should not repeat the prompt, got:\n%s", body)
	}

	// "other…" drops to the free-text textarea: the prompt must come back.
	m.pickerCursor = len(m.pickerOpts) // the "other…" row
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.picking {
		t.Fatal("enter on other… should leave picker mode")
	}
	body = stripANSI(m.bodyFor(ev))
	if !strings.Contains(body, "which database?") {
		t.Fatalf("free-text mode should restore the full question body, got:\n%s", body)
	}
}

// The status bar shows a visually distinct budget segment: a warn readout once a
// session crosses ~80% of its cap, escalating to "budget reached" on breach
// (task 0137, spec §20.6).
func TestStatusBarBudgetSegment(t *testing.T) {
	m := model{state: stateSession, status: "running", mode: "work", w: 160, budgetPct: 0.85}
	bar := m.statusBar()
	if !strings.Contains(bar, "budget 85%") {
		t.Fatalf("status bar missing budget warning:\n%s", bar)
	}
	if strings.Contains(bar, "budget reached") {
		t.Fatalf("warn state should not show 'budget reached':\n%s", bar)
	}

	m.budgetExceeded = true
	bar = m.statusBar()
	if !strings.Contains(bar, "budget reached") {
		t.Fatalf("status bar missing budget breach:\n%s", bar)
	}
}

// applyLoopDecision halts the loop when cumulative token spend reaches the loop
// token cap, even though a ready task remains and the backlog changed (task 0137).
func TestLoopDecisionStopsAtTokenCap(t *testing.T) {
	m := &model{
		looping: true, loopStarted: true, loopPrevFP: "0001:todo",
		loopTokenCap: 1000, loopRun: &loopRunState{cumTokens: 1500},
	}
	next, _ := m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done"})
	mm := next.(model)
	if mm.looping {
		t.Fatalf("expected loop to stop at token cap, still looping")
	}
	if mm.state != stateMenu || !strings.Contains(mm.status, "budget") {
		t.Fatalf("expected budget-stop outcome, state=%v status=%q", mm.state, mm.status)
	}
}

// applyLoopDecision halts on the cost cap too.
func TestLoopDecisionStopsAtCostCap(t *testing.T) {
	m := &model{
		looping: true, loopStarted: true, loopPrevFP: "0001:todo",
		loopCostCap: 1.0, loopRun: &loopRunState{cumCost: 2.0},
	}
	next, _ := m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done"})
	mm := next.(model)
	if mm.looping || !strings.Contains(mm.status, "budget") {
		t.Fatalf("expected cost-cap budget stop, looping=%v status=%q", mm.looping, mm.status)
	}
}

// applyLoopDecision halts when a loop session's own budget was breached
// daemon-side, with a distinct outcome (task 0137).
func TestLoopDecisionStopsOnSessionBreach(t *testing.T) {
	m := &model{
		looping: true, loopStarted: true, loopPrevFP: "0001:todo",
		loopSessBreach: true, loopRun: &loopRunState{},
	}
	next, _ := m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done"})
	mm := next.(model)
	if mm.looping {
		t.Fatalf("expected loop to stop on session breach, still looping")
	}
	if !strings.Contains(mm.status, "session budget reached") {
		t.Fatalf("expected 'session budget reached' outcome, got %q", mm.status)
	}
}

// With no loop caps configured, the loop continues normally past any spend
// (unlimited default preserved).
func TestLoopDecisionNoCapContinues(t *testing.T) {
	fc := newFakeClient()
	m := &model{
		looping: true, loopStarted: true, loopPrevFP: "0001:todo",
		loopRun: &loopRunState{cumTokens: 999999, cumCost: 999}, client: fc, ctx: context.Background(),
	}
	next, cmd := m.applyLoopDecision(loopDecisionMsg{next: "0002", fp: "0001:done"})
	mm := next.(model)
	if !mm.looping || cmd == nil {
		t.Fatalf("expected loop to continue with no caps, looping=%v cmd=%v", mm.looping, cmd)
	}
}

// TestYankText covers the per-event clipboard text extraction used by `y`
// (task 0141): commit_made yields the sha, session_error the error text, and a
// model_turn its raw text.
func TestYankText(t *testing.T) {
	m := model{w: 100, expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1}

	commit := &v1.Event{Seq: 1, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123def","message":"do the thing"}`}
	if got := m.yankText(commit); got != "abc123def" {
		t.Fatalf("yankText(commit_made) = %q, want %q", got, "abc123def")
	}

	errEv := &v1.Event{Seq: 2, Type: "session_error", Actor: "coordinator", DataJson: `{"msg":"boom failure"}`}
	if got := m.yankText(errEv); got != "boom failure" {
		t.Fatalf("yankText(session_error) = %q, want %q", got, "boom failure")
	}

	turn := &v1.Event{Seq: 3, Type: "model_turn", Actor: "coordinator", DataJson: `{"text":"the model answer"}`}
	if got := m.yankText(turn); got != "the model answer" {
		t.Fatalf("yankText(model_turn) = %q, want %q", got, "the model answer")
	}
}

// TestYankKeyCopiesRow verifies that `y` on a selected commit row with empty
// input arms the "copied ✓" notice, while `y` mid-composition types into the
// textarea instead (task 0141). It also confirms the notice self-clears on the
// matching flashClearMsg tick.
func TestYankKeyCopiesRow(t *testing.T) {
	m := model{
		client: newFakeClient(), ctx: context.Background(),
		state: stateSession, status: "running", connected: true, sessionID: "s_live", follow: true,
		input:    newSessionInput(),
		expanded: map[int]bool{}, bodyCache: map[int]string{}, selected: -1,
		thinkLevels: map[string]string{},
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(model)
	m.input.Focus()
	m.appendEvent(&v1.Event{Seq: 1, Type: "commit_made", Actor: "coordinator", DataJson: `{"sha":"abc123def","message":"do the thing"}`})
	m.rebuild()
	m.selected = 0

	// Empty input: `y` yanks and arms the notice (no text typed into the input).
	m = drive(t, m, "y")
	if m.flashNote != "copied ✓" {
		t.Fatalf("y on empty input did not set flashNote: %q", m.flashNote)
	}
	if m.input.Value() != "" {
		t.Fatalf("y on empty input typed into the textarea: %q", m.input.Value())
	}

	// The clear tick dismisses the notice.
	updated, _ = m.Update(flashClearMsg{seq: m.flashSeq})
	m = updated.(model)
	if m.flashNote != "" {
		t.Fatalf("flashNote did not clear on the matching tick: %q", m.flashNote)
	}

	// With content in the input, `y` types normally instead of yanking.
	m = typeText(t, m, "abc")
	m.flashNote = ""
	updated, _ = m.Update(keyMsg("y"))
	m = updated.(model)
	if m.flashNote != "" {
		t.Fatalf("y mid-composition armed a flash: %q", m.flashNote)
	}
	if m.input.Value() != "abcy" {
		t.Fatalf("y mid-composition did not type into the textarea: %q", m.input.Value())
	}
}
