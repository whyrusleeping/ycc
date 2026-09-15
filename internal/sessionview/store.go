// Package sessionview maintains the rebuildable SQLite projection used by the
// paginated session presentation API. The append-only JSONL log remains the
// authority; the database is only a read index.
package sessionview

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/whyrusleeping/ycc/internal/event"
)

const schemaVersion = 2

// Bounds are deliberately below Connect's normal message limits. MaxBytes is a
// budget for the complete encoded response, not merely the row payloads. The
// minimum leaves room for the bounded current-state envelope and one compact
// row; callers asking for less receive this safe minimum instead.
const (
	DefaultRows         = 200
	DefaultBytes        = 384 << 10
	MinBytes            = 32 << 10
	MaxRows             = 500
	MaxBytes            = 768 << 10
	maxSummaryJSONBytes = 8 << 10
)

type Question struct {
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
}

type State struct {
	IndexedThrough int64      `json:"indexed_through"`
	LastTimestamp  string     `json:"last_timestamp,omitempty"`
	Phase          string     `json:"phase"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	ErrorRetryable bool       `json:"error_retryable"`
	Coordinator    string     `json:"coordinator,omitempty"`
	ContextTokens  int64      `json:"context_tokens,omitempty"`
	HasContext     bool       `json:"has_context"`
	Rollover       bool       `json:"rollover"`
	Pending        []Question `json:"pending,omitempty"`
	PendingRowID   string     `json:"pending_row_id,omitempty"`

	// Reducer bookkeeping, not exposed on the wire.
	OpenQuestionRowID string `json:"open_question_row_id,omitempty"`
	LastRowID         string `json:"last_row_id,omitempty"`
	LastModelText     string `json:"last_model_text,omitempty"`
}

type Row struct {
	ID          string
	PositionSeq int64
	UpdatedSeq  int64
	Events      []event.Event
	HasDetail   bool
}

type Update struct {
	RowID   string
	Deleted bool
}

type Store struct {
	mu sync.Mutex
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The index contains transcript text just like the private session logs. Repair
	// a pre-existing broad .ycc directory and create/chmod the database before
	// SQLite can create any journal beside it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) init() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != schemaVersion {
		if _, err := s.db.Exec(`DROP TABLE IF EXISTS updates; DROP TABLE IF EXISTS rows; DROP TABLE IF EXISTS sessions;`); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
 session_id TEXT PRIMARY KEY, indexed_seq INTEGER NOT NULL, indexed_offset INTEGER NOT NULL,
 source_dev INTEGER NOT NULL, source_ino INTEGER NOT NULL, source_size INTEGER NOT NULL,
 source_mtime_ns INTEGER NOT NULL, head_hash TEXT NOT NULL, boundary_hash TEXT NOT NULL,
 state_json BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS rows (
 session_id TEXT NOT NULL, row_id TEXT NOT NULL, position_seq INTEGER NOT NULL,
 updated_seq INTEGER NOT NULL, summary_json BLOB NOT NULL, detail_json BLOB NOT NULL,
 has_detail INTEGER NOT NULL, PRIMARY KEY(session_id,row_id)
);
CREATE INDEX IF NOT EXISTS rows_page ON rows(session_id,position_seq);
CREATE TABLE IF NOT EXISTS updates (
 session_id TEXT NOT NULL, seq INTEGER NOT NULL, row_id TEXT NOT NULL, deleted INTEGER NOT NULL,
 PRIMARY KEY(session_id,seq,row_id)
);
PRAGMA user_version = 2;`)
	return err
}

// CatchUp transactionally follows complete JSONL records. maxSeq >= 0 is an
// explicit durable upper bound (including zero for an empty live log); -1 means
// the closed/persisted file is authoritative through its complete snapshotted
// size. Live callers obtain maxSeq and maxOffset from event.Log, whose lock
// publishes neither boundary until append and fsync both succeed.
func (s *Store) CatchUp(ctx context.Context, sessionID, logPath string, maxSeq int64) (State, error) {
	return s.CatchUpBounded(ctx, sessionID, logPath, maxSeq, -1)
}

