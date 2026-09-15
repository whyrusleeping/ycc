package sessionview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"google.golang.org/protobuf/proto"
)

func writeLog(t *testing.T, path string, events []event.Event) {
	t.Helper()
	var out strings.Builder
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ev(seq int, typ event.Type, data map[string]any) event.Event {
	return event.Event{Seq: seq, TS: time.Unix(int64(seq), 0).UTC(), Actor: "coordinator", Type: typ, Data: data}
}

func TestIndexIncrementalPagesEditsAndDetails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	large := strings.Repeat("x", 40000)
	events := []event.Event{
		ev(1, event.SessionStarted, map[string]any{"coordinator": "claude"}),
		ev(2, event.UserInput, map[string]any{"text": "hello", "queued": true}),
		ev(3, event.ModelTurn, map[string]any{"text": "answer"}),
		ev(4, event.ToolCall, map[string]any{"id": "old", "name": "Read", "args": large}),
		ev(5, event.QuestionAsked, map[string]any{"question": "Proceed?", "options": []string{"yes", "no"}}),
	}
	writeLog(t, logPath, events)
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.CatchUp(ctx, "s1", logPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	if state.IndexedThrough != 5 || state.Coordinator != "claude" || len(state.Pending) != 1 {
		t.Fatalf("state=%+v", state)
	}

	page, cursor, err := store.Page(ctx, "s1", 0, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || cursor == 0 || page[0].ID != "tool-old" || page[1].ID != "seq-5" {
		t.Fatalf("page=%+v cursor=%d", page, cursor)
	}
	if !page[0].HasDetail {
		t.Fatal("large tool args were not abbreviated")
	}
	detail, err := store.Detail(ctx, "s1", "tool-old")
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Events[0].Data["args"].(string); got != large {
		t.Fatalf("detail length=%d", len(got))
	}
	earlier, next, err := store.Page(ctx, "s1", cursor, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if next != 0 || len(earlier) != 3 {
		t.Fatalf("earlier=%d next=%d", len(earlier), next)
	}

	// Catch up only through the exact live event sequence. The tool result edits a
	// row in an earlier page and the answer edits the question row in place.
	events = append(events,
		ev(6, event.ToolResult, map[string]any{"id": "old", "name": "Read", "result": "ok"}),
		ev(7, event.QuestionAnswered, map[string]any{"answer": "yes"}),
		ev(8, event.ModelTurn, map[string]any{"text": "later"}),
	)
	writeLog(t, logPath, events)
	state, err = store.CatchUp(ctx, "s1", logPath, 6)
	if err != nil {
		t.Fatal(err)
	}
	if state.IndexedThrough != 6 {
		t.Fatalf("watermark=%d", state.IndexedThrough)
	}
	updates, err := store.Updates(ctx, "s1", 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].RowID != "tool-old" {
		t.Fatalf("updates=%+v", updates)
	}
	state, err = store.CatchUp(ctx, "s1", logPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	if state.IndexedThrough != 8 || len(state.Pending) != 0 {
		t.Fatalf("state=%+v", state)
	}
	tool, err := store.Detail(ctx, "s1", "tool-old")
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.Events) != 2 || tool.UpdatedSeq != 6 {
		t.Fatalf("tool=%+v", tool)
	}
}

// TestMeasureLargeLog records cold-index and warm-page costs when a real log is
// explicitly supplied. It is skipped in ordinary test runs.
func TestMeasureLargeLog(t *testing.T) {
	logPath := os.Getenv("YCC_SESSION_VIEW_BENCH_LOG")
	if logPath == "" {
		t.Skip("set YCC_SESSION_VIEW_BENCH_LOG to measure a real session")
	}
	store, err := Open(filepath.Join(t.TempDir(), "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sid := filepath.Base(filepath.Dir(logPath))
	started := time.Now()
	state, err := store.CatchUp(ctx, sid, logPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(started)
	started = time.Now()
	state, err = store.CatchUp(ctx, sid, logPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.Page(ctx, sid, 0, DefaultRows, DefaultBytes)
	if err != nil {
		t.Fatal(err)
	}
	warm := time.Since(started)
	response := &v1.GetSessionViewResponse{State: &v1.SessionViewState{IndexedThroughSeq: state.IndexedThrough}}
	for _, row := range rows {
		encodedRow := &v1.SessionPresentationRow{Id: row.ID, PositionSeq: row.PositionSeq, UpdatedSeq: row.UpdatedSeq, HasDetail: row.HasDetail}
		for _, ev := range row.Events {
			data, _ := json.Marshal(ev.Data)
			encodedRow.Events = append(encodedRow.Events, &v1.Event{Seq: int64(ev.Seq), Ts: ev.TS.Format(time.RFC3339Nano), Actor: ev.Actor, Type: string(ev.Type), DataJson: string(data)})
		}
		response.Rows = append(response.Rows, encodedRow)
	}
	encoded, err := proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("events=%d cold_index=%s warm_first_page=%s rows=%d protobuf_payload=%d bytes", state.IndexedThrough, cold, warm, len(rows), len(encoded))
}

func TestIndexRebuildsWhenAuthoritativeLogChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	writeLog(t, logPath, []event.Event{ev(1, event.UserInput, map[string]any{"text": "one"}), ev(2, event.ModelTurn, map[string]any{"text": "old"})})
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	// Same sequence count and nearly identical size: source metadata plus the
	// indexed boundary must prevent an old row masquerading as current.
	writeLog(t, logPath, []event.Event{ev(1, event.UserInput, map[string]any{"text": "one"}), ev(2, event.ModelTurn, map[string]any{"text": "new"})})
	if _, err = store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	row, err := store.Detail(ctx, "s", "seq-2")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.Events[0].Data["text"]; got != "new" {
		t.Fatalf("stale row text=%v", got)
	}
}

func TestIndexDetectsSameSizeReplacementWithChangedMiddle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	original := []event.Event{
		ev(1, event.UserInput, map[string]any{"text": strings.Repeat("f", 400)}),
		ev(2, event.ModelTurn, map[string]any{"text": "middle-old"}),
		ev(3, event.ModelTurn, map[string]any{"text": strings.Repeat("l", 400)}),
	}
	writeLog(t, logPath, original)
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]event.Event(nil), original...)
	changed[1] = ev(2, event.ModelTurn, map[string]any{"text": "middle-new"})
	// Rewrite the existing inode with the same byte count. The unchanged first
	// line and final 256-byte boundary leave mtime as the replacement evidence.
	writeLog(t, logPath, changed)
	after, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() {
		t.Fatal("test replacement must preserve size")
	}
	later := before.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(logPath, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	row, err := store.Detail(ctx, "s", "seq-2")
	if err != nil {
		t.Fatal(err)
	}
	if got := row.Events[0].Data["text"]; got != "middle-new" {
		t.Fatalf("replacement left stale middle row: %v", got)
	}
}

func TestOlderDurableBoundaryDoesNotRebuildAfterOrdinaryAppend(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	writeLog(t, logPath, []event.Event{ev(1, event.ModelTurn, map[string]any{"text": "first"})})
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	captured, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(ev(2, event.ModelTurn, map[string]any{"text": "second"}))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Make the mtime difference deterministic on coarse filesystems. It belongs to
	// the larger observation and must not be compared to the captured byte cap.
	later := captured.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(logPath, later, later); err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_row_rebuild BEFORE DELETE ON rows BEGIN SELECT RAISE(FAIL, 'unexpected rebuild'); END`); err != nil {
		t.Fatal(err)
	}

	state, err := store.CatchUpBounded(ctx, "s", logPath, 1, captured.Size())
	if err != nil || state.IndexedThrough != 1 {
		t.Fatalf("older boundary rebuilt the index: state=%+v err=%v", state, err)
	}
	state, err = store.CatchUpBounded(ctx, "s", logPath, 2, current.Size())
	if err != nil || state.IndexedThrough != 2 {
		t.Fatalf("newer boundary did not catch up incrementally: state=%+v err=%v", state, err)
	}
	for _, id := range []string{"seq-1", "seq-2"} {
		if _, err := store.Detail(ctx, "s", id); err != nil {
			t.Fatalf("detail %s: %v", id, err)
		}
	}
}

func TestCatchUpHonorsExplicitDurableBoundAndCompleteLines(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	writeLog(t, logPath, []event.Event{
		ev(1, event.ModelTurn, map[string]any{"text": "committed"}),
		ev(2, event.ModelTurn, map[string]any{"text": "visible but not committed"}),
	})
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.CatchUp(ctx, "s", logPath, 0)
	if err != nil || state.IndexedThrough != 0 {
		t.Fatalf("empty durable bound indexed file bytes: state=%+v err=%v", state, err)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	committedOffset := int64(strings.IndexByte(string(contents), '\n') + 1)
	state, err = store.CatchUpBounded(ctx, "s", logPath, 1, committedOffset)
	if err != nil || state.IndexedThrough != 1 {
		t.Fatalf("durable bound not honored: state=%+v err=%v", state, err)
	}
	if _, err := store.Detail(ctx, "s", "seq-2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("uncommitted row is indexed: %v", err)
	}

	partialPath := filepath.Join(dir, "partial.jsonl")
	line, _ := json.Marshal(ev(1, event.ModelTurn, map[string]any{"text": "partial"}))
	if err := os.WriteFile(partialPath, line, 0o600); err != nil {
		t.Fatal(err)
	}
	partial, err := Open(filepath.Join(dir, "partial.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer partial.Close()
	state, err = partial.CatchUp(ctx, "partial", partialPath, -1)
	if err != nil || state.IndexedThrough != 0 {
		t.Fatalf("unterminated record was indexed: state=%+v err=%v", state, err)
	}
	f, err := os.OpenFile(partialPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	state, err = partial.CatchUp(ctx, "partial", partialPath, -1)
	if err != nil || state.IndexedThrough != 1 {
		t.Fatalf("completed record was not indexed: state=%+v err=%v", state, err)
	}
}

func TestChangesSinceCoalescesSharedAheadIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	writeLog(t, logPath, []event.Event{
		ev(1, event.ModelTurn, map[string]any{"text": "first"}),
		ev(2, event.ModelTurn, map[string]any{"text": "last"}),
		ev(3, event.SessionIdle, map[string]any{"report": "last"}),
	})
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CatchUp(ctx, "s", logPath, -1); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	oldBoundary := int64(strings.IndexByte(string(contents), '\n') + 1)
	snapshot, snapshotRows, _, err := store.View(ctx, "s", logPath, 1, oldBoundary, 20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IndexedThrough != 3 || slices.ContainsFunc(snapshotRows, func(row Row) bool { return row.ID == "seq-2" }) {
		t.Fatalf("snapshot state/rows disagree at shared watermark: state=%+v rows=%+v", snapshot, snapshotRows)
	}
	state, rows, deleted, err := store.ChangesSince(ctx, "s", logPath, 1, oldBoundary, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.IndexedThrough != 3 {
		t.Fatalf("advertised cursor=%d, want shared watermark 3", state.IndexedThrough)
	}
	if !slices.Contains(deleted, "seq-2") {
		t.Fatalf("coalesced changes missed old-row deletion: deleted=%v rows=%v", deleted, rows)
	}
	for _, row := range rows {
		if row.UpdatedSeq > state.IndexedThrough {
			t.Fatalf("row version %d outruns snapshot %d", row.UpdatedSeq, state.IndexedThrough)
		}
	}

	// Two streams can observe the same already-ahead shared index independently;
	// neither may receive only its triggering event while advertising seq 3.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _, tombstones, err := store.ChangesSince(ctx, "s", logPath, 1, oldBoundary, 0)
			if err == nil && (st.IndexedThrough != 3 || !slices.Contains(tombstones, "seq-2")) {
				err = fmt.Errorf("incomplete shared handoff: state=%+v tombstones=%v", st, tombstones)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenMakesTranscriptIndexPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide Unix permission semantics")
	}
	dir := filepath.Join(t.TempDir(), ".ycc")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-view.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, check := range []struct {
		path string
		want os.FileMode
	}{{dir, 0o700}, {path, 0o600}} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.want {
			t.Fatalf("mode %s=%#o, want %#o", check.path, got, check.want)
		}
	}
	companions, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, companion := range companions {
		info, err := os.Stat(companion)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("SQLite companion %s mode=%#o is not private", companion, info.Mode().Perm())
		}
	}
}
