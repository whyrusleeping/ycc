package workspacelease

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Line attribution for shared bookkeeping files (backlog task files and
// memory.md). Every session writes these, often the same file at once, so
// path claims are too coarse: a commit would carry another session's
// unfinished task edits or notes. Instead each scoped write is recorded as a
// line-level change against the file's committed content, and a session's
// commit takes HEAD plus only its own (and unattributed) line changes.
//
// A file is a sequence of line items anchored at its HEAD blob. An item is
// either committed content (By "") or inserted by a scope's write; a deleted
// item stays as a tombstone naming the deleting scope, so other scopes' views
// can keep it. Changes nobody recorded (shell edits, client RPC writes, a
// hand edit) are attributed to unattributedScope and ride along with any
// scope's view, as do changes by scopes that are no longer live.

const unattributedScope = "?"

type lineItem struct {
	ID   int    `json:"id"`
	Text string `json:"t"`
	By   string `json:"b,omitempty"`  // inserting scope; "" for committed content
	Ins  int    `json:"i,omitempty"`  // inserting hunk
	Del  string `json:"d,omitempty"`  // deleting scope; "" while live
	DelH int    `json:"dh,omitempty"` // deleting hunk
}

type lineFile struct {
	Head string `json:"head"` // HEAD blob the items are anchored at; "" when absent from HEAD
	// Created is the hunk (by CreatedBy) that created a file absent from HEAD.
	Created   int        `json:"created,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	Next      int        `json:"next"`
	Items     []lineItem `json:"items"`
	// Broken is set when a write could not be diffed reliably; the file's
	// line attribution is refused until it matches HEAD again.
	Broken bool `json:"broken,omitempty"`
	// LastID numbers items; Views keeps the most recent versions handed out
	// for commits, so the one that lands is folded exactly even after later
	// writes changed the scopes' current versions.
	LastID int         `json:"last_id"`
	Views  []savedView `json:"views,omitempty"`
}

// savedView is a scope's version by item IDs: the items it holds, and the
// items it inserted and deleted (gone from it and from the worktree).
type savedView struct {
	Scope   string `json:"scope"`
	Include []int  `json:"include"`
	Dropped []int  `json:"dropped,omitempty"`
}

// maxSavedViews bounds Views per scope and file. Only a scope's own review and
// commit inspections replace its views, so the one its commit publishes stays.
const maxSavedViews = 4

const linesRecordVersion = 1

type linesRecord struct {
	Version int                  `json:"version"`
	Files   map[string]*lineFile `json:"files"`
}

// SetLiveScopes installs the test for whether a scope (session) is still
// live. Line changes by scopes that are not live count as unattributed, so a
// finished session's leftover bookkeeping rides along with the next commit of
// that file instead of staying stranded. Without it every scope is live.
func (s *Service) SetLiveScopes(fn func(scope string) bool) {
	if s == nil {
		return
	}
	s.linesMu.Lock()
	defer s.linesMu.Unlock()
	s.live = fn
}

// Live reports whether scope is still live (SetLiveScopes); every scope is
// live without that test.
func (s *Service) Live(scope string) bool {
	if s == nil {
		return false
	}
	s.linesMu.Lock()
	fn := s.live
	s.linesMu.Unlock()
	return fn == nil || fn(scope)
}

// RecordLines attributes a write of the bookkeeping file at path, from before
// to after, to token's scope. Any difference between the recorded content and
// before (a write nobody recorded) is attributed as unattributed first. The
// caller holds the lock that serializes writes of path (the docs store lock).
// Unscoped tokens and paths outside a worktree are ignored.
func (s *Service) RecordLines(path, fallback string, token *Token, before, after []byte) {
	if s == nil || token == nil || token.scope == "" {
		return
	}
	key, rel, ok := relativeToWorktree(path, fallback)
	if !ok {
		return
	}
	s.linesMu.Lock()
	defer s.linesMu.Unlock()
	files := s.linesLocked(key)
	head := newHeadState(key, "HEAD", rel)
	f := files[rel]
	if f == nil {
		f = newLineFile(head.blob, head.text(string(before)))
	}
	f.foldHead(head, s.live)
	f.apply(unattributedScope, string(before))
	f.apply(token.scope, string(after))
	if head.same(f.liveText()) {
		delete(files, rel)
	} else {
		files[rel] = f
	}
	s.saveLinesLocked(key)
}

// LineView returns scope's version of the bookkeeping file rel (repository
// relative) in the worktree containing root: the content at commit head with
// only scope's and unattributed line changes applied, and whether that version
// exists as a file. A scope that deleted or replaced another scope's inserted
// lines takes that change along, as does an edit of a file another scope
// created, so the version stays well-formed. mine reports whether scope
// changed the file. ok is false when no live scope's change is recorded for
// it; the caller then applies its whole-file rules. head must still be the
// worktree's HEAD: a view of an older commit would re-anchor the record
// backwards, so it is refused. record keeps the version for reconciling a
// commit of it (inspections that may be committed set it; read-only ones
// do not).
func (s *Service) LineView(root, head, rel, scope string, record bool) (content []byte, exists, mine, ok bool, err error) {
	if s == nil || scope == "" {
		return nil, false, false, false, nil
	}
	key, err := Canonical(root)
	if err != nil {
		return nil, false, false, false, nil
	}
	s.linesMu.Lock()
	defer s.linesMu.Unlock()
	files := s.linesLocked(key)
	f := files[rel]
	if f == nil {
		return nil, false, false, false, nil
	}
	out, err := exec.Command("git", "-C", key, "rev-parse", "--verify", "--quiet", "HEAD").Output()
	if current := strings.TrimSpace(string(out)); err != nil || current != head {
		return nil, false, false, false, fmt.Errorf("HEAD moved from %s while inspecting %s; inspect again", head, rel)
	}
	before := f.snapshot()
	state := newHeadState(key, head, rel)
	f.foldHead(state, s.live)
	current, err := os.ReadFile(filepath.Join(key, filepath.FromSlash(rel)))
	if err != nil || state.same(string(current)) {
		// Deleted, or every change was undone or committed: nothing to attribute.
		delete(files, rel)
		s.saveLinesLocked(key)
		return nil, false, false, false, nil
	}
	f.apply(unattributedScope, string(current))
	if f.snapshot() != before {
		s.saveLinesLocked(key)
	}
	include, dropped, mine, others := f.visibility(scope, s.live)
	if !mine && !others {
		return nil, false, false, false, nil
	}
	if !mine {
		return nil, false, false, true, nil
	}
	if f.Broken {
		return nil, false, false, false, fmt.Errorf("%s: line attribution failed for a change there; name it in paths to commit it whole, or wait until it is committed", rel)
	}
	if record {
		f.saveView(scope, include, dropped)
		s.saveLinesLocked(key)
	}
	text := f.text(include)
	return []byte(text), state.blob != "" || text != "", mine, true, nil
}

func (f *lineFile) saveView(scope string, include, dropped []bool) {
	v := savedView{Scope: scope}
	for i, it := range f.Items {
		if include[i] {
			v.Include = append(v.Include, it.ID)
		} else if dropped[i] {
			v.Dropped = append(v.Dropped, it.ID)
		}
	}
	// Keep the scope's newest earlier views (other than this one) and every
	// other scope's.
	views, kept := f.Views[:0:0], 0
	for i := len(f.Views) - 1; i >= 0; i-- {
		old := f.Views[i]
		if old.Scope == scope {
			if kept == maxSavedViews-1 || slices.Equal(old.Include, v.Include) && slices.Equal(old.Dropped, v.Dropped) {
				continue
			}
			kept++
		}
		views = append(views, old)
	}
	slices.Reverse(views)
	f.Views = append(views, v)
}

func newLineFile(head, headText string) *lineFile {
	f := &lineFile{Head: head}
	for _, line := range splitLines(headText) {
		f.LastID++
		f.Items = append(f.Items, lineItem{ID: f.LastID, Text: line})
	}
	return f
}

func (f *lineFile) snapshot() string {
	data, _ := json.Marshal(f)
	return string(data)
}

func (f *lineFile) liveText() string {
	var b strings.Builder
	for _, it := range f.Items {
		if it.Del == "" {
			b.WriteString(it.Text)
		}
	}
	return b.String()
}

func (f *lineFile) text(include []bool) string {
	var b strings.Builder
	for i, it := range f.Items {
		if include[i] {
			b.WriteString(it.Text)
		}
	}
	return b.String()
}

// apply records the change from the current live content to text as scope's
// hunks: deleted lines become tombstones and inserted lines are placed just
// before the next live line (after any tombstones).
func (f *lineFile) apply(scope, text string) {
	var live []int
	var a []string
	for i, it := range f.Items {
		if it.Del == "" {
			live = append(live, i)
			a = append(a, it.Text)
		}
	}
	b := splitLines(text)
	hunks, ok := diffLines(a, b)
	if !ok {
		f.Broken = true
	}
	if len(hunks) == 0 {
		return
	}
	creating := f.Head == "" && f.Created == 0 && len(a) == 0
	before := map[int][]lineItem{}
	var tail []lineItem
	for _, h := range hunks {
		f.Next++
		id := f.Next
		if creating {
			f.Created, f.CreatedBy, creating = id, scope, false
		}
		for i := h.a0; i < h.a1; i++ {
			f.Items[live[i]].Del, f.Items[live[i]].DelH = scope, id
		}
		var ins []lineItem
		for _, line := range b[h.b0:h.b1] {
			f.LastID++
			ins = append(ins, lineItem{ID: f.LastID, Text: line, By: scope, Ins: id})
		}
		if h.a1 < len(live) {
			before[live[h.a1]] = append(before[live[h.a1]], ins...)
		} else {
			tail = append(tail, ins...)
		}
	}
	items := make([]lineItem, 0, len(f.Items)+len(b))
	for i, it := range f.Items {
		items = append(items, before[i]...)
		items = append(items, it)
	}
	f.Items = append(items, tail...)
}

// mask selects the items in scope's version. Scopes that are not live (per
// isLive) are treated as unattributed.
func (f *lineFile) mask(scope string, isLive func(string) bool) (include []bool, mine, others bool) {
	include, _, mine, others = f.visibility(scope, isLive)
	return include, mine, others
}

// visibility is mask plus, per item, whether scope's version takes its
// deletion (dropped: inserted and deleted within that version).
func (f *lineFile) visibility(scope string, isLive func(string) bool) (include, dropped []bool, mine, others bool) {
	foreign := func(by string) bool {
		return by != "" && by != scope && by != unattributedScope && (isLive == nil || isLive(by))
	}
	for _, it := range f.Items {
		if it.By == scope || it.Del == scope {
			mine = true
		}
		if foreign(it.By) || foreign(it.Del) {
			others = true
		}
	}
	// Adopt the foreign hunks this scope's changes build on: one that created
	// the file, and (transitively) any whose inserted lines an adopted or own
	// hunk deleted, so a replaced line never appears twice.
	adopted := map[int]bool{}
	if mine && f.Created != 0 && foreign(f.CreatedBy) {
		adopted[f.Created] = true
	}
	for changed := true; changed; {
		changed = false
		for _, it := range f.Items {
			if !foreign(it.By) || adopted[it.Ins] {
				continue
			}
			if it.Del == scope || (it.Del != "" && adopted[it.DelH]) {
				adopted[it.Ins], changed = true, true
			}
		}
	}
	// A list item, indented or blank line this scope inserted under a heading
	// or key line another scope inserted (looking back over such lines) keeps
	// that parent line, and the line of the same key its hunk replaced,
	// without the rest of that hunk: a note stays in its section, a list item
	// under its key.
	type hunkKey struct {
		hunk int
		key  string
	}
	parent, replaced := map[int]bool{}, map[hunkKey]bool{}
	for i, it := range f.Items {
		if it.By != scope || it.Del == scope || !childLine(it.Text) {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			p := f.Items[j]
			if p.Del != "" || childLine(p.Text) {
				continue
			}
			if foreign(p.By) && !adopted[p.Ins] {
				parent[j], replaced[hunkKey{p.Ins, lineKey(p.Text)}] = true, true
			}
			break
		}
	}
	include, dropped = make([]bool, len(f.Items)), make([]bool, len(f.Items))
	for i, it := range f.Items {
		inserted := !foreign(it.By) || adopted[it.Ins] || parent[i]
		deleted := it.Del != "" && (!foreign(it.Del) || adopted[it.DelH] || replaced[hunkKey{it.DelH, lineKey(it.Text)}])
		include[i] = inserted && !deleted
		dropped[i] = inserted && deleted
	}
	return include, dropped, mine, others
}

// childLine reports lines that sit under a heading or key rather than start
// one: blank, indented, or list items.
func childLine(text string) bool {
	line := strings.TrimRight(text, "\r\n")
	return strings.TrimSpace(line) == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") ||
		strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")
}

// lineKey identifies what a heading or key line names: a heading's text, or a
// "key: value" line's key.
func lineKey(text string) string {
	line := strings.TrimSpace(text)
	if !strings.HasPrefix(line, "#") {
		if key, _, found := strings.Cut(line, ":"); found {
			return key
		}
	}
	return line
}

// foldHead re-anchors f when the file's HEAD blob changed. A commit that took
// exactly one scope's version (or the whole live content) folds those lines
// into committed content and keeps every other scope's pending changes
// attributed; any other HEAD change re-anchors with the remaining differences
// unattributed.
func (f *lineFile) foldHead(head *headState, isLive func(string) bool) {
	if f.Head == head.blob {
		return
	}
	defer func() {
		f.Head = head.blob
		if head.blob != "" {
			f.Created, f.CreatedBy = 0, ""
		}
	}()
	scopes := map[string]bool{}
	for _, it := range f.Items {
		for _, by := range []string{it.By, it.Del} {
			if by != "" && by != unattributedScope {
				scopes[by] = true
			}
		}
	}
	names := make([]string, 0, len(scopes))
	for scope := range scopes {
		names = append(names, scope)
	}
	sort.Strings(names)
	type candidate struct{ include, dropped []bool }
	var candidates []candidate
	for i := len(f.Views) - 1; i >= 0; i-- {
		in, out := map[int]bool{}, map[int]bool{}
		for _, id := range f.Views[i].Include {
			in[id] = true
		}
		for _, id := range f.Views[i].Dropped {
			out[id] = true
		}
		c := candidate{make([]bool, len(f.Items)), make([]bool, len(f.Items))}
		for j, it := range f.Items {
			c.include[j], c.dropped[j] = in[it.ID], out[it.ID]
		}
		candidates = append(candidates, c)
	}
	for _, scope := range names {
		for _, isLive := range []func(string) bool{isLive, nil} {
			include, dropped, _, _ := f.visibility(scope, isLive)
			candidates = append(candidates, candidate{include, dropped})
		}
	}
	whole := candidate{make([]bool, len(f.Items)), make([]bool, len(f.Items))}
	for i, it := range f.Items {
		whole.include[i] = it.Del == ""
		whole.dropped[i] = it.Del != ""
	}
	candidates = append(candidates, whole)
	for _, c := range candidates {
		if !head.same(f.text(c.include)) {
			continue
		}
		// Lines the commit took become committed content; lines it inserted
		// and deleted are gone. Every other item (another scope's pending
		// insertion, live or already replaced) keeps its attribution.
		items := f.Items[:0:0]
		for i, it := range f.Items {
			switch {
			case c.include[i]:
				it.By, it.Ins = "", 0
				items = append(items, it)
			case !c.dropped[i]:
				items = append(items, it)
			}
		}
		f.Items = items
		f.Views = nil
		return
	}
	live := f.liveText()
	*f = *newLineFile(head.blob, head.text(live))
	f.apply(unattributedScope, live)
}

type lineHunk struct{ a0, a1, b0, b1 int }

// maxDiffEdits bounds the edit distance diffLines searches for itself (its
// trace grows with its square); larger differences go to git's diff.
var maxDiffEdits = 2000

// diffLines returns the hunks that turn a into b: Myers' shortest edit script
// over the lines between their common prefix and suffix.
func diffLines(a, b []string) ([]lineHunk, bool) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	q := 0
	for q < len(a)-p && q < len(b)-p && a[len(a)-1-q] == b[len(b)-1-q] {
		q++
	}
	am, bm := a[p:len(a)-q], b[p:len(b)-q]
	if len(am) == 0 && len(bm) == 0 {
		return nil, true
	}
	whole := []lineHunk{{p, p + len(am), p, p + len(bm)}}
	if len(am) == 0 || len(bm) == 0 {
		return whole, true
	}
	n, m := len(am), len(bm)
	offset := n + m + 1
	v := make([]int, 2*offset+1)
	// trace[d] holds v[-d..d] as it was before step d.
	var trace [][]int
	found := false
	for d := 0; d <= n+m && d <= maxDiffEdits && !found; d++ {
		trace = append(trace, append([]int(nil), v[offset-d:offset+d+1]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && am[x] == bm[y] {
				x, y = x+1, y+1
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		hunks, ok := gitDiffLines(am, bm)
		if !ok {
			return whole, false
		}
		for i := range hunks {
			hunks[i].a0, hunks[i].a1, hunks[i].b0, hunks[i].b1 = hunks[i].a0+p, hunks[i].a1+p, hunks[i].b0+p, hunks[i].b1+p
		}
		return hunks, true
	}
	// Walk back from (n, m), collecting the matched (diagonal) lines.
	var matches [][2]int
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		prev := func(k int) int { return trace[d][k+d] }
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && prev(k-1) < prev(k+1)) {
			prevK = k + 1
		}
		prevX := prev(prevK)
		prevY := prevX - prevK
		// The edit moves down (an insertion) or right (a deletion), then the
		// diagonal runs to (x, y).
		midX := prevX
		if prevK == k-1 {
			midX++
		}
		for x > midX {
			x, y = x-1, y-1
			matches = append(matches, [2]int{x, y})
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		matches = append(matches, [2]int{x, y})
	}
	var hunks []lineHunk
	i, j := 0, 0
	for idx := len(matches) - 1; idx >= -1; idx-- {
		mi, mj := n, m
		if idx >= 0 {
			mi, mj = matches[idx][0], matches[idx][1]
		}
		if i < mi || j < mj {
			hunks = append(hunks, lineHunk{p + i, p + mi, p + j, p + mj})
		}
		i, j = mi+1, mj+1
	}
	return hunks, true
}

// gitDiffLines diffs large, far-apart line lists with git's minimal diff,
// which has no edit budget.
func gitDiffLines(a, b []string) ([]lineHunk, bool) {
	dir, err := os.MkdirTemp("", "ycc-lines-*")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)
	pa, pb := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if os.WriteFile(pa, []byte(strings.Join(a, "")), 0o600) != nil || os.WriteFile(pb, []byte(strings.Join(b, "")), 0o600) != nil {
		return nil, false
	}
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "-c", "diff.interHunkContext=0", "diff", "--no-index", "--no-color",
		"--no-ext-diff", "--no-textconv", "--diff-algorithm=myers", "--minimal", "--text", "-U0", "--inter-hunk-context=0", "--", pa, pb)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			return nil, false
		}
	}
	var hunks []lineHunk
	for _, line := range strings.Split(string(out), "\n") {
		var as, ac, bs, bc int
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		header := strings.Fields(line)
		if len(header) < 3 || !parseRange(header[1], "-", &as, &ac) || !parseRange(header[2], "+", &bs, &bc) {
			return nil, false
		}
		// A zero-length range names the line it follows; otherwise its first line.
		a0, b0 := as-1, bs-1
		if ac == 0 {
			a0 = as
		}
		if bc == 0 {
			b0 = bs
		}
		hunks = append(hunks, lineHunk{a0, a0 + ac, b0, b0 + bc})
	}
	// Trust the hunks only if they turn a into b.
	var got []string
	last := 0
	for _, h := range hunks {
		if h.a0 < last || h.a1 > len(a) || h.b0 < 0 || h.b1 > len(b) {
			return nil, false
		}
		got = append(append(got, a[last:h.a0]...), b[h.b0:h.b1]...)
		last = h.a1
	}
	got = append(got, a[last:]...)
	return hunks, len(hunks) > 0 && slices.Equal(got, b)
}

func parseRange(field, sign string, start, count *int) bool {
	field, ok := strings.CutPrefix(field, sign)
	if !ok {
		return false
	}
	*count = 1
	first, rest, hasCount := strings.Cut(field, ",")
	var err error
	if *start, err = strconv.Atoi(first); err != nil {
		return false
	}
	if hasCount {
		if *count, err = strconv.Atoi(rest); err != nil {
			return false
		}
	}
	return true
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// headState is a file's content at a commit. Recorded writes are in worktree
// form, which line-ending conversion or filters can make differ from the
// committed blob while git treats them as unchanged, so content is compared by
// the blob it would clean to, not byte for byte.
type headState struct {
	key, rev, rel string
	blob          string // "" when absent at rev (or rev is unborn)
}

func newHeadState(key, rev, rel string) *headState {
	h := &headState{key: key, rev: rev, rel: rel}
	if out, err := exec.Command("git", "-C", key, "rev-parse", "--verify", "--quiet", rev+":"+rel).Output(); err == nil {
		h.blob = strings.TrimSpace(string(out))
	}
	return h
}

// same reports whether text is the committed content.
func (h *headState) same(text string) bool {
	if h.blob == "" {
		return text == ""
	}
	cmd := exec.Command("git", "-C", h.key, "hash-object", "--stdin", "--path="+h.rel)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == h.blob
}

// text returns the committed content in worktree form: the first of
// candidates that is the committed content, else the checkout form.
func (h *headState) text(candidates ...string) string {
	if h.blob == "" {
		return ""
	}
	for _, c := range candidates {
		if h.same(c) {
			return c
		}
	}
	out, err := exec.Command("git", "-C", h.key, "cat-file", "--filters", h.rev+":"+h.rel).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// linesLocked returns key's line records, loading persisted ones on first use.
func (s *Service) linesLocked(key string) map[string]*lineFile {
	if s.lines == nil {
		s.lines = make(map[string]map[string]*lineFile)
	}
	files := s.lines[key]
	if files != nil {
		return files
	}
	files = make(map[string]*lineFile)
	s.lines[key] = files
	s.mu.Lock()
	persist := s.persist
	s.mu.Unlock()
	if !persist {
		return files
	}
	path := linesFile(key)
	if path == "" {
		return files
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return files
	}
	var record linesRecord
	if json.Unmarshal(data, &record) != nil || record.Version != linesRecordVersion {
		return files
	}
	for rel, f := range record.Files {
		if f != nil {
			files[rel] = f
		}
	}
	return files
}

// saveLinesLocked persists key's line records (best effort: a lost record
// degrades those files to unattributed changes).
func (s *Service) saveLinesLocked(key string) {
	s.mu.Lock()
	persist := s.persist
	s.mu.Unlock()
	if !persist {
		return
	}
	path := linesFile(key)
	if path == "" {
		return
	}
	data, err := json.Marshal(linesRecord{Version: linesRecordVersion, Files: s.lines[key]})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bookkeeping-lines-*")
	if err != nil {
		return
	}
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
	}
}

func linesFile(key string) string {
	out, err := exec.Command("git", "-C", key, "rev-parse", "--path-format=absolute", "--git-path", "ycc/bookkeeping-lines.json").Output()
	if err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}