func (s *Store) CatchUpBounded(ctx context.Context, sessionID, logPath string, maxSeq, maxOffset int64) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catchUpLocked(ctx, sessionID, logPath, maxSeq, maxOffset, true)
}

func (s *Store) catchUpLocked(ctx context.Context, sessionID, logPath string, maxSeq, maxOffset int64, allowRebuild bool) (State, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return State{}, err
	}
	observedSize := info.Size()
	size := observedSize
	if maxOffset >= 0 && size > maxOffset {
		size = maxOffset
	}
	dev, ino := fileIdentity(info)
	head, err := firstLineHash(f, size)
	if err != nil {
		return State{}, err
	}

	var seq, offset, storedDev, storedIno, storedSize, storedMtime int64
	var storedHead, storedBoundary string
	var stateJSON []byte
	err = s.db.QueryRowContext(ctx, `SELECT indexed_seq,indexed_offset,source_dev,source_ino,source_size,source_mtime_ns,head_hash,boundary_hash,state_json FROM sessions WHERE session_id=?`, sessionID).Scan(&seq, &offset, &storedDev, &storedIno, &storedSize, &storedMtime, &storedHead, &storedBoundary, &stateJSON)
	state := State{Phase: "running", ErrorRetryable: true, Rollover: true}
	hadIndex := err == nil
	if err == nil {
		if json.Unmarshal(stateJSON, &state) != nil {
			storedHead = "!corrupt"
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return State{}, err
	}
	// Another request may already have indexed a later committed boundary than
	// this caller captured. Validate and return that shared watermark rather than
	// mistaking the caller's older byte cap for source truncation.
	if hadIndex && maxSeq >= 0 && seq >= maxSeq && offset > size && observedSize >= offset {
		size = offset
		head, err = firstLineHash(f, size)
		if err != nil {
			return State{}, err
		}
	}
	boundary := ""
	if offset <= size {
		var boundaryErr error
		boundary, boundaryErr = boundaryHash(f, offset)
		if boundaryErr != nil {
			return State{}, boundaryErr
		}
	}
	mtime := info.ModTime().UnixNano()
	// Size and mtime describe the same f.Stat observation. The effective size may
	// be an older durable cap, so its smaller value must neither be paired with the
	// later mtime nor turn an ordinary concurrent append into replacement evidence.
	sourceChanged := hadIndex && (offset > size || storedDev != dev || storedIno != ino ||
		(observedSize == size && storedSize == observedSize && storedMtime != mtime) ||
		storedHead != head || storedBoundary != boundary)
	if sourceChanged {
		if !allowRebuild {
			return State{}, errors.New("session view index source changed during rebuild")
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM updates WHERE session_id=?; DELETE FROM rows WHERE session_id=?; DELETE FROM sessions WHERE session_id=?`, sessionID, sessionID, sessionID); err != nil {
			return State{}, err
		}
		return s.catchUpLocked(ctx, sessionID, logPath, maxSeq, maxOffset, false)
	}
	if offset == size || (maxSeq >= 0 && seq >= maxSeq) {
		state.IndexedThrough = seq
		if hadIndex {
			if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET source_dev=?,source_ino=?,source_size=?,source_mtime_ns=?,head_hash=?,boundary_hash=? WHERE session_id=?`, dev, ino, observedSize, mtime, head, boundary, sessionID); err != nil {
				return State{}, err
			}
		}
		return state, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer tx.Rollback()
	section := io.NewSectionReader(f, offset, size-offset)
	scanner := bufio.NewScanner(section)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	consumed := offset
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lineBytes := int64(len(line))
		// Scanner returns a final token even without a delimiter. Leave that token at
		// the watermark until its terminating newline is durably visible.
		if consumed+lineBytes >= size {
			break
		}
		if len(line) == 0 {
			consumed++
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return State{}, fmt.Errorf("corrupt event log %s: %w", logPath, err)
		}
		if maxSeq >= 0 && int64(ev.Seq) > maxSeq {
			break
		}
		if int64(ev.Seq) != seq+1 {
			return State{}, fmt.Errorf("session view sequence discontinuity: got %d after %d", ev.Seq, seq)
		}
		if err := applyEvent(ctx, tx, sessionID, &state, ev); err != nil {
			return State{}, err
		}
		consumed += lineBytes + 1
		seq = int64(ev.Seq)
		state.IndexedThrough = seq
		if maxSeq >= 0 && seq == maxSeq {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return State{}, err
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	boundary, err = boundaryHash(f, consumed)
	if err != nil {
		return State{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(session_id,indexed_seq,indexed_offset,source_dev,source_ino,source_size,source_mtime_ns,head_hash,boundary_hash,state_json) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET indexed_seq=excluded.indexed_seq,indexed_offset=excluded.indexed_offset,source_dev=excluded.source_dev,source_ino=excluded.source_ino,source_size=excluded.source_size,source_mtime_ns=excluded.source_mtime_ns,head_hash=excluded.head_hash,boundary_hash=excluded.boundary_hash,state_json=excluded.state_json`, sessionID, seq, consumed, dev, ino, observedSize, mtime, head, boundary, encodedState)
	if err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return state, nil
}

func fileIdentity(info os.FileInfo) (int64, int64) {
	// os.FileInfo.Sys is platform-specific. Read the Unix Dev/Ino fields through
	// reflection so this package still compiles on platforms whose stat type has a
	// different shape; size/mtime/head/boundary metadata remains the fallback.
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return reflectedInt(value, "Dev"), reflectedInt(value, "Ino")
}

func reflectedInt(value reflect.Value, name string) int64 {
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0
	}
	field := value.FieldByName(name)
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(field.Uint())
	default:
		return 0
	}
}

func firstLineHash(f *os.File, size int64) (string, error) {
	if size == 0 {
		return "", nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	r := bufio.NewReader(io.LimitReader(f, min64(size, 1<<20)))
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(line)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
func boundaryHash(f *os.File, offset int64) (string, error) {
	if offset <= 0 {
		return "", nil
	}
	start := offset - min64(offset, 256)
	buf := make([]byte, offset-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(buf)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func applyEvent(ctx context.Context, tx *sql.Tx, sid string, st *State, ev event.Event) error {
	st.LastTimestamp = ev.TS.Format("2006-01-02T15:04:05.000Z07:00")
	actor := ev.Actor
	isSub := isSubagent(actor)
	str := func(k string) string { v, _ := ev.Data[k].(string); return v }
	if !isSub {
		switch ev.Type {
		case event.Interrupted:
			st.Phase = "paused"
		case event.SessionIdle:
			st.Phase = "idle"
		case event.SessionError:
			st.Phase = "error"
			st.ErrorMessage = firstNonempty(str("msg"), str("error"), str("text"))
			st.ErrorRetryable = boolDefault(ev.Data["retryable"], true)
			if str("action") == "switch_model" {
				st.Rollover = false
			}
		case event.SessionStopped, event.Type("session_ended"):
			st.Phase = "stopped"
		case event.RoleConfigChanged:
			if c := str("coordinator"); c != "" {
				if c != st.Coordinator {
					st.Rollover = true
				}
				st.Coordinator = c
			}
		case event.Resumed, event.SessionStarted:
			st.Phase = "running"
		default:
			if st.Phase != "paused" && isActivityType(ev.Type) {
				st.Phase = "running"
			}
		}
	}
	switch ev.Type {
	case event.SessionStarted, event.RoleConfigChanged:
		if c := str("coordinator"); c != "" {
			st.Coordinator = c
		}
	case event.ModelTurn:
		if actor == "" || actor == "coordinator" {
			if c := str("model_name"); c != "" {
				st.Coordinator = c
			}
			if n, ok := number(ev.Data["context_tokens_est"]); ok && n >= 0 {
				st.ContextTokens = n
				st.HasContext = true
			}
		}
	case event.QuestionAsked:
		st.Pending = questions(ev.Data)
		st.PendingRowID = fmt.Sprintf("seq-%d", ev.Seq)
		st.OpenQuestionRowID = st.PendingRowID
	case event.QuestionAnswered:
		st.Pending = nil
		st.PendingRowID = ""
	}

	if ev.Type == event.SessionIdle {
		report, _ := ev.Data["report"].(string)
		if reportStartsWithTurn(report, st.LastModelText) && st.LastRowID != "" {
			deletedID := st.LastRowID
			if _, err := tx.ExecContext(ctx, `DELETE FROM rows WHERE session_id=? AND row_id=?`, sid, deletedID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO updates(session_id,seq,row_id,deleted) VALUES(?,?,?,1)`, sid, ev.Seq, deletedID); err != nil {
				return err
			}
			st.LastRowID, st.LastModelText = "", ""
		}
	}
	rowID, position, edit, makeRow := classifyRow(st, ev)
	if !makeRow {
		return nil
	}
	if edit {
		var detail []byte
		if err := tx.QueryRowContext(ctx, `SELECT detail_json FROM rows WHERE session_id=? AND row_id=?`, sid, rowID).Scan(&detail); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			// Orphan result is a standalone row; other missing edit targets remain
			// represented by state and will be current if their page is later loaded.
			if ev.Type != event.ToolResult {
				return nil
			}
			position = int64(ev.Seq)
			detail = nil
		}
		var bundle []event.Event
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &bundle); err != nil {
				return err
			}
		}
		bundle = append(bundle, ev)
		return upsertRow(ctx, tx, sid, rowID, position, int64(ev.Seq), bundle)
	}
	return upsertRow(ctx, tx, sid, rowID, position, int64(ev.Seq), []event.Event{ev})
}

