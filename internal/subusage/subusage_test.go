package subusage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/antigravity"
	"github.com/whyrusleeping/ycc/internal/googleauth"
)

func TestHTTPFetcherAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got == "" {
			t.Fatal("missing anthropic oauth beta header")
		}
		if got := r.Header.Get("x-app"); got != "cli" {
			t.Fatalf("x-app = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":12.5,"resets_at":"2026-07-20T12:00:00Z"},"seven_day":{"utilization":42,"resets_at":"2026-07-25T12:00:00Z"}}`))
	}))
	defer srv.Close()

	f := NewHTTPFetcher()
	f.AnthropicURL = srv.URL
	f.AnthropicToken = func(context.Context) (string, error) { return "secret", nil }
	got, err := f.Fetch(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 2 || got.Windows[0].Label != "5 hour" || got.Windows[0].UsedPercent != 12.5 {
		t.Fatalf("windows = %+v", got.Windows)
	}
}

func TestHTTPFetcherOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct" {
			t.Fatalf("account header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_at":1784548800},"secondary_window":{"used_percent":55,"limit_window_seconds":604800,"reset_at":1784980800}}}`))
	}))
	defer srv.Close()

	f := NewHTTPFetcher()
	f.OpenAIURL = srv.URL
	f.OpenAICredentials = func(context.Context) (string, string, error) { return "secret", "acct", nil }
	got, err := f.Fetch(context.Background(), "openai")
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan != "pro" || len(got.Windows) != 2 || got.Windows[0].Label != "5 hour" {
		t.Fatalf("account = %+v", got)
	}
}

func TestHTTPFetcherGoogle(t *testing.T) {
	// Shape of a live retrieveUserQuotaSummary reply (2026-11-26).
	const body = `{"groups":[
	 {"displayName":"Gemini Models","buckets":[
	  {"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","window":"weekly","resetTime":"2026-12-01T06:32:27Z","remainingFraction":0.88},
	  {"bucketId":"gemini-5h","displayName":"Five Hour Limit Remaining","window":"5h","resetTime":"2026-11-26T08:52:46Z","remainingFraction":0.75}]},
	 {"displayName":"Claude and GPT models","buckets":[
	  {"bucketId":"3p-weekly","window":"weekly","remainingFraction":1},
	  {"bucketId":"3p-off","window":"5h","disabled":true,"remainingFraction":1}]}]}`
	f := NewHTTPFetcher()
	f.GoogleQuota = func(context.Context) (antigravity.QuotaSummary, error) {
		var s antigravity.QuotaSummary
		return s, json.Unmarshal([]byte(body), &s)
	}
	got, err := f.Fetch(context.Background(), "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("windows = %+v", got.Windows)
	}
	w := got.Windows[0]
	if w.ID != "gemini-5h" || w.Label != "Gemini Models · 5 hour" || w.UsedPercent != 25 || w.WindowSeconds != 5*60*60 || w.ResetsAt.IsZero() {
		t.Fatalf("first window = %+v", w)
	}
	if got.Windows[1].Label != "Gemini Models · 7 day" || got.Windows[2].Label != "Claude and GPT models · 7 day" || got.Windows[2].UsedPercent != 0 {
		t.Fatalf("windows = %+v", got.Windows)
	}

	f.GoogleQuota = func(context.Context) (antigravity.QuotaSummary, error) {
		return antigravity.QuotaSummary{}, googleauth.ErrNoLogin
	}
	if _, err := f.Fetch(context.Background(), "gemini"); err == nil || err.Error() != "subscription credentials unavailable" {
		t.Fatalf("no-login error = %v", err)
	}
}

type fakeFetcher struct {
	calls int
	value Account
	err   error
}

func (f *fakeFetcher) Fetch(context.Context, string) (Account, error) {
	f.calls++
	return f.value, f.err
}

func TestServiceCachesAndServesStale(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	ff := &fakeFetcher{value: Account{Windows: []Window{{Label: "5 hour", UsedPercent: 10}}}}
	s := NewService(ff)
	s.now = func() time.Time { return now }

	first := s.Get(context.Background(), map[string][]string{"anthropic": {"a", "b"}}, true)
	if ff.calls != 1 || first[0].State != "fresh" || len(first[0].Models) != 2 {
		t.Fatalf("first=%+v calls=%d", first, ff.calls)
	}
	_ = s.Get(context.Background(), map[string][]string{"anthropic": {"a"}}, true)
	if ff.calls != 1 {
		t.Fatalf("cache was bypassed: calls=%d", ff.calls)
	}

	now = now.Add(2 * time.Minute)
	ff.err = errors.New("network details that should not escape fetcher")
	stale := s.Get(context.Background(), map[string][]string{"anthropic": {"a"}}, true)
	if stale[0].State != "stale" || stale[0].FetchedAt.IsZero() {
		t.Fatalf("stale=%+v", stale[0])
	}
}
