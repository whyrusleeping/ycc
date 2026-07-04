package server_test

// Remote-access end-to-end tests (task 0007, M5 rescoped): they exercise the
// exact shape a remote client uses to reach a workspace daemon over a private
// network (Tailscale/VPN) — a token-auth'd h2c server dialed with
// daemon.DialClient carrying a bearer token. A loopback bind stands in for the
// tailnet; what is under test is the token-auth'd Connect path (unary +
// server-streaming) and the curl-able Connect HTTP/JSON protocol, NOT routing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/server"
	"github.com/whyrusleeping/ycc/internal/session"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"github.com/whyrusleeping/ycc/proto/ycc/v1/yccv1connect"
)

const remoteToken = "secret-tailnet-token"

// newRemoteServer stands up the real Connect handler — server.New(mgr) wrapped
// with the production bearer-token interceptor — over an h2c httptest server,
// mirroring daemon.buildHandler. It returns the base URL, the manager, and the
// workspace root. Clients dial it with daemon.DialClient, exercising the real
// bearer interceptor for both unary and streaming RPCs.
func newRemoteServer(t *testing.T, token string) (string, *session.Manager, string) {
	t.Helper()
	reg := config.NewRegistry(&config.Config{
		Models: map[string]config.Model{"a": {Backend: "ollama", BaseURL: "http://localhost:1", Model: "model-a"}},
		Roles:  config.Roles{Coordinator: "a", Implementer: "a", Reviewers: []string{"a"}},
	})
	ws := t.TempDir()
	mgr := session.NewManager(reg, ws)

	// Same handler + interceptor pairing as daemon.buildHandler.
	path, handler := yccv1connect.NewSessionServiceHandler(
		server.New(mgr),
		connect.WithInterceptors(server.NewAuthInterceptor(token)),
	)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(srv.Close)
	return srv.URL, mgr, ws
}

// seedSession writes a persisted event log with two events and re-opens it live
// via the ResumeSession RPC, returning the live session. Mirrors the
// subscribe_transient_test idiom (a live model is not needed to arrange this).
func seedSession(t *testing.T, client yccv1connect.SessionServiceClient, mgr *session.Manager, ws, id string) *session.Session {
	t.Helper()
	logPath := filepath.Join(ws, ".ycc", "sessions", id, "events.jsonl")
	lg, err := event.OpenLog(logPath)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	lg.Record("user", event.UserInput, map[string]any{"text": "hi"})
	lg.Record("coordinator", event.ModelTurn, map[string]any{"text": "hello"})
	lg.Close()

	if _, err := client.ResumeSession(context.Background(), connect.NewRequest(&v1.ResumeSessionRequest{SessionId: id})); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	sess, ok := mgr.Get(id)
	if !ok {
		t.Fatal("session not registered after ResumeSession")
	}
	t.Cleanup(func() { mgr.Stop(id) })
	return sess
}

