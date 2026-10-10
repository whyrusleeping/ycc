package session

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestSessionOriginHistoryReaderParity(t *testing.T) {
	input := func(data map[string]any) event.Event {
		return event.Event{Type: event.UserInput, Actor: "user", Data: data}
	}
	answer := func(data map[string]any) event.Event {
		return event.Event{Type: event.QuestionAnswered, Data: data}
	}
	opening := input(map[string]any{"text": defaultPrompt("work"), "opening": true})
	legacyOpening := input(map[string]any{"text": defaultPrompt("work")})
	cases := []struct {
		name   string
		origin any
		events []event.Event
		human  bool
	}{
		{"human empty opening", OriginUser, []event.Event{input(map[string]any{"text": ""})}, true},
		{"human before opening echo", OriginUser, nil, true},
		{"manual canned preset", OriginUser, []event.Event{opening}, true},
		{"work loop opening", OriginWorkLoop, []event.Event{opening}, false},
		{"automatic groom opening", OriginMemoryGroom, []event.Event{opening}, false},
		{"automation custom opening", OriginAutomation, []event.Event{input(map[string]any{"text": "custom automation", "opening": true})}, false},
		{"legacy canned preset is unknown", nil, []event.Event{legacyOpening}, false},
		{"legacy custom prompt is unknown", nil, []event.Event{input(map[string]any{"text": "custom prompt"})}, false},
		{"malformed origin is unknown", true, []event.Event{opening}, false},
		{"explicit unknown", "unknown", []event.Event{opening}, false},
		{"later input", OriginWorkLoop, []event.Event{opening, input(map[string]any{"text": "follow up"})}, true},
		{"queued images only", OriginMemoryGroom, []event.Event{opening, input(map[string]any{"queued": true, "images": []any{map[string]any{"attachment_id": "img"}}})}, true},
		{"legacy later input", nil, []event.Event{legacyOpening, input(map[string]any{"text": ""})}, true},
		{"real input before launch echo", OriginWorkLoop, []event.Event{input(map[string]any{"text": "human"}), opening}, true},
		{"single human answer", OriginAutomation, []event.Event{opening, answer(map[string]any{"answer": "No"})}, true},
		{"batch human answer", OriginWorkLoop, []event.Event{opening, answer(map[string]any{"answers": []string{"Yes", "No"}, "auto": false})}, true},
		{"automatic single answer", OriginWorkLoop, []event.Event{opening, answer(map[string]any{"answer": "assumption", "auto": true})}, false},
		{"automatic batch answer", OriginMemoryGroom, []event.Event{opening, answer(map[string]any{"answers": []string{"assumption"}, "auto": true})}, false},
		{"pending question is not participation", OriginWorkLoop, []event.Event{opening, {Type: event.QuestionAsked, Data: map[string]any{"question": "May I proceed?"}}}, false},
		{"reopen retains participation", OriginWorkLoop, []event.Event{opening, answer(map[string]any{"answer": "No"}), {Type: event.SessionReopened}, answer(map[string]any{"auto": true})}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			evs := []event.Event{{Type: event.SessionStarted, Data: map[string]any{
				"origin": tc.origin, "mode": "work", "preset": memoryGroomPreset,
				"prompt": strings.Repeat("discard startup payload ", 1000),
			}}}
			evs = append(evs, tc.events...)
			for i := range evs {
				evs[i].Seq, evs[i].TS = i+1, ts(i+1)
			}
			writeSession(t, ws, "s_origin", evs)
			path := filepath.Join(ws, ".ycc", "sessions", "s_origin", "events.jsonl")
			full, fullOK := readEventsTolerantResult(path)
			selective, selectiveOK := readSummaryEventsTolerantResult(path)
			if !fullOK || !selectiveOK || len(full) != len(selective) {
				t.Fatalf("readers: full=%d/%v selective=%d/%v", len(full), fullOK, len(selective), selectiveOK)
			}
			want := reduceSessionSummary(ws, path, full)
			got := reduceSessionSummary(ws, path, selective)
			origin, _ := tc.origin.(string)
			if got.Origin != origin || got.HumanParticipated != tc.human {
				t.Fatalf("origin/human = %q/%v, want %q/%v", got.Origin, got.HumanParticipated, origin, tc.human)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("selective/full summary mismatch: %+v / %+v", got, want)
			}
			for _, ev := range selective {
				for _, key := range []string{"prompt", "answer", "answers", "images", "question"} {
					if _, ok := ev.Data[key]; ok {
						t.Fatalf("selective reader retained %s payload", key)
					}
				}
			}
		})
	}
}

