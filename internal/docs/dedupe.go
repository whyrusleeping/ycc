package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Duplicate task ids happen even though Create assigns ids under a per-directory
// lock: the lock only covers one machine's processes, so two branches (or two
// checkouts) that each add tasks and are then merged land two files claiming the
// same id. That is corrupting rather than cosmetic — every by-id path (Get,
// update_task, the daemon GetTask RPC, the TUI backlog browser) resolves to the
// FIRST match, so the other holder of the id becomes invisible/unclickable and
// status updates silently hit the wrong task.
//
// The store therefore self-heals: any scan that sees a duplicate id renumbers
// all but one holder onto fresh ids (spec §6.2).

// Renumber records one duplicate-id resolution: a task that was moved off a
// shared id onto a fresh one.
type Renumber struct {
	OldID string
	NewID string
	Title string
	Path  string // path after the rename
}

func (r Renumber) String() string {
	return fmt.Sprintf("%s → %s (%s)", r.OldID, r.NewID, r.Title)
}

// DedupeIDs renumbers duplicate task ids and reports what changed. Callers that
// want the healing to be visible (the doctor command, daemon startup) call it
// explicitly; every List does it implicitly and silently.
func (s *Store) DedupeIDs() ([]Renumber, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.scanLocked()
	if err != nil {
		return nil, err
	}
	if !hasDuplicateIDs(tasks) {
		return nil, nil
	}
	return s.dedupeLocked(tasks)
}

// hasDuplicateIDs reports whether two task files claim the same id.
func hasDuplicateIDs(tasks []*Task) bool {
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		if seen[t.ID] {
			return true
		}
		seen[t.ID] = true
	}
	return false
}

// dedupeLocked resolves every duplicate id in tasks (which must be a fresh scan
// of the backlog dir). For each colliding id one task KEEPS it — the oldest
// claimant, which is the one existing references most likely mean — and the
// others are rewritten with ids continuing after the current maximum, their file
// renamed to match and a work-log line recording the move.
//
// depends_on references are deliberately left alone: a dependency on the shared
// id is ambiguous, and the surviving (oldest) task is the better guess. The
// work-log breadcrumb is what makes a wrong guess recoverable by hand.
func (s *Store) dedupeLocked(tasks []*Task) ([]Renumber, error) {
	groups := map[string][]*Task{}
	max := 0
	for _, t := range tasks {
		groups[t.ID] = append(groups[t.ID], t)
		if n, err := strconv.Atoi(t.ID); err == nil && n > max {
			max = n
		}
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		if len(groups[id]) > 1 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	today := time.Now().Format("2006-01-02")
	var out []Renumber
	for _, id := range ids {
		group := groups[id]
		sort.SliceStable(group, func(i, j int) bool { return keepsIDBefore(group[i], group[j]) })
		for _, t := range group[1:] {
			max++
			newID := fmt.Sprintf("%04d", max)
			oldID, oldPath := t.ID, t.Path
			slug := t.Slug
			if slug == "" {
				slug = slugify(t.Title)
			}
			t.ID = newID
			t.Slug = slug
			t.Path = filepath.Join(s.dir, newID+"-"+slug+".md")
			t.Updated = today
			t.Body = appendWorkLogLine(t.Body, today,
				fmt.Sprintf("renumbered %s → %s (duplicate id detected, %s kept by another task)", oldID, newID, oldID))
			if err := s.write(t); err != nil {
				return out, err
			}
			if oldPath != "" && oldPath != t.Path {
				if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
					return out, err
				}
			}
			out = append(out, Renumber{OldID: oldID, NewID: newID, Title: t.Title, Path: t.Path})
		}
	}
	return out, nil
}

// keepsIDBefore orders the claimants of a duplicated id: the first one keeps the
// id. Oldest `created` wins, then oldest `updated`, then path — a total,
// deterministic order so independent checkouts heal the same way.
func keepsIDBefore(a, b *Task) bool {
	ac, bc := dateKey(a.Created), dateKey(b.Created)
	if ac != bc {
		return ac < bc
	}
	au, bu := dateKey(a.Updated), dateKey(b.Updated)
	if au != bu {
		return au < bu
	}
	return a.Path < b.Path
}

// dateKey makes a YYYY-MM-DD string sortable, pushing missing/unparseable dates
// last (an undated file is treated as the younger claimant).
func dateKey(s string) string {
	s = strings.TrimSpace(s)
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return "9999-99-99"
	}
	return s
}

// appendWorkLogLine appends a dated bullet under the body's "## Work log".
func appendWorkLogLine(body, date, line string) string {
	body = ensureWorkLog(body)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + fmt.Sprintf("- %s %s\n", date, line)
}
