package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"connectrpc.com/connect"
)

// Admission gates RPCs through persistent daemon startup and graceful shutdown
// (docs/design/daemon-restart.md §1). Until Open, every RPC is refused with
// Unavailable while startup recovery runs behind the already-bound listener.
// Fence refuses every new RPC with Unavailable and waits for admitted ones to
// return. Read handlers are not all pure (some repair persisted state), so the
// fence admits nothing rather than trusting a read/write classification.
// Long-lived subscriptions (LongLivedProcedure) are not waited for: they end
// when their sessions close. Non-RPC paths (web assets) pass through.
type Admission struct {
	mu     sync.Mutex
	open   bool
	fenced bool
	active int
	idle   chan struct{} // closed once fenced with no admitted RPC left
}

// NewAdmission returns a gate that refuses every RPC until Open.
func NewAdmission() *Admission { return &Admission{} }

// Open starts admitting RPCs (a no-op once fenced).
func (a *Admission) Open() {
	a.mu.Lock()
	a.open = true
	a.mu.Unlock()
}

// Fence stops admitting RPCs and waits until every admitted one (except
// long-lived subscriptions) has returned, or ctx ends. It reports whether all
// of them finished.
func (a *Admission) Fence(ctx context.Context) bool {
	a.mu.Lock()
	if !a.fenced {
		a.fenced = true
		a.idle = make(chan struct{})
		if a.active == 0 {
			close(a.idle)
		}
	}
	idle := a.idle
	a.mu.Unlock()
	select {
	case <-idle:
		return true
	case <-ctx.Done():
		return false
	}
}

// admit reports whether an RPC may run now, registering it as in flight
// (release with done) when tracked is true.
func (a *Admission) admit(tracked bool) (ok bool, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.fenced:
		return false, "daemon is shutting down; retry after it restarts"
	case !a.open:
		return false, "daemon is starting; retry shortly"
	}
	if tracked {
		a.active++
	}
	return true, ""
}

func (a *Admission) done() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if a.fenced && a.active == 0 {
		select {
		case <-a.idle:
		default:
			close(a.idle)
		}
	}
}

// Handler wraps the Connect handler mounted at prefix (e.g.
// "/ycc.v1.SessionService/"). Rejection happens before the request body is read.
func (a *Admission) Handler(prefix string, next http.Handler) http.Handler {
	ew := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure, isRPC := strings.CutPrefix(r.URL.Path, prefix)
		if !isRPC {
			next.ServeHTTP(w, r)
			return
		}
		tracked := !LongLivedProcedure(procedure)
		ok, reason := a.admit(tracked)
		if !ok {
			_ = ew.Write(w, r, connect.NewError(connect.CodeUnavailable, errors.New(reason)))
			return
		}
		if tracked {
			defer a.done()
		}
		next.ServeHTTP(w, r)
	})
}

// LongLivedProcedure reports whether a SessionService method is a
// subscription stream that stays open for as long as its client watches.
func LongLivedProcedure(method string) bool {
	return method == "Subscribe" || method == "SubscribeSessionView"
}
