// Package subusage fetches and caches best-effort provider-side subscription
// allowance. These provider-internal endpoints are telemetry only: callers must
// never use their results to gate or route model turns.
package subusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
)

const (
	anthropicUsageURL = "https://api.anthropic.com/api/oauth/usage"
	openAIUsageURL    = "https://chatgpt.com/backend-api/wham/usage"
)

// Window is one provider-side rolling allowance bucket.
type Window struct {
	ID            string
	Label         string
	UsedPercent   float64
	ResetsAt      time.Time
	WindowSeconds int64
}

// Account is one OAuth provider account's allowance snapshot.
type Account struct {
	Provider  string
	Plan      string
	Models    []string
	Windows   []Window
	State     string // fresh | stale | unavailable
	FetchedAt time.Time
	Message   string
}

// Fetcher obtains a live snapshot. Implementations must not return credentials
// or include them in errors.
type Fetcher interface {
	Fetch(context.Context, string) (Account, error)
}

// HTTPFetcher talks to the provider-internal endpoints used by their official
// coding clients. Function fields make credential access and HTTP testable
// without exposing either over the daemon API.
type HTTPFetcher struct {
	Client            *http.Client
	AnthropicURL      string
	OpenAIURL         string
	AnthropicToken    func(context.Context) (string, error)
	OpenAICredentials func(context.Context) (string, string, error)
}

// NewHTTPFetcher returns the production fetcher.
func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{
		Client:            http.DefaultClient,
		AnthropicURL:      anthropicUsageURL,
		OpenAIURL:         openAIUsageURL,
		AnthropicToken:    anthropicauth.AccessToken,
		OpenAICredentials: openaiauth.AccessToken,
	}
}

// Fetch obtains a provider snapshot.
func (f *HTTPFetcher) Fetch(ctx context.Context, provider string) (Account, error) {
	switch provider {
	case "anthropic":
		return f.fetchAnthropic(ctx)
	case "openai":
		return f.fetchOpenAI(ctx)
	default:
		return Account{}, fmt.Errorf("subscription usage is unsupported for %s", provider)
	}
}

func (f *HTTPFetcher) fetchAnthropic(ctx context.Context) (Account, error) {
	token, err := f.AnthropicToken(ctx)
	if err != nil {
		return Account{}, errors.New("subscription credentials unavailable")
	}
	var payload map[string]json.RawMessage
	if err := f.getJSON(ctx, f.AnthropicURL, token, "", &payload, true); err != nil {
		return Account{}, err
	}
	type bucketPayload struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	buckets := map[string]bucketPayload{}
	keys := make([]string, 0, len(payload))
	for key, raw := range payload {
		var bucket bucketPayload
		if json.Unmarshal(raw, &bucket) == nil && bucket.Utilization != nil {
			keys = append(keys, key)
			buckets[key] = bucket
		}
	}
	sort.Slice(keys, func(i, j int) bool { return anthropicRank(keys[i]) < anthropicRank(keys[j]) })
	out := Account{Provider: "anthropic"}
	for _, key := range keys {
		bucket := buckets[key]
		reset, _ := time.Parse(time.RFC3339, bucket.ResetsAt)
		out.Windows = append(out.Windows, Window{
			ID: key, Label: anthropicLabel(key), UsedPercent: clamp(*bucket.Utilization),
			ResetsAt: reset, WindowSeconds: inferredAnthropicWindow(key),
		})
	}
	if len(out.Windows) == 0 {
		return Account{}, errors.New("provider returned no subscription usage windows")
	}
	return out, nil
}

func anthropicRank(key string) string {
	switch key {
	case "five_hour":
		return "0"
	case "seven_day":
		return "1"
	case "seven_day_opus":
		return "2"
	case "seven_day_sonnet":
		return "3"
	default:
		return "9" + key
	}
}

func anthropicLabel(key string) string {
	switch key {
	case "five_hour":
		return "5 hour"
	case "seven_day":
		return "7 day"
	case "seven_day_opus":
		return "7 day · Opus"
	case "seven_day_sonnet":
		return "7 day · Sonnet"
	default:
		return strings.ReplaceAll(key, "_", " ")
	}
}

func inferredAnthropicWindow(key string) int64 {
	if key == "five_hour" {
		return 5 * 60 * 60
	}
	if strings.HasPrefix(key, "seven_day") {
		return 7 * 24 * 60 * 60
	}
	return 0
}

type openAIWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type openAIRateLimit struct {
	Primary   *openAIWindow `json:"primary_window"`
	Secondary *openAIWindow `json:"secondary_window"`
}

type openAIExtra struct {
	LimitName      string          `json:"limit_name"`
	MeteredFeature string          `json:"metered_feature"`
	RateLimit      openAIRateLimit `json:"rate_limit"`
}

type openAIPayload struct {
	PlanType             string          `json:"plan_type"`
	RateLimit            openAIRateLimit `json:"rate_limit"`
	AdditionalRateLimits []openAIExtra   `json:"additional_rate_limits"`
}

