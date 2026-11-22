package sessionview

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/whyrusleeping/ycc/internal/event"
)

// searchSchemaVersion rebuilds the whole search index when it changes.
const searchSchemaVersion = 1

// maxSearchDocBytes bounds one indexed message; longer pastes are indexed
// through this prefix only.
const maxSearchDocBytes = 256 << 10

// Snippet highlight markers. Indexed text has them replaced by spaces, so a
// marker in a snippet is always one SQLite inserted.
const (
	snippetOpen  = "\x02"
	snippetClose = "\x03"
)

// SearchIndex is a project's rebuildable full-text index of conversational
// session text: user input, coordinator assistant text, and asked questions
// with their human answers. Tool calls and results, thinking, and summaries are
// never indexed. Like Store it follows each JSONL log from a durable byte
// watermark and is never authoritative. Document row ids are the presentation
// row ids Store assigns from the same log, so a hit names a row GetSessionView
// can serve.
//
// The database runs in WAL mode so Search reads the last committed state
// while a catch-up transaction is open; mu serializes only writers.
type SearchIndex struct {
	mu   sync.Mutex
	db   *sql.DB
	path string
	file os.FileInfo
}

// SearchHit is the best-ranked matching message of one session.
type SearchHit struct {
	SessionID string
	RowID     string
	Seq       int64
	Kind      string // user | assistant | question | answer
	Actor     string
	TS        time.Time
	Snippet   []SnippetSpan
	Matches   int
}

type SnippetSpan struct {
	Text  string
	Match bool
}

// searchState carries the row-identity bookkeeping classifyRow needs between
// catch-ups.
type searchState struct {
	OpenQuestionRowID string `json:"q,omitempty"`
	LastRowID         string `json:"r,omitempty"`
	LastModelText     string `json:"t,omitempty"`
}

func OpenSearch(path string) (*SearchIndex, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		// A WAL left beside a deleted database belongs to the old one.
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	}
	db, err := openPrivateDB(path, "?_busy_timeout=10000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	x := &SearchIndex{db: db, path: path}
	if err := x.init(); err != nil {
		db.Close()
		return nil, err
	}
	if x.file, err = os.Stat(path); err != nil {
		db.Close()
		return nil, err
	}
	return x, nil
}

// Stale reports whether the database file was deleted or replaced since it
// was opened; the caller reopens it to rebuild from the logs.
func (x *SearchIndex) Stale() bool {
	info, err := os.Stat(x.path)
	return err != nil || !os.SameFile(info, x.file)
}

func (x *SearchIndex) Close() error { return x.db.Close() }

func (x *SearchIndex) init() error {
	var version int
	if err := x.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != searchSchemaVersion {
		if _, err := x.db.Exec(`DROP TABLE IF EXISTS docs_fts; DROP TABLE IF EXISTS docs; DROP TABLE IF EXISTS sources;`); err != nil {
			return err
		}
	}
	_, err := x.db.Exec(`
CREATE TABLE IF NOT EXISTS sources (
 session_id TEXT PRIMARY KEY, indexed_seq INTEGER NOT NULL, indexed_offset INTEGER NOT NULL,
 source_dev INTEGER NOT NULL, source_ino INTEGER NOT NULL, source_size INTEGER NOT NULL,
 source_mtime_ns INTEGER NOT NULL, head_hash TEXT NOT NULL, boundary_hash TEXT NOT NULL,
 state_json BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS docs (
 id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, row_id TEXT NOT NULL, seq INTEGER NOT NULL,
 kind TEXT NOT NULL, actor TEXT NOT NULL, ts_unix_ns INTEGER NOT NULL, body TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS docs_session ON docs(session_id,row_id);
CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(body, content='docs', content_rowid='id', tokenize='unicode61 remove_diacritics 2');
CREATE TRIGGER IF NOT EXISTS docs_ai AFTER INSERT ON docs BEGIN
 INSERT INTO docs_fts(rowid,body) VALUES(new.id,new.body);
END;
CREATE TRIGGER IF NOT EXISTS docs_ad AFTER DELETE ON docs BEGIN
 INSERT INTO docs_fts(docs_fts,rowid,body) VALUES('delete',old.id,old.body);
END;
` + fmt.Sprintf("PRAGMA user_version = %d;", searchSchemaVersion))
	return err
}

// IndexSession follows one session log through the same durable bound
// CatchUpBounded takes (maxSeq/maxOffset -1 for a closed log). A replaced or
// rewritten source drops the session's documents and indexes it again.
func (x *SearchIndex) IndexSession(ctx context.Context, sessionID, logPath string, maxSeq, maxOffset int64) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.indexLocked(ctx, sessionID, logPath, maxSeq, maxOffset, true)
}

