package config

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeModelUsesDraftRequestShape(t *testing.T) {
	const key = "fw-secret-test-key"
	t.Setenv("PROBE_API_KEY", key)

	type capturedRequest struct {
		path          string
		authorization string
		body          map[string]any
	}
	captured := make(chan capturedRequest, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured <- capturedRequest{path: r.URL.Path, authorization: r.Header.Get("Authorization"), body: body}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"model\":\"glm\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer provider.Close()

	draft := Model{
		Backend: "openai", BaseURL: provider.URL, Model: "accounts/fireworks/models/glm-5p3",
		KeyEnv: "PROBE_API_KEY", Thinking: "adaptive", Effort: "max", ThinkingDisplay: "summarized",
		Disabled: true,
	}
	duration, err := ProbeModel(context.Background(), "fireworks-glm", draft)
	if err != nil {
		t.Fatalf("ProbeModel: %v", err)
	}
	if duration < 0 {
		t.Fatalf("duration = %v", duration)
	}

	request := <-captured
	if request.path != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", request.path)
	}
	if request.authorization != "Bearer "+key {
		t.Fatalf("Authorization = %q", request.authorization)
	}
	if got := request.body["model"]; got != draft.Model {
		t.Fatalf("model = %v, want %q", got, draft.Model)
	}
	if got := request.body["reasoning_effort"]; got != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", got)
	}
	if got := request.body["max_tokens"]; got != float64(modelProbeMaxTokens) {
		t.Fatalf("max_tokens = %v, want %d", got, modelProbeMaxTokens)
	}
	if got := request.body["stream"]; got != true {
		t.Fatalf("stream = %v, want true", got)
	}
	messages, ok := request.body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want system + user", request.body["messages"])
	}
	if role, _ := messages[0].(map[string]any)["role"].(string); role != "system" {
		t.Fatalf("first message role = %q, want system", role)
	}
}

func TestProbeModelRejectsEmptyCompletionStream(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	_, err := ProbeModel(context.Background(), "empty", Model{
		Backend: "openai", BaseURL: provider.URL, Model: "empty",
	})
	if err == nil || !strings.Contains(err.Error(), "no completion choices") {
		t.Fatalf("ProbeModel error = %v, want empty-completion error", err)
	}
}

func TestProbeModelProviderFailureRedactsCredential(t *testing.T) {
	const key = "credential-that-must-not-escape"
	t.Setenv("PROBE_API_KEY", key)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `invalid key `+key, http.StatusUnauthorized)
	}))
	defer provider.Close()

	_, err := ProbeModel(context.Background(), "bad", Model{
		Backend: "openai", BaseURL: provider.URL, Model: "missing", KeyEnv: "PROBE_API_KEY",
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("credential leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("redacted error = %q, want redaction marker", err)
	}
}

func TestProbeModelHonorsContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer provider.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-requestStarted
		cancel()
	}()
	_, err := ProbeModel(ctx, "cancelled", Model{Backend: "openai", BaseURL: provider.URL, Model: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ProbeModel error = %v, want context canceled", err)
	}
}

func TestProbeModelHonorsContextDeadline(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Some transports do not propagate client cancellation to the server-side
		// request context immediately. Keep the fallback finite so Server.Close does
		// not hang while still making the client deadline win decisively.
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ProbeModel(ctx, "slow", Model{Backend: "openai", BaseURL: provider.URL, Model: "slow"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ProbeModel error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probe ignored deadline: %v", elapsed)
	}
}
