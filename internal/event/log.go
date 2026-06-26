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
type Log struct {
	mu     sync.Mutex
	cond   *sync.Cond
	path   string
	f      *os.File
	events []Event
	seq    int
	closed bool
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
	l := &Log{path: path, f: f, events: existing}
	if n := len(existing); n > 0 {
		l.seq = existing[n-1].Seq
	}
	l.cond = sync.NewCond(&l.mu)
	return l, nil
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
// called or the log is closed. Delivery is lossless and ordered. The caller must
// drain the channel; cancel unblocks the pump.
func (l *Log) Subscribe(fromSeq int) (<-chan Event, func()) {
	ch := make(chan Event)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			l.mu.Lock()
			l.cond.Broadcast()
			l.mu.Unlock()
		})
	}

	go func() {
		defer close(ch)
		cursor := 0 // index into l.events of the next event to consider
		for {
			l.mu.Lock()
			for !l.closed && cursor >= len(l.events) {
				select {
				case <-done:
					l.mu.Unlock()
					return
				default:
				}
				l.cond.Wait()
			}
			// Snapshot the new tail under lock, then release before sending.
			batch := make([]Event, 0, len(l.events)-cursor)
			for ; cursor < len(l.events); cursor++ {
				if l.events[cursor].Seq > int(fromSeq) {
					batch = append(batch, l.events[cursor])
				}
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
			if closed {
				return
			}
		}
	}()

	return ch, cancel
}