func classifyRow(st *State, ev event.Event) (string, int64, bool, bool) {
	seqID := fmt.Sprintf("seq-%d", ev.Seq)
	str := func(k string) string { v, _ := ev.Data[k].(string); return v }
	switch ev.Type {
	case event.UserInput:
		st.LastRowID = seqID
		st.LastModelText = ""
		return seqID, int64(ev.Seq), false, true
	case event.UserInputDelivered:
		n, ok := number(ev.Data["seq"])
		if !ok {
			return "", 0, false, false
		}
		return fmt.Sprintf("seq-%d", n), 0, true, true
	case event.ModelTurn:
		text := str("text")
		if text == "" {
			return "", 0, false, false
		}
		st.LastRowID = seqID
		st.LastModelText = text
		return seqID, int64(ev.Seq), false, true
	case event.Thinking:
		if str("text") == "" {
			return "", 0, false, false
		}
	case event.ToolCall:
		id := str("id")
		if id != "" {
			seqID = "tool-" + id
		}
	case event.ToolResult:
		id := str("id")
		if id != "" {
			return "tool-" + id, 0, true, true
		}
	case event.QuestionAsked:
	case event.QuestionAnswered:
		id := st.OpenQuestionRowID
		st.OpenQuestionRowID = ""
		if id == "" {
			return "", 0, false, false
		}
		return id, 0, true, true
	case event.SessionIdle:
	case event.JobNotified:
		return "", 0, false, false
	}
	st.LastRowID = seqID
	st.LastModelText = ""
	return seqID, int64(ev.Seq), false, true
}

