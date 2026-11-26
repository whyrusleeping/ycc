package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
)

const testRPCPrefix = "/ycc.v1.SessionService/"

func admissionCode(t *testing.T, h http.Handler, method string) connect.Code {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, testRPCPrefix+method, nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		return 0
	}
	if rec.Code == http.StatusServiceUnavailable {
		return connect.CodeUnavailable
	}
	t.Fatalf("%s: unexpected HTTP status %d", method, rec.Code)
	return 0
}

// The gate refuses every RPC while starting, admits all once open, and refuses
// every new RPC after the fence. Non-RPC paths pass.
func TestAdmissionPhases(t *testing.T) {
	a := NewAdmission()
	h := a.Handler(testRPCPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, m := range []string{"ListSessions", "SendInput"} {
		if got := admissionCode(t, h, m); got != connect.CodeUnavailable {
			t.Fatalf("starting: %s code = %v, want unavailable", m, got)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("non-RPC path status = %d while starting", rec.Code)
	}
	a.Open()
	for _, m := range []string{"ListSessions", "SendInput"} {
		if got := admissionCode(t, h, m); got != 0 {
			t.Fatalf("open: %s code = %v, want admitted", m, got)
		}
	}
	if !a.Fence(context.Background()) {
		t.Fatal("Fence with nothing in flight did not finish")
	}
	for _, m := range []string{"ListSessions", "GetWorkLoop", "Subscribe", "SendInput", "StartSession"} {
		if got := admissionCode(t, h, m); got != connect.CodeUnavailable {
			t.Fatalf("fenced: %s code = %v, want unavailable", m, got)
		}
	}
}

// Fence waits for RPCs admitted before it, bounded by its context, but not for
// long-lived subscriptions.
func TestAdmissionFenceWaitsForAdmittedMutation(t *testing.T) {
	a := NewAdmission()
	a.Open()
	entered, release := make(chan struct{}), make(chan struct{})
	h := a.Handler(testRPCPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, testRPCPrefix+"SendInput", nil))
	<-entered
	// A subscription that stays open does not hold the fence.
	b := NewAdmission()
	b.Open()
	subEntered, subRelease := make(chan struct{}), make(chan struct{})
	defer close(subRelease)
	sub := b.Handler(testRPCPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(subEntered)
		<-subRelease
	}))
	go sub.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, testRPCPrefix+"Subscribe", nil))
	<-subEntered
	if !b.Fence(context.Background()) {
		t.Fatal("Fence waited on a subscription")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if a.Fence(ctx) {
		t.Fatal("Fence returned clean with a mutation still in flight")
	}
	done := make(chan bool, 1)
	go func() { done <- a.Fence(context.Background()) }()
	select {
	case <-done:
		t.Fatal("Fence finished before the admitted mutation returned")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Fence reported unfinished mutations")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Fence did not finish after the mutation returned")
	}
}
