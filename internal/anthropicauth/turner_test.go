package anthropicauth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
)

type captureTurner struct {
	turnOpts   gollama.RequestOptions
	streamOpts gollama.RequestOptions
	delta      string
	err        error
}

func (c *captureTurner) TurnCtx(_ context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	c.turnOpts = opts
	return nil, c.err
}

func (c *captureTurner) TurnStreamCtx(_ context.Context, opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	c.streamOpts = opts
	if c.delta != "" {
		onDelta(c.delta)
	}
	return nil, c.err
}

func wantBlocks(prompt string) []gollama.SystemBlock {
	return []gollama.SystemBlock{
		{Text: BillingSystemPrefix},
		{Text: AgentSystemPrefix},
		{Text: prompt, Cache: true},
	}
}

func TestPrefixSystemString(t *testing.T) {
	opts := gollama.RequestOptions{System: "You are ycc."}
	got := PrefixSystem(opts)
	if got.System != "" {
		t.Fatalf("System = %q, want empty after promotion", got.System)
	}
	if want := wantBlocks("You are ycc."); !reflect.DeepEqual(got.SystemBlocks, want) {
		t.Fatalf("SystemBlocks = %#v, want %#v", got.SystemBlocks, want)
	}
	// The input value and repeated wrapping are stable.
	if opts.System != "You are ycc." || len(opts.SystemBlocks) != 0 {
		t.Fatalf("input mutated: %#v", opts)
	}
	if twice := PrefixSystem(got); !reflect.DeepEqual(twice.SystemBlocks, got.SystemBlocks) {
		t.Fatalf("PrefixSystem is not idempotent: %#v", twice.SystemBlocks)
	}
}

func TestPrefixSystemPreservesExistingBlocks(t *testing.T) {
	original := []gollama.SystemBlock{{Text: "static", Cache: true}, {Text: "dynamic"}}
	got := PrefixSystem(gollama.RequestOptions{System: "ignored by gollama precedence", SystemBlocks: original})
	want := []gollama.SystemBlock{{Text: BillingSystemPrefix}, {Text: AgentSystemPrefix}, original[0], original[1]}
	if !reflect.DeepEqual(got.SystemBlocks, want) || got.System != "" {
		t.Fatalf("got System=%q blocks=%#v, want blocks=%#v", got.System, got.SystemBlocks, want)
	}
}

func TestTurnerPrefixesStreamingAndNonStreaming(t *testing.T) {
	inner := &captureTurner{delta: "live", err: errors.New("stop")}
	turner := NewTurner(inner)
	opts := gollama.RequestOptions{System: "You are ycc."}
	if _, err := turner.TurnCtx(context.Background(), opts); !errors.Is(err, inner.err) {
		t.Fatalf("Turn error = %v", err)
	}
	var delta string
	if _, err := turner.TurnStreamCtx(context.Background(), opts, func(s string) { delta = s }); !errors.Is(err, inner.err) {
		t.Fatalf("TurnStream error = %v", err)
	}
	if delta != "live" {
		t.Fatalf("delta = %q", delta)
	}
	want := wantBlocks("You are ycc.")
	if !reflect.DeepEqual(inner.turnOpts.SystemBlocks, want) {
		t.Fatalf("Turn blocks = %#v", inner.turnOpts.SystemBlocks)
	}
	if !reflect.DeepEqual(inner.streamOpts.SystemBlocks, want) {
		t.Fatalf("TurnStream blocks = %#v", inner.streamOpts.SystemBlocks)
	}
}

// scriptedTurner replies with the queued outcome for each call, recording the
// bearer credential the transport carried at that moment.
type scriptedTurner struct {
	token   *string
	replies []error
	seen    []string
	deltas  int
}

func (s *scriptedTurner) next() error {
	s.seen = append(s.seen, *s.token)
	if len(s.replies) == 0 {
		return nil
	}
	err := s.replies[0]
	s.replies = s.replies[1:]
	return err
}

func (s *scriptedTurner) TurnCtx(context.Context, gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return nil, s.next()
}

func (s *scriptedTurner) TurnStreamCtx(_ context.Context, _ gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	err := s.next()
	if err == nil {
		s.deltas++
		onDelta("live")
	}
	return nil, err
}

// oauthFixture wires a scripted client to a token source over a mutable stored
// token, standing in for the secrets store that other ycc processes share.
type oauthFixture struct {
	inner    *scriptedTurner
	turner   *Turner
	stored   string // what the "secrets store" currently holds
	onDisk   string // bearer installed on the transport
	refresh  func(stale string) string
	refreshN int
}

func newOAuthFixture(t *testing.T, stored string, replies ...error) *oauthFixture {
	t.Helper()
	f := &oauthFixture{stored: stored}
	f.inner = &scriptedTurner{token: &f.onDisk, replies: replies}
	f.turner = NewOAuthTurner(f.inner, TokenSource{
		Token: func(context.Context) (string, error) { return f.stored, nil },
		Refresh: func(_ context.Context, stale string) (string, error) {
			f.refreshN++
			if f.refresh == nil {
				return stale, nil
			}
			f.stored = f.refresh(stale)
			return f.stored, nil
		},
		Apply: func(tok string) { f.onDisk = tok },
	})
	return f
}