func upsertRow(ctx context.Context, tx *sql.Tx, sid, rowID string, pos, updated int64, bundle []event.Event) error {
	detail, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	summaryBundle := make([]event.Event, len(bundle))
	copy(summaryBundle, bundle)
	hasDetail := false
	for i := range summaryBundle {
		var abbreviated bool
		if summaryBundle[i].Type == event.QuestionAsked && questionStateNeedsDetail(summaryBundle[i].Data) {
			hasDetail = true
		}
		summaryBundle[i].Data, abbreviated = presentationData(summaryBundle[i].Data)
		hasDetail = hasDetail || abbreviated
		for _, key := range []string{"args", "result", "text", "report"} {
			if value, ok := summaryBundle[i].Data[key].(string); ok && len(value) > 32768 {
				summaryBundle[i].Data[key] = truncateUTF8(value, 32768) + "\n…"
				hasDetail = true
			}
		}
	}
	summary, err := json.Marshal(summaryBundle)
	if err != nil {
		return err
	}
	if len(summary) > maxSummaryJSONBytes {
		for i := range summaryBundle {
			summaryBundle[i].Data = compactPresentationData(summaryBundle[i].Data)
		}
		hasDetail = true
		summary, err = json.Marshal(summaryBundle)
		if err != nil {
			return err
		}
	}
	if len(summary) > maxSummaryJSONBytes {
		for i := range summaryBundle {
			summaryBundle[i].Data = minimalPresentationData(summaryBundle[i])
		}
		summary, err = json.Marshal(summaryBundle)
		if err != nil {
			return err
		}
	}
	if len(summary) > maxSummaryJSONBytes && len(summaryBundle) > 2 {
		summaryBundle = []event.Event{summaryBundle[0], summaryBundle[len(summaryBundle)-1]}
		summary, err = json.Marshal(summaryBundle)
		if err != nil {
			return err
		}
	}
	if len(summary) > maxSummaryJSONBytes {
		return fmt.Errorf("session view summary for row %s exceeds %d bytes", rowID, maxSummaryJSONBytes)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rows(session_id,row_id,position_seq,updated_seq,summary_json,detail_json,has_detail) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(session_id,row_id) DO UPDATE SET updated_seq=excluded.updated_seq,summary_json=excluded.summary_json,detail_json=excluded.detail_json,has_detail=excluded.has_detail`, sid, rowID, pos, updated, summary, detail, hasDetail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO updates(session_id,seq,row_id,deleted) VALUES(?,?,?,0)`, sid, updated, rowID)
	return err
}

