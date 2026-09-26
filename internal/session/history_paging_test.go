package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/event"
	v1 "github.com/whyrusleeping/ycc/proto/ycc/v1"
	"google.golang.org/protobuf/proto"
)

func TestSessionHistoryPages(t *testing.T) {
	ws := t.TempDir()
	// Tied last-activity times exercise the secondary timestamp and ID keys.
	for i := 0; i < 6; i++ {
		writeSession(t, ws, fmt.Sprintf("s_%02d", i), []event.Event{
			{Seq: 1, TS: ts(i%2 + 1), Type: event.SessionStarted},
			{Seq: 2, TS: ts(10), Type: event.ModelTurn},
		})
	}
	m := NewManager(config.NewRegistry(nil), ws)
	full, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, 2, 5, 6, 7, 1000} {
		var ids []string
		cursor := ""
		for {
			page, pinned, next, err := m.ListSessionHistoryPage("", limit, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(pinned) != 0 {
				t.Fatalf("unexpected pinned: %+v", pinned)
			}
			if len(page) > limit || len(page) > 500 {
				t.Fatalf("oversized page: %d", len(page))
			}
			for _, row := range page {
				ids = append(ids, row.ID)
			}
			if next == "" {
				break
			}
			if next == cursor {
				t.Fatal("cursor did not advance")
			}
			cursor = next
		}
		if len(ids) != len(full) {
			t.Fatalf("limit %d: got %v", limit, ids)
		}
		for i, row := range full {
			if ids[i] != row.ID {
				t.Fatalf("limit %d at %d: %v", limit, i, ids)
			}
		}
	}
	legacy, pinned, cursor, err := m.ListSessionHistoryPage("", 0, "")
	if err != nil || len(legacy) != 6 || len(pinned) != 0 || cursor != "" {
		t.Fatalf("legacy: %d %d %q %v", len(legacy), len(pinned), cursor, err)
	}
	for _, bad := range []string{"%%%", "W10", "WyJiYWQiLCJiYWQiLCJpZCJd"} {
		_, _, _, err := m.ListSessionHistoryPage("", 2, bad)
		if !errors.Is(err, ErrInvalidHistoryCursor) {
			t.Fatalf("cursor %q: %v", bad, err)
		}
	}
}

func TestSessionHistoryWirePrecisionOrderAndCursor(t *testing.T) {
	ws := t.TempDir()
	base := ts(10)
	for _, tc := range []struct {
		id              string
		activity, start time.Time
	}{
		{"s_a", base.Add(100 * time.Microsecond), ts(1).Add(100 * time.Microsecond)},
		{"s_b", base.Add(900 * time.Microsecond), ts(1).Add(900 * time.Microsecond)},
		{"s_c", base.Add(time.Millisecond), ts(1)},
		{"s_d", ts(9), ts(1)},
	} {
		writeSession(t, ws, tc.id, []event.Event{
			{Seq: 1, TS: tc.start, Type: event.SessionStarted},
			{Seq: 2, TS: tc.activity, Type: event.ModelTurn},
		})
	}
	m := NewManager(config.NewRegistry(nil), ws)
	want := []string{"s_c", "s_a", "s_b", "s_d"}
	full, err := m.ListSessionHistory("")
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range full {
		if row.ID != want[i] {
			t.Fatalf("full order at %d: %s, want %s", i, row.ID, want[i])
		}
	}
	cursor := ""
	for _, id := range want {
		page, _, next, err := m.ListSessionHistoryPage("", 1, cursor)
		if err != nil || len(page) != 1 || page[0].ID != id {
			t.Fatalf("page after %q: %+v %v", cursor, page, err)
		}
		if id == "s_a" {
			encoded, err := base64.RawURLEncoding.DecodeString(next)
			if err != nil {
				t.Fatal(err)
			}
			var key []string
			if err := json.Unmarshal(encoded, &key); err != nil {
				t.Fatal(err)
			}
			if key[0] != "2026-01-01T00:00:10.000Z" || key[1] != "2026-01-01T00:00:01.000Z" {
				t.Fatalf("cursor not wire-precision: %v", key)
			}
		}
		cursor = next
	}
	if cursor != "" {
		t.Fatalf("expected final cursor to be empty: %q", cursor)
	}
}

