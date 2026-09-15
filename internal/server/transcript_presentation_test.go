package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

func TestTranscriptPresentationPreservesSharedProviderState(t *testing.T) {
	original := event.Event{Seq: 7, TS: time.Now(), Actor: "coordinator", Type: event.ModelTurn, Data: map[string]any{
		"text": "answer", "model_name": "a", "context_tokens_est": 100,
		"thinking_blocks": []event.ThinkingBlock{{Thinking: "private reasoning", Signature: "signed", Redacted: "opaque"}},
		"usage":           event.Usage{Input: 10, Output: 20},
	}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var persisted event.Event
	if err := json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	for name, ev := range map[string]event.Event{"live-typed": original, "persisted": persisted} {
		t.Run(name, func(t *testing.T) {
			full := toProto(ev)
			want := ev
			want.Data = maps.Clone(ev.Data)
			delete(want.Data, "thinking_blocks")
			got := transcriptEventToProto(ev, true)
			if !proto.Equal(got, toProto(want)) {
				t.Fatal("presentation changed fields beyond model_turn.thinking_blocks")
			}
			if !proto.Equal(full, transcriptEventToProto(ev, false)) || !proto.Equal(full, toProto(ev)) {
				t.Fatal("presentation mutated provider state shared with the live log")
			}
			for _, kind := range []event.Type{event.Thinking, event.ToolResult} {
				ev.Type = kind
				if !proto.Equal(toProto(ev), transcriptEventToProto(ev, true)) {
					t.Fatalf("presentation changed non-model event %s", kind)
				}
			}
		})
	}
}

func TestTranscriptPresentationOverBinaryAndJSON(t *testing.T) {
	ws := t.TempDir()
	logPath := filepath.Join(ws, ".ycc", "sessions", "s1", "events.jsonl")
	log, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log.Record("coordinator", event.Thinking, map[string]any{"text": "visible reasoning"})
	log.Record("coordinator", event.ModelTurn, map[string]any{
		"text": "answer", "thinking_blocks": []event.ThinkingBlock{{Redacted: "opaque provider state"}},
	})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(config.NewRegistry(&config.Config{}), ws)
	defer mgr.ReclaimAll()
	_, handler := yccv1connect.NewSessionServiceHandler(New(mgr))
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	for _, useJSON := range []bool{false, true} {
		options := []connect.ClientOption{}
		if useJSON {
			options = append(options, connect.WithProtoJSON())
		}
		client := yccv1connect.NewSessionServiceClient(httpServer.Client(), httpServer.URL, options...)
		for _, omit := range []bool{false, true, false} {
			response, err := client.GetSessionTranscript(context.Background(), connect.NewRequest(&v1.GetSessionTranscriptRequest{
				SessionId: "s1", OmitProviderState: omit,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Msg.Events) != 2 || response.Msg.Events[0].Seq != 1 || response.Msg.Events[1].Seq != 2 {
				t.Fatal("presentation lost events or cursor")
			}
			var data map[string]any
			if err := json.Unmarshal([]byte(response.Msg.Events[1].DataJson), &data); err != nil {
				t.Fatal(err)
			}
			_, hasProviderState := data["thinking_blocks"]
			if hasProviderState == omit || data["text"] != "answer" || response.Msg.Events[0].DataJson != `{"text":"visible reasoning"}` {
				t.Fatalf("JSON=%v omit=%v: unexpected presentation", useJSON, omit)
			}
		}
	}
	after, err := os.ReadFile(logPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("presentation changed the persisted log", err)
	}
}

// Set YCC_BENCH_TRANSCRIPT to a local events.jsonl to measure actual encoded
// transcript cost without checking private session data into a fixture.
func BenchmarkTranscriptEncoding(b *testing.B) {
	path := os.Getenv("YCC_BENCH_TRANSCRIPT")
	if path == "" {
		b.Skip("set YCC_BENCH_TRANSCRIPT to an events.jsonl path")
	}
	events, err := event.ReadLog(path)
	if err != nil {
		b.Fatal(err)
	}
	for name, omit := range map[string]bool{"full": false, "presentation": true} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				response := &v1.GetSessionTranscriptResponse{Events: make([]*v1.Event, 0, len(events))}
				for _, ev := range events {
					response.Events = append(response.Events, transcriptEventToProto(ev, omit))
				}
				payload, err := proto.Marshal(response)
				if err != nil {
					b.Fatal(err)
				}
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := io.Copy(writer, bytes.NewReader(payload)); err != nil {
					b.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(payload)), "proto-bytes")
				b.ReportMetric(float64(compressed.Len()), "gzip-bytes")
			}
		})
	}
}