var presentationKeys = map[string]bool{
	"text": true, "images": true, "queued": true, "seq": true, "name": true, "id": true,
	"args": true, "result": true, "error": true, "question": true, "options": true,
	"questions": true, "answer": true, "answers": true, "report": true, "mode": true,
	"coordinator": true, "msg": true, "action": true, "retryable": true, "role": true,
	"agent_id": true, "model": true, "summary": true, "sha": true, "message": true,
	"decision": true, "task": true, "implementer": true, "reviewers": true, "to": true,
	"status": true, "label": true, "reason": true, "attachment_id": true,
	"media_type": true, "filename": true,
}

func compactPresentationData(in map[string]any) map[string]any {
	out := make(map[string]any)
	for _, k := range []string{"id", "name", "text", "args", "result", "report", "question", "msg", "error", "status", "sha"} {
		v, ok := in[k]
		if !ok {
			continue
		}
		switch value := v.(type) {
		case string:
			if len(value) > 4096 {
				value = truncateUTF8(value, 4096) + "…"
			}
			out[k] = value
		case bool, float64, int, int64, json.Number:
			out[k] = value
		}
	}
	return out
}

func minimalPresentationData(ev event.Event) map[string]any {
	keys := []string{"text", "id", "name", "args", "result", "error", "msg", "report", "question", "options", "questions", "answer", "answers", "queued", "status", "sha", "role", "agent_id", "model"}
	out := make(map[string]any)
	for _, key := range keys {
		if value, ok := ev.Data[key]; ok {
			out[key] = boundMinimalValue(value, 0)
		}
	}
	return out
}

