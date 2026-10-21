package sessionview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/whyrusleeping/ycc/internal/event"
)

// The activity projection answers "who ran what" for a session: the
// coordinator, each subagent actor with its spawn→finish rounds and progress
// counters, and each background (non-agent) job under the actor that started
// it. The index keeps only compacted lifecycle and subagent turn/tool events,
// so building the graph never rescans a large log.

const maxActivityString = 8 << 10

// ActivitySpan is one round of a subagent actor, from spawn to finish.
type ActivitySpan struct {
	JobID             string
	Round             int
	Status            string // running | done | failed | blocked | killed | lost
	ContextMode       string
	Started, Finished time.Time
}

// ActivityNode is one coordinator, subagent actor, or background job.
type ActivityNode struct {
	ID       string // "coordinator", the subagent actor, or the job id
	ParentID string // empty for the coordinator root
	Kind     string // coordinator | generic | implementer | reviewer | job kind (e.g. bash)
	Label    string
	Actor    string // actor whose events form this node's transcript; empty for jobs
	Model    string
	Status   string
	Mutates  bool
	JobID    string // job nodes: their own id; actors: latest round's job, if any
	Result   string // bounded final report or output tail
	Started  time.Time
	Finished time.Time
	Spans    []ActivitySpan
	// Progress of an actor's latest round (see ActorProgress).
	CurrentTool  string // set only while the round runs
	Turns        int
	TotalTokens  int64
	LastActivity time.Time
}

func isActivityLifecycle(t event.Type) bool {
	switch t {
	case event.SessionStarted, event.SessionReopened, event.SubagentSpawned, event.SubagentFinished, event.JobStarted,
		event.JobFinished, event.JobClaimed, event.JobNotified, event.JobHandedOff:
		return true
	}
	return false
}

var activityKeys = []string{"id", "kind", "label", "mutates", "status", "role", "agent_id", "model",
	"logical_model", "job_id", "round", "context_mode", "error", "blocked", "mutating", "owner", "tail", "result"}

func compactActivityEvent(ev event.Event) event.Event {
	data := make(map[string]any)
	for _, k := range activityKeys {
		v, ok := ev.Data[k]
		if !ok {
			continue
		}
		if s, isStr := v.(string); isStr && len(s) > maxActivityString {
			v = "…" + tailUTF8(s, maxActivityString)
		}
		data[k] = v
	}
	return event.Event{Seq: ev.Seq, TS: ev.TS, Actor: ev.Actor, Type: ev.Type, Data: data}
}

func tailUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && (value[start]&0xc0) == 0x80 {
		start++
	}
	return value[start:]
}

// recordActivityEvent indexes lifecycle events compactly, and reduces each
// subagent turn/tool event to a progress row (tool name, token total) so polls
// aggregate progress in SQL instead of decoding every turn.
func recordActivityEvent(ctx context.Context, tx *sql.Tx, sid string, ev event.Event) error {
	switch ev.Type {
	case event.ModelTurn, event.ToolCall, event.ToolResult:
		if !isSubagent(ev.Actor) {
			return nil
		}
		name, _ := ev.Data["name"].(string)
		var tokens int64
		if usage, ok := ev.Data["usage"].(map[string]any); ok {
			tokens, _ = number(usage["total"])
		}
		_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO activity_progress(session_id,seq,actor,type,name,tokens,ts_unix_ns) VALUES(?,?,?,?,?,?,?)`,
			sid, ev.Seq, ev.Actor, string(ev.Type), truncateUTF8(name, 128), tokens, ev.TS.UnixNano())
		return err
	}
	if !isActivityLifecycle(ev.Type) {
		return nil
	}
	encoded, err := json.Marshal(compactActivityEvent(ev))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO activity(session_id,seq,event_json) VALUES(?,?,?)`, sid, ev.Seq, encoded)
	return err
}

// ActorProgress aggregates a subagent actor's turn/tool events since its latest
// spawn: metadata only, never model text.
type ActorProgress struct {
	Turns       int
	TotalTokens int64
	CurrentTool string // the latest event was a call still awaiting its result
	Last        time.Time
}

