package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/git"
)

// ErrInvalidForkPoint rejects a fork whose at_seq is not a user turn.
var ErrInvalidForkPoint = errors.New("not a fork point")

// ErrForkUnsupported rejects forking a session whose log lives in an in-flight
// workstream's worktree: merge or discard removes that tree.
var ErrForkUnsupported = errors.New("forking an in-flight workstream session is not supported")

// ForkResult is a new fork's id and the text of the user turn it was cut
// before, for the client to offer for editing and resending.
type ForkResult struct {
	SessionID string
	Text      string
}

// ForkSession creates a session whose log holds the events of session id that
// precede the user turn at atSeq, followed by a session_forked marker. A steer
// (a turn queued while the agent worked) is cut where the model received it.
// The fork's first input is the next thing its model sees, wherever the copied
// conversation stopped. The parent's log is only read. The fork is not
// made live: like any persisted session, its first input reopens it, replaying
// the copied history. Picture attachments the copied turns reference are
// copied beside the new log, and the fork gets its own git baseline of the
// shared tree as it is now; a fork that cannot capture one is not created.
func (m *Manager) ForkSession(project, id string, atSeq int) (ForkResult, error) {
	if id == "" || id == "." || filepath.Base(id) != id {
		return ForkResult{}, fmt.Errorf("%w %q", ErrUnknownSession, id)
	}
	ws, err := m.resolveProjectWorkspace(project)
	if err != nil {
		return ForkResult{}, err
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return ForkResult{}, fmt.Errorf("resolve workspace: %w", err)
	}
	logWS, logPath, err := m.locateSessionLog(absWS, id)
	if err != nil {
		return ForkResult{}, err
	}
	if logWS != absWS {
		return ForkResult{}, ErrForkUnsupported
	}
	lines, text, err := forkPrefix(logPath, atSeq)
	if err != nil {
		return ForkResult{}, err
	}
	repo, err := git.OpenExisting(absWS)
	if err != nil {
		return ForkResult{}, fmt.Errorf("prepare git workspace: %w", err)
	}

	newID, err := m.mintSessionID(absWS)
	if err != nil {
		return ForkResult{}, err
	}
	sessions := filepath.Join(absWS, ".ycc", "sessions")
	dir := filepath.Join(sessions, newID)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return ForkResult{}, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return ForkResult{}, fmt.Errorf("create fork: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()

	// Review and commit scope the fork's own changes from its baseline of the
	// shared tree as it is now; a fork that could not capture one would open
	// with them disabled, so that fails the fork instead.
	baseline, err := repo.CaptureBaseline()
	if err != nil {
		return ForkResult{}, fmt.Errorf("capture fork git baseline: %w", err)
	}
	if err := repo.PersistBaseline(newID, baseline); err != nil {
		return ForkResult{}, fmt.Errorf("persist fork git baseline: %w", err)
	}
	records, err := forkRecords(lines, id, atSeq, time.Now().UTC())
	if err != nil {
		return ForkResult{}, err
	}
	parentAttachments := filepath.Join(filepath.Dir(logPath), "attachments")
	for _, a := range forkAttachmentIDs(lines) {
		if err := copyAttachment(filepath.Join(parentAttachments, a), filepath.Join(dir, "attachments", a)); err != nil {
			return ForkResult{}, fmt.Errorf("copy attachment: %w", err)
		}
	}
	// The log is the session's existence: it appears, complete, by rename once
	// everything it references is in place.
	tmp := filepath.Join(dir, "events.jsonl.tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ForkResult{}, err
	}
	for _, rec := range records {
		if _, err := f.Write(append(rec, '\n')); err != nil {
			f.Close()
			return ForkResult{}, err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return ForkResult{}, err
	}
	if err := f.Close(); err != nil {
		return ForkResult{}, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "events.jsonl")); err != nil {
		return ForkResult{}, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	ok = true
	return ForkResult{SessionID: newID, Text: text}, nil
}

// forkAwaitingInput reports a fork that no input has reached yet: its log
// has a session_forked marker with no user input or model turn after the last
// one (a reopen without input leaves only lifecycle markers).
func forkAwaitingInput(events []event.Event) bool {
	awaiting := false
	for _, ev := range events {
		switch ev.Type {
		case event.SessionForked:
			awaiting = true
		case event.UserInput, event.ModelTurn:
			awaiting = false
		}
	}
	return awaiting
}

type forkLine struct {
	raw []byte
	ev  event.Event
}

// forkPrefix reads the committed records of logPath that precede the user turn
// at atSeq, and that turn's text. Every line before an existing record was
// synced before that record was written, so the prefix is durable even while
// the parent is live.
func forkPrefix(logPath string, atSeq int) ([]forkLine, string, error) {
	var all []forkLine
	_, err := event.ScanLogFrom(logPath, 0, func(line []byte) error {
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("corrupt event log %s: %w", logPath, err)
		}
		all = append(all, forkLine{raw: append([]byte(nil), line...), ev: ev})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	target := -1
	for i, l := range all {
		if l.ev.Seq == atSeq && l.ev.Type == event.UserInput {
			target = i
			break
		}
	}
	if atSeq <= 0 || target < 0 {
		return nil, "", fmt.Errorf("%w: seq %d is not a user turn", ErrInvalidForkPoint, atSeq)
	}
	turn := all[target].ev
	text, _ := turn.Data["text"].(string)
	cut, delivery := target, -1
	if queued, _ := turn.Data["queued"].(bool); queued {
		// A steer reached the model where the live loop posted it, after its
		// delivery checkpoint's tool-call batch; one never delivered is cut at
		// its echo. Either way the fork resumes with the edited steer there.
		for i := target + 1; i < len(all); i++ {
			if all[i].ev.Type == event.UserInputDelivered && deliveredSeq(all[i].ev) == atSeq {
				delivery, cut = i, postedAt(all, i)
				break
			}
		}
	}
	var copied []forkLine
	for i, l := range all[:cut] {
		if i == target || i == delivery {
			continue
		}
		// Messages delivered after the steer while its batch was open were
		// posted after it, so they follow the cut.
		if delivery >= 0 && i > delivery && deferredInjection(l.ev) {
			continue
		}
		copied = append(copied, l)
	}
	// A steer queued before the cut whose delivery came after it never reached
	// the model there, and its echo would show as queued forever.
	delivered := map[int]bool{}
	for _, l := range copied {
		if l.ev.Type == event.UserInputDelivered {
			delivered[deliveredSeq(l.ev)] = true
		}
	}
	var kept []forkLine
	for _, l := range copied {
		if l.ev.Type == event.UserInput {
			if q, _ := l.ev.Data["queued"].(bool); q && !delivered[l.ev.Seq] {
				continue
			}
		}
		kept = append(kept, l)
	}
	return kept, text, nil
}

// deferredInjection reports a user-role message the live loop posts at a
// checkpoint, and so defers to the end of an open tool-call batch.
func deferredInjection(ev event.Event) bool {
	switch ev.Type {
	case event.UserInputDelivered, event.JobNotified, event.PlanOnlyDelivered, event.CoordinationNotice, event.RestartContext:
		return true
	case event.BudgetExceeded:
		return ev.Actor == "user"
	}
	return false
}

// postedAt returns the index before which the live loop posted the steer
// delivered at all[d]: a delivery recorded while the coordinator's current
// tool-call batch still had unanswered calls is posted once the batch's last
// result is in, or when the batch ends early (a control tool such as finish
// skips the rest of it).
func postedAt(all []forkLine, d int) int {
	expected, results, turn := 0, 0, -1
	for i := 0; i < d; i++ {
		ev := all[i].ev
		if ev.Actor != "coordinator" {
			continue
		}
		switch ev.Type {
		case event.ModelTurn:
			turn, results = i, 0
			expected = intData(ev.Data, "tool_calls")
		case event.ToolResult:
			results++
		}
	}
	if turn < 0 || results >= expected {
		return d
	}
	for i := d + 1; i < len(all); i++ {
		ev := all[i].ev
		switch ev.Type {
		case event.ToolResult:
			if ev.Actor != "coordinator" {
				continue
			}
			if results++; results >= expected {
				return i + 1
			}
		case event.ModelTurn:
			if ev.Actor == "coordinator" {
				return i
			}
		case event.SessionIdle, event.SessionError, event.SessionStopped,
			event.SessionReopened, event.ContextViewChanged:
			// A pause (interrupted, then resumed) continues the same batch.
			return i
		case event.UserInput:
			if q, _ := ev.Data["queued"].(bool); !q {
				return i
			}
		}
	}
	return len(all)
}

func intData(data map[string]any, key string) int {
	switch n := data[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// forkRecords returns the fork's log records: the copied prefix, a terminal
// job_claimed for each background job not yet reported at the cut, then the
// session_forked marker. Settling the jobs keeps the fork from waking for
// them or replaying a lost-job note.
func forkRecords(lines []forkLine, parent string, atSeq int, now time.Time) ([][]byte, error) {
	maxSeq := 0
	out := make([][]byte, 0, len(lines)+1)
	for _, l := range lines {
		if l.ev.Seq > maxSeq {
			maxSeq = l.ev.Seq
		}
		out = append(out, l.raw)
	}
	synthetic := forkJobClaims(lines, parent, now, maxSeq)
	synthetic = append(synthetic, event.Event{
		Seq: maxSeq + len(synthetic) + 1, TS: now, Actor: "coordinator",
		Type: event.SessionForked, Data: map[string]any{"parent": parent, "at_seq": atSeq},
	})
	for _, ev := range synthetic {
		b, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// forkJobClaims settles every job started in lines whose report had not
// reached the conversation by the cut, numbering the claims after seq: a
// finished job keeps its result, a running one belongs to the parent and is
// lost. A claim suppresses the automatic report, which would otherwise arrive
// in the fork after its first input.
func forkJobClaims(lines []forkLine, parent string, now time.Time, seq int) []event.Event {
	type job struct {
		actor          string
		data           map[string]any
		status, result any
		finished       bool
		reported       bool
	}
	jobsByID := map[string]*job{}
	var order []string
	for _, l := range lines {
		id, _ := l.ev.Data["id"].(string)
		if id == "" {
			continue
		}
		j := jobsByID[id]
		switch l.ev.Type {
		case event.JobStarted:
			if j == nil {
				order = append(order, id)
				jobsByID[id] = &job{actor: l.ev.Actor, data: l.ev.Data}
			}
		case event.JobFinished:
			if j != nil {
				j.finished, j.status, j.result = true, l.ev.Data["status"], l.ev.Data["tail"]
			}
		case event.JobClaimed, event.JobNotified:
			if j != nil {
				j.reported = true
			}
		}
	}
	var out []event.Event
	for _, id := range order {
		j := jobsByID[id]
		if j.reported {
			continue
		}
		status, result := j.status, j.result
		if !j.finished {
			status = "lost"
			result = fmt.Sprintf("job lost: it ran in session %s, which this session was forked from", parent)
		}
		seq++
		out = append(out, event.Event{Seq: seq, TS: now, Actor: j.actor, Type: event.JobClaimed, Data: map[string]any{
			"id": id, "kind": j.data["kind"], "label": j.data["label"], "status": status,
			"result": result, "reason": "fork",
		}})
	}
	return out
}

func deliveredSeq(ev event.Event) int {
	switch n := ev.Data["seq"].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return -1
}

// forkAttachmentIDs lists the valid picture attachment ids the copied turns
// reference, once each.
func forkAttachmentIDs(lines []forkLine) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		if l.ev.Type != event.UserInput && l.ev.Type != event.UserInputDelivered {
			continue
		}
		images, _ := l.ev.Data["images"].([]any)
		for _, raw := range images {
			img, _ := raw.(map[string]any)
			id, _ := img["attachment_id"].(string)
			if validAttachmentID(id) && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// copyAttachment copies one retained picture owner-only. A picture the parent
// no longer has stays missing in the fork too.
func copyAttachment(src, dst string) error {
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
