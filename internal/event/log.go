package event

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Log is the persistent, append-only event store for one session (spec §5.1).
// It implements Sink (so an Emitter writes through it), persists every event to
// an events.jsonl file, mirrors them in memory, and fans them out losslessly to
// any number of subscribers — including late ones, via replay-from-offset.
//
// Alongside the lossless persisted stream it also fans out transient,
// broadcast-only events (Broadcast): these are delivered to live subscribers
// but never persisted, replayed, or seq-numbered (spec §5, task 0114).
type Log struct {
	mu     sync.Mutex
	cond   *sync.Cond
	path   string
	f      *os.File
	events []Event
	seq    int
	closed bool

	// subs tracks live subscribers so Broadcast can deliver transient events to
	// them. Each subscriber owns a small bounded queue that Broadcast appends to
	// under mu; the subscriber's pump drains it after the persisted tail.
	subs   map[int]*subscriber
	nextID int
}

// transientQueueMax bounds each subscriber's pending transient events. Transient
// events are ephemeral UI hints (e.g. turn_delta), so when a subscriber falls
// behind we drop the OLDEST queued transient to keep the freshest output flowing
// rather than block the log or grow unboundedly. Persisted events are never
// dropped; only transients are lossy under backpressure.
const transientQueueMax = 256

// subscriber holds a live subscriber's transient delivery queue.
type subscriber struct {
	transient []Event
}

// OpenLog opens (creating if needed) the JSONL log at path, loading any existing
// events into memory so seq numbering and replay continue across restarts.
func OpenLog(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	existing, err := readEvents(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	l := &Log{path: path, f: f, events: existing, subs: map[int]*subscriber{}}
	if n := len(existing); n > 0 {
		l.seq = existing[n-1].Seq
	}
	l.cond = sync.NewCond(&l.mu)
	return l, nil
}

// ReadLog reads and parses all persisted events from a session's events.jsonl at
// path, reusing the strict line-by-line decoder. A missing file yields (nil, nil);
// a corrupt line is a hard error. It backs the read-only transcript view (spec §18.6).
func ReadLog(path string) ([]Event, error) {
	return readEvents(path)
}

func readEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("corrupt event log %s: %w", path, err)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

// LastSeq returns the seq of the most recent persisted event, or 0 if empty.
func (l *Log) LastSeq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) == 0 {
		return 0
	}
	return l.events[len(l.events)-1].Seq
}

// Snapshot returns a copy of the log's events so callers can reduce/project over
// them without racing the writer.
func (l *Log) Snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, len(l.events))
	copy(out, l.events)
	return out
}

// Record assigns the next seq, persists, and broadcasts the event. It is the
// session's single sequence authority and satisfies Recorder.
func (l *Log) Record(actor string, t Type, data map[string]any) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		// Don't burn a sequence number on a closed log; the event isn't recorded.
		return Event{Actor: actor, Type: t, Data: data}
	}
	l.seq++
	ev := Event{Seq: l.seq, TS: time.Now(), Actor: actor, Type: t, Data: data}
	if line, err := json.Marshal(ev); err == nil {
		if _, werr := l.f.Write(append(line, '\n')); werr != nil {
			// The log is the source of truth; surface a failed append rather than
			// silently diverging from the in-memory/subscriber view.
			log.Printf("ycc: event log append failed (%s, seq %d): %v", l.path, ev.Seq, werr)
		} else {
			l.f.Sync()
		}
	}
	l.events = append(l.events, ev)
	l.cond.Broadcast()
	return ev
}

// Broadcast delivers a transient, non-persisted event to live subscribers only
// (spec §5, task 0114). Unlike Record it assigns NO sequence number (Seq stays
// 0), never writes to events.jsonl, and never appends to the in-memory replay
// slice — so the event is invisible to Snapshot, ReadLog, and late subscribers,
// and it never advances a resume cursor. It is used for ephemeral UI hints such
// as streaming turn_delta output.
//
// Delivery is best-effort: it enqueues onto each live subscriber's bounded
// transient queue and wakes the pumps. A slow or cancelled subscriber can never
// wedge the log or other subscribers — under backpressure the oldest queued
// transient is dropped (see transientQueueMax). Broadcast on a closed log is a
// no-op. The persisted stream's losslessness and ordering are unaffected.
func (l *Log) Broadcast(actor string, t Type, data map[string]any) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	ev := Event{TS: time.Now(), Actor: actor, Type: t, Data: data, Transient: true}
	if l.closed {
		return ev
	}
	for _, sub := range l.subs {
		if len(sub.transient) >= transientQueueMax {
			// Drop the oldest queued transient to bound memory and keep the
			// freshest output flowing; transients are lossy by design.
			sub.transient = sub.transient[1:]
		}
		sub.transient = append(sub.transient, ev)
	}
	l.cond.Broadcast()
	return ev
}

// Close stops the log and wakes all subscribers so they terminate.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.cond.Broadcast()
	return l.f.Close()
}

// Subscribe streams events with Seq > fromSeq, first replaying any already
// persisted and then delivering live ones, until the returned cancel func is
// called or the log is closed. Persisted delivery is lossless and ordered.
//
// Transient events broadcast via Broadcast are also delivered to this stream
// (interleaved after each persisted tail), but only those emitted while this
// subscriber is live — they are never replayed, carry Seq=0, and are lossy under
// backpressure. The caller must drain the channel; cancel unblocks the pump and
// deregisters the subscriber.
func (l *Log) Subscribe(fromSeq int) (<-chan Event, func()) {
	ch := make(chan Event)
	done := make(chan struct{})

	// Register this subscriber so Broadcast can queue transient events for it.
	l.mu.Lock()
	id := l.nextID
	l.nextID++
	sub := &subscriber{}
	l.subs[id] = sub
	l.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			l.mu.Lock()
			delete(l.subs, id)
			l.cond.Broadcast()
			l.mu.Unlock()
		})
	}

	go func() {
		defer close(ch)
		defer func() {
			l.mu.Lock()
			delete(l.subs, id)
			l.mu.Unlock()
		}()
		cursor := 0 // index into l.events of the next persisted event to consider
		for {
			l.mu.Lock()
			for !l.closed && cursor >= len(l.events) && len(sub.transient) == 0 {
				select {
				case <-done:
					l.mu.Unlock()
					return
				default:
				}
				l.cond.Wait()
			}
			// Snapshot the new persisted tail under lock, then release before
			// sending. Persisted events go first so their order/losslessness is
			// unaffected by transient delivery.
			batch := make([]Event, 0, len(l.events)-cursor)
			for ; cursor < len(l.events); cursor++ {
				if l.events[cursor].Seq > int(fromSeq) {
					batch = append(batch, l.events[cursor])
				}
			}
			// Drain any queued transients after the persisted tail.
			var transients []Event
			if len(sub.transient) > 0 {
				transients = sub.transient
				sub.transient = nil
			}
			closed := l.closed
			l.mu.Unlock()

			for _, ev := range batch {
				select {
				case ch <- ev:
				case <-done:
					return
				}
			}
			for _, ev := range transients {
				select {
				case ch <- ev:
				case <-done:
					return
				}
			}
			if closed {
				return
			}
		}
	}()

	return ch, cancel
}
