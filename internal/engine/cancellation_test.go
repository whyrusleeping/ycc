package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

type blockingStreamTurner struct {
	started chan struct{}
	exited  chan struct{}
}

func (b *blockingStreamTurner) TurnCtx(ctx context.Context, _ gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	return b.wait(ctx)
}

func (b *blockingStreamTurner) TurnStreamCtx(ctx context.Context, _ gollama.RequestOptions, _ func(string)) (*gollama.ResponseMessageGenerate, error) {
	return b.wait(ctx)
}

func (b *blockingStreamTurner) wait(ctx context.Context) (*gollama.ResponseMessageGenerate, error) {
	close(b.started)
	<-ctx.Done()
	close(b.exited)
	return nil, ctx.Err()
}

func TestLoopCancellationStopsInflightStreamingTurnWithoutSessionError(t *testing.T) {
	log, err := event.OpenLog(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	backend := &blockingStreamTurner{started: make(chan struct{}), exited: make(chan struct{})}
	loop := &Loop{
		Client:  backend,
		Model:   "test",
		Tools:   tools.New(),
		Emitter: event.NewEmitter(log, "agent"),
	}
	loop.Seed("go")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := loop.Run(ctx)
		done <- err
	}()

	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("streaming turn did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return promptly after cancellation")
	}
	select {
	case <-backend.exited:
	case <-time.After(time.Second):
		t.Fatal("backend goroutine did not exit after cancellation")
	}
	for _, ev := range log.Snapshot() {
		if ev.Type == event.SessionError {
			t.Fatalf("cancellation emitted session_error: %+v", ev)
		}
	}
}
