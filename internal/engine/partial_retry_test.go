package engine

import (
	"context"
	"errors"
	"io"

	"github.com/whyrusleeping/gollama"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

type generatedOnlyTurner struct{ calls int }

func (f *generatedOnlyTurner) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return f.TurnStreamCtx(ctx, opts, nil)
}
func (f *generatedOnlyTurner) TurnStreamCtx(ctx context.Context, _ gollama.RequestOptions, _ func(string)) (*gollama.ResponseMessageGenerate, error) {
	f.calls++
	if f.calls == 1 {
		llmhttp.MarkGenerated(ctx)
	} // reasoning/tool arguments, no assistant text callback
	if f.calls <= 2 {
		return nil, io.ErrUnexpectedEOF
	}
	return assistantText("unexpected success"), nil
}

func TestGeneratedWithoutTextCallbackCapsRetries(t *testing.T) {
	client := &generatedOnlyTurner{}
	loop, rec, _ := newRetryLoop(t, client, DefaultRetryPolicy())
	_, err := loop.Run(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) || client.calls != 2 {
		t.Fatalf("error=%v calls=%d, want failure after two attempts", err, client.calls)
	}
	if errs := sessionErrors(rec); len(errs) != 1 || errs[0].Data["attempts"] != 2 {
		t.Fatalf("final failure not recorded: %+v", errs)
	}
}

func TestPartialStreamRetryExhaustion(t *testing.T) {
	orig := llmhttp.ErrStreamStalled
	// A later attempt can fail before producing output; the earlier partial
	// stream must still bound the total retry budget.
	client := &scriptStreamTurner{attempts: []streamAttempt{
		{snaps: []string{"partial"}, err: orig},
		{err: orig},
		{resp: assistantText("should not succeed")},
	}}
	loop, rec, slept := newRetryLoop(t, client, DefaultRetryPolicy())
	_, err := loop.Run(context.Background())
	if !errors.Is(err, orig) {
		t.Fatalf("error = %v, want original failure", err)
	}
	if client.streamCalls != 2 || len(*slept) != 1 {
		t.Fatalf("calls/sleeps = %d/%d, want 2/1", client.streamCalls, len(*slept))
	}
	if errs := sessionErrors(rec); len(errs) != 1 || errs[0].Data["attempts"] != 2 {
		t.Fatalf("final error not recorded after two attempts: %+v", errs)
	}
}

func TestPolicyTimeoutsAreRetryable(t *testing.T) {
	for _, err := range []error{llmhttp.ErrStreamStalled, llmhttp.ErrTotalTimeout} {
		info := ClassifyAPIError(err)
		if info.Kind != KindTimeout || !info.Retryable {
			t.Fatalf("classification of %v: %+v", err, info)
		}
	}
	if info := ClassifyAPIError(context.Canceled); info.Retryable {
		t.Fatalf("caller cancellation retryable: %+v", info)
	}
}

func TestConfiguredPartialCap(t *testing.T) {
	client := &scriptStreamTurner{attempts: []streamAttempt{
		{snaps: []string{"partial"}, err: llmhttp.ErrStreamStalled},
		{snaps: []string{"partial again"}, err: llmhttp.ErrStreamStalled},
		{snaps: []string{"third"}, err: llmhttp.ErrStreamStalled},
		{resp: assistantText("not reached")},
	}}
	loop, _, slept := newRetryLoop(t, client, RetryPolicy{MaxAttempts: 8, PartialMaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	_, err := loop.Run(context.Background())
	if !errors.Is(err, llmhttp.ErrStreamStalled) || client.streamCalls != 3 || len(*slept) != 2 {
		t.Fatalf("error=%v calls=%d sleeps=%d", err, client.streamCalls, len(*slept))
	}
}
