package session

import (
	"context"
	"errors"
	"testing"

	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/event"
)

func TestSessionOriginLaunchAndReopen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    Config
		groom  bool
		origin string
		human  bool
	}{
		{name: "manual preset", cfg: Config{Mode: "pm", Preset: memoryGroomPreset}, origin: OriginUser, human: true},
		{name: "unattended default", cfg: Config{Mode: "chat", Unattended: true}, origin: OriginAutomation},
		{name: "explicit loop", cfg: Config{Mode: "chat", Unattended: true, Origin: OriginWorkLoop}, origin: OriginWorkLoop},
		{name: "automatic groom", groom: true, origin: OriginMemoryGroom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := presetTestManager(t, "b")
			ws := t.TempDir()
			t.Cleanup(m.ReclaimAll)
			if _, err := m.AddProject(ws, "origin-test"); err != nil {
				t.Fatal(err)
			}
			tc.cfg.Workspace = ws
			var s *Session
			var err error
			if tc.groom {
				s, err = m.startMemoryGroomSession(ws, docs.MemoryStatus{})
			} else {
				s, err = m.Start(tc.cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			started := waitForSessionEvent(t, s, event.SessionStarted)
			waitForSessionEvent(t, s, event.UserInput)
			if started.Data["origin"] != tc.origin {
				t.Fatalf("persisted launch origin = %v, want %s", started.Data["origin"], tc.origin)
			}
			if origin, human := s.liveParticipation(); origin != tc.origin || human != tc.human {
				t.Fatalf("live launch = %q/%v, want %q/%v", origin, human, tc.origin, tc.human)
			}
			if err := m.Stop(s.ID); err != nil {
				t.Fatal(err)
			}
			// Reopen changes execution to attended, but not the durable launch origin.
			reopened, err := m.Reopen("", s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if origin, human := reopened.liveParticipation(); origin != tc.origin || human != tc.human {
				t.Fatalf("reopened = %q/%v, want %q/%v", origin, human, tc.origin, tc.human)
			}
			if !tc.human {
				if err := reopened.SendInput("human follow-up"); err != nil {
					t.Fatal(err)
				}
				if origin, human := reopened.liveParticipation(); origin != tc.origin || !human {
					t.Fatalf("follow-up = %q/%v", origin, human)
				}
				if err := m.Stop(s.ID); err != nil {
					t.Fatal(err)
				}
				rows, err := m.ListSessionHistory("")
				if err != nil || len(rows) != 1 || rows[0].Origin != tc.origin || !rows[0].HumanParticipated {
					t.Fatalf("durable follow-up = %+v, %v", rows, err)
				}
			}
		})
	}
}

func TestSessionOriginLegacyReopenRemainsUnknown(t *testing.T) {
	m, _, _ := presetTestManager(t, "b")
	ws := t.TempDir()
	t.Cleanup(m.ReclaimAll)
	if _, err := m.AddProject(ws, "legacy"); err != nil {
		t.Fatal(err)
	}
	sessionGitAt(t, ws, "init", "--initial-branch=main")
	sessionGitAt(t, ws, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	writeSession(t, ws, "s_legacy", []event.Event{
		{Seq: 1, TS: ts(1), Type: event.SessionStarted, Data: map[string]any{"mode": "pm", "preset": memoryGroomPreset}},
		{Seq: 2, TS: ts(2), Type: event.UserInput, Data: map[string]any{"text": defaultPrompt("pm")}},
	})
	s, err := m.Reopen("", "s_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if origin, human := s.liveParticipation(); origin != "" || human {
		t.Fatalf("legacy reopened = %q/%v; mode/preset must not fabricate origin", origin, human)
	}
}

func TestSessionOriginInputBeforeStarted(t *testing.T) {
	early := event.Event{Type: event.UserInput, Data: map[string]any{"text": "real input before startup"}}
	started := event.Event{Type: event.SessionStarted, Data: map[string]any{"origin": OriginWorkLoop}}
	opening := event.Event{Type: event.UserInput, Data: map[string]any{"text": defaultPrompt("work"), "opening": true}}
	// A launch's live state is seeded before registration, while persisted history
	// learns the explicit origin only when it reaches the lifecycle marker.
	live := sessionParticipation{origin: OriginWorkLoop, knownOrigin: true}
	live.consume([]event.Event{early})
	if !live.human {
		t.Fatal("early actual input did not count live")
	}
	var persisted sessionParticipation
	persisted.consume([]event.Event{early, started, opening})
	if !persisted.human || persisted.origin != OriginWorkLoop {
		t.Fatalf("early durable input classification = %+v", persisted)
	}
}

func TestSessionOriginWorkLoopLaunchSite(t *testing.T) {
	stop := errors.New("stop before creating test session")
	wl := &workLoop{startSession: func(cfg Config) (*Session, error) {
		if cfg.Origin != OriginWorkLoop || !cfg.Unattended {
			t.Fatalf("work loop launch config = %+v", cfg)
		}
		return nil, stop
	}}
	if _, _, err := wl.realRunSession(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("launch error = %v", err)
	}
}