func revoked() error {
	return errors.New(`API returned non-200 status code 401: {"type":"error","error":{"type":"authentication_error","message":"OAuth access token has been revoked."},"request_id":null}`)
}

// A session outlives the token it was built with: every turn must install
// whatever credential the shared store currently holds, so a refresh performed
// by another process is picked up instead of sending a dead token.
func TestOAuthTurnerResolvesTokenEveryTurn(t *testing.T) {
	f := newOAuthFixture(t, "tok-1", nil, nil, nil)
	if _, err := f.turner.TurnCtx(context.Background(), gollama.RequestOptions{System: "sys"}); err != nil {
		t.Fatal(err)
	}
	f.stored = "tok-2" // another ycc process refreshed underneath us
	if _, err := f.turner.TurnCtx(context.Background(), gollama.RequestOptions{System: "sys"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.turner.TurnStreamCtx(context.Background(), gollama.RequestOptions{System: "sys"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tok-1", "tok-2", "tok-2"}; !reflect.DeepEqual(f.inner.seen, want) {
		t.Fatalf("tokens sent = %v, want %v", f.inner.seen, want)
	}
	if f.refreshN != 0 {
		t.Fatalf("forced %d refreshes on healthy turns", f.refreshN)
	}
	// The system prefix still applies on the refreshing path.
	if f.inner.deltas != 1 {
		t.Fatalf("stream delta count = %d", f.inner.deltas)
	}
}

// The rotation can also land mid-turn, after the token was resolved: a revoked
// 401 is recovered with one forced refresh and one retry, not a dead session.
func TestOAuthTurnerRetriesOnceAfterRevoked(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*oauthFixture) error
	}{
		{"turn", func(f *oauthFixture) error {
			_, err := f.turner.TurnCtx(context.Background(), gollama.RequestOptions{System: "sys"})
			return err
		}},
		{"stream", func(f *oauthFixture) error {
			_, err := f.turner.TurnStreamCtx(context.Background(), gollama.RequestOptions{System: "sys"}, func(string) {})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t, "tok-revoked", revoked(), nil)
			f.refresh = func(string) string { return "tok-fresh" }
			if err := tc.run(f); err != nil {
				t.Fatalf("turn not recovered: %v", err)
			}
			if want := []string{"tok-revoked", "tok-fresh"}; !reflect.DeepEqual(f.inner.seen, want) {
				t.Fatalf("tokens sent = %v, want %v", f.inner.seen, want)
			}
			if f.refreshN != 1 {
				t.Fatalf("forced refreshes = %d, want 1", f.refreshN)
			}
		})
	}
}

// Recovery is attempted once. When it cannot produce a different credential the
// caller sees the provider's own rejection, which carries the actionable hint.
func TestOAuthTurnerReportsUnrecoverableRevocation(t *testing.T) {
	f := newOAuthFixture(t, "tok-revoked", revoked(), revoked())
	err := func() error {
		_, err := f.turner.TurnCtx(context.Background(), gollama.RequestOptions{System: "sys"})
		return err
	}()
	if err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("err = %v, want the provider rejection", err)
	}
	if len(f.inner.seen) != 1 || f.refreshN != 1 {
		t.Fatalf("attempts = %v, refreshes = %d; want one of each", f.inner.seen, f.refreshN)
	}
}

// A rejection that is not about the credential must not burn a refresh.
func TestOAuthTurnerDoesNotRefreshOnOtherErrors(t *testing.T) {
	f := newOAuthFixture(t, "tok", errors.New("API returned non-200 status code 429: rate_limit_error"))
	if _, err := f.turner.TurnCtx(context.Background(), gollama.RequestOptions{System: "sys"}); err == nil {
		t.Fatal("want the rate-limit error")
	}
	if f.refreshN != 0 || len(f.inner.seen) != 1 {
		t.Fatalf("refreshes = %d, attempts = %v", f.refreshN, f.inner.seen)
	}
}

func TestOAuthTurnerTokenResolutionUsesTurnContext(t *testing.T) {
	started := make(chan struct{})
	inner := &captureTurner{}
	turner := NewOAuthTurner(inner, TokenSource{
		Token: func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		},
		Refresh: func(context.Context, string) (string, error) { return "", nil },
		Apply:   func(string) {},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := turner.TurnCtx(ctx, gollama.RequestOptions{})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("token resolution did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("turn error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("token resolution did not stop promptly")
	}
}

func TestIsRevokedCredential(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{revoked(), true},
		{errors.New(`status code 401: {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired."}}`), true},
		{errors.New(`status code 401: {"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"}}`), false},
		{errors.New(`status code 403: {"type":"error","error":{"type":"permission_error","message":"token revoked"}}`), false},
		{errors.New("status code 429: rate_limit_error"), false},
	} {
		if got := IsRevokedCredential(tc.err); got != tc.want {
			t.Errorf("IsRevokedCredential(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