func TestSessionHistoryPinnedAndEmpty(t *testing.T) {
	ws := t.TempDir()
	m := NewManager(config.NewRegistry(nil), ws)
	rows, pinned, next, err := m.ListSessionHistoryPage("", 2, "")
	if err != nil || len(rows) != 0 || len(pinned) != 0 || next != "" {
		t.Fatalf("empty: %+v %+v %q %v", rows, pinned, next, err)
	}
	// Use a live session whose on-disk snapshot sorts older than the first page.
	for i := 0; i < 3; i++ {
		writeSession(t, ws, fmt.Sprintf("s_%02d", i), []event.Event{{Seq: 1, TS: ts(i + 1), Type: event.SessionStarted}})
	}
	m.sessions["s_00"] = &Session{ID: "s_00", Workspace: filepath.Clean(ws), Mode: "work", status: event.StatusRunning}
	first, pinned, next, err := m.ListSessionHistoryPage("", 1, "")
	if err != nil || first[0].ID != "s_02" || len(pinned) != 1 || pinned[0].ID != "s_00" || !pinned[0].Live {
		t.Fatalf("first page: %+v %+v %v", first, pinned, err)
	}
	second, pinned, _, err := m.ListSessionHistoryPage("", 1, next)
	if err != nil || second[0].ID != "s_01" || len(pinned) != 0 {
		t.Fatalf("second page: %+v %+v %v", second, pinned, err)
	}
	// Once the live row itself is within the page it is not duplicated in pinned.
	first, pinned, _, err = m.ListSessionHistoryPage("", 3, "")
	if err != nil || len(first) != 3 || len(pinned) != 0 {
		t.Fatalf("all rows: %+v %+v %v", first, pinned, err)
	}
}

func TestSessionHistoryLimitClamp(t *testing.T) {
	ws := t.TempDir()
	for i := 0; i < 501; i++ {
		writeSession(t, ws, fmt.Sprintf("s_%04d", i), []event.Event{{Seq: 1, TS: ts(i), Type: event.SessionStarted}})
	}
	m := NewManager(config.NewRegistry(nil), ws)
	page, _, next, err := m.ListSessionHistoryPage("", 1000, "")
	if err != nil || len(page) != 500 || next == "" {
		t.Fatalf("clamp: %d %q %v", len(page), next, err)
	}
	last, _, cursor, err := m.ListSessionHistoryPage("", 1000, next)
	if err != nil || len(last) != 1 || cursor != "" {
		t.Fatalf("last: %d %q %v", len(last), cursor, err)
	}
}

func BenchmarkSessionHistoryPages(b *testing.B) {
	ws := b.TempDir()
	for i := 0; i < 1000; i++ {
		writeSession(b, ws, fmt.Sprintf("s_%04d", i), []event.Event{{Seq: 1, TS: ts(i), Type: event.SessionStarted}, {Seq: 2, TS: ts(i + 1), Type: event.ModelTurn}})
	}
	m := NewManager(config.NewRegistry(nil), ws)
	for _, limit := range []int{0, 50} {
		b.Run(fmt.Sprintf("limit_%d", limit), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, pinned, next, err := m.ListSessionHistoryPage("", limit, "")
				if err != nil {
					b.Fatal(err)
				}
				msg := &v1.ListSessionHistoryResponse{NextCursor: next}
				for _, row := range rows {
					msg.Sessions = append(msg.Sessions, &v1.SessionSummary{SessionId: row.ID, Mode: row.Mode, Status: string(row.Status), Workspace: row.Workspace, Title: row.Title, StartedAt: row.StartedAt.Format("2006-01-02T15:04:05Z07:00"), LastActivity: row.LastActivity.Format("2006-01-02T15:04:05Z07:00")})
				}
				for _, row := range pinned {
					msg.Pinned = append(msg.Pinned, &v1.SessionSummary{SessionId: row.ID})
				}
				b.ReportMetric(float64(proto.Size(msg)), "wire_bytes/op")
			}
		})
	}
}
