package session

import (
	"errors"
	"testing"

	"github.com/whyrusleeping/ycc/internal/event"
)

func TestSubagentModelsRestrictsGenericSpawnOnly(t *testing.T) {
	s := &Session{reg: testRegistry(), subagentModels: []string{"b"}}
	catalog := s.agentModels()
	if len(catalog) != 1 || catalog[0].Name != "b" {
		t.Fatalf("generic spawn catalog = %+v, want only b", catalog)
	}
	if _, err := s.resolveSubagentSpec("a"); err == nil {
		t.Fatal("resolve disallowed model a succeeded")
	}
	if _, err := s.resolveSubagentSpec("b"); err != nil {
		t.Fatalf("resolve allowed model b: %v", err)
	}
	// The restriction is separate from role assignments: role specs still resolve.
	if _, err := s.agentSpec("a"); err != nil {
		t.Fatalf("role model a was affected: %v", err)
	}
}

func TestSubagentModelsNilAndEmptyRemainDistinctOnReplay(t *testing.T) {
	cases := []struct {
		name    string
		data    map[string]any
		want    []string
		wantNil bool
	}{
		{name: "legacy missing", data: map[string]any{}, wantNil: true},
		{name: "explicit empty", data: map[string]any{"subagent_models": []string{}}, want: []string{}},
		{name: "explicit names", data: map[string]any{"subagent_models": []string{"b"}}, want: []string{"b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replaySubagentModels([]event.Event{{Type: event.SessionStarted, Data: tc.data}})
			if (got == nil) != tc.wantNil || len(got) != len(tc.want) {
				t.Fatalf("replay = %#v; want %#v (nil=%v)", got, tc.want, tc.wantNil)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("replay = %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestGenericAgentToolsetSurvivesReopen(t *testing.T) {
	m, _ := newWorkstreamManager(t)
	for _, tc := range []struct {
		name      string
		models    []string
		wantTools bool
	}{
		{name: "explicit none", models: []string{}, wantTools: false},
		{name: "default", models: nil, wantTools: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, err := m.SpawnWorkstream(SpawnWorkstreamConfig{Project: "demo", Mode: "chat", Prompt: "test", SubagentModels: tc.models})
			if err != nil {
				t.Fatalf("SpawnWorkstream: %v", err)
			}
			check := func(stage string, session *Session) {
				t.Helper()
				found := map[string]bool{}
				for _, def := range session.loop.Tools.APIDefs() {
					found[def.Function.Name] = true
				}
				for _, name := range []string{"spawn_agent", "send_to_agent"} {
					if found[name] != tc.wantTools {
						t.Errorf("%s %s present=%v, want %v", stage, name, found[name], tc.wantTools)
					}
				}
			}
			check("started", s)
			if err := m.Stop(s.ID); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			reopened, err := m.Reopen("demo", s.ID)
			if err != nil {
				t.Fatalf("Reopen: %v", err)
			}
			defer m.Stop(reopened.ID)
			check("reopened", reopened)
			if (reopened.subagentModels == nil) != (tc.models == nil) {
				t.Fatalf("reopened policy nil=%v, want %v", reopened.subagentModels == nil, tc.models == nil)
			}
		})
	}
}

func TestSubagentModelsValidation(t *testing.T) {
	m := NewManager(testRegistry(), t.TempDir())
	if err := m.validateSubagentModels(nil); err != nil {
		t.Fatalf("nil allowlist: %v", err)
	}
	if err := m.validateSubagentModels([]string{}); err != nil {
		t.Fatalf("empty allowlist: %v", err)
	}
	if err := m.validateSubagentModels([]string{"missing"}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown model err = %v, want ErrUnknownModel", err)
	}
}
