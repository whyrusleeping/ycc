package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// drive feeds a key through Update and, if a command is returned, runs it and
// feeds the resulting message back through Update (recursing until no command).
// It threads the model value through, mirroring the Bubble Tea runtime.
func drive(t *testing.T, m model, key string) model {
	t.Helper()
	var km tea.KeyMsg
	switch key {
	case "enter":
		km = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		km = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		km = tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		km = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		km = tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		km = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		km = tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+n":
		km = tea.KeyMsg{Type: tea.KeyCtrlN}
	case "ctrl+p":
		km = tea.KeyMsg{Type: tea.KeyCtrlP}
	default:
		km = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, cmd := m.Update(km)
	m = updated.(model)
	return runCmds(t, m, cmd)
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
		km := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
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
	updated, _ := m.updateBacklog(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
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
	updated, _ = m.updateBacklog(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = updated.(model)
	if m.backlogShowDone {
		t.Fatalf("expected toggle back to hide done")
	}
	if m.backlogCursor >= len(m.visibleBacklogTasks()) {
		t.Fatalf("cursor=%d out of range for %d visible", m.backlogCursor, len(m.visibleBacklogTasks()))
	}
}
