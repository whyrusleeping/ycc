package llmhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProgressAndHeartbeatsOutlastIdle(t *testing.T) {
	const idle = 120 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fl := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			_, _ = io.WriteString(w, ": ping\n\n")
			fl.Flush()
			time.Sleep(35 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: complete\n\n")
	}))
	defer server.Close()
	resp, err := NewClient(Policy{StreamIdle: idle, Total: time.Second}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "complete") {
		t.Fatalf("missing completion: %q", data)
	}
}

func TestIdleStallInterruptsRead(t *testing.T) {
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: partial\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	resp, err := NewClient(Policy{StreamIdle: 75 * time.Millisecond}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrStreamStalled) {
		t.Fatalf("read error = %v, want stall", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stalled read did not return promptly")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server handler stranded after stall")
	}
}

func TestCallerCancellationIsNotStall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": ping\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	resp, err := NewClient(Policy{StreamIdle: 250 * time.Millisecond}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrStreamStalled) {
		t.Fatalf("read error = %v, want caller cancellation", err)
	}
}

func TestTotalDeadlineDuringStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			_, _ = io.WriteString(w, ": ping\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
	}))
	defer server.Close()
	resp, err := NewClient(Policy{StreamIdle: time.Second, Total: 120 * time.Millisecond}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrTotalTimeout) || errors.Is(err, ErrStreamStalled) {
		t.Fatalf("read error = %v, want total timeout", err)
	}
}

func TestTotalDeadlineBeforeHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	_, err := NewClient(Policy{Total: 60 * time.Millisecond}).Get(server.URL)
	if !errors.Is(err, ErrTotalTimeout) {
		t.Fatalf("request error = %v, want total timeout", err)
	}
}

func getCtx(t *testing.T, client *http.Client, ctx context.Context, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client.Do(req)
}

func TestStreamWithoutHeadersStallsFast(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := NewClient(Policy{ResponseHeader: 60 * time.Millisecond, StreamIdle: time.Hour, Total: time.Hour})
	start := time.Now()
	_, err := getCtx(t, client, ExpectStream(context.Background()), server.URL)
	if !errors.Is(err, ErrStreamStalled) || errors.Is(err, ErrTotalTimeout) {
		t.Fatalf("request error = %v, want stall", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stall took %s", elapsed)
	}
}

func TestResponseHeaderDeadlineOnlyForStreams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `{"done":true}`)
	}))
	defer server.Close()
	client := NewClient(Policy{ResponseHeader: 40 * time.Millisecond})
	resp, err := getCtx(t, client, context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unmarked request error = %v", err)
	}
	resp.Body.Close()
}

func TestResponseHeaderDeadlineStopsOnceStreamOpens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "data: complete\n\n")
	}))
	defer server.Close()
	client := NewClient(Policy{ResponseHeader: 40 * time.Millisecond, StreamIdle: time.Second})
	resp, err := getCtx(t, client, ExpectStream(context.Background()), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "complete") {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestCallerCancellationBeforeHeadersIsNotStall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(ExpectStream(context.Background()), 30*time.Millisecond)
	defer cancel()
	_, err := getCtx(t, NewClient(Policy{ResponseHeader: time.Hour}), ctx, server.URL)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrStreamStalled) {
		t.Fatalf("request error = %v, want caller deadline", err)
	}
}