func boundMinimalValue(v any, depth int) any {
	if depth >= 4 {
		return nil
	}
	switch x := v.(type) {
	case string:
		if len(x) > 256 {
			return truncateUTF8(x, 256) + "…"
		}
		return x
	case []string:
		if len(x) > 4 {
			x = x[:4]
		}
		out := make([]string, len(x))
		for i, item := range x {
			out[i], _ = boundMinimalValue(item, depth+1).(string)
		}
		return out
	case []any:
		if len(x) > 4 {
			x = x[:4]
		}
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = boundMinimalValue(item, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any)
		count := 0
		for key, item := range x {
			if count == 8 {
				break
			}
			out[key] = boundMinimalValue(item, depth+1)
			count++
		}
		return out
	default:
		return v
	}
}

func presentationData(in map[string]any) (map[string]any, bool) {
	out := make(map[string]any)
	changed := false
	for k, v := range in {
		if !presentationKeys[k] {
			continue
		}
		bounded, c := boundPresentationValue(v, 0)
		out[k] = bounded
		changed = changed || c
	}
	return out, changed
}
func boundPresentationValue(v any, depth int) (any, bool) {
	if depth >= 4 {
		return nil, true
	}
	switch x := v.(type) {
	case string:
		if len(x) > 32768 {
			return truncateUTF8(x, 32768) + "\n…", true
		}
		return x, false
	case []any:
		limit := len(x)
		changed := false
		if limit > 100 {
			limit = 100
			changed = true
		}
		out := make([]any, 0, limit)
		for _, item := range x[:limit] {
			b, c := boundPresentationValue(item, depth+1)
			out = append(out, b)
			changed = changed || c
		}
		return out, changed
	case []string:
		limit := len(x)
		changed := false
		if limit > 100 {
			limit = 100
			changed = true
		}
		out := make([]string, 0, limit)
		for _, item := range x[:limit] {
			if len(item) > 4096 {
				item = truncateUTF8(item, 4096) + "…"
				changed = true
			}
			out = append(out, item)
		}
		return out, changed
	case map[string]any:
		out := make(map[string]any)
		changed := false
		count := 0
		for k, item := range x {
			if count >= 100 {
				changed = true
				break
			}
			b, c := boundPresentationValue(item, depth+1)
			out[k] = b
			changed = changed || c
			count++
		}
		return out, changed
	default:
		return v, false
	}
}

// ByteLimit returns the daemon-enforced complete encoded response budget.
func ByteLimit(bytes int) int {
	if bytes <= 0 {
		return DefaultBytes
	}
	if bytes < MinBytes {
		return MinBytes
	}
	if bytes > MaxBytes {
		return MaxBytes
	}
	return bytes
}

func normalizeBounds(rows, bytes int) (int, int) {
	if rows <= 0 {
		rows = DefaultRows
	}
	if rows > MaxRows {
		rows = MaxRows
	}
	bytes = ByteLimit(bytes)
	return rows, bytes
}

// Page returns ascending rows before beforeSeq. beforeSeq==0 means the newest
// page. The cursor is the oldest returned position; positions never change.
func (s *Store) Page(ctx context.Context, sid string, beforeSeq int64, maxRows, maxBytes int) ([]Row, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageLocked(ctx, sid, beforeSeq, maxRows, maxBytes)
}

