package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

func TestCodexPartialCleanEOFIsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}))
	defer server.Close()
	client := New(server.URL, func(context.Context) (string, string, error) { return "token", "account", nil })
	var partial string
	resp, err := client.TurnStreamCtx(context.Background(), gollama.RequestOptions{Model: "test"}, func(text string) { partial = text })
	if resp != nil || partial != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("response=%v partial=%q error=%v, want partial and unexpected EOF", resp, partial, err)
	}
}

func TestConfiguredTransportStallsPartialCodexStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(server.URL, func(context.Context) (string, string, error) { return "token", "account", nil })
	client.SetHTTPClient(llmhttp.NewClient(llmhttp.Policy{StreamIdle: 75 * time.Millisecond}))
	var partial string
	_, err := client.TurnStreamCtx(context.Background(), gollama.RequestOptions{Model: "test"}, func(text string) { partial = text })
	if partial != "partial" || !errors.Is(err, llmhttp.ErrStreamStalled) {
		t.Fatalf("partial=%q error=%v, want partial + stall", partial, err)
	}
}
