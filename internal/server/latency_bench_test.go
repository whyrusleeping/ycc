package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Run with -bench=BenchmarkLatencyLargeHistory -benchtime=3x -run='^$'.
// Loopback client elapsed includes transport, protobuf decode and daemon time;
// Server-Timing is handler-only. Neither measures iOS radio, decode or layout.
func BenchmarkLatencyLargeHistory(b *testing.B) {
	ws := b.TempDir()
	seed := func(id string, n int) {
		b.Helper()
		lg, err := event.OpenLog(filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < n; i++ {
			lg.Record("user", event.UserInput, map[string]any{"text": "synthetic prompt"})
		}
		if err := lg.Close(); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		seed(fmt.Sprintf("s_%03d", i), 1000)
	}
	seed("s_large", 20000)
	mgr := session.NewManager(config.NewRegistry(&config.Config{}), ws)
	defer mgr.ReclaimAll()
	recorder := server.NewLatencyRecorder()
	path, handler := yccv1connect.NewSessionServiceHandler(server.New(mgr), connect.WithInterceptors(recorder))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	ts := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	defer ts.Close()
	client := yccv1connect.NewSessionServiceClient(ts.Client(), ts.URL)
	// Same manager and synthetic logs, without the diagnostics interceptor: a
	// loopback baseline, not an earlier iOS or daemon release measurement.
	barePath, bareHandler := yccv1connect.NewSessionServiceHandler(server.New(mgr))
	bareMux := http.NewServeMux()
	bareMux.Handle(barePath, bareHandler)
	bareServer := httptest.NewServer(h2c.NewHandler(bareMux, &http2.Server{}))
	defer bareServer.Close()
	bareClient := yccv1connect.NewSessionServiceClient(bareServer.Client(), bareServer.URL)
	ctx := context.Background()
	view, err := client.GetSessionView(ctx, connect.NewRequest(&v1.GetSessionViewRequest{SessionId: "s_large", MaxRows: 200, MaxBytes: 393216}))
	if err != nil || view.Msg.EarlierCursor == "" {
		b.Fatalf("large view cursor unavailable: %v", err)
	}
	cursor := view.Msg.EarlierCursor
	for _, tc := range []struct {
		name string
		call func(yccv1connect.SessionServiceClient) (http.Header, error)
	}{
		{"ListSessionHistory_100logs_120kEvents", func(c yccv1connect.SessionServiceClient) (http.Header, error) {
			r, e := c.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{}))
			if e != nil {
				return nil, e
			}
			return r.Header(), nil
		}},
		{"ListSessionHistory_limit50_100logs_120kEvents", func(c yccv1connect.SessionServiceClient) (http.Header, error) {
			r, e := c.ListSessionHistory(ctx, connect.NewRequest(&v1.ListSessionHistoryRequest{Limit: 50}))
			if e != nil {
				return nil, e
			}
			return r.Header(), nil
		}},
		{"GetSessionView_20kEvents", func(c yccv1connect.SessionServiceClient) (http.Header, error) {
			r, e := c.GetSessionView(ctx, connect.NewRequest(&v1.GetSessionViewRequest{SessionId: "s_large", MaxRows: 200, MaxBytes: 393216}))
			if e != nil {
				return nil, e
			}
			return r.Header(), nil
		}},
		{"GetSessionViewPage_20kEvents", func(c yccv1connect.SessionServiceClient) (http.Header, error) {
			r, e := c.GetSessionViewPage(ctx, connect.NewRequest(&v1.GetSessionViewPageRequest{SessionId: "s_large", Cursor: cursor, MaxRows: 200, MaxBytes: 393216}))
			if e != nil {
				return nil, e
			}
			return r.Header(), nil
		}},
	} {
		for _, variant := range []struct {
			name   string
			client yccv1connect.SessionServiceClient
			timed  bool
		}{
			{"baseline", bareClient, false}, {"instrumented", client, true},
		} {
			b.Run(tc.name+"/"+variant.name, func(b *testing.B) {
				var serverMS, totalMS float64
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					start := time.Now()
					hdr, err := tc.call(variant.client)
					if err != nil {
						b.Fatal(err)
					}
					totalMS += float64(time.Since(start)) / float64(time.Millisecond)
					if variant.timed {
						serverMS += timingDuration(b, hdr.Get("Server-Timing"))
					}
				}
				b.StopTimer()
				b.ReportMetric(totalMS/float64(b.N), "loopback_ms/op")
				if variant.timed {
					b.ReportMetric(serverMS/float64(b.N), "daemon_ms/op")
				}
			})
		}
	}
}

func timingDuration(b *testing.B, value string) float64 {
	b.Helper()
	ms, err := strconv.ParseFloat(strings.TrimPrefix(value, "app;dur="), 64)
	if err != nil {
		b.Fatalf("Server-Timing %q: %v", value, err)
	}
	return ms
}
