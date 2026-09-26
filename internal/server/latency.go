package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
)

const latencyCapacity = 512
const requestIDHeader = "Ycc-Request-Id"

// LatencyEntry contains only bounded, non-payload RPC metadata. Durations are milliseconds.
type LatencyEntry struct {
	Time         time.Time `json:"time"`
	Procedure    string    `json:"procedure"`
	Kind         string    `json:"kind"`
	RequestID    string    `json:"requestId,omitempty"`
	DurationMS   float64   `json:"durationMs"`
	Outcome      string    `json:"outcome"`
	FirstSendMS  float64   `json:"firstSendMs,omitempty"`
	MessagesSent int       `json:"messagesSent,omitempty"`
}

type LatencyAggregate struct {
	Count  int     `json:"count"`
	P50MS  float64 `json:"p50Ms"`
	P95MS  float64 `json:"p95Ms"`
	MaxMS  float64 `json:"maxMs"`
	Errors int     `json:"errors"`
}

type LatencySnapshot struct {
	Entries    []LatencyEntry              `json:"entries"`
	Aggregates map[string]LatencyAggregate `json:"aggregates"` // kind + space + procedure
}

type LatencyRecorder struct {
	mu          sync.Mutex
	entries     [latencyCapacity]LatencyEntry
	next, count int
}

func NewLatencyRecorder() *LatencyRecorder { return &LatencyRecorder{} }

func (r *LatencyRecorder) record(e LatencyEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % latencyCapacity
	if r.count < latencyCapacity {
		r.count++
	}
}

func (r *LatencyRecorder) Snapshot() LatencySnapshot {
	r.mu.Lock()
	entries := make([]LatencyEntry, r.count)
	for i := range entries {
		entries[i] = r.entries[(r.next-r.count+i+latencyCapacity)%latencyCapacity]
	}
	r.mu.Unlock()
	groups := make(map[string][]float64)
	errorsByKey := make(map[string]int)
	for _, e := range entries {
		key := e.Kind + " " + e.Procedure
		groups[key] = append(groups[key], e.DurationMS)
		if e.Outcome != "ok" {
			errorsByKey[key]++
		}
	}
	aggregates := make(map[string]LatencyAggregate, len(groups))
	for key, durations := range groups {
		sort.Float64s(durations)
		aggregates[key] = LatencyAggregate{Count: len(durations), P50MS: percentile(durations, 50), P95MS: percentile(durations, 95), MaxMS: durations[len(durations)-1], Errors: errorsByKey[key]}
	}
	return LatencySnapshot{Entries: entries, Aggregates: aggregates}
}

func percentile(sorted []float64, p int) float64 { return sorted[(len(sorted)*p+99)/100-1] }

func validRequestID(id string) string {
	if len(id) == 0 || len(id) > 64 {
		return ""
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return ""
	}
	return id
}

func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code().String()
	}
	if errors.Is(err, context.Canceled) {
		return connect.CodeCanceled.String()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.CodeDeadlineExceeded.String()
	}
	return connect.CodeUnknown.String()
}

// LatencyInterceptor must be installed inside auth: denied requests are not recorded.
func (r *LatencyRecorder) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		id := validRequestID(req.Header().Get(requestIDHeader))
		res, err := next(ctx, req)
		ms := float64(time.Since(start)) / float64(time.Millisecond)
		headers := http.Header(nil)
		if err != nil {
			var ce *connect.Error
			if errors.As(err, &ce) {
				headers = ce.Meta()
			} else {
				ce = connect.NewError(connect.CodeUnknown, err)
				err = ce
				headers = ce.Meta()
			}
		} else if res != nil {
			headers = res.Header()
		}
		if headers != nil {
			headers.Set("Server-Timing", fmt.Sprintf("app;dur=%.3f", ms))
			if id != "" {
				headers.Set(requestIDHeader, id)
			}
		}
		r.record(LatencyEntry{Time: time.Now(), Procedure: req.Spec().Procedure, Kind: "unary", RequestID: id, DurationMS: ms, Outcome: outcome(err)})
		return res, err
	}
}

func (r *LatencyRecorder) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

type timingStream struct {
	connect.StreamingHandlerConn
	start   time.Time
	firstMS float64
	sent    int
}

func (s *timingStream) Send(message any) error {
	err := s.StreamingHandlerConn.Send(message)
	if err == nil {
		if s.sent == 0 {
			s.firstMS = float64(time.Since(s.start)) / float64(time.Millisecond)
		}
		s.sent++
	}
	return err
}

func (r *LatencyRecorder) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		id := validRequestID(conn.RequestHeader().Get(requestIDHeader))
		stream := &timingStream{StreamingHandlerConn: conn, start: start}
		err := next(ctx, stream)
		r.record(LatencyEntry{Time: time.Now(), Procedure: conn.Spec().Procedure, Kind: "stream", RequestID: id, DurationMS: float64(time.Since(start)) / float64(time.Millisecond), Outcome: outcome(err), FirstSendMS: stream.firstMS, MessagesSent: stream.sent})
		return err
	}
}

// LatencyHandler serves bounded in-memory diagnostics. With an empty daemon token,
// authentication is disabled just as it is for loopback RPCs.
func (r *LatencyRecorder) LatencyHandler(token string) http.Handler {
	auth := authInterceptor{token: token}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !auth.ok(req.Header.Get("Authorization")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Snapshot())
	})
}
