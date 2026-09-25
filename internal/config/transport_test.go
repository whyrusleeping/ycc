package config

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

// A clean EOF is not a successful model turn unless the provider emitted its
// terminal marker. Both gollama backends must preserve that error through Build.
func TestRegistryBuiltGollamaTruncatedStream(t *testing.T) {
	for _, tc := range []struct{ backend, frame string }{
		{"openai-compatible", `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"},
		{"anthropic", `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n\n"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.frame)
			}))
			defer server.Close()
			reg := NewRegistry(&Config{Models: map[string]Model{"test": {Backend: tc.backend, BaseURL: server.URL, Model: "test"}}})
			client, model, err := reg.Build("test")
			if err != nil {
				t.Fatal(err)
			}
			var partial string
			resp, err := client.(engine.StreamTurner).TurnStreamCtx(context.Background(), gollama.RequestOptions{Model: model}, func(text string) { partial = text })
			info := engine.ClassifyAPIError(err)
			if resp != nil || partial != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) || info.Kind != engine.KindNetwork || !info.Retryable {
				t.Fatalf("resp=%v partial=%q err=%v classification=%+v, want retryable truncated stream", resp, partial, err, info)
			}
		})
	}
}

func TestRegistryBuiltGollamaStreamStall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	idle := 1
	reg := NewRegistry(&Config{Models: map[string]Model{"test": {Backend: "openai-compatible", BaseURL: server.URL, Model: "test"}}, Transport: Transport{StreamIdleSeconds: &idle}})
	client, _, err := reg.Build("test")
	if err != nil {
		t.Fatal(err)
	}
	var partial string
	_, err = client.(engine.StreamTurner).TurnStreamCtx(context.Background(), gollama.RequestOptions{Model: "test"}, func(text string) { partial = text })
	if partial != "partial" || !errors.Is(err, llmhttp.ErrStreamStalled) {
		t.Fatalf("partial=%q error=%v, want partial + stall", partial, err)
	}
}

func TestTransportConfigDefaultsAndExplicitZero(t *testing.T) {
	reg := baseRegistry()
	if got := reg.TransportPolicy(); got != llmhttp.DefaultPolicy() {
		t.Fatalf("default = %+v", got)
	}
	src := `[models.c]
backend = "ollama"
model = "m"
[roles]
coordinator = "c"
implementer = "c"
reviewers = ["c"]
[transport]
stream_idle_seconds = 0
total_timeout_seconds = 90
[retry]
partial_max_attempts = 3
`
	path := filepath.Join(t.TempDir(), "ycc.toml")
	if err := os.WriteFile(path, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reg = NewRegistry(cfg)
	if got := reg.TransportPolicy(); got.StreamIdle != 0 || got.Total != 90*time.Second {
		t.Fatalf("policy = %+v", got)
	}
	if got := reg.RetryPolicy().PartialMaxAttempts; got != 3 {
		t.Fatalf("partial cap = %d", got)
	}
	neg := -1
	cfg.Transport.StreamIdleSeconds = &neg
	if err := cfg.validate(); err == nil {
		t.Fatal("negative idle accepted")
	}
}