func (s *Store) pageLocked(ctx context.Context, sid string, beforeSeq int64, maxRows, maxBytes int) ([]Row, int64, error) {
	maxRows, maxBytes = normalizeBounds(maxRows, maxBytes)
	query := `SELECT row_id,position_seq,updated_seq,summary_json,has_detail FROM rows WHERE session_id=?`
	args := []any{sid}
	if beforeSeq > 0 {
		query += ` AND position_seq < ?`
		args = append(args, beforeSeq)
	}
	query += ` ORDER BY position_seq DESC LIMIT ?`
	args = append(args, maxRows+1)
	rs, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()
	var desc []Row
	used := 0
	more := false
	for rs.Next() {
		var r Row
		var raw []byte
		var hd int
		if err := rs.Scan(&r.ID, &r.PositionSeq, &r.UpdatedSeq, &raw, &hd); err != nil {
			return nil, 0, err
		}
		r.HasDetail = hd != 0
		if err := json.Unmarshal(raw, &r.Events); err != nil {
			return nil, 0, err
		}
		cost := len(raw) + len(r.ID) + 32
		if len(desc) >= maxRows || (len(desc) > 0 && used+cost > maxBytes) {
			more = true
			break
		}
		desc = append(desc, r)
		used += cost
	}
	if err := rs.Err(); err != nil {
		return nil, 0, err
	}
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	cursor := int64(0)
	if more && len(desc) > 0 {
		cursor = desc[0].PositionSeq
	}
	return desc, cursor, nil
}

func (s *Store) Detail(ctx context.Context, sid, rowID string) (Row, error) {
	return s.row(ctx, sid, rowID, true)
}
func (s *Store) Summary(ctx context.Context, sid, rowID string) (Row, error) {
	return s.row(ctx, sid, rowID, false)
}
func (s *Store) row(ctx context.Context, sid, rowID string, detail bool) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rowLocked(ctx, sid, rowID, detail)
}
func (s *Store) rowLocked(ctx context.Context, sid, rowID string, detail bool) (Row, error) {
	var r Row
	var raw []byte
	var hd int
	column := "summary_json"
	if detail {
		column = "detail_json"
	}
	err := s.db.QueryRowContext(ctx, `SELECT row_id,position_seq,updated_seq,`+column+`,has_detail FROM rows WHERE session_id=? AND row_id=?`, sid, rowID).Scan(&r.ID, &r.PositionSeq, &r.UpdatedSeq, &raw, &hd)
	if err != nil {
		return Row{}, err
	}
	r.HasDetail = hd != 0 && !detail
	if err := json.Unmarshal(raw, &r.Events); err != nil {
		return Row{}, err
	}
	return r, nil
}
func (s *Store) Updates(ctx context.Context, sid string, seq int64) ([]Update, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, err := s.db.QueryContext(ctx, `SELECT row_id,deleted FROM updates WHERE session_id=? AND seq=?`, sid, seq)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Update
	for rs.Next() {
		var u Update
		var d int
		if err := rs.Scan(&u.RowID, &d); err != nil {
			return nil, err
		}
		u.Deleted = d != 0
		out = append(out, u)
	}
	return out, rs.Err()
}

// View catches up and reads state plus the newest page under one store lock, so
// no other viewer can advance rows beyond the advertised state watermark.
func (s *Store) View(ctx context.Context, sid, logPath string, maxSeq, maxOffset int64, maxRows, maxBytes int) (State, []Row, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.catchUpLocked(ctx, sid, logPath, maxSeq, maxOffset, true)
	if err != nil {
		return State{}, nil, 0, err
	}
	rows, before, err := s.pageLocked(ctx, sid, 0, maxRows, maxBytes)
	return st, rows, before, err
}

// EarlierPage catches up and reads an earlier page atomically. Its returned
// watermark lets clients reject a page that races a newer streamed row version.
func (s *Store) EarlierPage(ctx context.Context, sid, logPath string, maxSeq, maxOffset, beforeSeq int64, maxRows, maxBytes int) (State, []Row, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.catchUpLocked(ctx, sid, logPath, maxSeq, maxOffset, true)
	if err != nil {
		return State{}, nil, 0, err
	}
	rows, before, err := s.pageLocked(ctx, sid, beforeSeq, maxRows, maxBytes)
	return st, rows, before, err
}

func (s *Store) CurrentDetail(ctx context.Context, sid, logPath, rowID string, maxSeq, maxOffset int64) (State, Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.catchUpLocked(ctx, sid, logPath, maxSeq, maxOffset, true)
	if err != nil {
		return State{}, Row{}, err
	}
	row, err := s.rowLocked(ctx, sid, rowID, true)
	return st, row, err
}