// indexLocked applies Store.catchUpLocked's source validation to the search
// watermark; see there for the reasoning behind each check.
func (x *SearchIndex) indexLocked(ctx context.Context, sessionID, logPath string, maxSeq, maxOffset int64, allowRebuild bool) error {
	f, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	observedSize := info.Size()
	size := observedSize
	if maxOffset >= 0 && size > maxOffset {
		size = maxOffset
	}
	dev, ino := fileIdentity(info)
	head, err := firstLineHash(f, size)
	if err != nil {
		return err
	}

	var seq, offset, storedDev, storedIno, storedSize, storedMtime int64
	var storedHead, storedBoundary string
	var stateJSON []byte
	err = x.db.QueryRowContext(ctx, `SELECT indexed_seq,indexed_offset,source_dev,source_ino,source_size,source_mtime_ns,head_hash,boundary_hash,state_json FROM sources WHERE session_id=?`, sessionID).Scan(&seq, &offset, &storedDev, &storedIno, &storedSize, &storedMtime, &storedHead, &storedBoundary, &stateJSON)
	var st searchState
	hadIndex := err == nil
	if err == nil {
		if json.Unmarshal(stateJSON, &st) != nil {
			storedHead = "!corrupt"
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if hadIndex && maxSeq >= 0 && seq >= maxSeq && offset > size && observedSize >= offset {
		size = offset
		if head, err = firstLineHash(f, size); err != nil {
			return err
		}
	}
	boundary := ""
	if offset <= size {
		if boundary, err = boundaryHash(f, offset); err != nil {
			return err
		}
	}
	mtime := info.ModTime().UnixNano()
	sourceChanged := hadIndex && (offset > size || storedDev != dev || storedIno != ino ||
		(observedSize == size && storedSize == observedSize && storedMtime != mtime) ||
		storedHead != head || storedBoundary != boundary)
	if sourceChanged {
		if !allowRebuild {
			return errors.New("session search index source changed during rebuild")
		}
		if err := x.dropLocked(ctx, sessionID); err != nil {
			return err
		}
		return x.indexLocked(ctx, sessionID, logPath, maxSeq, maxOffset, false)
	}
	if offset == size || (maxSeq >= 0 && seq >= maxSeq) {
		if hadIndex {
			_, err := x.db.ExecContext(ctx, `UPDATE sources SET source_dev=?,source_ino=?,source_size=?,source_mtime_ns=?,head_hash=?,boundary_hash=? WHERE session_id=?`, dev, ino, observedSize, mtime, head, boundary, sessionID)
			return err
		}
		return nil
	}

	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scanner := bufio.NewScanner(io.NewSectionReader(f, offset, size-offset))
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	consumed := offset
	for scanner.Scan() {
		line := scanner.Bytes()
		lineBytes := int64(len(line))
		if consumed+lineBytes >= size {
			break // unterminated: not yet committed
		}
		if len(line) == 0 {
			consumed++
			continue
		}
		var env searchEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			return fmt.Errorf("corrupt event log %s: %w", logPath, err)
		}
		if maxSeq >= 0 && int64(env.Seq) > maxSeq {
			break
		}
		if int64(env.Seq) > seq {
			// Out-of-order and repeated numbers are dropped, as Store does.
			if err := indexSearchEvent(ctx, tx, sessionID, &st, env.event()); err != nil {
				return err
			}
			seq = int64(env.Seq)
		}
		consumed += lineBytes + 1
		if maxSeq >= 0 && seq == maxSeq {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	encodedState, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if boundary, err = boundaryHash(f, consumed); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sources(session_id,indexed_seq,indexed_offset,source_dev,source_ino,source_size,source_mtime_ns,head_hash,boundary_hash,state_json) VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id) DO UPDATE SET indexed_seq=excluded.indexed_seq,indexed_offset=excluded.indexed_offset,source_dev=excluded.source_dev,source_ino=excluded.source_ino,source_size=excluded.source_size,source_mtime_ns=excluded.source_mtime_ns,head_hash=excluded.head_hash,boundary_hash=excluded.boundary_hash,state_json=excluded.state_json`, sessionID, seq, consumed, dev, ino, observedSize, mtime, head, boundary, encodedState); err != nil {
		return err
	}
	return tx.Commit()
}

func (x *SearchIndex) dropLocked(ctx context.Context, sessionID string) error {
	_, err := x.db.ExecContext(ctx, `DELETE FROM docs WHERE session_id=?; DELETE FROM sources WHERE session_id=?`, sessionID, sessionID)
	return err
}

// Prune drops every indexed session not in keep (logs that no longer exist).
func (x *SearchIndex) Prune(ctx context.Context, keep map[string]bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	rs, err := x.db.QueryContext(ctx, `SELECT session_id FROM sources`)
	if err != nil {
		return err
	}
	var gone []string
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			rs.Close()
			return err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	if err := rs.Close(); err != nil {
		return err
	}
	for _, id := range gone {
		if err := x.dropLocked(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// searchEnvelope defers decoding event data until the type is known, so the
// bulk of a log (tool output) is scanned but never materialized as maps.
type searchEnvelope struct {
	Seq   int             `json:"seq"`
	TS    time.Time       `json:"ts"`
	Actor string          `json:"actor"`
	Type  event.Type      `json:"type"`
	Data  json.RawMessage `json:"data"`
}

func (env searchEnvelope) event() event.Event {
	ev := event.Event{Seq: env.Seq, TS: env.TS, Actor: env.Actor, Type: env.Type}
	if len(env.Data) == 0 {
		return ev
	}
	switch env.Type {
	case event.UserInput, event.ModelTurn, event.QuestionAsked, event.QuestionAnswered, event.SessionIdle:
		_ = json.Unmarshal(env.Data, &ev.Data)
		return ev
	}
	// classifyRow reads only these fields of other events.
	var light struct {
		ID   string   `json:"id"`
		Text string   `json:"text"`
		Seq  *float64 `json:"seq"`
	}
	if json.Unmarshal(env.Data, &light) != nil {
		_ = json.Unmarshal(env.Data, &ev.Data)
		return ev
	}
	ev.Data = map[string]any{}
	if light.ID != "" {
		ev.Data["id"] = light.ID
	}
	if light.Text != "" {
		ev.Data["text"] = light.Text
	}
	if light.Seq != nil {
		ev.Data["seq"] = *light.Seq
	}
	return ev
}

// indexSearchEvent mirrors applyEvent's row identity so documents land on
// the presentation row that shows their text.
func indexSearchEvent(ctx context.Context, tx *sql.Tx, sid string, st *searchState, ev event.Event) error {
	coordinator := ev.Actor == "" || ev.Actor == "coordinator"
	str := func(k string) string { v, _ := ev.Data[k].(string); return v }
	if ev.Type == event.QuestionAsked {
		st.OpenQuestionRowID = fmt.Sprintf("seq-%d", ev.Seq)
	}
	if ev.Type == event.SessionIdle && reportStartsWithTurn(str("report"), st.LastModelText) && st.LastRowID != "" {
		// The report row replaces the final model turn's row.
		if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE session_id=? AND row_id=?`, sid, st.LastRowID); err != nil {
			return err
		}
		st.LastRowID, st.LastModelText = "", ""
	}
	rs := State{OpenQuestionRowID: st.OpenQuestionRowID, LastRowID: st.LastRowID, LastModelText: st.LastModelText}
	rowID, _, _, makeRow := classifyRow(&rs, ev)
	st.OpenQuestionRowID, st.LastRowID, st.LastModelText = rs.OpenQuestionRowID, rs.LastRowID, rs.LastModelText
	if !makeRow {
		return nil
	}
	var kind, body string
	switch ev.Type {
	case event.UserInput:
		kind, body = "user", str("text")
	case event.ModelTurn:
		if coordinator {
			kind, body = "assistant", str("text")
		}
	case event.SessionIdle:
		if coordinator {
			kind, body = "assistant", str("report")
		}
	case event.QuestionAsked:
		kind, body = "question", questionText(ev.Data)
	case event.QuestionAnswered:
		if ev.Data["auto"] != true {
			kind, body = "answer", answerText(ev.Data)
		}
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	body = strings.NewReplacer(snippetOpen, " ", snippetClose, " ").Replace(truncateUTF8(body, maxSearchDocBytes))
	_, err := tx.ExecContext(ctx, `INSERT INTO docs(session_id,row_id,seq,kind,actor,ts_unix_ns,body) VALUES(?,?,?,?,?,?,?)`, sid, rowID, ev.Seq, kind, ev.Actor, ev.TS.UnixNano(), body)
	return err
}

func questionText(data map[string]any) string {
	var parts []string
	add := func(m map[string]any) {
		if q, _ := m["question"].(string); q != "" {
			parts = append(parts, q)
		}
		parts = append(parts, stringSlice(m["options"])...)
	}
	add(data)
	if raw, ok := data["questions"].([]any); ok {
		for _, v := range raw {
			if m, ok := v.(map[string]any); ok {
				add(m)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func answerText(data map[string]any) string {
	if a, _ := data["answer"].(string); a != "" {
		return a
	}
	return strings.Join(stringSlice(data["answers"]), "\n")
}

// ftsQuery turns user text into an FTS5 query: every whitespace-separated
// term is required and matched as a quoted word prefix, so FTS5 syntax
// characters in the input are literal. Terms with no letters or digits are
// dropped; the result is empty when nothing searchable remains.
func ftsQuery(query string) string {
	var terms []string
	for _, f := range strings.Fields(query) {
		if !strings.ContainsFunc(f, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"*`)
	}
	return strings.Join(terms, " ")
}

// Search returns the best-ranked matching message of each session (bm25, then
// newest), skipping offset sessions. more reports whether later hits exist.
func (x *SearchIndex) Search(ctx context.Context, query string, offset, limit int) (hits []SearchHit, more bool, err error) {
	match := ftsQuery(query)
	if match == "" || limit <= 0 {
		return nil, false, nil
	}
	// One read transaction: the page and its snippets see the same commit.
	tx, err := x.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	rs, err := tx.QueryContext(ctx, `SELECT id,session_id,row_id,seq,kind,actor,ts_unix_ns,n FROM (
 SELECT d.id,d.session_id,d.row_id,d.seq,d.kind,d.actor,d.ts_unix_ns,docs_fts.rank AS r,
  ROW_NUMBER() OVER (PARTITION BY d.session_id ORDER BY docs_fts.rank, d.ts_unix_ns DESC) AS rn,
  COUNT(*) OVER (PARTITION BY d.session_id) AS n
 FROM docs_fts JOIN docs d ON d.id=docs_fts.rowid WHERE docs_fts MATCH ?
) WHERE rn=1 ORDER BY r, ts_unix_ns DESC LIMIT ? OFFSET ?`, match, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	var ids []int64
	for rs.Next() {
		var h SearchHit
		var id, ts int64
		if err := rs.Scan(&id, &h.SessionID, &h.RowID, &h.Seq, &h.Kind, &h.Actor, &ts, &h.Matches); err != nil {
			rs.Close()
			return nil, false, err
		}
		h.TS = time.Unix(0, ts).UTC()
		hits = append(hits, h)
		ids = append(ids, id)
	}
	if err := rs.Close(); err != nil {
		return nil, false, err
	}
	if len(hits) > limit {
		hits, ids, more = hits[:limit], ids[:limit], true
	}
	if len(ids) == 0 {
		return hits, more, nil
	}
	// Snippets only for the returned page, not for every matching message.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{match}
	for _, id := range ids {
		args = append(args, id)
	}
	srs, err := tx.QueryContext(ctx, `SELECT rowid, snippet(docs_fts,0,char(2),char(3),'…',16) FROM docs_fts WHERE docs_fts MATCH ? AND rowid IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, false, err
	}
	defer srs.Close()
	snippets := make(map[int64]string, len(ids))
	for srs.Next() {
		var id int64
		var s string
		if err := srs.Scan(&id, &s); err != nil {
			return nil, false, err
		}
		snippets[id] = s
	}
	if err := srs.Err(); err != nil {
		return nil, false, err
	}
	for i, id := range ids {
		hits[i].Snippet = snippetSpans(snippets[id])
	}
	return hits, more, nil
}

func snippetSpans(s string) []SnippetSpan {
	var out []SnippetSpan
	for s != "" {
		open := strings.Index(s, snippetOpen)
		if open < 0 {
			out = append(out, SnippetSpan{Text: s})
			break
		}
		if open > 0 {
			out = append(out, SnippetSpan{Text: s[:open]})
		}
		s = s[open+len(snippetOpen):]
		end := strings.Index(s, snippetClose)
		if end < 0 {
			end = len(s)
		}
		if end > 0 {
			out = append(out, SnippetSpan{Text: s[:end], Match: true})
		}
		s = strings.TrimPrefix(s[end:], snippetClose)
	}
	return out
}

// EncodeSearchCursor and DecodeSearchCursor keep the paging offset opaque.
func EncodeSearchCursor(offset int) string { return "o" + strconv.Itoa(offset) }

func DecodeSearchCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(cursor, "o"))
	if err != nil || n < 0 || !strings.HasPrefix(cursor, "o") {
		return 0, errors.New("invalid search cursor")
	}
	return n, nil
}
