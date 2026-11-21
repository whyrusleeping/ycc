package sessionview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

func openSearch(t *testing.T, path string) *SearchIndex {
	t.Helper()
	x, err := OpenSearch(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func searchRows(t *testing.T, x *SearchIndex, query string) map[string]SearchHit {
	t.Helper()
	hits, _, err := x.Search(context.Background(), query, 0, 100)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	out := map[string]SearchHit{}
	for _, h := range hits {
		out[h.SessionID] = h
	}
	return out
}

func TestSearchIndexesConversationOnPresentationRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	sub := ev(6, event.ModelTurn, map[string]any{"text": "implementer mentions zebra"})
	sub.Actor = "implementer"
	events := []event.Event{
		ev(1, event.UserInput, map[string]any{"text": "please find the walrus"}),
		ev(2, event.Thinking, map[string]any{"text": "thinking about narwhal"}),
		ev(3, event.ToolCall, map[string]any{"id": "c1", "name": "Bash", "args": `{"command":"grep pelican"}`}),
		ev(4, event.ToolResult, map[string]any{"id": "c1", "result": "pelican.go:1: flamingo"}),
		ev(5, event.ModelTurn, map[string]any{"text": "The walrus lives in the arctic."}),
		sub,
		ev(7, event.QuestionAsked, map[string]any{"question": "Which ocean?", "options": []any{"Atlantic", "Pacific"}}),
		ev(8, event.QuestionAnswered, map[string]any{"answer": "Pacific please"}),
		ev(9, event.QuestionAsked, map[string]any{"question": "Proceed with octopus?", "auto": true}),
		ev(10, event.QuestionAnswered, map[string]any{"answer": "assume squid", "auto": true}),
	}
	writeLog(t, path, events)
	x := openSearch(t, filepath.Join(dir, "search.sqlite"))
	if err := x.IndexSession(ctx, "s", path, -1, -1); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "view.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CatchUp(ctx, "s", path, -1); err != nil {
		t.Fatal(err)
	}

	for query, want := range map[string]struct{ row, kind string }{
		"walrus arctic": {"seq-5", "assistant"},
		"find walr":     {"seq-1", "user"},
		"ocean":         {"seq-7", "question"},
		"pacific pl":    {"seq-7", "answer"},
		"octopus":       {"seq-9", "question"},
	} {
		hit, ok := searchRows(t, x, query)["s"]
		if !ok || hit.RowID != want.row || hit.Kind != want.kind {
			t.Fatalf("%q: hit %+v, want row %s kind %s", query, hit, want.row, want.kind)
		}
		if _, err := store.Summary(ctx, "s", hit.RowID); err != nil {
			t.Fatalf("%q: hit row %s is not a presentation row: %v", query, hit.RowID, err)
		}
	}
	for _, query := range []string{"narwhal", "pelican", "flamingo", "zebra", "squid"} {
		if hits := searchRows(t, x, query); len(hits) != 0 {
			t.Fatalf("%q must not be indexed: %+v", query, hits)
		}
	}
	hit := searchRows(t, x, "walrus")["s"]
	if hit.Matches != 2 {
		t.Fatalf("walrus matches = %d, want 2", hit.Matches)
	}
	var marked string
	for _, span := range hit.Snippet {
		if span.Match {
			marked += span.Text
		}
	}
	if marked != "walrus" {
		t.Fatalf("snippet %+v: highlighted %q, want walrus", hit.Snippet, marked)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(filepath.Join(dir, "search.sqlite"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("search.sqlite%s mode %v, want owner-only", suffix, info.Mode().Perm())
		}
	}
	// FTS5 syntax in user input is literal, not a query error.
	if _, _, err := x.Search(ctx, `walrus" OR (NEAR -* "`, 0, 10); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFollowsAppendsReportsAndRewrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	dbPath := filepath.Join(dir, "search.sqlite")
	events := []event.Event{ev(1, event.UserInput, map[string]any{"text": "start with alpha"})}
	writeLog(t, path, events)
	x := openSearch(t, dbPath)
	index := func() {
		t.Helper()
		if err := x.IndexSession(ctx, "s", path, -1, -1); err != nil {
			t.Fatal(err)
		}
	}
	index()

	// A live bound excludes events past the durable sequence.
	events = append(events,
		ev(2, event.ModelTurn, map[string]any{"text": "bravo done"}),
		ev(3, event.SessionIdle, map[string]any{"report": "bravo done\ncharlie follows"}))
	writeLog(t, path, events)
	if err := x.IndexSession(ctx, "s", path, 1, -1); err != nil {
		t.Fatal(err)
	}
	if len(searchRows(t, x, "bravo")) != 0 {
		t.Fatal("indexed past the durable bound")
	}
	index()
	// The idle report replaces the final turn's row, as in the presentation index.
	for _, query := range []string{"bravo", "charlie"} {
		if hit := searchRows(t, x, query)["s"]; hit.RowID != "seq-3" || hit.Matches != 1 {
			t.Fatalf("%q: %+v, want the report row seq-3 once", query, hit)
		}
	}

	// A rewritten log drops what it no longer says.
	events = []event.Event{ev(1, event.UserInput, map[string]any{"text": "delta only"})}
	writeLog(t, path, events)
	index()
	if len(searchRows(t, x, "alpha")) != 0 || len(searchRows(t, x, "delta")) != 1 {
		t.Fatal("rewritten log was not reindexed")
	}

	// A schema change rebuilds from the logs.
	if _, err := x.db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	x.Close()
	x = openSearch(t, dbPath)
	if len(searchRows(t, x, "delta")) != 0 {
		t.Fatal("schema change kept old documents")
	}
	index()
	if len(searchRows(t, x, "delta")) != 1 {
		t.Fatal("rebuild after schema change found nothing")
	}

	if err := x.Prune(ctx, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if len(searchRows(t, x, "delta")) != 0 {
		t.Fatal("pruned session still matches")
	}
}

func TestSearchRanksOneHitPerSessionAndPages(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	x := openSearch(t, filepath.Join(dir, "search.sqlite"))
	for i, text := range []string{"kiwi", "kiwi kiwi kiwi", "kiwi and mango"} {
		id := string(rune('a' + i))
		path := filepath.Join(dir, id, "events.jsonl")
		writeLog(t, path, []event.Event{
			ev(1, event.UserInput, map[string]any{"text": text}),
			ev(2, event.ModelTurn, map[string]any{"text": "noted " + text}),
		})
		if err := x.IndexSession(ctx, id, path, -1, -1); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cursor := 0
	for {
		hits, more, err := x.Search(ctx, "kiwi", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			seen = append(seen, h.SessionID)
			if h.Matches != 2 {
				t.Fatalf("%s matches = %d, want 2", h.SessionID, h.Matches)
			}
		}
		if !more {
			break
		}
		cursor += len(hits)
	}
	if len(seen) != 3 || seen[0] != "b" {
		t.Fatalf("sessions %v: want three, densest match (b) first", seen)
	}
	if hits := searchRows(t, x, "kiwi mango"); len(hits) != 1 || hits["c"].SessionID != "c" {
		t.Fatalf("all terms are required: %+v", hits)
	}
}

// A catch-up transaction in progress does not hold up readers.
func TestSearchReadsDuringOpenWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	writeLog(t, path, []event.Event{ev(1, event.UserInput, map[string]any{"text": "lychee"})})
	x := openSearch(t, filepath.Join(dir, "search.sqlite"))
	if err := x.IndexSession(ctx, "s", path, -1, -1); err != nil {
		t.Fatal(err)
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO docs(session_id,row_id,seq,kind,actor,ts_unix_ns,body) VALUES('t','seq-1',1,'user','',0,'lychee')`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	hits, _, err := x.Search(readCtx, "lychee", 0, 10)
	if err != nil || len(hits) != 1 || hits[0].SessionID != "s" {
		t.Fatalf("search during write: %+v, %v; want the committed hit only", hits, err)
	}
}