// Activity catches up and returns the state, the compacted lifecycle events,
// and each subagent actor's progress since its latest spawn, all through the
// state's watermark under one store lock.
func (s *Store) Activity(ctx context.Context, sid, logPath string, maxSeq, maxOffset int64) (State, []event.Event, map[string]ActorProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.catchUpLocked(ctx, sid, logPath, maxSeq, maxOffset, true)
	if err != nil {
		return State{}, nil, nil, err
	}
	rs, err := s.db.QueryContext(ctx, `SELECT event_json FROM activity WHERE session_id=? AND seq<=? ORDER BY seq`, sid, st.IndexedThrough)
	if err != nil {
		return State{}, nil, nil, err
	}
	var events []event.Event
	latestSpawn := make(map[string]int)
	for rs.Next() {
		var raw []byte
		if err := rs.Scan(&raw); err != nil {
			rs.Close()
			return State{}, nil, nil, err
		}
		var ev event.Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			rs.Close()
			return State{}, nil, nil, err
		}
		if ev.Type == event.SubagentSpawned {
			if actor := subagentActor(ev.Data); actor != "" {
				latestSpawn[actor] = ev.Seq
			}
		}
		events = append(events, ev)
	}
	if err := rs.Close(); err != nil {
		return State{}, nil, nil, err
	}
	progress := make(map[string]ActorProgress, len(latestSpawn))
	for actor, since := range latestSpawn {
		var p ActorProgress
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(type='model_turn'),0),COALESCE(SUM(tokens),0) FROM activity_progress WHERE session_id=? AND actor=? AND seq>? AND seq<=?`,
			sid, actor, since, st.IndexedThrough).Scan(&p.Turns, &p.TotalTokens); err != nil {
			return State{}, nil, nil, err
		}
		var typ, name string
		var ts int64
		err := s.db.QueryRowContext(ctx, `SELECT type,name,ts_unix_ns FROM activity_progress WHERE session_id=? AND actor=? AND seq>? AND seq<=? ORDER BY seq DESC LIMIT 1`,
			sid, actor, since, st.IndexedThrough).Scan(&typ, &name, &ts)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return State{}, nil, nil, err
		}
		if err == nil {
			p.Last = time.Unix(0, ts).UTC()
			if typ == string(event.ToolCall) {
				p.CurrentTool = name
			}
		}
		progress[actor] = p
	}
	return st, events, progress, nil
}

// subagentActor maps a subagent lifecycle event to the actor its loop emits as.
func subagentActor(data map[string]any) string {
	str := func(k string) string { v, _ := data[k].(string); return v }
	switch role := str("role"); role {
	case "generic":
		if id := str("agent_id"); id != "" {
			return "agent:" + id
		}
	case "implementer":
		return "implementer"
	case "reviewer":
		if m := str("model"); m != "" {
			return "reviewer:" + m
		}
	default:
		return role
	}
	return ""
}

// BuildActivity projects lifecycle events and per-actor progress (since each
// actor's latest spawn) into the activity graph. Rounds and
// jobs still open at the end report "running"; callers overlay live state or
// mark them lost. Nodes are ordered root first, then by start.
func BuildActivity(events []event.Event, progress map[string]ActorProgress) []ActivityNode {
	root := &ActivityNode{ID: "coordinator", Kind: "coordinator", Label: "coordinator", Actor: "coordinator", Status: "running"}
	nodes := map[string]*ActivityNode{root.ID: root}
	order := []*ActivityNode{root}
	jobOwners := make(map[string][]*ActivityNode) // agent job id → actors with a round in it
	agentJobs := make(map[string]string)          // agent job id → status
	parentOf := func(actor string) string {
		if _, ok := nodes[actor]; ok && isSubagent(actor) {
			return actor
		}
		return root.ID
	}
	// running returns the actor node whose latest round is open, for progress.
	running := func(actor string) *ActivityNode {
		if n := nodes[actor]; n != nil && n.Actor != "" && len(n.Spans) > 0 && n.Spans[len(n.Spans)-1].Status == "running" {
			return n
		}
		return nil
	}
	finishSpan := func(n *ActivityNode, span *ActivitySpan, status string, at time.Time) {
		span.Status = status
		if span.Finished.IsZero() {
			span.Finished = at
		}
		if span == &n.Spans[len(n.Spans)-1] {
			n.Status, n.Finished, n.CurrentTool = status, span.Finished, ""
		}
	}
	str := func(ev event.Event, k string) string { v, _ := ev.Data[k].(string); return v }
	for _, ev := range events {
		if root.Started.IsZero() || ev.TS.Before(root.Started) {
			root.Started = ev.TS
		}
		switch ev.Type {
		case event.SessionReopened:
			// Nothing in flight survives a reopen: the restored registry reports
			// such jobs lost, and foreground rounds have no job to report at all.
			for _, n := range order[1:] {
				for i := range n.Spans {
					if n.Spans[i].Status == "running" {
						finishSpan(n, &n.Spans[i], "lost", ev.TS)
					}
				}
				if n.Status == "running" {
					n.Status, n.Finished = "lost", ev.TS
				}
			}
			for id, status := range agentJobs {
				if status == "running" {
					agentJobs[id] = "lost"
				}
			}
		case event.SubagentSpawned:
			actor := subagentActor(ev.Data)
			if actor == "" {
				continue
			}
			n := nodes[actor]
			if n == nil {
				label := actor
				switch str(ev, "role") {
				case "generic":
					label = str(ev, "agent_id")
				case "reviewer":
					label = "reviewer " + str(ev, "model")
				}
				n = &ActivityNode{ID: actor, ParentID: parentOf(ev.Actor), Kind: str(ev, "role"), Label: label,
					Actor: actor, Started: ev.TS}
				nodes[actor] = n
				order = append(order, n)
			}
			if m := firstNonempty(str(ev, "logical_model"), str(ev, "model")); m != "" {
				n.Model = m
			}
			if b, _ := ev.Data["mutating"].(bool); b || n.Kind == "implementer" {
				n.Mutates = true
			}
			round, _ := number(ev.Data["round"])
			// A context-recovery respawn replaces the open round rather than adding one.
			if k := len(n.Spans); k > 0 && n.Spans[k-1].Status == "running" {
				n.Spans = n.Spans[:k-1]
			}
			span := ActivitySpan{JobID: str(ev, "job_id"), Round: int(round), Status: "running",
				ContextMode: str(ev, "context_mode"), Started: ev.TS}
			if span.JobID != "" && !slices.Contains(jobOwners[span.JobID], n) {
				jobOwners[span.JobID] = append(jobOwners[span.JobID], n)
			}
			n.Spans = append(n.Spans, span)
			n.JobID, n.Status, n.Finished, n.Result = span.JobID, "running", time.Time{}, ""
		case event.SubagentFinished:
			n := running(subagentActor(ev.Data))
			if n == nil {
				continue
			}
			span := &n.Spans[len(n.Spans)-1]
			status := "done"
			switch {
			case str(ev, "error") != "":
				status = "failed"
			case ev.Data["blocked"] == true:
				status = "blocked"
			}
			// The job may already have finalized more severely (killed, or a
			// failed outcome decided after the loop returned).
			if js := agentJobs[span.JobID]; js == "killed" || js == "failed" {
				status = js
			}
			finishSpan(n, span, status, ev.TS)
		case event.JobStarted:
			id := str(ev, "id")
			if id == "" || nodes[id] != nil {
				continue
			}
			if str(ev, "kind") == "agent" {
				// Agent jobs are presented by their subagent actors' rounds.
				agentJobs[id] = "running"
				continue
			}
			mutates, _ := ev.Data["mutates"].(bool)
			n := &ActivityNode{ID: id, ParentID: parentOf(ev.Actor), Kind: str(ev, "kind"), Label: str(ev, "label"),
				Status: "running", Mutates: mutates, JobID: id, Started: ev.TS}
			nodes[id] = n
			order = append(order, n)
		case event.JobFinished, event.JobClaimed, event.JobNotified:
			id := str(ev, "id")
			status := str(ev, "status")
			if status == "" || status == "running" {
				continue
			}
			result := firstNonempty(str(ev, "tail"), str(ev, "result"))
			if js, ok := agentJobs[id]; ok {
				if js != "running" {
					continue
				}
				agentJobs[id] = status
				owners := jobOwners[id]
				for _, owner := range owners {
					if len(owners) == 1 {
						owner.Result = result // a shared fan-out job's report aggregates every owner
					}
					for i := len(owner.Spans) - 1; i >= 0; i-- {
						if span := &owner.Spans[i]; span.JobID == id {
							if status == "killed" || status == "failed" || span.Status == "running" {
								finishSpan(owner, span, status, ev.TS)
							}
							break
						}
					}
				}
				continue
			}
			if n := nodes[id]; n != nil && n.Status == "running" {
				n.Status, n.Result, n.Finished = status, result, ev.TS
			}
		}
	}
	for _, n := range order[1:] {
		if n.Actor == "" || len(n.Spans) == 0 {
			continue
		}
		p := progress[n.Actor]
		n.Turns, n.TotalTokens, n.LastActivity = p.Turns, p.TotalTokens, p.Last
		if latest := n.Spans[len(n.Spans)-1]; latest.Started.After(n.LastActivity) {
			n.LastActivity = latest.Started
		}
		if n.Status == "running" {
			n.CurrentTool = p.CurrentTool
		}
	}
	sort.SliceStable(order[1:], func(i, j int) bool { return order[1+i].Started.Before(order[1+j].Started) })
	out := make([]ActivityNode, len(order))
	for i, n := range order {
		out[i] = *n
	}
	return out
}
