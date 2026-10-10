package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestSessionOriginHistoryRPC(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		id      string
		origin  string
		human   bool
		waiting bool
	}{
		{"s_user", session.OriginUser, true, false},
		{"s_loop", session.OriginWorkLoop, false, false},
		{"s_groom_engaged", session.OriginMemoryGroom, true, false},
		{"s_legacy", "", false, false},
		{"s_automatic_human_gate", session.OriginAutomation, false, true},
		{"s_automatic_answered_gate", session.OriginAutomation, true, false},
		{"s_automatic_ask_only", session.OriginAutomation, false, false},
	}
	for _, tc := range cases {
		lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", tc.id, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lg.Record("coordinator", event.SessionStarted, map[string]any{"origin": tc.origin, "mode": "pm", "preset": "memory-groom"})
		lg.Record("user", event.UserInput, map[string]any{"text": "canned opening", "opening": true})
		switch tc.id {
		case "s_automatic_human_gate", "s_automatic_answered_gate":
			lg.Record("coordinator", event.ToolCall, map[string]any{"name": "start_work", "id": "confirm"})
			lg.Record("coordinator", event.QuestionAsked, map[string]any{"question": "Start work?", "options": []string{"Yes", "No"}})
			if tc.id == "s_automatic_answered_gate" {
				lg.Record("coordinator", event.QuestionAnswered, map[string]any{"answer": "No", "confirmed": false})
			}
		case "s_automatic_ask_only":
			lg.Record("coordinator", event.QuestionAsked, map[string]any{"question": "Assume?", "auto": true})
		default:
			lg.Record("coordinator", event.QuestionAsked, map[string]any{"question": "Assume?", "auto": true})
			lg.Record("coordinator", event.QuestionAnswered, map[string]any{"auto": true, "answer": "automatic"})
		}
		if tc.id == "s_groom_engaged" {
			lg.Record("coordinator", event.QuestionAnswered, map[string]any{"answers": []string{"real human answer"}})
		}
		if err := lg.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mgr := session.NewManager(config.NewRegistry(nil), ws)
	t.Cleanup(mgr.ReclaimAll)
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr))
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL)
	res, err := client.ListSessionHistory(context.Background(), connect.NewRequest(&v1.ListSessionHistoryRequest{Limit: 10}))
	if err != nil || len(res.Msg.Sessions) != len(cases) {
		t.Fatalf("history RPC = %+v, %v", res, err)
	}
	byID := make(map[string]*v1.SessionSummary)
	for _, row := range res.Msg.Sessions {
		byID[row.SessionId] = row
	}
	for _, tc := range cases {
		row := byID[tc.id]
		if row == nil || row.Live || row.Origin != tc.origin || row.HumanParticipated != tc.human || row.WaitingInput != tc.waiting {
			t.Fatalf("RPC %s = %+v, want origin %q human %v waiting %v without a live session", tc.id, row, tc.origin, tc.human, tc.waiting)
		}
	}
}