func (f *HTTPFetcher) fetchOpenAI(ctx context.Context) (Account, error) {
	token, accountID, err := f.OpenAICredentials(ctx)
	if err != nil {
		return Account{}, errors.New("subscription credentials unavailable")
	}
	var payload openAIPayload
	if err := f.getJSON(ctx, f.OpenAIURL, token, accountID, &payload, false); err != nil {
		return Account{}, err
	}
	out := Account{Provider: "openai", Plan: payload.PlanType}
	appendOpenAIWindows := func(prefix string, limits openAIRateLimit) {
		if limits.Primary != nil {
			out.Windows = append(out.Windows, mapOpenAIWindow(prefix+"primary", prefix, limits.Primary))
		}
		if limits.Secondary != nil {
			out.Windows = append(out.Windows, mapOpenAIWindow(prefix+"secondary", prefix, limits.Secondary))
		}
	}
	appendOpenAIWindows("", payload.RateLimit)
	for _, extra := range payload.AdditionalRateLimits {
		label := extra.LimitName
		if label == "" {
			label = extra.MeteredFeature
		}
		if label != "" {
			label += " · "
		}
		appendOpenAIWindows(label, extra.RateLimit)
	}
	if len(out.Windows) == 0 {
		return Account{}, errors.New("provider returned no subscription usage windows")
	}
	return out, nil
}

func mapOpenAIWindow(id, prefix string, in *openAIWindow) Window {
	reset := time.Time{}
	if in.ResetAt > 0 {
		reset = time.Unix(in.ResetAt, 0)
	} else if in.ResetAfterSeconds > 0 {
		reset = time.Now().Add(time.Duration(in.ResetAfterSeconds) * time.Second)
	}
	label := windowLabel(in.LimitWindowSeconds)
	if prefix != "" {
		label = prefix + label
	}
	return Window{ID: id, Label: label, UsedPercent: clamp(in.UsedPercent), ResetsAt: reset, WindowSeconds: in.LimitWindowSeconds}
}

func windowLabel(seconds int64) string {
	if seconds <= 0 {
		return "Allowance"
	}
	if seconds%(24*60*60) == 0 {
		return fmt.Sprintf("%d day", seconds/(24*60*60))
	}
	return fmt.Sprintf("%d hour", max(int64(1), seconds/(60*60)))
}

func (f *HTTPFetcher) getJSON(ctx context.Context, url, token, accountID string, out any, anthropic bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("building subscription usage request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "ycc")
	if anthropic {
		req.Header.Set("anthropic-beta", anthropicauth.BetaHeader)
	} else if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("subscription usage request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("subscription usage request returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("provider returned invalid subscription usage data")
	}
	return nil
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// Service applies cache, timeout, stale-on-error, and account/model association.
type Service struct {
	mu          sync.Mutex
	fetcher     Fetcher
	ttl         time.Duration
	timeout     time.Duration
	now         func() time.Time
	cache       map[string]Account
	lastAttempt map[string]time.Time
}

// NewService constructs a subscription usage service with production defaults.
func NewService(fetcher Fetcher) *Service {
	if fetcher == nil {
		fetcher = NewHTTPFetcher()
	}
	return &Service{
		fetcher: fetcher, ttl: time.Minute, timeout: 5 * time.Second, now: time.Now,
		cache: map[string]Account{}, lastAttempt: map[string]time.Time{},
	}
}

// Get returns one report per provider. models maps provider names to the logical
// model names sharing that OAuth account.
func (s *Service) Get(ctx context.Context, models map[string][]string, refresh bool) []Account {
	providers := make([]string, 0, len(models))
	for provider := range models {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	out := make([]Account, 0, len(providers))
	for _, provider := range providers {
		out = append(out, s.get(ctx, provider, models[provider], refresh))
	}
	return out
}

func (s *Service) get(ctx context.Context, provider string, models []string, _ bool) Account {
	now := s.now()
	s.mu.Lock()
	cached, ok := s.cache[provider]
	lastAttempt := s.lastAttempt[provider]
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < s.ttl {
		s.mu.Unlock()
		if ok {
			cached.Models = append([]string(nil), models...)
			return cached
		}
		return Account{Provider: provider, Models: append([]string(nil), models...), State: "unavailable", Message: "subscription allowance temporarily unavailable"}
	}
	s.lastAttempt[provider] = now
	s.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, s.timeout)
	fresh, err := s.fetcher.Fetch(fetchCtx, provider)
	cancel()
	if err == nil {
		fresh.Provider = provider
		fresh.Models = append([]string(nil), models...)
		sort.Strings(fresh.Models)
		fresh.State = "fresh"
		fresh.FetchedAt = now
		s.mu.Lock()
		s.cache[provider] = fresh
		s.mu.Unlock()
		return fresh
	}
	if ok {
		cached.Models = append([]string(nil), models...)
		cached.State = "stale"
		cached.Message = "refresh failed; showing last known allowance"
		s.mu.Lock()
		s.cache[provider] = cached
		s.mu.Unlock()
		return cached
	}
	return Account{Provider: provider, Models: append([]string(nil), models...), State: "unavailable", Message: err.Error()}
}