func TestSessionOriginLiveOverlayAndFallback(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(config.NewRegistry(nil), ws)
	t.Cleanup(func() {
		m.sessions = make(map[string]*Session) // minimally constructed rows have no run owner
		m.ReclaimAll()
	})
	lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", "s_live", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lg.Close() })
	s := &Session{ID: "s_live", Workspace: ws, Mode: "work", origin: OriginWorkLoop, log: lg, status: event.StatusIdle}
	m.sessions[s.ID] = s
	check := func(origin string, human bool) {
		t.Helper()
		rows, err := m.ListSessionHistory("")
		if err != nil || len(rows) != 1 {
			t.Fatalf("history: %+v, %v", rows, err)
		}
		if !rows[0].Live || rows[0].Origin != origin || rows[0].HumanParticipated != human {
			t.Fatalf("live overlay = %+v, want %q/%v", rows[0], origin, human)
		}
	}
	// Empty log: fallback must distinguish manual launch from automation already.
	check(OriginWorkLoop, false)
	lg.Record("coordinator", event.SessionStarted, map[string]any{"origin": OriginWorkLoop, "mode": "work"})
	lg.Record("user", event.UserInput, map[string]any{"text": defaultPrompt("work"), "opening": true})
	lg.Record("coordinator", event.QuestionAnswered, map[string]any{"auto": true})
	check(OriginWorkLoop, false)
	s.inter = &interaction{waiting: make(chan string, 1)}
	rows, err := m.ListSessionHistory("")
	if err != nil || len(rows) != 1 || !rows[0].Waiting || rows[0].HumanParticipated {
		t.Fatalf("real human gate should be visible without participation: %+v, %v", rows, err)
	}
	s.inter = nil
	lg.Record("user", event.UserInput, map[string]any{"queued": true, "images": []any{map[string]any{"attachment_id": "img"}}})
	check(OriginWorkLoop, true)
	check(OriginWorkLoop, true) // incremental cursor must not reclassify opening input
	delete(m.sessions, s.ID)
	checkPersisted, err := m.ListSessionHistory("")
	if err != nil || len(checkPersisted) != 1 || !checkPersisted[0].HumanParticipated || checkPersisted[0].Origin != OriginWorkLoop {
		t.Fatalf("persisted overlay parity = %+v, %v", checkPersisted, err)
	}
	// A new manual launch without a usable disk row must still be personal.
	m.sessions["s_manual"] = &Session{ID: "s_manual", Workspace: ws, Mode: "pm", origin: OriginUser, status: event.StatusIdle}
	rows, err = m.ListSessionHistory("")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == "s_manual" {
			found = true
			if !row.HumanParticipated || row.Origin != OriginUser {
				t.Fatalf("manual fallback = %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("manual fallback missing")
	}
	delete(m.sessions, "s_manual")
}

func TestSessionOriginPersistedHumanGate(t *testing.T) {
	asked := event.Event{Type: event.QuestionAsked, Actor: "coordinator", Data: map[string]any{"question": "May I proceed?", "options": []string{"Yes", "No"}}}
	autoAsked := event.Event{Type: event.QuestionAsked, Actor: "coordinator", Data: map[string]any{"question": "assumption", "auto": true}}
	for _, tc := range []struct {
		name    string
		events  []event.Event
		waiting bool
		human   bool
	}{
		{"confirmation after process loss", []event.Event{asked}, true, false},
		{"auto ask before auto answer", []event.Event{autoAsked}, false, false},
		{"auto answered", []event.Event{autoAsked, {Type: event.QuestionAnswered, Data: map[string]any{"auto": true}}}, false, false},
		{"human answered", []event.Event{asked, {Type: event.QuestionAnswered, Data: map[string]any{"answer": "No"}}}, false, true},
		{"batch answered", []event.Event{asked, {Type: event.QuestionAnswered, Data: map[string]any{"answers": []string{"Yes"}}}}, false, true},
		{"later human gate after auto answer", []event.Event{autoAsked, {Type: event.QuestionAnswered, Data: map[string]any{"auto": true}}, asked}, true, false},
		{"cancelled confirmation declined by tool", []event.Event{asked, {Type: event.ToolResult, Actor: "coordinator"}, {Type: event.SessionStopped}}, false, false},
		{"later model turn", []event.Event{asked, {Type: event.ModelTurn, Actor: "coordinator"}}, false, false},
		{"later tool call", []event.Event{asked, {Type: event.ToolCall, Actor: "coordinator"}}, false, false},
		{"later context replacement", []event.Event{asked, {Type: event.ContextViewChanged, Actor: "coordinator"}}, false, false},
		{"later user input", []event.Event{asked, {Type: event.UserInput, Actor: "user"}}, false, true},
		{"subagent plumbing does not clear gate", []event.Event{asked, {Type: event.ModelTurn, Actor: "implementer"}, {Type: event.ToolResult, Actor: "implementer"}, {Type: event.SubagentFinished, Actor: "implementer"}}, true, false},
		{"restorable ask stopped and reopened", append(askUserTail()[2:], event.Event{Type: event.SessionStopped}, event.Event{Type: event.SessionReopened}), true, false},
		{"restorable ask ended with error", append(askUserTail()[2:], event.Event{Type: event.SessionError}), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := t.TempDir()
			evs := []event.Event{
				{Type: event.SessionStarted, Actor: "coordinator", Data: map[string]any{"origin": OriginAutomation, "mode": "work"}},
				{Type: event.UserInput, Actor: "user", Data: map[string]any{"text": "automatic opening", "opening": true}},
			}
			evs = append(evs, tc.events...)
			for i := range evs {
				evs[i].Seq, evs[i].TS = i+1, ts(i+1)
			}
			writeSession(t, ws, "s_gate", evs)
			path := filepath.Join(ws, ".ycc", "sessions", "s_gate", "events.jsonl")
			full, fullOK := readEventsTolerantResult(path)
			selective, selectiveOK := readSummaryEventsTolerantResult(path)
			if !fullOK || !selectiveOK {
				t.Fatal("readers must accept gate log")
			}
			want := reduceSessionSummary(ws, path, full)
			got := reduceSessionSummary(ws, path, selective)
			if got.Waiting != tc.waiting || got.HumanParticipated != tc.human || !reflect.DeepEqual(got, want) {
				t.Fatalf("selective/full gate summaries = %+v / %+v, want waiting %v human %v", got, want, tc.waiting, tc.human)
			}
			for _, ev := range selective {
				for _, key := range []string{"question", "questions", "options", "answer", "answers", "args"} {
					if _, ok := ev.Data[key]; ok {
						t.Fatalf("selective reader retained %s payload", key)
					}
				}
			}
			if strings.HasPrefix(tc.name, "restorable") && findResumableQuestion(full) == nil {
				t.Fatal("lifecycle fixture must actually restore its ask_user gate")
			}
			m := NewManager(config.NewRegistry(nil), ws)
			t.Cleanup(m.ReclaimAll)
			for range 2 { // persisted-only cache must retain the waiting bit too
				rows, err := m.ListSessionHistory("")
				if err != nil || len(rows) != 1 || rows[0].Live || rows[0].Waiting != tc.waiting || rows[0].Origin != OriginAutomation || rows[0].HumanParticipated != tc.human || rows[0].Status == event.StatusRunning {
					t.Fatalf("persisted-only history = %+v, %v", rows, err)
				}
			}
		})
	}
}

func TestSessionOriginPendingLiveGateOverridesPersisted(t *testing.T) {
	ws := t.TempDir()
	path := filepath.Join(ws, ".ycc", "sessions", "s_gate", "events.jsonl")
	lg, err := event.OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lg.Close() })
	lg.Record("coordinator", event.SessionStarted, map[string]any{"origin": OriginWorkLoop, "mode": "work"})
	lg.Record("user", event.UserInput, map[string]any{"text": "automatic opening", "opening": true})
	lg.Record("coordinator", event.QuestionAsked, map[string]any{"question": "May I proceed?"})
	m := NewManager(config.NewRegistry(nil), ws)
	s := &Session{ID: "s_gate", Workspace: ws, Mode: "work", origin: OriginWorkLoop, log: lg, status: event.StatusRunning, inter: &interaction{}}
	t.Cleanup(func() {
		m.sessions = make(map[string]*Session) // minimally constructed session has no run owner
		m.ReclaimAll()
	})
	check := func(live, waiting, human bool) {
		t.Helper()
		rows, err := m.ListSessionHistory("")
		if err != nil || len(rows) != 1 || rows[0].Live != live || rows[0].Waiting != waiting || rows[0].HumanParticipated != human {
			t.Fatalf("history = %+v, %v; want live/waiting/human %v/%v/%v", rows, err, live, waiting, human)
		}
	}
	check(false, true, false) // process-loss row remains personal without participation
	m.sessions[s.ID] = s
	check(true, false, false) // stale persisted ask is not the current live gate
	s.inter.waiting = make(chan string, 1)
	check(true, true, false)
	s.inter.waiting = nil // answer accepted before question_answered reaches the log
	check(true, false, false)
	lg.Record("coordinator", event.QuestionAnswered, map[string]any{"answer": "No", "confirmed": false})
	check(true, false, true)
	delete(m.sessions, s.ID)
	check(false, false, true) // changed log invalidates cached unresolved gate
}
