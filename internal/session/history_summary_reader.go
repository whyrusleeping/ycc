package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/whyrusleeping/ycc/internal/event"
)

// readSummaryEventsTolerantResult keeps the event envelope (including every
// timestamp and lifecycle event), but only the data consumed by
// reduceSessionSummary. It must not be used for replay. The full reader remains
// available for that purpose; both readers share line/error/cacheability rules.
func readSummaryEventsTolerantResult(path string) ([]event.Event, bool) {
	return readHistoryEvents(path, decodeSummaryEvent)
}

func decodeSummaryEvent(line []byte, ev *event.Event) error {
	// Validate once with the standard library, then walk spans without decoding
	// arbitrary payloads. Falling back also preserves diagnostics for bad roots.
	if !json.Valid(line) {
		return json.Unmarshal(line, ev)
	}
	line = bytes.TrimSpace(line)
	if line[0] != '{' {
		return json.Unmarshal(line, ev)
	}
	// Decode a small envelope through encoding/json rather than materializing
	// each field separately. This retains its duplicate/null/type semantics.
	envelope := make([]byte, 1, 256)
	envelope[0] = '{'
	// Borrow spans until the envelope is decoded: data can precede type, and
	// repeated data objects merge (while null clears previously seen data).
	var fragments [][]byte
	for i := 1; ; {
		i = summarySkipSpace(line, i)
		if line[i] == '}' {
			break
		}
		start := i
		end := summaryStringEnd(line, i)
		key := summaryKey(line[i:end])
		i = summarySkipSpace(line, end) + 1 // colon
		i = summarySkipSpace(line, i)
		end, _ = summaryValueEnd(line, i, false)
		// EqualFold matches encoding/json's case-insensitive struct field lookup,
		// including Unicode folds. Data map keys below remain case-sensitive.
		switch {
		case strings.EqualFold(key, "data"):
			fragments = append(fragments, line[i:end])
		case strings.EqualFold(key, "seq"), strings.EqualFold(key, "ts"),
			strings.EqualFold(key, "actor"), strings.EqualFold(key, "type"),
			strings.EqualFold(key, "transient"):
			if len(envelope) > 1 {
				envelope = append(envelope, ',')
			}
			envelope = append(envelope, line[start:end]...)
		}
		i = summarySkipSpace(line, end)
		if line[i] == ',' {
			i++
		}
	}
	envelope = append(envelope, '}')
	if err := json.Unmarshal(envelope, ev); err != nil {
		return err
	}
	d := summaryEventData{typ: ev.Type}
	for _, data := range fragments {
		if err := d.decode(data); err != nil {
			return err
		}
	}
	ev.Data = d.values
	return nil
}

func summaryKey(b []byte) string {
	if bytes.IndexByte(b, '\\') < 0 {
		return string(b[1 : len(b)-1])
	}
	var key string
	_ = json.Unmarshal(b, &key) // syntax already validated
	return key
}

type summaryEventData struct {
	typ    event.Type
	values map[string]any
}

// Keep this allowlist in sync with reduceSessionSummary, including the fields
// of event.Reduce's projection that the summary consumes.
func (d *summaryEventData) wants(key string) bool {
	switch d.typ {
	case event.SessionStarted:
		return key == "mode" || key == "workspace"
	case event.UserInput:
		return key == "text"
	case event.TaskFocus:
		return key == "task" || key == "title"
	case event.ModelTurn:
		return key == "model_name" || key == "usage" || key == "context_tokens_est"
	}
	return false
}

func (d *summaryEventData) decode(b []byte) error {
	if b[0] == 'n' { // null also clears earlier duplicate data objects.
		d.values = nil
		return nil
	}
	if b[0] != '{' {
		return fmt.Errorf("session summary: data must be an object or null")
	}
	// These are map keys, not struct fields: do NOT case-fold them. Keep their
	// original untyped values so malformed fields remain tolerated by reducers.
	for i := 1; ; {
		i = summarySkipSpace(b, i)
		if b[i] == '}' {
			return nil
		}
		end := summaryStringEnd(b, i)
		key := summaryKey(b[i:end])
		i = summarySkipSpace(b, end) + 1 // colon
		i = summarySkipSpace(b, i)
		end, err := summaryValueEnd(b, i, true)
		if err != nil {
			return err
		}
		if d.wants(key) {
			var value any
			if err := json.Unmarshal(b[i:end], &value); err != nil {
				return err
			}
			if d.values == nil {
				d.values = make(map[string]any)
			}
			d.values[key] = value
		}
		i = summarySkipSpace(b, end)
		if b[i] == ',' {
			i++
		}
	}
}

func summarySkipSpace(b []byte, i int) int {
	for b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n' {
		i++
	}
	return i
}

func summaryStringEnd(b []byte, i int) int {
	for i++; ; i++ {
		// Most payload bytes are string content. Search for candidate quotes
		// in bulk; a quote is escaped only by an odd run of backslashes.
		i += bytes.IndexByte(b[i:], '"')
		j := i - 1
		for b[j] == '\\' {
			j--
		}
		if (i-1-j)%2 == 0 {
			return i + 1
		}
	}
}

// summaryValueEnd walks already-validated JSON without materializing strings,
// arrays or objects. Number overflow is the one additional validity check made
// by decoding into any (but not by JSON syntax validation), even in discarded
// payloads. Check potentially overflowing numbers to preserve corrupt-line and
// cacheability semantics. Ordinary short integers cannot overflow float64.
func summaryValueEnd(b []byte, start int, checkNumbers bool) (int, error) {
	depth := 0
	for i := start; i < len(b); {
		switch b[i] {
		case '"':
			i = summaryStringEnd(b, i)
			if depth == 0 {
				return i, nil
			}
		case '{', '[':
			depth++
			i++
		case '}', ']':
			if depth == 0 {
				return i, nil
			}
			depth--
			i++
			if depth == 0 {
				return i, nil
			}
		case ',', ' ', '\t', '\r', '\n':
			if depth == 0 {
				return i, nil
			}
			i++
		default:
			if b[i] == '-' || b[i] >= '0' && b[i] <= '9' {
				j, exponent := i+1, false
				for j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == '.' || b[j] == 'e' || b[j] == 'E' || b[j] == '+' || b[j] == '-') {
					exponent = exponent || b[j] == 'e' || b[j] == 'E'
					j++
				}
				if checkNumbers && (exponent || j-i > 308) {
					if _, err := strconv.ParseFloat(string(b[i:j]), 64); err != nil {
						return 0, err
					}
				}
				i = j
			} else {
				i++ // punctuation or true/false/null
			}
		}
	}
	return len(b), nil
}
