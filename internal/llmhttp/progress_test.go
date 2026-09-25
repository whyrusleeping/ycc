package llmhttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeneratedFramesExcludeHeartbeatAndLifecycle(t *testing.T) {
	for _, line := range []string{
		": ping", "data: [DONE]", `data: {"type":"ping"}`,
		`data: {"type":"message_start"}`, `data: {"type":"response.created"}`,
		`data: {"type":"response.in_progress"}`, `data: {"choices":[{"delta":{"role":"assistant"}}]}`,
	} {
		if generatedFrame([]byte(line)) {
			t.Fatalf("lifecycle frame counted as output: %s", line)
		}
	}
	for _, line := range []string{
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`data: {"type":"content_block_start","content_block":{"type":"tool_use"}}`,
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"hmm"}`,
		`data: {"type":"response.reasoning_text.delta","delta":"hmm"}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call"}}`,
		`data: {"choices":[{"delta":{"reasoning":"hmm"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`,
	} {
		if !generatedFrame([]byte(line)) {
			t.Fatalf("generated frame ignored: %s", line)
		}
	}
}

func TestProgressTracksSplitSSELines(t *testing.T) {
	for _, generated := range []string{
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"planning"}}` + "\n\n",
		`data: {"type":"response.function_call_arguments.delta","delta":"abc"}` + "\n\n",
	} {
		t.Run(generated[14:30], func(t *testing.T) {
			release := make(chan struct{})
			lifecycle := "data: {\"type\":\"response.created\"}\n\n: ping\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, lifecycle)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, generated)
			}))
			defer server.Close()
			ctx, progress := WithProgress(context.Background())
			req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
			resp, err := NewClient(Policy{}).Do(req)
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			defer resp.Body.Close()
			buf := make([]byte, 7) // force data lines to cross Read boundaries
			got := ""
			for len(got) < len(lifecycle) {
				n, err := resp.Body.Read(buf)
				if err != nil {
					close(release)
					t.Fatal(err)
				}
				got += string(buf[:n])
			}
			if got != lifecycle || progress.Generated() {
				close(release)
				t.Fatalf("lifecycle=%q progress=%v", got, progress.Generated())
			}
			close(release)
			rest, err := io.ReadAll(resp.Body)
			if err != nil || !strings.Contains(string(rest), "data:") || !progress.Generated() {
				t.Fatalf("rest=%q err=%v progress=%v", rest, err, progress.Generated())
			}
		})
	}
}
