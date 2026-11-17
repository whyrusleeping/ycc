package session

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/git"
)

// ErrInvalidCommitQuery marks a ListCommits branch or cursor the caller got
// wrong (unknown branch, malformed or foreign cursor).
var ErrInvalidCommitQuery = errors.New("invalid commit query")

// ErrNotGitRepo means the project directory is not a git work tree.
var ErrNotGitRepo = errors.New("project is not a git repository")

const (
	defaultCommitPage = 50
	maxCommitPage     = 200
	// rebasedCommitWindow bounds how far a rebased commit's author time may be
	// from its commit_made record for the subject fallback to link them.
	// Rebase preserves author time; commit_made follows the commit within
	// seconds unless finalization recovery replays it.
	rebasedCommitWindow = 10 * time.Minute
)

// LinkedCommit is a git commit with the ycc session/task that recorded it.
type LinkedCommit struct {
	git.Commit
	SessionID    string
	SessionTitle string
	TaskID       string
}

// BranchInfo is a local branch, marked when it belongs to an in-flight
// workstream of the project.
type BranchInfo struct {
	git.Branch
	WorkstreamID string
	TaskID       string
}

// CommitPage is one ListCommits page.
type CommitPage struct {
	Commits    []LinkedCommit
	NextCursor string
	Branch     string // resolved branch; empty for a detached HEAD
	Branches   []BranchInfo
}

// ListCommits pages the git history of one local branch of a project's primary
// tree (empty branch: the checked-out HEAD). A cursor pins the tip of the
// first page so later commits cannot shift pages.
func (m *Manager) ListCommits(project, branch string, limit int, cursor string) (CommitPage, error) {
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return CommitPage{}, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return CommitPage{}, fmt.Errorf("resolve workspace: %w", err)
	}
	repo, err := git.OpenExisting(absWS)
	if err != nil {
		return CommitPage{}, fmt.Errorf("%w: %v", ErrNotGitRepo, err)
	}
	if limit <= 0 {
		limit = defaultCommitPage
	}
	limit = min(limit, maxCommitPage)

	branches, err := repo.Branches()
	if err != nil {
		return CommitPage{}, err
	}
	page := CommitPage{Branches: m.branchInfos(absWS, branches)}

	var tip string
	skip := 0
	switch {
	case cursor != "":
		t, s, ok := strings.Cut(cursor, ":")
		n, err := strconv.Atoi(s)
		if !ok || err != nil || n < 0 || t == "" {
			return CommitPage{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCommitQuery)
		}
		tip, skip, page.Branch = t, n, branch
	case branch != "":
		for _, b := range branches {
			if b.Name == branch {
				tip = b.SHA
				break
			}
		}
		if tip == "" {
			return CommitPage{}, fmt.Errorf("%w: unknown branch %q", ErrInvalidCommitQuery, branch)
		}
		page.Branch = branch
	default:
		if tip, err = repo.HeadCommit(); err != nil {
			return CommitPage{}, err
		}
		for _, b := range branches {
			if b.Current {
				page.Branch = b.Name
			}
		}
	}
	if tip == "" {
		return page, nil // unborn HEAD: no history yet
	}

	commits, err := repo.Log(tip, skip, limit+1)
	if err != nil {
		if cursor != "" {
			return CommitPage{}, fmt.Errorf("%w: %v", ErrInvalidCommitQuery, err)
		}
		return CommitPage{}, err
	}
	if len(commits) > limit {
		commits = commits[:limit]
		page.NextCursor = tip + ":" + strconv.Itoa(skip+limit)
	}
	page.Commits = m.linkCommits(project, commits)
	return page, nil
}

// branchInfos orders the checked-out branch first and marks workstream
// branches of this project.
func (m *Manager) branchInfos(absWS string, branches []git.Branch) []BranchInfo {
	type wsRef struct{ id, task string }
	byBranch := map[string]wsRef{}
	if m.workstreams != nil {
		for _, w := range m.workstreams.List() {
			if !w.Status.InFlight() {
				continue
			}
			primary, ok := m.projects.Resolve(w.Project)
			if !ok {
				continue
			}
			if p, err := filepath.Abs(primary); err != nil || p != absWS {
				continue
			}
			byBranch[strings.TrimPrefix(w.Branch, "refs/heads/")] = wsRef{w.ID, w.TaskID}
		}
	}
	out := make([]BranchInfo, 0, len(branches))
	for _, b := range branches {
		ref := byBranch[b.Name]
		info := BranchInfo{Branch: b, WorkstreamID: ref.id, TaskID: ref.task}
		if b.Current {
			out = append([]BranchInfo{info}, out...)
		} else {
			out = append(out, info)
		}
	}
	return out
}

// linkCommits attaches the session that recorded each commit (commit_made).
// An exact sha wins; otherwise a commit whose subject matches a record and
// whose author time is within rebasedCommitWindow of it is taken to be that
// commit rewritten by a workstream rebase. Linking is best-effort: a history
// scan failure leaves commits unlinked.
func (m *Manager) linkCommits(project string, commits []git.Commit) []LinkedCommit {
	out := make([]LinkedCommit, len(commits))
	for i, c := range commits {
		out[i].Commit = c
	}
	sessions, err := m.ListSessionHistory(project)
	if err != nil {
		log.Printf("ycc: commit history: session links unavailable: %v", err)
		return out
	}
	type rec struct {
		session *SessionSummary
		commit  SessionCommit
	}
	bySHA := map[string]rec{}
	bySubject := map[string][]rec{}
	for i := range sessions {
		s := &sessions[i]
		for _, c := range s.Commits {
			r := rec{s, c}
			bySHA[c.SHA] = r
			if c.Subject != "" {
				bySubject[c.Subject] = append(bySubject[c.Subject], r)
			}
		}
	}
	for i := range out {
		c := &out[i]
		r, ok := bySHA[c.SHA]
		if !ok {
			best := rebasedCommitWindow + 1
			for _, cand := range bySubject[c.Subject] {
				if d := cand.commit.At.Sub(c.AuthoredAt).Abs(); d <= rebasedCommitWindow && d < best {
					r, ok, best = cand, true, d
				}
			}
		}
		if ok {
			c.SessionID, c.SessionTitle, c.TaskID = r.session.ID, r.session.Title, r.commit.Task
		}
	}
	return out
}
