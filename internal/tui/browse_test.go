package tui

import (
	"testing"
)

// TestBrowserNav exercises the shared list+detail component's cursor navigation:
// up/down move with clamping at both bounds and clamp() repairs an out-of-range
// cursor after the row set shrinks.
func TestBrowserNav(t *testing.T) {
	b := browser{rows: []browserRow{{text: "a"}, {text: "b"}, {text: "c"}}}
	b.up() // already at top: no-op
	if b.cursor != 0 {
		t.Fatalf("up at top: cursor=%d, want 0", b.cursor)
	}
	b.down()
	b.down()
	if b.cursor != 2 {
		t.Fatalf("two downs: cursor=%d, want 2", b.cursor)
	}
	b.down() // at bottom: clamped
	if b.cursor != 2 {
		t.Fatalf("down at bottom: cursor=%d, want 2", b.cursor)
	}
	// Shrink the row set out from under the cursor; clamp repairs it.
	b.rows = b.rows[:1]
	b.clamp()
	if b.cursor != 0 {
		t.Fatalf("clamp after shrink: cursor=%d, want 0", b.cursor)
	}
	// Empty list clamps to 0 (never negative).
	b.rows = nil
	b.clamp()
	if b.cursor != 0 {
		t.Fatalf("clamp on empty: cursor=%d, want 0", b.cursor)
	}
}

func TestListWindow(t *testing.T) {
	// n<=size: no clipping.
	if s, e := listWindow(0, 3, 5); s != 0 || e != 3 {
		t.Fatalf("n<=size: got (%d,%d), want (0,3)", s, e)
	}
	// size<=0: no clipping.
	if s, e := listWindow(2, 10, 0); s != 0 || e != 10 {
		t.Fatalf("size<=0: got (%d,%d), want (0,10)", s, e)
	}
	// cursor at top → start 0.
	if s, e := listWindow(0, 30, 10); s != 0 || e != 10 {
		t.Fatalf("cursor top: got (%d,%d), want (0,10)", s, e)
	}
	// cursor in middle → cursor within window and centered.
	s, e := listWindow(15, 30, 10)
	if !(s <= 15 && 15 < e) {
		t.Fatalf("cursor middle: 15 not in [%d,%d)", s, e)
	}
	if e-s != 10 {
		t.Fatalf("cursor middle: window len=%d, want 10", e-s)
	}
	if s != 15-10/2 {
		t.Fatalf("cursor middle: start=%d, want %d (centered)", s, 15-10/2)
	}
	// cursor at last index → end==n and last visible.
	s, e = listWindow(29, 30, 10)
	if e != 30 {
		t.Fatalf("cursor last: end=%d, want 30", e)
	}
	if !(s <= 29 && 29 < e) {
		t.Fatalf("cursor last: 29 not in [%d,%d)", s, e)
	}
	if e-s != 10 {
		t.Fatalf("cursor last: window len=%d, want 10", e-s)
	}
}