// ChangesSince catches up to at least maxSeq and coalesces every row mutation
// after afterSeq through the returned watermark. This is intentionally a range,
// not an exact-sequence lookup: a shared index may already have been advanced by
// another request. Reading changes and final row summaries under the same lock
// guarantees the stream delivers everything through the cursor it advertises.
func (s *Store) ChangesSince(ctx context.Context, sid, logPath string, maxSeq, maxOffset, afterSeq int64) (State, []Row, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.catchUpLocked(ctx, sid, logPath, maxSeq, maxOffset, true)
	if err != nil {
		return State{}, nil, nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT seq,row_id,deleted FROM updates WHERE session_id=? AND seq>? AND seq<=? ORDER BY seq,row_id`, sid, afterSeq, st.IndexedThrough)
	if err != nil {
		return State{}, nil, nil, err
	}
	latest := make(map[string]Update)
	for rs.Next() {
		var seq int64
		var u Update
		var deleted int
		if err := rs.Scan(&seq, &u.RowID, &deleted); err != nil {
			rs.Close()
			return State{}, nil, nil, err
		}
		u.Deleted = deleted != 0
		latest[u.RowID] = u
	}
	if err := rs.Close(); err != nil {
		return State{}, nil, nil, err
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rows []Row
	var deleted []string
	for _, id := range ids {
		if latest[id].Deleted {
			deleted = append(deleted, id)
			continue
		}
		row, err := s.rowLocked(ctx, sid, id, false)
		if errors.Is(err, sql.ErrNoRows) {
			// A later deletion should have been the latest update, but treating a
			// missing final row as a tombstone is safer than exposing stale content.
			deleted = append(deleted, id)
			continue
		}
		if err != nil {
			return State{}, nil, nil, err
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PositionSeq < rows[j].PositionSeq })
	return st, rows, deleted, nil
}

func isSubagent(actor string) bool {
	a := strings.ToLower(actor)
	return a != "" && a != "coordinator" && a != "user" && a != "system" && a != "daemon"
}
func isActivityType(t event.Type) bool {
	switch t {
	case event.UserInput, event.UserInputDelivered, event.ModelTurn, event.Thinking, event.ToolCall, event.ToolResult, event.QuestionAsked:
		return true
	}
	return false
}
func boolDefault(v any, d bool) bool {
	b, ok := v.(bool)
	if !ok {
		return d
	}
	return b
}
func number(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case json.Number:
		i, e := n.Int64()
		return i, e == nil
	}
	return 0, false
}
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && (value[limit]&0xc0) == 0x80 {
		limit--
	}
	return value[:limit]
}

func reportStartsWithTurn(report, turn string) bool {
	report = strings.TrimSpace(report)
	turn = strings.TrimSpace(turn)
	return turn != "" && (report == turn || strings.HasPrefix(report, turn+"\n"))
}
func firstNonempty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func questionStateNeedsDetail(data map[string]any) bool {
	qs := questions(data)
	if len(qs) > 8 {
		return true
	}
	for _, question := range qs {
		if len(question.Prompt) > 256 || len(question.Options) > 8 {
			return true
		}
		for _, option := range question.Options {
			if len(option) > 128 {
				return true
			}
		}
	}
	return false
}

func questions(data map[string]any) []Question {
	if q, ok := data["question"].(string); ok && q != "" {
		return []Question{{Prompt: q, Options: stringSlice(data["options"])}}
	}
	if raw, ok := data["questions"].([]any); ok {
		out := make([]Question, 0, len(raw))
		for _, v := range raw {
			m, _ := v.(map[string]any)
			q, _ := m["question"].(string)
			if q == "" {
				q = "a question was asked"
			}
			out = append(out, Question{Prompt: q, Options: stringSlice(m["options"])})
		}
		if len(out) > 0 {
			return out
		}
	}
	return []Question{{Prompt: "a question was asked"}}
}
func stringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, v := range x {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
