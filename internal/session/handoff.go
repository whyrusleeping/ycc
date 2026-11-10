package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
	"github.com/whyrusleeping/ycc/internal/orchestrator"
)

// handoffPollInterval is how often a chat's tracking job checks the work
// session it handed a task to.
var handoffPollInterval = 500 * time.Millisecond

// startWorkHandoff is the start_work hook: the chat's tool has already checked
// the task and obtained the user's confirmation. It starts an attended work
// session pinned to the task, in the chat's worktree or a new workstream.
func (m *Manager) startWorkHandoff(chatWS, chatID string, h orchestrator.WorkHandoff) (orchestrator.StartedWork, error) {
	var out orchestrator.StartedWork
	chatWS = filepath.Clean(chatWS)
	primary := m.primaryTreeFor(chatWS)
	prompt := orchestrator.ChatHandoffPrompt(chatID, h.TaskID, h.Handoff)
	record := map[string]any{"task": h.TaskID, "from_session": chatID}
	var s *Session
	if h.Workstream {
		if m.workstreams == nil {
			return out, errors.New("workstreams are not available in this daemon")
		}
		name := ""
		for _, p := range m.projects.List() {
			if filepath.Clean(p.Path) == primary {
				name = p.Name
			}
		}
		if name == "" {
			return out, fmt.Errorf("%s is not a registered project", primary)
		}
		if err := m.requireCommittedTask(chatWS, primary, h.TaskID); err != nil {
			return out, err
		}
		ws, sess, err := m.SpawnWorkstream(SpawnWorkstreamConfig{
			Project: name, TaskID: h.TaskID, Prompt: prompt, Mode: "work", targetTask: h.TaskID, handoff: record,
		})
		if err != nil {
			return out, err
		}
		s, out.Workstream, out.Branch = sess, ws.ID, ws.Branch
	} else {
		if primary != chatWS {
			// One live session per workstream worktree (SpawnWorkstream's guard).
			return out, errors.New("this chat runs in a workstream worktree; hand the task off with separate_workstream")
		}
		sess, err := m.start(Config{Workspace: chatWS, Mode: "work", Prompt: prompt, targetTask: h.TaskID, handoff: record}, false)
		if err != nil {
			return out, err
		}
		s = sess
	}
	out.SessionID = s.ID
	out.Wait = func(ctx context.Context) (orchestrator.WorkOutcome, bool) { return m.awaitWorkHandoff(ctx, s) }
	return out, nil
}

// requireCommittedTask refuses a workstream hand-off unless the task the chat
// sees is the one the new workstream will get: the workstream forks the
// primary tree's committed HEAD, so a task created or revised in the chat
// (in the primary tree or in the chat's own workstream) would be missing or
// stale there.
func (m *Manager) requireCommittedTask(chatWS, primary, taskID string) error {
	task, err := m.backlogStore(primary).Get(taskID)
	if err != nil {
		return fmt.Errorf("task %s is not in the project's backlog, and a separate workstream starts from its committed "+
			"HEAD; commit the task there first: %w", taskID, err)
	}
	rel, _ := filepath.Rel(primary, task.Path)
	rel = filepath.ToSlash(rel)
	repo, err := git.OpenExisting(primary)
	if err != nil {
		return err
	}
	committed, err := repo.WorktreeFileMatchesHEAD(task.Path)
	if err != nil {
		return fmt.Errorf("check task %s is committed: %w", taskID, err)
	}
	if !committed {
		return fmt.Errorf("task %s's file %s is new or changed since the last commit, and a separate workstream starts "+
			"from the committed HEAD; commit it first (e.g. commit with that path) or hand off in this worktree", taskID, rel)
	}
	if chatWS == primary {
		return nil
	}
	// The primary file now equals HEAD, so comparing against it compares the
	// chat's definition with what the new workstream will check out.
	chatTask, err := m.backlogStore(chatWS).Get(taskID)
	if err != nil {
		return err
	}
	chatRel, _ := filepath.Rel(chatWS, chatTask.Path)
	mine, err := os.ReadFile(chatTask.Path)
	if err != nil {
		return err
	}
	base, err := os.ReadFile(task.Path)
	if err != nil {
		return err
	}
	if filepath.ToSlash(chatRel) != rel || !bytes.Equal(mine, base) {
		return fmt.Errorf("task %s in this chat's workstream differs from the project's committed HEAD, where a new "+
			"workstream starts; integrate or commit the task definition on the project first", taskID)
	}
	return nil
}

// awaitWorkHandoff waits until a handed-off work session finishes or goes idle
// (an attended session idles after finish or when it needs the user), errors,
// is stopped, or leaves the manager. ok is false when ctx ends first.
func (m *Manager) awaitWorkHandoff(ctx context.Context, s *Session) (orchestrator.WorkOutcome, bool) {
	ticker := time.NewTicker(handoffPollInterval)
	defer ticker.Stop()
	for {
		status, awaitingJobs := s.StatusWithJobContinuation()
		live, ok := m.Get(s.ID)
		gone := !ok || live != s
		if gone || status == event.StatusError || status == event.StatusStopped || status == event.StatusIdle && !awaitingJobs {
			return workHandoffOutcome(s, status, gone), true
		}
		select {
		case <-ctx.Done():
			return orchestrator.WorkOutcome{}, false
		case <-ticker.C:
		}
	}
}

// workHandoffOutcome summarizes a handed-off work session for the chat: how it
// ended, its commits, and its last report or error.
func workHandoffOutcome(s *Session, status event.Status, gone bool) orchestrator.WorkOutcome {
	events := s.Log().Snapshot()
	blocked := false
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == event.SessionIdle {
			blocked, _ = events[i].Data["blocked"].(bool)
			break
		}
	}
	var out orchestrator.WorkOutcome
	var b strings.Builder
	switch {
	case status == event.StatusError:
		out.Failed = true
		_, msg, _, _ := loopSessionFailureDetails(events)
		fmt.Fprintf(&b, "Work session %s stopped with an error: %s", s.ID, msg)
	case status == event.StatusStopped:
		out.Failed = true
		fmt.Fprintf(&b, "Work session %s was stopped.", s.ID)
	case gone:
		fmt.Fprintf(&b, "Work session %s was closed.", s.ID)
	case blocked:
		out.Failed = true
		fmt.Fprintf(&b, "Work session %s is blocked and needs the user.", s.ID)
	default:
		fmt.Fprintf(&b, "Work session %s is idle: it finished or is waiting for the user.", s.ID)
	}
	for _, ev := range events {
		if ev.Type == event.CommitMade {
			fmt.Fprintf(&b, "\nCommitted %s: %s", strField(ev, "sha"), firstLine(strField(ev, "message")))
		}
	}
	if report := loopSessionReport(events); report != "" {
		b.WriteString("\nReport:\n" + report)
	}
	out.Result = b.String()
	return out
}