// TestRemoteClientHappyPath drives the supported remote shape with a valid
// token: Subscribe(from_seq=0) replay + live tail, SendInput, AnswerQuestion,
// and ListSessions — all over daemon.DialClient with the bearer token.
func TestRemoteClientHappyPath(t *testing.T) {
	base, mgr, ws := newRemoteServer(t, remoteToken)
	client := daemon.DialClient(base, remoteToken)
	ctx := context.Background()

	id := "sess_remote"
	sess := seedSession(t, client, mgr, ws, id)
	log := sess.Log()

	// Subscribe from seq 0: the two persisted events replay, then a live event
	// recorded after subscribing is tailed. Emit the live event from a goroutine
	// so it does not race the stream's header flush (mirrors real live traffic).
	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const liveMarker = "live-after-subscribe"
	go func() {
		time.Sleep(100 * time.Millisecond)
		log.Record("coordinator", event.ModelTurn, map[string]any{"text": liveMarker})
	}()

	stream, err := client.Subscribe(subCtx, connect.NewRequest(&v1.SubscribeRequest{SessionId: id, FromSeq: 0}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	var sawFirst, sawSecond, sawLive bool
	for stream.Receive() {
		ev := stream.Msg()
		switch {
		case ev.GetType() == string(event.UserInput) && bytesContains(ev.GetDataJson(), "hi"):
			sawFirst = true
		case ev.GetType() == string(event.ModelTurn) && bytesContains(ev.GetDataJson(), "hello"):
			sawSecond = true
		case ev.GetType() == string(event.ModelTurn) && bytesContains(ev.GetDataJson(), liveMarker):
			sawLive = true
		}
		if sawFirst && sawSecond && sawLive {
			break
		}
	}
	if !sawFirst || !sawSecond {
		t.Fatalf("Subscribe(from_seq=0) did not replay persisted events (first=%v second=%v)", sawFirst, sawSecond)
	}
	if !sawLive {
		t.Fatal("Subscribe did not deliver the live event recorded after subscribing")
	}

	// SendInput over the RPC succeeds (queues a prod on the idle resumed session).
	if _, err := client.SendInput(ctx, connect.NewRequest(&v1.SendInputRequest{SessionId: id, Text: "keep going"})); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// AnswerQuestion round-trip: with no live model driving the agent, no ask_user
	// gate is open, so the RPC reaches the handler (proving auth passed and the
	// request was routed) and returns FailedPrecondition ("no pending question").
	// Full answer-delivery semantics are covered by the session package's
	// interaction tests (a real pending question needs a running agent loop, which
	// cannot be arranged from the public RPC surface without a live model).
	_, err = client.AnswerQuestion(ctx, connect.NewRequest(&v1.AnswerQuestionRequest{SessionId: id, OptionIndex: -1, Text: "yes"}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("AnswerQuestion (no pending) code = %v, want FailedPrecondition", got)
	}
	// A wrong session id is NotFound, proving the id is honored end to end.
	_, err = client.AnswerQuestion(ctx, connect.NewRequest(&v1.AnswerQuestionRequest{SessionId: "nope", OptionIndex: -1, Text: "y"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("AnswerQuestion (unknown id) code = %v, want NotFound", got)
	}

	// ListSessions reports the live session over the authenticated unary path.
	ls, err := client.ListSessions(ctx, connect.NewRequest(&v1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, si := range ls.Msg.GetSessions() {
		if si.GetSessionId() == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListSessions did not include %s: %+v", id, ls.Msg.GetSessions())
	}
}

// TestRemoteClientRejectsBadAuth asserts that both unauthenticated and
// wrong-token requests are rejected with CodeUnauthenticated on a unary RPC
// (ListSessions) AND on the server-streaming RPC (Subscribe, where the error
// surfaces when the stream is first read).
func TestRemoteClientRejectsBadAuth(t *testing.T) {
	base, mgr, ws := newRemoteServer(t, remoteToken)
	// A live session must exist so a rejection is due to auth, not a missing
	// session (the auth interceptor runs before the handler either way, but this
	// removes any ambiguity).
	seedSession(t, daemon.DialClient(base, remoteToken), mgr, ws, "sess_auth")
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"wrong token", "not-the-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := daemon.DialClient(base, tc.token)

			// Unary.
			_, err := client.ListSessions(ctx, connect.NewRequest(&v1.ListSessionsRequest{}))
			if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
				t.Fatalf("ListSessions code = %v, want Unauthenticated", got)
			}

			// Streaming: the interceptor rejects the stream; the error surfaces on
			// open or on the first Receive.
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			stream, err := client.Subscribe(sctx, connect.NewRequest(&v1.SubscribeRequest{SessionId: "sess_auth", FromSeq: 0}))
			if err == nil {
				for stream.Receive() {
					// Drain until the auth error (no events should arrive).
				}
				err = stream.Err()
				stream.Close()
			}
			if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
				t.Fatalf("Subscribe streaming code = %v, want Unauthenticated", got)
			}
		})
	}
}

// TestRemoteConnectJSONUnary proves the curl-able Connect HTTP/JSON unary path
// works with a plain net/http client (HTTP/1.1 passes through the h2c handler):
// a bearer-authenticated POST of application/json returns 200 + a JSON body, and
// the unauthenticated request is rejected with 401.
//
// Equivalent curl:
//
//	curl -sS -H 'Authorization: Bearer <token>' \
//	     -H 'Content-Type: application/json' -d '{}' \
//	     <base>/ycc.v1.SessionService/ListSessions
func TestRemoteConnectJSONUnary(t *testing.T) {
	base, mgr, ws := newRemoteServer(t, remoteToken)
	seedSession(t, daemon.DialClient(base, remoteToken), mgr, ws, "sess_json")

	url := base + "/ycc.v1.SessionService/ListSessions"

	// Authenticated: 200 + JSON with our session.
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+remoteToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST ListSessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var decoded struct {
		Sessions []struct {
			Id string `json:"sessionId"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	found := false
	for _, s := range decoded.Sessions {
		if s.Id == "sess_json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("JSON ListSessions missing sess_json: %+v", decoded.Sessions)
	}

	// Unauthenticated: 401.
	reqNoAuth, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString("{}"))
	reqNoAuth.Header.Set("Content-Type", "application/json")
	respNoAuth, err := http.DefaultClient.Do(reqNoAuth)
	if err != nil {
		t.Fatalf("POST ListSessions (no auth): %v", err)
	}
	defer respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", respNoAuth.StatusCode)
	}
}

// TestRemoteConnectJSONSubscribe proves the phone-relevant server-streaming path
// works over the Connect streaming protocol with JSON (application/connect+json):
// the request is a single enveloped JSON frame; the response is a sequence of
// enveloped frames — data frames (flag 0x00) carrying Event JSON, then a final
// end-of-stream frame (flag 0x02) carrying the trailer JSON once the log closes.
//
// Equivalent curl (the request body is a 5-byte envelope header + JSON):
//
//	printf '\x00\x00\x00\x00\x1f{"sessionId":"s","fromSeq":0}' | \
//	  curl -sS --http2-prior-knowledge \
//	    -H 'Authorization: Bearer <token>' \
//	    -H 'Content-Type: application/connect+json' \
//	    --data-binary @- <base>/ycc.v1.SessionService/Subscribe | xxd
func TestRemoteConnectJSONSubscribe(t *testing.T) {
	base, mgr, ws := newRemoteServer(t, remoteToken)
	id := "sess_stream"
	seedSession(t, daemon.DialClient(base, remoteToken), mgr, ws, id)

	// Build the enveloped request frame: flag byte 0x00 + 4-byte big-endian
	// length + the JSON message.
	msg := []byte(`{"sessionId":"` + id + `","fromSeq":0}`)
	var body bytes.Buffer
	writeEnvelope(&body, 0x00, msg)

	url := base + "/ycc.v1.SessionService/Subscribe"
	req, _ := http.NewRequest(http.MethodPost, url, &body)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Authorization", "Bearer "+remoteToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST Subscribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("Subscribe status = %d, want 200 (body %s)", resp.StatusCode, b)
	}

	// Once we have started reading the replayed events, close the log so the
	// server ends the stream and emits the end-of-stream envelope.
	go func() {
		time.Sleep(200 * time.Millisecond)
		mgr.Stop(id)
	}()

	br := bufio.NewReader(resp.Body)
	var sawEvent, sawEnd bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		flag, payload, err := readEnvelope(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("readEnvelope: %v", err)
		}
		if flag&0x02 != 0 {
			// End-of-stream frame: payload is the trailer JSON (a valid object).
			var trailer map[string]json.RawMessage
			if err := json.Unmarshal(payload, &trailer); err != nil {
				t.Fatalf("end-of-stream trailer not JSON: %v (payload %s)", err, payload)
			}
			sawEnd = true
			break
		}
		// Data frame: payload is an Event JSON message.
		var ev struct {
			Seq  string `json:"seq"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &ev); err != nil {
			t.Fatalf("event frame not JSON: %v (payload %s)", err, payload)
		}
		if ev.Type != "" {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("connect+json Subscribe never delivered an event data frame")
	}
	if !sawEnd {
		t.Fatal("connect+json Subscribe never delivered an end-of-stream frame")
	}
}

// writeEnvelope writes one Connect enveloped message: 1 flag byte + 4-byte
// big-endian length + payload.
func writeEnvelope(w *bytes.Buffer, flag byte, payload []byte) {
	w.WriteByte(flag)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(payload)))
	w.Write(l[:])
	w.Write(payload)
}

// readEnvelope reads one Connect enveloped frame, returning its flag byte and
// payload.
func readEnvelope(r *bufio.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:5])
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

func bytesContains(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
